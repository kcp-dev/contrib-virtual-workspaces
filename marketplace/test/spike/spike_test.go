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

// Package spike proves spike #1 of ../../DESIGN.md before any of the
// marketplace is built: the binder composition.
//
// An identity ("mkt-operator") that holds
//
//   - the apibindings.apis.kcp.io permission claim in a consumer workspace
//     (through a capability APIExport bound there), and
//   - a granted `bind` verb on ONE provider's APIExport, in that
//     provider's workspace,
//
// can create, in the consumer workspace, a working APIBinding to that
// provider's export — and is refused for a second provider whose workspace
// granted it nothing, and has no standing in the consumer workspace
// outside the claim. If any of that fails, the marketplace design falls
// back to a privileged enabler, so this test is the design's keystone.
//
// Run through marketplace/hack/ci/run-spike.sh.
package spike

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// The workspaces the spike stands up, all directly under root. The harness
// wipes kcp between runs, so the names are fixed.
const (
	mktPath       = "root:spike-mkt"        // holds the binder export
	providerAPath = "root:spike-provider-a" // grants the operator `bind`
	providerBPath = "root:spike-provider-b" // grants nothing
	consumerPath  = "root:spike-consumer"   // binds the binder export
)

const (
	binderExport = "marketplace-binder"
	operatorUser = "mkt-operator"

	thingsExport  = "things.spike.contrib.kcp.io"
	gadgetsExport = "gadgets.spike.contrib.kcp.io"
)

// binderClaimVerbs is what the marketplace design claims: enough to
// enable, inspect and disable, and nothing else. Delete is load-bearing —
// an enable that cannot be undone is not a capability, it is a ratchet.
var binderClaimVerbs = []string{"get", "list", "watch", "create", "delete"}

var testScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(scheme.AddToScheme(testScheme))
	utilruntime.Must(apisv1alpha1.AddToScheme(testScheme))
	utilruntime.Must(apisv1alpha2.AddToScheme(testScheme))
	utilruntime.Must(corev1alpha1.AddToScheme(testScheme))
	utilruntime.Must(kcptenancyv1alpha1.AddToScheme(testScheme))
}

func adminRestConfig(t *testing.T, workspacePath string) *rest.Config {
	t.Helper()
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		t.Fatal("KUBECONFIG is not set; run this through marketplace/hack/ci/run-spike.sh")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("load kubeconfig: %v", err)
	}
	cfg.Host = hostForPath(cfg.Host, workspacePath)
	return cfg
}

func hostForPath(host, workspacePath string) string {
	if i := strings.Index(host, "/clusters/"); i >= 0 {
		host = host[:i]
	}
	if workspacePath == "" {
		return host
	}
	return strings.TrimSuffix(host, "/") + "/clusters/" + workspacePath
}

func adminClient(t *testing.T, workspacePath string) client.Client {
	t.Helper()
	c, err := client.New(adminRestConfig(t, workspacePath), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("build client for %s: %v", workspacePath, err)
	}
	return c
}

// operatorRestConfig authenticates as mkt-operator with the client
// certificate the harness minted, against the given absolute URL.
func operatorRestConfig(t *testing.T, host string) *rest.Config {
	t.Helper()
	pki := os.Getenv("MKT_PKI_DIR")
	if pki == "" {
		t.Fatal("MKT_PKI_DIR is not set; run this through marketplace/hack/ci/run-spike.sh")
	}
	return &rest.Config{
		Host: host,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true, // kcp's self-signed serving cert; identity is OUR cert below
			CertFile: filepath.Join(pki, operatorUser+".crt"),
			KeyFile:  filepath.Join(pki, operatorUser+".key"),
		},
	}
}

func operatorClient(t *testing.T, host string) client.Client {
	t.Helper()
	c, err := client.New(operatorRestConfig(t, host), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("build operator client for %s: %v", host, err)
	}
	return c
}

func waitFor(t *testing.T, ctx context.Context, what string, cond func(ctx context.Context) (bool, error)) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 3*time.Minute, true, cond); err != nil {
		t.Fatalf("timed out waiting for %s: %v", what, err)
	}
}

