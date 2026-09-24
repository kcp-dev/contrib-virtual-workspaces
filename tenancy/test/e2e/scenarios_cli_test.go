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
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// tenancyctl runs the CLI against the harness's kcp and returns its output.
func tenancyctl(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()

	bin := os.Getenv("TENANCYCTL_BIN")
	if bin == "" {
		t.Fatal("TENANCYCTL_BIN is not set; run this through hack/ci/run-e2e-tests.sh")
	}

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+os.Getenv("KUBECONFIG"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tenancyctl %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// tenancyctlStdin runs the CLI with something piped to stdin.
func tenancyctlStdin(t *testing.T, ctx context.Context, stdin string, args ...string) string {
	t.Helper()

	bin := os.Getenv("TENANCYCTL_BIN")
	if bin == "" {
		t.Fatal("TENANCYCTL_BIN is not set; run this through hack/ci/run-e2e-tests.sh")
	}
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+os.Getenv("KUBECONFIG"))
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tenancyctl %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// The CLI agrees the store is bound, which is one of the two tiers init
// binds by hand.
func TestScenarioTenancyctlSeesTheStoreWorkspace(t *testing.T) {
	ctx := testContext(t)

	out := tenancyctl(t, ctx, "org", "list")
	if !strings.Contains(out, "bound to the tenancy-platform export") {
		t.Errorf("org list did not report %s as bound: %s", storePath, out)
	}
}

// The whole administrator path through the CLI: create a tenant and a
// project, grant a user, and see the grant materialize as RBAC.
func TestScenarioTenancyctlDrivesTheModel(t *testing.T) {
	ctx := testContext(t)

	suffix := randomSuffix(t)
	tenant := "cli-" + suffix
	project := "web-" + suffix

	tenancyctl(t, ctx, "tenant", "create", tenant, "--display-name", "CLI Corp")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		bin := os.Getenv("TENANCYCTL_BIN")
		_ = exec.CommandContext(cleanupCtx, bin, "tenant", "delete", tenant).Run()
	})

	out := tenancyctl(t, ctx, "tenant", "list")
	if !strings.Contains(out, tenant) {
		t.Fatalf("tenant list does not mention %s: %s", tenant, out)
	}

	// project and grant address the tenant's OWN workspace, which the CLI
	// resolves by following the Tenant's reported URL.
	tenancyctl(t, ctx, "project", "create", project, "--tenant", tenant, "--display-name", "Web")
	tenancyctl(t, ctx, "grant", "alice", "--role", "admin", "--tenant", tenant)

	// Re-granting the same access is a no-op rather than a second object.
	again := tenancyctl(t, ctx, "grant", "alice", "--role", "admin", "--tenant", tenant)
	if !strings.Contains(again, "already grants") {
		t.Errorf("re-granting should be idempotent, got: %s", again)
	}
	// ...and changing the role updates in place.
	changed := tenancyctl(t, ctx, "grant", "alice", "--role", "edit", "--tenant", tenant)
	if !strings.Contains(changed, "changed from admin to edit") {
		t.Errorf("expected a role change, got: %s", changed)
	}

	// The grant materializes in the PROJECT workspace, not the tenant one.
	store := storeClient(t)
	var tenantObj tenancyv1alpha1.Tenant
	if err := store.Get(ctx, client.ObjectKey{Name: tenant}, &tenantObj); err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	tenantWS := workspaceClient(t, tenantObj.Status.URL)

	var projectObj tenancyv1alpha1.Project
	waitFor(t, ctx, "the CLI's project to report its workspace", func(ctx context.Context) (bool, error) {
		if err := tenantWS.Get(ctx, client.ObjectKey{Name: project}, &projectObj); err != nil {
			return false, nil
		}
		return projectObj.Status.URL != "", nil
	})
	projectWS := workspaceClient(t, projectObj.Status.URL)

	waitFor(t, ctx, "the CLI's grant to reach the project workspace", func(ctx context.Context) (bool, error) {
		crb, err := getBinding(ctx, projectWS, "alice-"+tenant)
		if err != nil {
			return false, nil
		}
		return strings.HasSuffix(crb.RoleRef.Name, ":edit"), nil
	})

	// Revoking removes it again.
	tenancyctl(t, ctx, "revoke", "alice", "--tenant", tenant)
	waitFor(t, ctx, "the CLI's revoke to remove the binding", func(ctx context.Context) (bool, error) {
		_, err := getBinding(ctx, projectWS, "alice-"+tenant)
		return err != nil, nil
	})
}

// The user-facing half: whoami and kubeconfig speak to the virtual
// workspace with nothing but a client certificate.
func TestScenarioTenancyctlWhoamiAndKubeconfig(t *testing.T) {
	ctx := testContext(t)

	suffix := randomSuffix(t)
	tenant := "who-" + suffix

	tenancyctl(t, ctx, "tenant", "create", tenant, "--display-name", "Whoami Corp")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		bin := os.Getenv("TENANCYCTL_BIN")
		_ = exec.CommandContext(cleanupCtx, bin, "tenant", "delete", tenant).Run()
	})
	// A grant needs somewhere to land, and that is a project workspace.
	tenancyctl(t, ctx, "project", "create", "web-"+suffix, "--tenant", tenant, "--display-name", "Web")
	tenancyctl(t, ctx, "grant", "bob", "--role", "view", "--tenant", tenant)

	pki := os.Getenv("TENANCY_PKI_DIR")
	vwURL := os.Getenv("TENANCY_VW_URL")
	if pki == "" || vwURL == "" {
		t.Fatal("TENANCY_PKI_DIR / TENANCY_VW_URL not set; run through hack/ci/run-e2e-tests.sh")
	}
	userArgs := []string{
		"--url", vwURL,
		"--client-cert", filepath.Join(pki, "bob.crt"),
		"--client-key", filepath.Join(pki, "bob.key"),
		"--insecure-skip-tls-verify",
	}

	// The directory is informer-fed, so the grant takes a moment to show.
	var whoami string
	waitFor(t, ctx, "bob's review to mention "+tenant, func(ctx context.Context) (bool, error) {
		out, err := exec.CommandContext(ctx, os.Getenv("TENANCYCTL_BIN"),
			append([]string{"whoami"}, userArgs...)...).CombinedOutput()
		if err != nil {
			return false, nil
		}
		whoami = string(out)
		return strings.Contains(whoami, tenant), nil
	})
	if !strings.Contains(whoami, "view") {
		t.Errorf("whoami does not show the view role: %s", whoami)
	}

	// And the same answer becomes a kubeconfig.
	out := filepath.Join(t.TempDir(), "bob.kubeconfig")
	tenancyctl(t, ctx, append([]string{"kubeconfig", "--output", out}, userArgs...)...)

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read generated kubeconfig: %v", err)
	}
	for _, want := range []string{tenant, "client-certificate", "clusters:"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("generated kubeconfig has no %q:\n%s", want, raw)
		}
	}
}
