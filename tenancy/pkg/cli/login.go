/*
Copyright 2026 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cli

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

// Login obtains an ID token — the JWT every other command presents — and
// caches it. Nothing else here mints credentials: kcp and the virtual
// workspace both validate this token against the same
// AuthenticationConfiguration, so whatever the identity provider says the
// caller is named is what a Membership has to match.

// tokenCacheEntry is one issuer's worth of cached credentials.
//
// Deliberately no refresh token: nothing here spends one, and a credential
// sitting on disk that no code path uses is a liability with no benefit.
// When the ID token expires the commands say so and `login` runs again.
type tokenCacheEntry struct {
	IDToken  string    `json:"idToken"`
	Expiry   time.Time `json:"expiry,omitempty"`
	Issuer   string    `json:"issuer"`
	ClientID string    `json:"clientID"`
}

// tokenCache is keyed by "issuer|clientID" so two logins against different
// providers, or the same provider as different clients, do not evict each
// other.
type tokenCache map[string]tokenCacheEntry

func defaultTokenCachePath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "tenancyctl", "tokens.json")
}

func cacheKey(issuer, clientID string) string { return issuer + "|" + clientID }

func readTokenCache(path string) tokenCache {
	cache := tokenCache{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cache
	}
	_ = json.Unmarshal(raw, &cache)
	return cache
}

func writeTokenCache(path string, cache tokenCache) error {
	if path == "" {
		return fmt.Errorf("no token cache path; pass --token-cache")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create the token cache directory: %w", err)
	}
	raw, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	// The file holds a bearer credential, so it is readable only by its
	// owner — the same reason a kubeconfig with a token is.
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write the token cache: %w", err)
	}
	return nil
}

// cachedToken returns a still-valid token for the issuer, if one was
// cached. An expired token is ignored rather than presented, because the
// error a server gives for an expired JWT reads like a permissions
// problem.
func cachedToken(path, issuer, clientID string) string {
	entry, ok := readTokenCache(path)[cacheKey(issuer, clientID)]
	if !ok || entry.IDToken == "" {
		return ""
	}
	if !entry.Expiry.IsZero() && time.Now().After(entry.Expiry.Add(-30*time.Second)) {
		return ""
	}
	return entry.IDToken
}

// anyCachedToken returns the single cached token when exactly one exists,
// so the common case — one provider — needs no --issuer on every command.
func anyCachedToken(path string) (string, bool) {
	cache := readTokenCache(path)
	var found string
	for _, entry := range cache {
		if entry.IDToken == "" {
			continue
		}
		if !entry.Expiry.IsZero() && time.Now().After(entry.Expiry.Add(-30*time.Second)) {
			continue
		}
		if found != "" {
			return "", false // ambiguous; make the caller say which
		}
		found = entry.IDToken
	}
	return found, found != ""
}

// oidcEndpoints is the slice of the discovery document this needs.
type oidcEndpoints struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

func discover(ctx context.Context, client *http.Client, issuer string) (*oidcEndpoints, error) {
	url := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	var out oidcEndpoints
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode %s: %w", url, err)
	}
	if out.TokenEndpoint == "" {
		return nil, fmt.Errorf("%s advertises no token endpoint", url)
	}
	return &out, nil
}

// httpClientFor builds the client used to talk to the issuer. A local
// provider serves a throwaway certificate, which is why both knobs exist.
func httpClientFor(caFile string, insecure bool) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case insecure:
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // opt-in, for a local issuer
	case caFile != "":
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read the issuer CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s contains no certificate", caFile)
		}
		tlsCfg.RootCAs = pool
	}
	return &http.Client{
		Timeout:   time.Minute,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}, nil
}

func newLoginCommand() *cobra.Command {
	var (
		issuer        string
		clientID      string
		clientSecret  string
		scopes        []string
		username      string
		passwordStdin bool
		password      string
		listen        string
		caFile        string
		insecure      bool
		cachePath     string
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Obtain an ID token from the identity provider and cache it",
		Long: `Log in and cache the resulting JWT, so whoami and kubeconfig need
no credential flags afterwards.

Two flows:

  tenancyctl login --issuer https://dex.example.com
      Opens a browser, listens on a loopback address for the redirect, and
      exchanges the code with PKCE. The default.

  tenancyctl login --issuer ... --username alice@example.com --password-stdin
      Exchanges a username and password directly. Needs a provider
      configured for it and is meant for scripts and tests, not people.

The token is what kcp and the virtual workspace both validate against the
same AuthenticationConfiguration, so the username it maps to is the string
a Membership has to name.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if issuer == "" {
				return fmt.Errorf("--issuer is required, or set TENANCY_OIDC_ISSUER")
			}
			if cachePath == "" {
				return fmt.Errorf("no token cache path could be determined; pass --token-cache")
			}

			httpClient, err := httpClientFor(caFile, insecure)
			if err != nil {
				return err
			}
			ctx := context.WithValue(cmd.Context(), oauth2.HTTPClient, httpClient)

			endpoints, err := discover(ctx, httpClient, issuer)
			if err != nil {
				return fmt.Errorf("discover %s: %w%s", issuer, err, issuerHint(err))
			}

			cfg := &oauth2.Config{
				ClientID:     clientID,
				ClientSecret: clientSecret,
				Scopes:       scopes,
				Endpoint: oauth2.Endpoint{
					AuthURL:  endpoints.AuthorizationEndpoint,
					TokenURL: endpoints.TokenEndpoint,
				},
			}

			var token *oauth2.Token
			if username != "" {
				if passwordStdin {
					raw, err := readAllStdin()
					if err != nil {
						return err
					}
					password = strings.TrimRight(raw, "\r\n")
				}
				if password == "" {
					return fmt.Errorf("--username needs a password: pass --password-stdin")
				}
				token, err = cfg.PasswordCredentialsToken(ctx, username, password)
				if err != nil {
					return fmt.Errorf("exchange the password for a token: %w", err)
				}
			} else {
				token, err = browserLogin(ctx, cfg, listen)
				if err != nil {
					return err
				}
			}

			idToken, _ := token.Extra("id_token").(string)
			if idToken == "" {
				return fmt.Errorf("the provider returned no id_token; ask for the openid scope " +
					"(--scopes) and check the client is allowed to receive one")
			}

			cache := readTokenCache(cachePath)
			cache[cacheKey(issuer, clientID)] = tokenCacheEntry{
				IDToken:  idToken,
				Expiry:   token.Expiry,
				Issuer:   issuer,
				ClientID: clientID,
			}
			if err := writeTokenCache(cachePath, cache); err != nil {
				return err
			}

			who := describeToken(idToken)
			fmt.Printf("logged in to %s as %s\n", issuer, who)
			fmt.Printf("token cached in %s\n", cachePath)
			if !token.Expiry.IsZero() {
				fmt.Printf("expires %s\n", token.Expiry.Local().Format(time.RFC1123))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&issuer, "issuer", envOr("TENANCY_OIDC_ISSUER", os.Getenv("DEX_ISSUER")),
		"OIDC issuer URL. Defaults to $TENANCY_OIDC_ISSUER.")
	cmd.Flags().StringVar(&clientID, "client-id", envOr("TENANCY_OIDC_CLIENT_ID", "kcp"),
		"OAuth2 client id registered with the issuer.")
	cmd.Flags().StringVar(&clientSecret, "client-secret", "",
		"Client secret, for a confidential client. Public clients need none.")
	cmd.Flags().StringSliceVar(&scopes, "scopes", []string{"openid", "email", "groups"},
		"Scopes to request. openid is what produces the id_token.")
	cmd.Flags().StringVar(&username, "username", "",
		"Exchange a password for a token as this user instead of opening a browser.")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the password from stdin.")
	cmd.Flags().StringVar(&listen, "listen", "localhost:8000",
		"Address the browser flow listens on for the redirect. Must match a redirect URI "+
			"registered with the client.")
	cmd.Flags().StringVar(&caFile, "certificate-authority", os.Getenv("DEX_CA_FILE"),
		"CA bundle that signs the issuer's serving certificate.")
	cmd.Flags().BoolVar(&insecure, "insecure-skip-tls-verify", false,
		"Do not verify the issuer's certificate. For a local provider only.")
	cmd.Flags().StringVar(&cachePath, "token-cache", defaultTokenCachePath(),
		"File the token is cached in.")

	return cmd
}

func newLogoutCommand() *cobra.Command {
	var (
		issuer    string
		clientID  string
		cachePath string
		all       bool
	)

	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Forget cached tokens",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if all {
				if err := os.Remove(cachePath); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove the token cache: %w", err)
				}
				fmt.Printf("forgot every cached token (%s)\n", cachePath)
				return nil
			}
			if issuer == "" {
				return fmt.Errorf("--issuer is required, or pass --all")
			}
			cache := readTokenCache(cachePath)
			delete(cache, cacheKey(issuer, clientID))
			if err := writeTokenCache(cachePath, cache); err != nil {
				return err
			}
			fmt.Printf("forgot the token for %s\n", issuer)
			return nil
		},
	}
	cmd.Flags().StringVar(&issuer, "issuer", envOr("TENANCY_OIDC_ISSUER", os.Getenv("DEX_ISSUER")), "Issuer to forget.")
	cmd.Flags().StringVar(&clientID, "client-id", envOr("TENANCY_OIDC_CLIENT_ID", "kcp"), "Client id to forget.")
	cmd.Flags().StringVar(&cachePath, "token-cache", defaultTokenCachePath(), "File the token is cached in.")
	cmd.Flags().BoolVar(&all, "all", false, "Forget every cached token.")
	return cmd
}

// browserLogin runs the authorization-code flow with PKCE against a
// loopback listener. PKCE rather than a client secret because a CLI cannot
// keep one: the binary is on the user's disk.
func browserLogin(ctx context.Context, cfg *oauth2.Config, listen string) (*oauth2.Token, error) {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen on %s for the redirect: %w "+
			"(--listen must match a redirect URI registered with the client)", listen, err)
	}
	defer func() { _ = listener.Close() }()

	cfg.RedirectURL = "http://" + listen + "/callback"

	state, err := randomString()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()
	authURL := cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))

	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			http.Error(w, e+": "+q.Get("error_description"), http.StatusBadRequest)
			results <- result{err: fmt.Errorf("the provider refused: %s %s", e, q.Get("error_description"))}
			return
		}
		// The state check is the whole defence against a different site
		// walking a victim's browser through this callback.
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			results <- result{err: errors.New("state mismatch: the redirect did not come from the login this command started")}
			return
		}
		_, _ = w.Write([]byte("<html><body><h3>Signed in.</h3>You can close this tab and return to the terminal.</body></html>"))
		results <- result{code: q.Get("code")}
	})

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Shutdown(context.Background()) }()

	fmt.Printf("Opening %s\n", authURL)
	fmt.Println("If no browser opens, paste that URL into one.")
	openBrowser(authURL)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(3 * time.Minute):
		return nil, errors.New("timed out waiting for the browser redirect")
	case res := <-results:
		if res.err != nil {
			return nil, res.err
		}
		token, err := cfg.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return nil, fmt.Errorf("exchange the authorization code: %w", err)
		}
		return token, nil
	}
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	// Best effort: the URL is printed either way, so a headless machine
	// loses nothing by this failing.
	_ = exec.Command(cmd, append(args, url)...).Start() //nolint:gosec // fixed command, URL we built
}

// readAllStdin reads a secret piped in, so it never appears in a process
// list or a shell history.
func readAllStdin() (string, error) {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read the password from stdin: %w", err)
	}
	return string(raw), nil
}

func randomString() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// describeToken reads the claims for display only. It does NOT verify the
// signature — the servers do that, and a CLI pretending to would be
// security theatre.
func describeToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "(an opaque token)"
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "(unreadable claims)"
	}
	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		Subject           string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "(unreadable claims)"
	}
	switch {
	case claims.Email != "":
		return claims.Email
	case claims.PreferredUsername != "":
		return claims.PreferredUsername
	default:
		return claims.Subject
	}
}

func issuerHint(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "x509") || strings.Contains(msg, "certificate signed by unknown authority"):
		return "\n\nThe issuer's certificate is not trusted. Pass --certificate-authority, " +
			"or --insecure-skip-tls-verify for a local provider."
	case strings.Contains(msg, "connection refused"):
		return "\n\nNothing is listening there. Check --issuer."
	default:
		return ""
	}
}

// envOr returns the environment variable, or the fallback when it is unset.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