// createWorkspace creates a child workspace under root and waits for it to
// be ready, returning its logical cluster id.
func createWorkspace(t *testing.T, ctx context.Context, name string) string {
	t.Helper()
	root := adminClient(t, "root")
	ws := &kcptenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := root.Create(ctx, ws); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create workspace %s: %v", name, err)
	}
	waitFor(t, ctx, "workspace "+name+" ready", func(ctx context.Context) (bool, error) {
		if err := root.Get(ctx, client.ObjectKey{Name: name}, ws); err != nil {
			return false, nil
		}
		return ws.Status.Phase == corev1alpha1.LogicalClusterPhaseReady && ws.Spec.Cluster != "", nil
	})
	return ws.Spec.Cluster
}

// thingSchemaJSON is the minimal OpenAPI schema an APIResourceSchema
// version carries. One string field is enough to prove the API serves.
const thingSchemaJSON = `{
  "type": "object",
  "properties": {
    "apiVersion": {"type": "string"},
    "kind": {"type": "string"},
    "metadata": {"type": "object"},
    "spec": {
      "type": "object",
      "properties": {"message": {"type": "string"}}
    }
  }
}`

// installProviderExport stands up one provider: an APIResourceSchema, an
// APIExport serving it, and — when granted — the `bind` ClusterRole that
// is the marketplace's registration handshake.
func installProviderExport(t *testing.T, ctx context.Context, workspacePath, exportName, plural, kind string, grantBindToOperator bool) {
	t.Helper()
	c := adminClient(t, workspacePath)

	schemaName := "v1alpha1." + exportName
	group := strings.SplitN(exportName, ".", 2)[1]

	sch := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{Name: schemaName},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   plural,
				Singular: strings.ToLower(kind),
				Kind:     kind,
				ListKind: kind + "List",
			},
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apisv1alpha1.APIResourceVersion{{
				Name:    "v1alpha1",
				Served:  true,
				Storage: true,
				Schema:  runtime.RawExtension{Raw: []byte(thingSchemaJSON)},
			}},
		},
	}
	if err := c.Create(ctx, sch); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create schema in %s: %v", workspacePath, err)
	}

	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: exportName},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{{
				Name:   plural,
				Group:  group,
				Schema: schemaName,
				Storage: apisv1alpha2.ResourceSchemaStorage{
					CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
				},
			}},
		},
	}
	if err := c.Create(ctx, export); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create export in %s: %v", workspacePath, err)
	}

	if !grantBindToOperator {
		return
	}
	// The registration handshake: this grant, and nothing else, is what
	// lets the operator later bind consumers to THIS export.
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "marketplace:bind:" + exportName},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{"apis.kcp.io"},
			Resources:     []string{"apiexports"},
			ResourceNames: []string{exportName},
			Verbs:         []string{"bind"},
		}},
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "marketplace:bind:" + exportName + ":" + operatorUser},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: operatorUser, APIGroup: rbacv1.GroupName}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
	}
	for _, obj := range []client.Object{role, binding} {
		if err := c.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("create bind RBAC in %s: %v", workspacePath, err)
		}
	}
}

