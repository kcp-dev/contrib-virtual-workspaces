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
// stands it up: a real kcp, the operator and the virtual workspace as
// local processes. Each test creates its own organization workspace, so
// tests do not see each other.
package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

const exportPath = "root:tenancy:controllers"

// testScheme covers everything the tests touch.
var testScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(scheme.AddToScheme(testScheme))
	utilruntime.Must(corev1alpha1.AddToScheme(testScheme))
	utilruntime.Must(kcptenancyv1alpha1.AddToScheme(testScheme))
	utilruntime.Must(tenancyv1alpha1.AddToScheme(testScheme))
}

// adminRestConfig returns the kcp admin config from $KUBECONFIG, pointed
// at the given absolute workspace path.
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

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// organization creates a fresh org workspace under root, binds the tenancy
// APIExport into it with the workspace claim accepted, and returns its
// absolute path. Cleaned up with the test.
func organization(t *testing.T, ctx context.Context) string {
	t.Helper()

	name := "e2e-org-" + randomSuffix(t)
	root := adminClient(t, "root")

	ws := &kcptenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := root.Create(ctx, ws); err != nil {
		t.Fatalf("create org workspace: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = root.Delete(cleanupCtx, &kcptenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name}})
	})

	waitFor(t, ctx, "org workspace "+name+" ready", func(ctx context.Context) (bool, error) {
		var current kcptenancyv1alpha1.Workspace
		if err := root.Get(ctx, client.ObjectKey{Name: name}, &current); err != nil {
			return false, nil
		}
		return current.Status.Phase == corev1alpha1.LogicalClusterPhaseReady, nil
	})

	orgPath := "root:" + name
	bindTenancyExport(t, ctx, orgPath)
	return orgPath
}

var (
	apiBindingGVR        = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
	apiExportGVR         = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	apiResourceSchemaGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
)

// dynamicFor returns a dynamic client for one workspace path.
func dynamicFor(t *testing.T, workspacePath string) dynamic.Interface {
	t.Helper()
	dyn, err := dynamic.NewForConfig(adminRestConfig(t, workspacePath))
	if err != nil {
		t.Fatalf("build dynamic client for %s: %v", workspacePath, err)
	}
	return dyn
}

// bindTenancyExport binds the tenancy APIExport into the workspace,
// accepting every claim the export asks for (mirrored verbatim, so the
// per-deployment identity hash init injected comes along), and waits until
// the bound resources are usable.
func bindTenancyExport(t *testing.T, ctx context.Context, workspacePath string) {
	t.Helper()

	export, err := dynamicFor(t, exportPath).Resource(apiExportGVR).Get(ctx, "tenancy.contrib.kcp.io", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read the tenancy APIExport: %v", err)
	}
	exportClaims, _, err := unstructured.NestedSlice(export.Object, "spec", "permissionClaims")
	if err != nil {
		t.Fatalf("read export claims: %v", err)
	}
	acceptedClaims := make([]any, 0, len(exportClaims))
	for _, c := range exportClaims {
		claim, ok := c.(map[string]any)
		if !ok {
			t.Fatalf("unexpected claim shape: %#v", c)
		}
		claim["state"] = "Accepted"
		claim["selector"] = map[string]any{"matchAll": true}
		acceptedClaims = append(acceptedClaims, claim)
	}

	dyn, err := dynamic.NewForConfig(adminRestConfig(t, workspacePath))
	if err != nil {
		t.Fatalf("build dynamic client: %v", err)
	}

	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": "tenancy.contrib.kcp.io"},
		"spec": map[string]any{
			"reference": map[string]any{
				"export": map[string]any{
					"path": exportPath,
					"name": "tenancy.contrib.kcp.io",
				},
			},
			"permissionClaims": acceptedClaims,
		},
	}}

	if _, err := dyn.Resource(apiBindingGVR).Create(ctx, binding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create APIBinding in %s: %v", workspacePath, err)
	}

	// Bound and claims applied: the practical signal is that a Tenant list
	// succeeds in the workspace.
	org := adminClient(t, workspacePath)
	waitFor(t, ctx, "tenancy API usable in "+workspacePath, func(ctx context.Context) (bool, error) {
		var tenants tenancyv1alpha1.TenantList
		if err := org.List(ctx, &tenants); err != nil {
			return false, nil
		}
		return true, nil
	})
}

// createTenant creates a Tenant and waits for it to become Ready.
func createTenant(t *testing.T, ctx context.Context, org client.Client, name, displayName string) *tenancyv1alpha1.Tenant {
	t.Helper()

	tenant := &tenancyv1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       tenancyv1alpha1.TenantSpec{DisplayName: displayName},
	}
	if err := org.Create(ctx, tenant); err != nil {
		t.Fatalf("create tenant %s: %v", name, err)
	}

	waitFor(t, ctx, "tenant "+name+" ready", func(ctx context.Context) (bool, error) {
		if err := org.Get(ctx, client.ObjectKey{Name: name}, tenant); err != nil {
			return false, nil
		}
		return tenant.Status.Phase == tenancyv1alpha1.PhaseReady, nil
	})

	if tenant.Status.Workspace == "" || tenant.Status.WorkspaceCluster == "" || tenant.Status.URL == "" {
		t.Fatalf("tenant %s is Ready but its status is incomplete: %+v", name, tenant.Status)
	}
	return tenant
}

// createProject creates a Project in the tenant and waits for Ready.
func createProject(t *testing.T, ctx context.Context, org client.Client, name, tenantName, displayName string) *tenancyv1alpha1.Project {
	t.Helper()

	project := &tenancyv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       tenancyv1alpha1.ProjectSpec{Tenant: tenantName, DisplayName: displayName},
	}
	if err := org.Create(ctx, project); err != nil {
		t.Fatalf("create project %s: %v", name, err)
	}

	waitFor(t, ctx, "project "+name+" ready", func(ctx context.Context) (bool, error) {
		if err := org.Get(ctx, client.ObjectKey{Name: name}, project); err != nil {
			return false, nil
		}
		return project.Status.Phase == tenancyv1alpha1.PhaseReady, nil
	})
	return project
}

// createMembership creates a Membership and waits for Ready.
func createMembership(t *testing.T, ctx context.Context, org client.Client, name string, spec tenancyv1alpha1.MembershipSpec) *tenancyv1alpha1.Membership {
	t.Helper()

	membership := &tenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       spec,
	}
	if err := org.Create(ctx, membership); err != nil {
		t.Fatalf("create membership %s: %v", name, err)
	}

	waitFor(t, ctx, "membership "+name+" applied", func(ctx context.Context) (bool, error) {
		if err := org.Get(ctx, client.ObjectKey{Name: name}, membership); err != nil {
			return false, nil
		}
		return membership.Status.Phase == tenancyv1alpha1.PhaseReady, nil
	})
	return membership
}

// workspaceClient returns a client for a provisioned workspace, addressed
// by the URL the operator reported.
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

// userVWConfig returns a rest config for the tenancy virtual workspace,
// authenticated as one of the harness-minted users (alice, bob, mallory).
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
			// The VW serves a self-signed cert in this harness.
			Insecure: true,
			CertFile: filepath.Join(pki, username+".crt"),
			KeyFile:  filepath.Join(pki, username+".key"),
		},
	}
}

// selfTenancyReview asks the VW who the given user is, retrying while the
// directory catches up with recently created objects, until check accepts
// the answer.
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
