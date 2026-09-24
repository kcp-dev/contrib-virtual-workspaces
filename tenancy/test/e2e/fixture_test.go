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

// Package e2e tests the tenancy stack the way hack/ci/run-e2e-tests.sh
// stands it up: a real kcp, the operator and the virtual workspace as local
// processes.
//
// The tiers are the point of most of these tests. `init` installs the four
// exports in root:tenancy:controllers and binds root:tenancy:tenants as the
// platform tier. Tenants are created there; their Projects and Memberships
// live inside each tenant's own workspace; and RBAC materializes only in
// project workspaces. Nothing below the platform tier is bound by hand —
// the WorkspaceTypes do it — so a test that finds the right bindings in the
// right places has proved the whole claim-based model.
package e2e

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// The three workspaces init creates. The store holds Tenant records; the
// tenants workspace is the parent their workspaces are created under, and
// holds no tenancy objects at all.
const (
	exportsPath = "root:tenancy:controllers"
	storePath   = "root:tenancy:store"
	tenantsPath = "root:tenancy:tenants"
)

// The four exports, by the name their APIBinding takes.
const (
	exportPlatform    = "tenancy-platform"
	exportTenancy     = "tenancy"
	exportProvisioner = "tenancy-provisioner"
	exportAccess      = "tenancy-access"
)

// testScheme covers everything the tests touch.
var testScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(scheme.AddToScheme(testScheme))
	utilruntime.Must(corev1alpha1.AddToScheme(testScheme))
	utilruntime.Must(kcptenancyv1alpha1.AddToScheme(testScheme))
	utilruntime.Must(tenancyv1alpha1.AddToScheme(testScheme))
}

// adminRestConfig returns the kcp admin config from $KUBECONFIG, pointed at
// the given absolute workspace path.
func adminRestConfig(t *testing.T, workspacePath string) *rest.Config {
	t.Helper()
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		t.Fatal("KUBECONFIG is not set; run this through hack/ci/run-e2e-tests.sh")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("load kubeconfig: %v", err)
	}
	if workspacePath != "" {
		host := cfg.Host
		if i := strings.Index(host, "/clusters/"); i >= 0 {
			host = host[:i]
		}
		cfg.Host = strings.TrimSuffix(host, "/") + "/clusters/" + workspacePath
	}
	return cfg
}

func adminClient(t *testing.T, workspacePath string) client.Client {
	t.Helper()
	c, err := client.New(adminRestConfig(t, workspacePath), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("build client for %s: %v", workspacePath, err)
	}
	return c
}

// storeClient addresses the registry Tenant records live in.
func storeClient(t *testing.T) client.Client {
	t.Helper()
	return adminClient(t, storePath)
}

// workspaceClient addresses a provisioned workspace by the URL kcp reported
// for it — how the tests reach a tenant's or a project's own cluster.
func workspaceClient(t *testing.T, workspaceURL string) client.Client {
	t.Helper()
	cfg := adminRestConfig(t, "")
	cfg.Host = workspaceURL
	c, err := client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("build client for %s: %v", workspaceURL, err)
	}
	return c
}

func dynamicForURL(t *testing.T, workspaceURL string) dynamic.Interface {
	t.Helper()
	cfg := adminRestConfig(t, "")
	cfg.Host = workspaceURL
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("build dynamic client for %s: %v", workspaceURL, err)
	}
	return dyn
}

func dynamicFor(t *testing.T, workspacePath string) dynamic.Interface {
	t.Helper()
	dyn, err := dynamic.NewForConfig(adminRestConfig(t, workspacePath))
	if err != nil {
		t.Fatalf("build dynamic client for %s: %v", workspacePath, err)
	}
	return dyn
}

