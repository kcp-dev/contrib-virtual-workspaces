//go:build e2e

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

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// These scenarios use a real identity provider rather than a certificate.
// kcp and the virtual workspace are started with the same
// AuthenticationConfiguration, so the username a token maps to is the same
// on both sides — which is the only reason a Membership written against one
// means anything to the other.

func requireDex(t *testing.T) {
	t.Helper()
	if !dexEnabled() {
		t.Skip("no identity provider; the harness was run with DEX=false")
	}
}

// The same identity, proved two ways: the answer a certificate gets and the
// answer a Dex token gets are the same answer.
func TestScenarioDexTokenGetsTheSameReviewAsACertificate(t *testing.T) {
	requireDex(t)
	ctx := testContext(t)

	tenantName := "dex-" + randomSuffix(t)
	tenant := createTenant(t, ctx, tenantName, "Dex Corp")
	tenantWS := workspaceClient(t, tenant.Status.URL)
	createProject(t, ctx, tenantWS, "web", tenantName, "Web")
	createMembership(t, ctx, tenantWS, "alice-admin", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "alice"},
		Role:    tenancyv1alpha1.RoleAdmin,
		Tenant:  tenantName,
	})

	// The certificate path.
	byCert := selfTenancyReview(t, ctx, "alice", "alice's certificate to see "+tenantName,
		func(r *tenancyv1alpha1.SelfTenancyReview) bool {
			return claimFor(r, tenantName) != nil
		})

	// The token path, against the same virtual workspace.
	token := dexToken(t, ctx, "alice")
	tokenClient, err := client.New(
		tokenConfig(envOr("TENANCY_VW_URL", "")+"/services/tenancy", token),
		client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("build a token-authenticated VW client: %v", err)
	}

	var byToken *tenancyv1alpha1.SelfTenancyReview
	waitFor(t, ctx, "alice's Dex token to see "+tenantName, func(ctx context.Context) (bool, error) {
		attempt := &tenancyv1alpha1.SelfTenancyReview{}
		if err := tokenClient.Create(ctx, attempt); err != nil {
			return false, nil
		}
		if claimFor(attempt, tenantName) == nil {
			return false, nil
		}
		byToken = attempt
		return true, nil
	})

	certClaim := claimFor(byCert, tenantName)
	tokenClaim := claimFor(byToken, tenantName)
	if certClaim.Cluster != tokenClaim.Cluster {
		t.Errorf("the two credentials resolved to different tenants: %q vs %q",
			certClaim.Cluster, tokenClaim.Cluster)
	}
	if !slices.Equal(certClaim.Roles, tokenClaim.Roles) {
		t.Errorf("the two credentials got different roles: %v vs %v",
			certClaim.Roles, tokenClaim.Roles)
	}
}

// Dex's password database carries no groups, so the
// AuthenticationConfiguration derives them from the email domain. This
// proves the derived group is what a Membership matches on.
func TestScenarioDexGroupsComeFromTheClaimMapping(t *testing.T) {
	requireDex(t)
	ctx := testContext(t)

	tenantName := "dexgrp-" + randomSuffix(t)
	tenant := createTenant(t, ctx, tenantName, "Dex Group Corp")
	tenantWS := workspaceClient(t, tenant.Status.URL)
	createProject(t, ctx, tenantWS, "web", tenantName, "Web")
	createMembership(t, ctx, tenantWS, "team-a-edit", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindGroup, Name: "team-a"},
		Role:    tenancyv1alpha1.RoleEdit,
		Tenant:  tenantName,
	})

	// alice@team-a.example.com maps to group team-a and so is granted...
	assertTokenSees(t, ctx, "alice", tenantName, true)
	// ...while bob@team-b.example.com maps to team-b and is not.
	assertTokenSees(t, ctx, "bob", tenantName, false)
}

// The point of the whole system: a token from the identity provider reaches
// the project workspace the grant created RBAC in. kcp authenticates the
// token, the operator's ClusterRoleBinding authorizes it, and neither the
// test nor the user ever held an admin credential for that workspace.
func TestScenarioDexTokenReachesTheProjectWorkspace(t *testing.T) {
	requireDex(t)
	ctx := testContext(t)

	tenantName := "dexrbac-" + randomSuffix(t)
	tenant := createTenant(t, ctx, tenantName, "Dex RBAC Corp")
	tenantWS := workspaceClient(t, tenant.Status.URL)
	project := createProject(t, ctx, tenantWS, "web", tenantName, "Web")
	membership := createMembership(t, ctx, tenantWS, "alice-admin", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "alice"},
		Role:    tenancyv1alpha1.RoleAdmin,
		Tenant:  tenantName,
	})

	// Wait until the grant has actually materialized there.
	projectWS := workspaceClient(t, project.Status.URL)
	waitFor(t, ctx, "the grant to reach the project workspace", func(ctx context.Context) (bool, error) {
		_, err := getBinding(ctx, projectWS, membership.Name)
		return err == nil, nil
	})

	aliceToken := dexToken(t, ctx, "alice")
	alice, err := client.New(tokenConfig(project.Status.URL, aliceToken), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("build alice's project client: %v", err)
	}
	waitFor(t, ctx, "alice's token to be allowed into the project workspace", func(ctx context.Context) (bool, error) {
		var namespaces corev1.NamespaceList
		return alice.List(ctx, &namespaces) == nil, nil
	})

	// mallory holds a perfectly valid token from the same issuer and no
	// grant, so authentication succeeds and authorization does not.
	malloryToken := dexToken(t, ctx, "mallory")
	mallory, err := client.New(tokenConfig(project.Status.URL, malloryToken), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("build mallory's project client: %v", err)
	}
	var namespaces corev1.NamespaceList
	if err := mallory.List(ctx, &namespaces); err == nil {
		t.Error("mallory reached the project workspace with no membership at all")
	}
}

