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

// Package e2e tests the marketplace stack the way hack/ci/run-e2e-tests.sh
// stands it up: a real kcp, `marketplace-vw init` (twice), the operator
// and the virtual workspace as local processes.
//
// The tiers and the grants are the point of most of these tests: where
// bindings land and where they don't, whether `bind` follows the Offering,
// and whether the VW's enablement path really is nothing but a relayed
// SubjectAccessReview.
package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/offering"
	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

// The three workspaces init creates.
const (
	exportsPath   = "root:marketplace:controllers"
	catalogPath   = "root:marketplace:catalog"
	producersPath = "root:marketplace:producers"
)

// The three exports, by the name their APIBinding takes.
const (
	exportCatalog     = "marketplace-catalog"
	exportPublish     = "marketplace-publish"
	exportProvisioner = "marketplace-provisioner"
)

var testScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(scheme.AddToScheme(testScheme))
	utilruntime.Must(corev1alpha1.AddToScheme(testScheme))
	utilruntime.Must(kcptenancyv1alpha1.AddToScheme(testScheme))
	utilruntime.Must(apisv1alpha1.AddToScheme(testScheme))
	utilruntime.Must(apisv1alpha2.AddToScheme(testScheme))
	utilruntime.Must(marketplacev1alpha1.AddToScheme(testScheme))
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

// userVWConfig authenticates as one of the PKI users against the virtual
// workspace, at RootPath or a per-cluster path below it.
func userVWConfig(t *testing.T, user, subPath string) *rest.Config {
	t.Helper()
	base := os.Getenv("MARKETPLACE_VW_URL")
	if base == "" {
		t.Fatal("MARKETPLACE_VW_URL is not set; run this through hack/ci/run-e2e-tests.sh")
	}
	pki := os.Getenv("MARKETPLACE_PKI_DIR")
	if pki == "" {
		t.Fatal("MARKETPLACE_PKI_DIR is not set; run this through hack/ci/run-e2e-tests.sh")
	}
	return &rest.Config{
		Host: strings.TrimSuffix(base, "/") + "/services/marketplace" + subPath,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true, // the VW's self-signed serving cert; identity is the client cert
			CertFile: filepath.Join(pki, user+".crt"),
			KeyFile:  filepath.Join(pki, user+".key"),
		},
	}
}

// userKcpConfig authenticates as one of the PKI users straight at kcp,
// pointed at a workspace path — how a real user reaches the workspace a
// tenancy system granted them.
func userKcpConfig(t *testing.T, user, workspacePath string) *rest.Config {
	t.Helper()
	pki := os.Getenv("MARKETPLACE_PKI_DIR")
	if pki == "" {
		t.Fatal("MARKETPLACE_PKI_DIR is not set; run this through hack/ci/run-e2e-tests.sh")
	}
	cfg := adminRestConfig(t, workspacePath)
	return &rest.Config{
		Host: cfg.Host,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
			CertFile: filepath.Join(pki, user+".crt"),
			KeyFile:  filepath.Join(pki, user+".key"),
		},
	}
}

func userVWClient(t *testing.T, user, subPath string) client.Client {
	t.Helper()
	c, err := client.New(userVWConfig(t, user, subPath), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("build VW client for %s: %v", user, err)
	}
	return c
}

func waitFor(t *testing.T, ctx context.Context, what string, cond func(ctx context.Context) (bool, error)) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 3*time.Minute, true, cond); err != nil {
		t.Fatalf("timed out waiting for %s: %v", what, err)
	}
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// createWorkspace creates a workspace under root with the given type and
// waits for readiness (which, for typed workspaces, includes kcp having
// installed the type's default bindings). Cleaned up with the test.
func createWorkspace(t *testing.T, ctx context.Context, name, typeName, typePath string) *kcptenancyv1alpha1.Workspace {
	t.Helper()
	root := adminClient(t, "root")
	ws := &kcptenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if typeName != "" {
		ws.Spec.Type = &kcptenancyv1alpha1.WorkspaceTypeReference{
			Name: kcptenancyv1alpha1.WorkspaceTypeName(typeName),
			Path: typePath,
		}
	}
	if err := root.Create(ctx, ws); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create workspace %s: %v", name, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = root.Delete(cleanupCtx, &kcptenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name}})
	})
	waitFor(t, ctx, "workspace "+name+" ready", func(ctx context.Context) (bool, error) {
		if err := root.Get(ctx, client.ObjectKey{Name: name}, ws); err != nil {
			return false, nil
		}
		return ws.Status.Phase == corev1alpha1.LogicalClusterPhaseReady && ws.Spec.Cluster != "", nil
	})
	return ws
}

