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

// Package bootstrap installs the kcp-side objects the tenancy component
// needs: the four APIExports and their endpoint slices, the two
// WorkspaceTypes that bind the capability exports into provisioned
// workspaces, and the platform tier's own bindings.
//
// Workspace-path plumbing is shared with the access component
// (access/pkg/bootstrap); this package only owns the tenancy assets.
package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/config/apiexport"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// The four exports. Each has an endpoint slice of the same name, and the
// operator runs one manager per slice — which is how "this controller may
// only do this" becomes structural rather than conventional.
const (
	// ExportPlatform serves Tenants in the platform workspace.
	ExportPlatform = "tenancy-platform"
	// ExportTenancy serves Projects and Memberships in tenant workspaces.
	ExportTenancy = "tenancy"
	// ExportProvisioner grants the claim to create workspaces.
	ExportProvisioner = "tenancy-provisioner"
	// ExportAccess grants the claims to write inside a project workspace.
	ExportAccess = "tenancy-access"
)

// The workspace types the operator provisions with.
const (
	WorkspaceTypeTenant  = "tenant"
	WorkspaceTypeProject = "project"
)

// Defaults for where things are installed.
const (
	DefaultWorkspacePrefix      = "root:tenancy"
	DefaultControllersWorkspace = "controllers"

	// DefaultStoreWorkspace is the registry: the workspace Tenant records
	// live in. It is deliberately not the workspace they are provisioned
	// under, so nothing that can create a workspace can also rewrite the
	// records that say which workspaces should exist.
	DefaultStoreWorkspace = "store"

	// DefaultTenantsWorkspace is the provisioning parent: every tenant
	// workspace is created as its child. It holds no tenancy objects at
	// all.
	DefaultTenantsWorkspace = "tenants"
)

// NudgeAnnotation is bumped on an endpoint slice to make kcp recompute its
// URLs. kcp's URLs controller runs when the slice object is reconciled, and
// a consumer binding the export is not a slice event, so a slice created
// before its first consumer would otherwise stay URL-less indefinitely.
const NudgeAnnotation = tenancyv1alpha1.GroupName + "/nudged-at"

// Options configures Bootstrap.
type Options struct {
	// ServerUsers and ServerGroups are additional identities granted the
	// server role in the exports workspace — the identities the operator
	// and the virtual workspace authenticate as when they are not this
	// bootstrap's own credential.
	ServerUsers  []string
	ServerGroups []string
}

// Result reports what Bootstrap installed.
type Result struct {
	// ExportsPath is the canonical path of the workspace holding the
	// exports, and ExportsCluster its logical cluster name.
	ExportsPath    string
	ExportsCluster string
	// WorkspacesIdentityHash is the identity of kcp's tenancy.kcp.io
	// export, which every claim on `workspaces` must repeat.
	WorkspacesIdentityHash string
}

// Bootstrap applies the exports workspace's assets into the workspace cfg
// points at. Idempotent: safe to run on every pod start and every upgrade.
func Bootstrap(ctx context.Context, cfg *rest.Config, exportsPath string, opts Options) (*Result, error) {
	logger := klog.FromContext(ctx)

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build dynamic client: %w", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build discovery client: %w", err)
	}
	cache := memory.NewMemCacheClient(disc)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cache)

	// A claim on kcp's own exported types must name that export's identity,
	// and the hash is per install — kcp computes it from the export's
	// identity secret — so it is discovered rather than committed.
	hash, err := exportIdentityHash(ctx, cfg, "root", "tenancy.kcp.io")
	if err != nil {
		return nil, fmt.Errorf("discover the identity hash of kcp's tenancy.kcp.io export: %w", err)
	}

	cluster, err := logicalClusterName(ctx, dyn)
	if err != nil {
		return nil, fmt.Errorf("resolve the logical cluster of %s: %w", exportsPath, err)
	}

	subs := map[string]string{
		apiexport.IdentityHashPlaceholder: hash,
		apiexport.ExportsRefPlaceholder:   cluster,
		apiexport.ExportsPathPlaceholder:  exportsPath,
	}

	logger.Info("installing the tenancy APIExports",
		"assets", len(apiexport.ExportsOrder), "workspace", exportsPath)
	if err := applyAll(ctx, dyn, mapper, cache, apiexport.ExportsOrder, subs); err != nil {
		return nil, err
	}

	if len(opts.ServerUsers)+len(opts.ServerGroups) > 0 {
		kube, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("build kubernetes client: %w", err)
		}
		if err := applyServerIdentity(ctx, kube, opts.ServerUsers, opts.ServerGroups); err != nil {
			return nil, fmt.Errorf("grant server identities: %w", err)
		}
	}

	return &Result{
		ExportsPath:            exportsPath,
		ExportsCluster:         cluster,
		WorkspacesIdentityHash: hash,
	}, nil
}