func assertTokenSees(t *testing.T, ctx context.Context, user, tenantName string, want bool) {
	t.Helper()

	token := dexToken(t, ctx, user)
	c, err := client.New(
		tokenConfig(envOr("TENANCY_VW_URL", "")+"/services/tenancy", token),
		client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("build %s's VW client: %v", user, err)
	}

	what := user + " to see " + tenantName
	if !want {
		what = user + " not to see " + tenantName
	}
	waitFor(t, ctx, what, func(ctx context.Context) (bool, error) {
		review := &tenancyv1alpha1.SelfTenancyReview{}
		if err := c.Create(ctx, review); err != nil {
			return false, nil
		}
		return (claimFor(review, tenantName) != nil) == want, nil
	})
}

func claimFor(review *tenancyv1alpha1.SelfTenancyReview, tenantName string) *tenancyv1alpha1.TenantClaim {
	for i := range review.Status.Tenants {
		if review.Status.Tenants[i].Name == tenantName {
			return &review.Status.Tenants[i]
		}
	}
	return nil
}

// `tenancyctl login` against a real provider, then a command that names no
// credential at all. That second half is the point: a login is only useful
// if it removes flags from everything afterwards.
func TestScenarioTenancyctlLoginCachesAUsableToken(t *testing.T) {
	requireDex(t)
	ctx := testContext(t)

	tenantName := "login-" + randomSuffix(t)
	tenant := createTenant(t, ctx, tenantName, "Login Corp")
	tenantWS := workspaceClient(t, tenant.Status.URL)
	createProject(t, ctx, tenantWS, "web", tenantName, "Web")
	createMembership(t, ctx, tenantWS, "alice-admin", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "alice"},
		Role:    tenancyv1alpha1.RoleAdmin,
		Tenant:  tenantName,
	})

	// A cache private to this test, so it neither reads nor clobbers a
	// developer's real login.
	cache := filepath.Join(t.TempDir(), "tokens.json")

	// The scripted flow: no browser, so exchange a password directly.
	out := tenancyctlStdin(t, ctx, envOr("DEX_PASSWORD", "password"),
		"login",
		"--issuer", os.Getenv("DEX_ISSUER"),
		"--client-id", envOr("DEX_CLIENT_ID", "kcp"),
		"--username", dexUsers["alice"],
		"--password-stdin",
		"--insecure-skip-tls-verify",
		"--token-cache", cache,
	)
	if !strings.Contains(out, dexUsers["alice"]) {
		t.Errorf("login did not report who it logged in as: %s", out)
	}

	// The cached file is a JWT, and readable only by its owner.
	info, err := os.Stat(cache)
	if err != nil {
		t.Fatalf("stat the token cache: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("token cache is mode %o; a bearer credential should be 0600", mode)
	}
	raw, err := os.ReadFile(cache)
	if err != nil {
		t.Fatalf("read the token cache: %v", err)
	}
	if !strings.Contains(string(raw), "idToken") || strings.Count(string(raw), ".") < 2 {
		t.Errorf("the cache holds no JWT: %s", raw)
	}

	// ...and now a command with NO credential flags at all.
	var whoami string
	waitFor(t, ctx, "the cached login to answer whoami for "+tenantName, func(ctx context.Context) (bool, error) {
		cmd := exec.CommandContext(ctx, os.Getenv("TENANCYCTL_BIN"),
			"whoami", "--url", os.Getenv("TENANCY_VW_URL"),
			"--insecure-skip-tls-verify", "--token-cache", cache)
		got, err := cmd.CombinedOutput()
		if err != nil {
			return false, nil
		}
		whoami = string(got)
		return strings.Contains(whoami, tenantName), nil
	})
	if !strings.Contains(whoami, "admin") {
		t.Errorf("whoami from the cached login does not show the role: %s", whoami)
	}

	// Logging out takes the credential away again.
	tenancyctl(t, ctx, "logout", "--all", "--token-cache", cache)
	cmd := exec.CommandContext(ctx, os.Getenv("TENANCYCTL_BIN"),
		"whoami", "--url", os.Getenv("TENANCY_VW_URL"),
		"--insecure-skip-tls-verify", "--token-cache", cache)
	if got, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("whoami still worked after logout: %s", got)
	}
}