// registerProducer creates a Provider record in the catalog and, for mode
// provisioned, waits for its workspace. Cleaned up with the test.
func registerProducer(t *testing.T, ctx context.Context, name string, mode marketplacev1alpha1.ProducerMode, workspacePath string) *marketplacev1alpha1.Producer {
	t.Helper()
	catalog := adminClient(t, catalogPath)
	provider := &marketplacev1alpha1.Producer{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: marketplacev1alpha1.ProducerSpec{
			DisplayName:   strings.ToUpper(name[:1]) + name[1:],
			Mode:          mode,
			WorkspacePath: workspacePath,
		},
	}
	if err := catalog.Create(ctx, provider); err != nil {
		t.Fatalf("create provider %s: %v", name, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = catalog.Delete(cleanupCtx, &marketplacev1alpha1.Producer{ObjectMeta: metav1.ObjectMeta{Name: name}})
	})
	if mode == marketplacev1alpha1.ProducerModeProvisioned {
		waitFor(t, ctx, "provider "+name+" ready", func(ctx context.Context) (bool, error) {
			if err := catalog.Get(ctx, client.ObjectKey{Name: name}, provider); err != nil {
				return false, nil
			}
			return provider.Status.Phase == marketplacev1alpha1.PhaseReady &&
				provider.Status.WorkspaceCluster != "", nil
		})
	}
	return provider
}

// thingSchemaJSON is the minimal schema the test provider's API carries.
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

// installThingsExport stands up the test provider's own API in its
// workspace: a schema and an APIExport named exportName that also declares
// a tenant-side claim on configmaps — the consent surface the
// offering scenarios exercise.
func installThingsExport(t *testing.T, ctx context.Context, c client.Client, exportName string) {
	t.Helper()
	group := strings.SplitN(exportName, ".", 2)[1]
	schemaName := "v1alpha1." + exportName

	sch := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{Name: schemaName},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "things", Singular: "thing", Kind: "Thing", ListKind: "ThingList",
			},
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apisv1alpha1.APIResourceVersion{{
				Name: "v1alpha1", Served: true, Storage: true,
				Schema: runtime.RawExtension{Raw: []byte(thingSchemaJSON)},
			}},
		},
	}
	if err := c.Create(ctx, sch); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create schema: %v", err)
	}

	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: exportName},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{{
				Name: "things", Group: group, Schema: schemaName,
				Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
			}},
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"}, Verbs: []string{"get", "list", "watch"}},
			},
		},
	}
	if err := c.Create(ctx, export); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create export: %v", err)
	}
}

// publishExport puts the offering annotations on an existing APIExport and
// waits for the bind grant that publishing materializes.
//
// There is no object to wait on and no status to poll: the export IS the
// offering. What tells us the operator has seen it is the one thing
// publishing produces — `bind` on that export.
func publishExport(t *testing.T, ctx context.Context, c client.Client, exportName, displayName string) *apisv1alpha2.APIExport {
	t.Helper()
	export := annotateExport(t, ctx, c, exportName, map[string]string{
		marketplacev1alpha1.AnnotationPublish:     "true",
		marketplacev1alpha1.AnnotationDisplayName: displayName,
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = unannotate(cleanupCtx, c, exportName)
	})
	waitFor(t, ctx, "the bind grant for "+exportName, func(ctx context.Context) (bool, error) {
		var role rbacv1.ClusterRole
		err := c.Get(ctx, client.ObjectKey{Name: "marketplace:bind:" + exportName}, &role)
		return err == nil, nil
	})
	return export
}

// unpublishExport removes the publish annotation, which is how an offering
// comes off the catalog.
func unpublishExport(t *testing.T, ctx context.Context, c client.Client, exportName string) {
	t.Helper()
	if err := unannotate(ctx, c, exportName); err != nil {
		t.Fatalf("unpublish %s: %v", exportName, err)
	}
}

// annotateExport merges annotations onto an export, retrying the read the
// update raced.
func annotateExport(t *testing.T, ctx context.Context, c client.Client, exportName string, annotations map[string]string) *apisv1alpha2.APIExport {
	t.Helper()
	var export apisv1alpha2.APIExport
	waitFor(t, ctx, "annotate export "+exportName, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, client.ObjectKey{Name: exportName}, &export); err != nil {
			return false, nil
		}
		if export.Annotations == nil {
			export.Annotations = map[string]string{}
		}
		for k, v := range annotations {
			export.Annotations[k] = v
		}
		return c.Update(ctx, &export) == nil, nil
	})
	return &export
}