// BindStore makes the workspace cfg points at the tenant registry: it
// binds tenancy-platform, then waits until Tenants are actually servable.
func BindStore(ctx context.Context, cfg *rest.Config, result *Result) error {
	if err := applyTier(ctx, cfg, result, apiexport.StoreOrder); err != nil {
		return err
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build dynamic client: %w", err)
	}
	klog.FromContext(ctx).Info("waiting for the Tenant API to become servable in the store workspace")
	tenantsGVR := schema.GroupVersionResource{
		Group: tenancyv1alpha1.GroupName, Version: "v1alpha1", Resource: "tenants",
	}
	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		if _, err := dyn.Resource(tenantsGVR).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("the Tenant API did not become usable after binding: %w", err)
	}
	return nil
}

// BindTenantsParent makes the workspace cfg points at the provisioning
// parent: it binds tenancy-provisioner, so tenant workspaces can be created
// as its children.
//
// There is nothing to wait for here — the claim adds no servable API, it
// adds reach — so a readiness probe would have nothing to probe.
func BindTenantsParent(ctx context.Context, cfg *rest.Config, result *Result) error {
	return applyTier(ctx, cfg, result, apiexport.TenantsOrder)
}

// applyTier applies one hand-bound tier's assets.
//
// These two tiers are the only ones bound this way. Every workspace below
// them is bound by its WorkspaceType, because something provisions it.
func applyTier(ctx context.Context, cfg *rest.Config, result *Result, order []string) error {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build dynamic client: %w", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build discovery client: %w", err)
	}
	cache := memory.NewMemCacheClient(disc)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cache)

	subs := map[string]string{
		apiexport.IdentityHashPlaceholder: result.WorkspacesIdentityHash,
		apiexport.ExportsRefPlaceholder:   result.ExportsCluster,
		apiexport.ExportsPathPlaceholder:  result.ExportsPath,
	}
	return applyAll(ctx, dyn, mapper, cache, order, subs)
}

func applyAll(ctx context.Context, dyn dynamic.Interface, mapper *restmapper.DeferredDiscoveryRESTMapper, cache discovery.CachedDiscoveryInterface, order []string, subs map[string]string) error {
	logger := klog.FromContext(ctx)
	for _, filename := range order {
		raw, err := apiexport.FS.ReadFile(filename)
		if err != nil {
			return fmt.Errorf("read embedded asset %s: %w", filename, err)
		}
		for placeholder, value := range subs {
			raw = bytes.ReplaceAll(raw, []byte(placeholder), []byte(value))
		}
		if err := applyManifests(ctx, dyn, mapper, cache, raw); err != nil {
			return fmt.Errorf("apply %s: %w", filename, err)
		}
		logger.V(2).Info("applied", "asset", filename)
	}
	return nil
}

var (
	endpointSliceGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
	}
	apiExportGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports",
	}
	logicalClusterGVR = schema.GroupVersionResource{
		Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
	}
)

// logicalClusterName reads the logical cluster of the workspace the client
// is pointed at. WorkspaceType references resolve against it.
func logicalClusterName(ctx context.Context, dyn dynamic.Interface) (string, error) {
	lc, err := dyn.Resource(logicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if name := lc.GetAnnotations()["kcp.io/cluster"]; name != "" {
		return name, nil
	}
	return "", fmt.Errorf("the LogicalCluster carries no kcp.io/cluster annotation")
}

// exportIdentityHash reads status.identityHash of an APIExport in the given
// workspace, reached by retargeting cfg's host.
func exportIdentityHash(ctx context.Context, cfg *rest.Config, workspacePath, exportName string) (string, error) {
	scoped := rest.CopyConfig(cfg)
	host := scoped.Host
	if i := strings.Index(host, "/clusters/"); i >= 0 {
		host = host[:i]
	}
	scoped.Host = strings.TrimSuffix(host, "/") + "/clusters/" + workspacePath

	dyn, err := dynamic.NewForConfig(scoped)
	if err != nil {
		return "", fmt.Errorf("build dynamic client for %s: %w", workspacePath, err)
	}

	var hash string
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		export, err := dyn.Resource(apiExportGVR).Get(ctx, exportName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		hash, _, _ = unstructured.NestedString(export.Object, "status", "identityHash")
		return hash != "", nil
	})
	if err != nil {
		return "", fmt.Errorf("APIExport %s in %s has no identity hash: %w", exportName, workspacePath, err)
	}
	return hash, nil
}