var (
	apiBindingGVR        = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
	apiExportGVR         = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	apiResourceSchemaGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
	workspaceTypeGVR     = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspacetypes"}
)

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// createTenant creates a Tenant in the platform workspace and waits for the
// operator to provision its workspace. Cleaned up with the test.
func createTenant(t *testing.T, ctx context.Context, name, displayName string) *tenancyv1alpha1.Tenant {
	t.Helper()

	store := storeClient(t)
	tenant := &tenancyv1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       tenancyv1alpha1.TenantSpec{DisplayName: displayName},
	}
	if err := store.Create(ctx, tenant); err != nil {
		t.Fatalf("create tenant %s: %v", name, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = store.Delete(cleanupCtx, &tenancyv1alpha1.Tenant{ObjectMeta: metav1.ObjectMeta{Name: name}})
	})

	waitFor(t, ctx, "tenant "+name+" ready", func(ctx context.Context) (bool, error) {
		if err := store.Get(ctx, client.ObjectKey{Name: name}, tenant); err != nil {
			return false, nil
		}
		return tenant.Status.Phase == tenancyv1alpha1.PhaseReady, nil
	})

	if tenant.Status.Workspace == "" || tenant.Status.WorkspaceCluster == "" || tenant.Status.URL == "" {
		t.Fatalf("tenant %s is Ready but its status is incomplete: %+v", name, tenant.Status)
	}
	return tenant
}

// createProject creates a Project inside a tenant's own workspace.
func createProject(t *testing.T, ctx context.Context, tenantWS client.Client, name, tenantName, displayName string) *tenancyv1alpha1.Project {
	t.Helper()

	project := &tenancyv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       tenancyv1alpha1.ProjectSpec{Tenant: tenantName, DisplayName: displayName},
	}
	if err := tenantWS.Create(ctx, project); err != nil {
		t.Fatalf("create project %s: %v", name, err)
	}

	waitFor(t, ctx, "project "+name+" ready", func(ctx context.Context) (bool, error) {
		if err := tenantWS.Get(ctx, client.ObjectKey{Name: name}, project); err != nil {
			return false, nil
		}
		return project.Status.Phase == tenancyv1alpha1.PhaseReady, nil
	})
	if project.Status.WorkspaceCluster == "" || project.Status.URL == "" {
		t.Fatalf("project %s is Ready but its status is incomplete: %+v", name, project.Status)
	}
	return project
}

// createMembership creates a Membership inside a tenant's own workspace and
// waits for the operator to materialize it.
func createMembership(t *testing.T, ctx context.Context, tenantWS client.Client, name string, spec tenancyv1alpha1.MembershipSpec) *tenancyv1alpha1.Membership {
	t.Helper()

	membership := &tenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       spec,
	}
	if err := tenantWS.Create(ctx, membership); err != nil {
		t.Fatalf("create membership %s: %v", name, err)
	}

	waitFor(t, ctx, "membership "+name+" applied", func(ctx context.Context) (bool, error) {
		if err := tenantWS.Get(ctx, client.ObjectKey{Name: name}, membership); err != nil {
			return false, nil
		}
		return membership.Status.Phase == tenancyv1alpha1.PhaseReady, nil
	})
	return membership
}

// bindingNames lists the APIBindings present in a workspace. It is how the
// tests check that a tier carries exactly the capabilities it should — and,
// more importantly, none that it should not.
func bindingNames(t *testing.T, ctx context.Context, dyn dynamic.Interface) []string {
	t.Helper()
	list, err := dyn.Resource(apiBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list apibindings: %v", err)
	}
	names := make([]string, 0, len(list.Items))
	for _, b := range list.Items {
		names = append(names, b.GetName())
	}
	return names
}

func hasBinding(names []string, export string) bool {
	for _, n := range names {
		if n == export {
			return true
		}
		// kcp names a default binding `<export>-<token>`, where the token
		// carries no dash. Matching on the prefix alone is not enough:
		// export names contain dashes themselves, so "tenancy" would match
		// a binding for "tenancy-access" and every placement assertion
		// would quietly pass.
		rest, ok := strings.CutPrefix(n, export+"-")
		if ok && rest != "" && !strings.Contains(rest, "-") {
			return true
		}
	}
	return false
}

// userVWConfig returns a rest config for the tenancy virtual workspace,
// authenticated as one of the harness-minted users.
func userVWConfig(t *testing.T, username string) *rest.Config {
	t.Helper()
	base := os.Getenv("TENANCY_VW_URL")
	pki := os.Getenv("TENANCY_PKI_DIR")
	if base == "" || pki == "" {
		t.Fatal("TENANCY_VW_URL / TENANCY_PKI_DIR not set; run this through hack/ci/run-e2e-tests.sh")
	}
	return &rest.Config{
		Host: base + "/services/tenancy",
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
			CertFile: filepath.Join(pki, username+".crt"),
			KeyFile:  filepath.Join(pki, username+".key"),
		},
	}
}