func unannotate(ctx context.Context, c client.Client, exportName string) error {
	var export apisv1alpha2.APIExport
	if err := c.Get(ctx, client.ObjectKey{Name: exportName}, &export); err != nil {
		return client.IgnoreNotFound(err)
	}
	delete(export.Annotations, marketplacev1alpha1.AnnotationPublish)
	return c.Update(ctx, &export)
}

// acceptedClaimsFrom mirrors, on the consumer side, the claim surface the
// catalog advertises — what a consumer consents to when they enable.
//
// It projects the export's own permission claims with the same function
// the catalog serves them through, so the test accepts exactly what a
// person reading the catalog would have been shown.
func acceptedClaimsFrom(export *apisv1alpha2.APIExport) []apisv1alpha2.AcceptablePermissionClaim {
	var out []apisv1alpha2.AcceptablePermissionClaim
	for _, claim := range offering.Claims(export.Spec.PermissionClaims) {
		selector := apisv1alpha2.PermissionClaimSelector{MatchAll: true}
		if len(claim.MatchLabels) > 0 {
			selector = apisv1alpha2.PermissionClaimSelector{
				LabelSelector: metav1.LabelSelector{MatchLabels: claim.MatchLabels},
			}
		}
		out = append(out, apisv1alpha2.AcceptablePermissionClaim{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{Group: claim.Group, Resource: claim.Resource},
					Verbs:         claim.Verbs,
					IdentityHash:  claim.IdentityHash,
				},
				Selector: selector,
			},
			State: apisv1alpha2.ClaimAccepted,
		})
	}
	return out
}

// bindingFor is the APIBinding enabling an offering means: a plain kcp
// bind, authored by the consumer, referencing the published export with
// its claims accepted.
func bindingFor(name, producerPath string, export *apisv1alpha2.APIExport) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: producerPath,
					Name: export.Name,
				},
			},
			PermissionClaims: acceptedClaimsFrom(export),
		},
	}
}

// bindOffering enables natively as whoever c authenticates as, and waits
// for the binding to be bound. Cleaned up with the test.
func bindOffering(t *testing.T, ctx context.Context, c client.Client, name, producerPath string, export *apisv1alpha2.APIExport) {
	t.Helper()
	binding := bindingFor(name, producerPath, export)
	waitFor(t, ctx, "binding "+name+" to be accepted", func(ctx context.Context) (bool, error) {
		err := c.Create(ctx, binding.DeepCopy())
		return err == nil || apierrors.IsAlreadyExists(err), nil
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = c.Delete(cleanupCtx, &apisv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: name}})
	})
	waitFor(t, ctx, "binding "+name+" to be bound", func(ctx context.Context) (bool, error) {
		var got apisv1alpha2.APIBinding
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &got); err != nil {
			return false, nil
		}
		return got.Status.Phase == apisv1alpha2.APIBindingPhaseBound, nil
	})
}

// hasBinding reports whether the workspace has an APIBinding REFERENCING
// the named export. The reference, not the binding's name, is what decides
// which export serves here — name heuristics made "marketplace-catalog"
// look like a binding of "marketplace" and inverted an assertion.
func hasBinding(t *testing.T, ctx context.Context, c client.Client, exportName string) bool {
	t.Helper()
	var bindings apisv1alpha2.APIBindingList
	if err := c.List(ctx, &bindings); err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	for _, b := range bindings.Items {
		if b.Spec.Reference.Export != nil && b.Spec.Reference.Export.Name == exportName {
			return true
		}
	}
	return false
}

// grantWorkspaceAdmin gives a user full RBAC plus kcp's workspace `access`
// verb in one workspace — the shape a tenancy system would materialize for
// an admin Membership, minted by hand here because this suite deliberately
// runs without one.
func grantWorkspaceAdmin(t *testing.T, ctx context.Context, c client.Client, user string) {
	t.Helper()
	suffix := randomSuffix(t)
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-admin-" + user + "-" + suffix},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}},
			{NonResourceURLs: []string{"/"}, Verbs: []string{"access"}},
		},
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: user}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
	}
	for _, obj := range []client.Object{role, binding} {
		if err := c.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("grant admin to %s: %v", user, err)
		}
	}
}