// applyManifests applies every YAML document in raw, create-or-update.
func applyManifests(ctx context.Context, dyn dynamic.Interface, mapper *restmapper.DeferredDiscoveryRESTMapper, cache discovery.CachedDiscoveryInterface, raw []byte) error {
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj); err != nil {
			if err.Error() == "EOF" {
				return nil
			}
			return fmt.Errorf("decode manifest: %w", err)
		}
		if len(obj.Object) == 0 {
			continue
		}

		gvk := obj.GroupVersionKind()
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			// A kind this bootstrap just installed may not be in the cached
			// discovery yet; refresh once and retry.
			cache.Invalidate()
			mapper.Reset()
			mapping, err = mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			if err != nil {
				return fmt.Errorf("map %s: %w", gvk, err)
			}
		}

		client := dyn.Resource(mapping.Resource)
		if _, err := client.Create(ctx, &obj, metav1.CreateOptions{}); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("create %s %s: %w", gvk.Kind, obj.GetName(), err)
			}
			existing, err := client.Get(ctx, obj.GetName(), metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("get existing %s %s: %w", gvk.Kind, obj.GetName(), err)
			}
			obj.SetResourceVersion(existing.GetResourceVersion())
			if _, err := client.Update(ctx, &obj, metav1.UpdateOptions{}); err != nil {
				// Some kcp objects reject spec updates (APIResourceSchemas
				// are immutable); existing is as good as applied.
				if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
					continue
				}
				return fmt.Errorf("update %s %s: %w", gvk.Kind, obj.GetName(), err)
			}
		}
	}
}

// applyServerIdentity grants the server role to the given identities.
func applyServerIdentity(ctx context.Context, kube kubernetes.Interface, users, groups []string) error {
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: tenancyv1alpha1.GroupName + ":server"},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:           []string{"access"},
				NonResourceURLs: []string{"/"},
			},
			{
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apiexports", "apiexportendpointslices", "apibindings"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				// The operator nudges a URL-less endpoint slice to work
				// around kcp not reconciling slices on new bindings.
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apiexportendpointslices"},
				Verbs:     []string{"patch"},
			},
			{
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apiexports/content"},
				Verbs:     []string{"*"},
			},
		},
	}
	if _, err := kube.RbacV1().ClusterRoles().Create(ctx, role, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		if _, err := kube.RbacV1().ClusterRoles().Update(ctx, role, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: tenancyv1alpha1.GroupName + ":server"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     role.Name,
		},
	}
	for _, u := range users {
		binding.Subjects = append(binding.Subjects, rbacv1.Subject{
			APIGroup: rbacv1.GroupName, Kind: "User", Name: u,
		})
	}
	for _, g := range groups {
		binding.Subjects = append(binding.Subjects, rbacv1.Subject{
			APIGroup: rbacv1.GroupName, Kind: "Group", Name: g,
		})
	}
	if _, err := kube.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		existing, err := kube.RbacV1().ClusterRoleBindings().Get(ctx, binding.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		binding.ResourceVersion = existing.ResourceVersion
		if _, err := kube.RbacV1().ClusterRoleBindings().Update(ctx, binding, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// WaitForEndpointSliceURLs reports the virtual workspace URLs one endpoint
// slice has published. kcp fills a slice only once a workspace binds the
// export, so this is worth calling after binding, not before. An empty
// answer is informational, never fatal: the operator follows the slice and
// picks URLs up as they appear.
func WaitForEndpointSliceURLs(ctx context.Context, cfg *rest.Config, sliceName string) ([]string, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build dynamic client: %w", err)
	}
	return waitForEndpointSlice(ctx, dyn, sliceName), nil
}

func waitForEndpointSlice(ctx context.Context, dyn dynamic.Interface, sliceName string) []string {
	logger := klog.FromContext(ctx)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// See NudgeAnnotation: without a write to the slice, the URLs for a
	// freshly bound export never appear and this wait is 30 seconds of
	// nothing.
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, NudgeAnnotation, time.Now().UTC().Format(time.RFC3339))
	if _, err := dyn.Resource(endpointSliceGVR).Patch(ctx, sliceName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		logger.V(2).Info("could not nudge the endpoint slice", "slice", sliceName, "error", err.Error())
	}

	var urls []string
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		slice, err := dyn.Resource(endpointSliceGVR).Get(ctx, sliceName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		endpoints, _, err := unstructured.NestedSlice(slice.Object, "status", "endpoints")
		if err != nil || len(endpoints) == 0 {
			return false, nil
		}
		urls = urls[:0]
		for _, e := range endpoints {
			if m, ok := e.(map[string]any); ok {
				if u, ok := m["url"].(string); ok && u != "" {
					urls = append(urls, u)
				}
			}
		}
		return len(urls) > 0, nil
	})
	if err != nil {
		logger.Info("endpoint slice has no virtual workspace endpoints yet; "+
			"the servers pick them up as they appear", "slice", sliceName)
		return nil
	}
	return urls
}