func TestSpikeBinderComposition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()

	t.Log("creating the four workspaces")
	createWorkspace(t, ctx, "spike-mkt")
	createWorkspace(t, ctx, "spike-provider-a")
	createWorkspace(t, ctx, "spike-provider-b")
	consumerCluster := createWorkspace(t, ctx, "spike-consumer")

	t.Log("installing the binder export in " + mktPath)
	mkt := adminClient(t, mktPath)
	binder := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: binderExport},
		Spec: apisv1alpha2.APIExportSpec{
			// Deliberately NO resources: the whole export is the claim.
			PermissionClaims: []apisv1alpha2.PermissionClaim{{
				GroupResource: apisv1alpha2.GroupResource{Group: "apis.kcp.io", Resource: "apibindings"},
				Verbs:         binderClaimVerbs,
			}},
		},
	}
	if err := mkt.Create(ctx, binder); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create binder export: %v", err)
	}

	// The operator reaches the binder's virtual workspace through
	// apiexports/content, verb-for-verb with the requests it will make —
	// AND through kcp's workspace-content gate: RBAC in a workspace means
	// nothing to a subject that cannot enter it, so without the `access`
	// verb the content grant below evaluates to "access denied". Found by
	// running this spike, kept as its first lesson.
	contentRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "marketplace:binder:content"},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"apis.kcp.io"},
				Resources:     []string{"apiexports/content"},
				ResourceNames: []string{binderExport},
				Verbs:         []string{"*"},
			},
			{
				NonResourceURLs: []string{"/"},
				Verbs:           []string{"access"},
			},
		},
	}
	contentBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "marketplace:binder:content:" + operatorUser},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: operatorUser, APIGroup: rbacv1.GroupName}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: contentRole.Name},
	}
	// Who may bind the binder itself: open, as tenancy's rbac-bind is —
	// the claim only takes effect where a consumer accepts it.
	bindRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "marketplace:binder:bind"},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{"apis.kcp.io"},
			Resources:     []string{"apiexports"},
			ResourceNames: []string{binderExport},
			Verbs:         []string{"bind"},
		}},
	}
	bindBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "marketplace:binder:bind:authenticated"},
		Subjects:   []rbacv1.Subject{{Kind: "Group", Name: "system:authenticated", APIGroup: rbacv1.GroupName}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: bindRole.Name},
	}
	for _, obj := range []client.Object{contentRole, contentBinding, bindRole, bindBinding} {
		if err := mkt.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("create binder RBAC: %v", err)
		}
	}

	// The slice is gated on `bind`, and RBAC propagation is asynchronous,
	// so creating it may be refused for a moment.
	slice := &apisv1alpha1.APIExportEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: binderExport},
		Spec: apisv1alpha1.APIExportEndpointSliceSpec{
			APIExport: apisv1alpha1.ExportBindingReference{Name: binderExport},
		},
	}
	waitFor(t, ctx, "endpoint slice creation to be authorized", func(ctx context.Context) (bool, error) {
		err := mkt.Create(ctx, slice.DeepCopy())
		if err == nil || apierrors.IsAlreadyExists(err) {
			return true, nil
		}
		return false, nil
	})

	t.Log("installing the two provider exports")
	installProviderExport(t, ctx, providerAPath, thingsExport, "things", "Thing", true)
	installProviderExport(t, ctx, providerBPath, gadgetsExport, "gadgets", "Gadget", false)

	t.Log("binding the binder into the consumer workspace, claim accepted")
	consumer := adminClient(t, consumerPath)
	consumerBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: binderExport},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{Path: mktPath, Name: binderExport},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{{
				ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
					PermissionClaim: apisv1alpha2.PermissionClaim{
						GroupResource: apisv1alpha2.GroupResource{Group: "apis.kcp.io", Resource: "apibindings"},
						Verbs:         binderClaimVerbs,
					},
					Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
				},
				State: apisv1alpha2.ClaimAccepted,
			}},
		},
	}
	waitFor(t, ctx, "consumer binding of the binder", func(ctx context.Context) (bool, error) {
		err := consumer.Create(ctx, consumerBinding.DeepCopy())
		return err == nil || apierrors.IsAlreadyExists(err), nil
	})

	t.Log("waiting for the binder virtual workspace URL (nudging the slice)")
	var vwURL string
	waitFor(t, ctx, "binder endpoint slice URLs", func(ctx context.Context) (bool, error) {
		got := &apisv1alpha1.APIExportEndpointSlice{}
		if err := mkt.Get(ctx, client.ObjectKey{Name: binderExport}, got); err != nil {
			return false, nil
		}
		if len(got.Status.APIExportEndpoints) > 0 {
			vwURL = got.Status.APIExportEndpoints[0].URL
			return true, nil
		}
		// kcp only recomputes slice URLs when the slice itself is
		// reconciled; a consumer binding is not a slice event. Nudge.
		patched := got.DeepCopy()
		if patched.Annotations == nil {
			patched.Annotations = map[string]string{}
		}
		patched.Annotations["marketplace.contrib.kcp.io/nudged-at"] = time.Now().Format(time.RFC3339Nano)
		_ = mkt.Patch(ctx, patched, client.MergeFrom(got))
		return false, nil
	})

	operatorVW := operatorClient(t, strings.TrimSuffix(vwURL, "/")+"/clusters/"+consumerCluster)

	t.Run("OperatorBindsAGrantedExportThroughTheClaim", func(t *testing.T) {
		enable := &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "things"},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: providerAPath, Name: thingsExport},
				},
			},
		}
		var lastErr error
		waitFor(t, ctx, "operator creating the binding through the claim", func(ctx context.Context) (bool, error) {
			lastErr = operatorVW.Create(ctx, enable.DeepCopy())
			return lastErr == nil || apierrors.IsAlreadyExists(lastErr), nil
		})
		t.Logf("bound (last error before success: %v)", lastErr)

		// Proof the binding is real: the consumer workspace now serves the
		// provider's API and an object can be created there.
		dyn, err := dynamic.NewForConfig(adminRestConfig(t, consumerPath))
		if err != nil {
			t.Fatalf("dynamic client: %v", err)
		}
		thingGVR := schema.GroupVersionResource{Group: "spike.contrib.kcp.io", Version: "v1alpha1", Resource: "things"}
		thing := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "spike.contrib.kcp.io/v1alpha1",
			"kind":       "Thing",
			"metadata":   map[string]interface{}{"name": "proof"},
			"spec":       map[string]interface{}{"message": "the binder composition works"},
		}}
		waitFor(t, ctx, "the bound API to serve", func(ctx context.Context) (bool, error) {
			_, err := dyn.Resource(thingGVR).Create(ctx, thing, metav1.CreateOptions{})
			return err == nil || apierrors.IsAlreadyExists(err), nil
		})
	})

	t.Run("OperatorIsRefusedForAnUngrantedExport", func(t *testing.T) {
		enable := &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "gadgets"},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: providerBPath, Name: gadgetsExport},
				},
			},
		}
		waitFor(t, ctx, "the ungranted bind to be refused for the right reason", func(ctx context.Context) (bool, error) {
			err := operatorVW.Create(ctx, enable.DeepCopy())
			if err == nil {
				t.Fatal("binding an ungranted export SUCCEEDED; the bind check did not hold")
			}
			return strings.Contains(err.Error(), "no permission to bind to export"), nil
		})
	})

	t.Run("OperatorHasNoStandingOutsideTheClaim", func(t *testing.T) {
		// Through the virtual workspace, only the claimed resource exists:
		// a ConfigMap write must be refused (the VW has no such route).
		dynVW, err := dynamic.NewForConfig(operatorRestConfig(t, strings.TrimSuffix(vwURL, "/")+"/clusters/"+consumerCluster))
		if err != nil {
			t.Fatalf("dynamic client: %v", err)
		}
		cmGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
		cm := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]interface{}{"name": "should-not-exist", "namespace": "default"},
		}}
		if _, err := dynVW.Resource(cmGVR).Namespace("default").Create(ctx, cm, metav1.CreateOptions{}); err == nil {
			t.Fatal("creating an unclaimed resource through the binder VW succeeded")
		}

		// And the front door proves the claim is the ONLY standing: the
		// same identity, the same verb, the same object, directly against
		// the consumer workspace — refused.
		front := operatorClient(t, hostForPath(adminRestConfig(t, "").Host, consumerPath))
		direct := &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "front-door"},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: providerAPath, Name: thingsExport},
				},
			},
		}
		err = front.Create(ctx, direct)
		if err == nil {
			t.Fatal("the operator could write an APIBinding directly in the consumer workspace; it should have no standing there")
		}
		if !apierrors.IsForbidden(err) {
			t.Fatalf("expected forbidden at the front door, got: %v", err)
		}
	})

	t.Run("DisableRemovesTheBinding", func(t *testing.T) {
		if err := operatorVW.Delete(ctx, &apisv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "things"}}); err != nil {
			t.Fatalf("operator delete through the claim: %v", err)
		}
		waitFor(t, ctx, "the binding to be gone", func(ctx context.Context) (bool, error) {
			got := &apisv1alpha2.APIBinding{}
			err := consumer.Get(ctx, client.ObjectKey{Name: "things"}, got)
			return apierrors.IsNotFound(err), nil
		})
	})

	fmt.Println("SPIKE: the binder composition holds")
}