// selfTenancyReview asks the VW who the given user is, retrying while the
// directory catches up, until check accepts the answer.
func selfTenancyReview(t *testing.T, ctx context.Context, username, what string, check func(*tenancyv1alpha1.SelfTenancyReview) bool) *tenancyv1alpha1.SelfTenancyReview {
	t.Helper()

	c, err := client.New(userVWConfig(t, username), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("build VW client for %s: %v", username, err)
	}

	var review *tenancyv1alpha1.SelfTenancyReview
	waitFor(t, ctx, what, func(ctx context.Context) (bool, error) {
		attempt := &tenancyv1alpha1.SelfTenancyReview{}
		if err := c.Create(ctx, attempt); err != nil {
			return false, nil
		}
		if !check(attempt) {
			return false, nil
		}
		review = attempt
		return true, nil
	})
	return review
}

func waitFor(t *testing.T, ctx context.Context, what string, cond wait.ConditionWithContextFunc) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 3*time.Minute, true, cond); err != nil {
		t.Fatalf("timed out waiting for %s: %v", what, err)
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// bindingName mirrors the operator's deterministic binding naming.
func bindingName(membershipName string) string {
	return fmt.Sprintf("tenancy.contrib.kcp.io:membership:%s", membershipName)
}

// getBinding fetches the materialized ClusterRoleBinding for a membership
// from a provisioned workspace.
func getBinding(ctx context.Context, ws client.Client, membershipName string) (*rbacv1.ClusterRoleBinding, error) {
	var crb rbacv1.ClusterRoleBinding
	if err := ws.Get(ctx, client.ObjectKey{Name: bindingName(membershipName)}, &crb); err != nil {
		return nil, err
	}
	return &crb, nil
}

func isNotFound(err error) bool { return apierrors.IsNotFound(err) }

// dexEnabled reports whether the harness stood an identity provider up.
// Without one the certificate path is still fully exercised; the Dex
// scenarios skip.
func dexEnabled() bool { return os.Getenv("DEX_ISSUER") != "" }

// dexUsers maps the short test names to the identity Dex knows. The email
// is the whole identity: the AuthenticationConfiguration derives the
// username from its local part and the group from its domain, so these are
// the same alice/team-a and bob/team-b the certificates carry.
var dexUsers = map[string]string{
	"alice":   "alice@team-a.example.com",
	"bob":     "bob@team-b.example.com",
	"mallory": "mallory@strangers.example.com",
}

// dexToken fetches an ID token for a test user with the resource-owner
// password grant, which is what a test without a browser can use.
func dexToken(t *testing.T, ctx context.Context, user string) string {
	t.Helper()

	email, ok := dexUsers[user]
	if !ok {
		t.Fatalf("unknown Dex test user %q", user)
	}
	issuer := os.Getenv("DEX_ISSUER")
	if issuer == "" {
		t.Fatal("DEX_ISSUER is not set; run this through hack/ci/run-e2e-tests.sh")
	}

	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {envOr("DEX_CLIENT_ID", "kcp")},
		"username":   {email},
		"password":   {envOr("DEX_PASSWORD", "password")},
		"scope":      {"openid email groups"},
	}

	// The issuer serves a throwaway certificate; the components verify it
	// through the AuthenticationConfiguration, the test does not need to.
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // throwaway test issuer
	}}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build token request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("ask Dex for a token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read Dex response: %v", err)
	}
	var out struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode Dex response %q: %v", body, err)
	}
	if out.IDToken == "" {
		t.Fatalf("Dex issued no token for %s: %s %s", email, out.Error, out.Desc)
	}
	return out.IDToken
}

// tokenConfig builds a client config that authenticates with a bearer
// token rather than a certificate.
func tokenConfig(host, token string) *rest.Config {
	return &rest.Config{
		Host:            host,
		BearerToken:     token,
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
