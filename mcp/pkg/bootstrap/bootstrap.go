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

// Package bootstrap installs the mcp component's kcp-side objects: the
// workspace that owns them, the APIExport a consumer enables by binding,
// and the grant the operator needs to read through it.
package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	accessbootstrap "github.com/kcp-dev/contrib-virtual-workspaces/access/pkg/bootstrap"
	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/config/apiexport"
	mcpv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/mcp/sdk/apis/mcp/v1alpha1"
)

const (
	// DefaultWorkspacePrefix is the parent the component's workspace is
	// created under.
	DefaultWorkspacePrefix = "root:mcp"
	// DefaultControllersWorkspace is the leaf this component owns.
	DefaultControllersWorkspace = "controllers"
	// DefaultWorkspaceType is the type used for workspaces created here.
	DefaultWorkspaceType = "universal"

	// ExportName is the export a consumer binds to enable MCP, and the
	// name of the endpoint slice that follows it.
	ExportName = "mcp"

	// exportsPathPlaceholder is substituted at apply time: the endpoint
	// slice has to name the workspace the export landed in, which is
	// configurable, so no committed manifest can carry it.
	exportsPathPlaceholder = "__EXPORTS_PATH__"
)

// Options configures Init.
type Options struct {
	// WorkspacePrefix and ControllersWorkspace locate the workspace the
	// objects are installed into.
	WorkspacePrefix      string
	ControllersWorkspace string
	WorkspaceType        string

	// ServerUsers and ServerGroups are the identities granted the role the
	// operator needs to read through the export. kcp-operator mounts a
	// client certificate, so its server arrives as a User.
	ServerUsers  []string
	ServerGroups []string

	Timeout time.Duration
}

// Result reports what Init installed.
type Result struct {
	// WorkspacePath is where the objects landed.
	WorkspacePath string
	// ExportCluster is the logical cluster of that workspace, which is
	// what a consumer's APIBinding references.
	ExportCluster string
	// SliceURLs are the per-shard virtual workspace URLs the slice
	// resolved to. Empty until a consumer binds.
	SliceURLs []string
}

// Init creates the component's workspace and applies its assets.
func Init(ctx context.Context, cfg *rest.Config, opts Options) (*Result, error) {
	if opts.WorkspacePrefix == "" {
		opts.WorkspacePrefix = DefaultWorkspacePrefix
	}
	if opts.ControllersWorkspace == "" {
		opts.ControllersWorkspace = DefaultControllersWorkspace
	}
	if opts.WorkspaceType == "" {
		opts.WorkspaceType = DefaultWorkspaceType
	}
	if opts.Timeout == 0 {
		opts.Timeout = 3 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	logger := klog.FromContext(ctx)

	path, err := accessbootstrap.JoinWorkspacePath(opts.WorkspacePrefix, opts.ControllersWorkspace)
	if err != nil {
		return nil, err
	}

	logger.Info("creating the mcp workspace", "path", path)
	scoped, err := accessbootstrap.CreateWorkspacePath(ctx, cfg, path, opts.WorkspaceType)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}

	if err := applyAssets(ctx, scoped, path); err != nil {
		return nil, err
	}

	if len(opts.ServerUsers)+len(opts.ServerGroups) > 0 {
		kube, err := kubernetes.NewForConfig(scoped)
		if err != nil {
			return nil, fmt.Errorf("build kubernetes client: %w", err)
		}
		if err := applyServerIdentity(ctx, kube, opts.ServerUsers, opts.ServerGroups); err != nil {
			return nil, fmt.Errorf("grant the operator identity: %w", err)
		}
	}

	cluster, err := logicalClusterOf(ctx, scoped)
	if err != nil {
		return nil, err
	}

	// A slice publishes no URL until something binds the export, so an
	// empty answer here is the normal state of a fresh install, not a
	// failure: the operator follows the slice and picks URLs up later.
	urls := sliceURLs(ctx, scoped)

	logger.Info("mcp install complete",
		"workspace", path, "cluster", cluster, "export", ExportName, "sliceURLs", urls)
	return &Result{WorkspacePath: path, ExportCluster: cluster, SliceURLs: urls}, nil
}

func applyAssets(ctx context.Context, cfg *rest.Config, exportsPath string) error {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build dynamic client: %w", err)
	}
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build discovery client: %w", err)
	}
	cache := memory.NewMemCacheClient(disco)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cache)

	for _, asset := range apiexport.Order {
		raw, err := apiexport.FS.ReadFile(asset)
		if err != nil {
			return fmt.Errorf("read %s: %w", asset, err)
		}
		raw = bytes.ReplaceAll(raw, []byte(exportsPathPlaceholder), []byte(exportsPath))
		if err := applyManifest(ctx, dyn, mapper, cache, raw); err != nil {
			return fmt.Errorf("apply %s: %w", asset, err)
		}
	}
	return nil
}

func applyManifest(
	ctx context.Context,
	dyn dynamic.Interface,
	mapper *restmapper.DeferredDiscoveryRESTMapper,
	cache discovery.CachedDiscoveryInterface,
	raw []byte,
) error {
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
			// A kind this bootstrap just installed may not be in cached
			// discovery yet; refresh once and retry.
			cache.Invalidate()
			mapper.Reset()
			mapping, err = mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			if err != nil {
				return fmt.Errorf("map %s: %w", gvk, err)
			}
		}

		// A namespaced kind has to be addressed through its namespace, or
		// the server answers "method not allowed" — which reads like an
		// RBAC problem and is not one. Cluster-scoped kinds have no
		// namespace to give.
		var client dynamic.ResourceInterface = dyn.Resource(mapping.Resource)
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			ns := obj.GetNamespace()
			if ns == "" {
				ns = metav1.NamespaceDefault
				obj.SetNamespace(ns)
			}
			client = dyn.Resource(mapping.Resource).Namespace(ns)
		}
		if _, err := client.Create(ctx, &obj, metav1.CreateOptions{}); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("create %s %s: %w", gvk.Kind, obj.GetName(), err)
			}
			// kcp keeps reconciling some of these (it creates and updates a
			// default endpoint slice per export), so a get+update can 409.
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				existing, err := client.Get(ctx, obj.GetName(), metav1.GetOptions{})
				if err != nil {
					return err
				}
				obj.SetResourceVersion(existing.GetResourceVersion())
				_, err = client.Update(ctx, &obj, metav1.UpdateOptions{})
				return err
			})
			if err != nil {
				// APIResourceSchemas are immutable; existing is as good as
				// applied.
				if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
					continue
				}
				return fmt.Errorf("update %s %s: %w", gvk.Kind, obj.GetName(), err)
			}
		}
	}
}

// applyServerIdentity grants the operator what it needs to read the export
// and act through its claims: kcp's workspace gate, the export objects
// themselves, and content on the export.
func applyServerIdentity(ctx context.Context, kube kubernetes.Interface, users, groups []string) error {
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: mcpv1alpha1.GroupName + ":server"},
		Rules: []rbacv1.PolicyRule{
			{
				// RBAC means nothing to an identity that cannot enter the
				// workspace in the first place.
				Verbs:           []string{"access"},
				NonResourceURLs: []string{"/"},
			},
			{
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apiexports", "apiexportendpointslices", "apibindings"},
				Verbs:     []string{"get", "list", "watch"},
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
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			existing, err := kube.RbacV1().ClusterRoles().Get(ctx, role.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			existing.Rules = role.Rules
			_, err = kube.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{})
			return err
		}); err != nil {
			return err
		}
	}

	subjects := make([]rbacv1.Subject, 0, len(users)+len(groups))
	for _, u := range users {
		subjects = append(subjects, rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: u})
	}
	for _, g := range groups {
		subjects = append(subjects, rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: rbacv1.GroupKind, Name: g})
	}

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
		Subjects:   subjects,
	}
	if _, err := kube.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			existing, err := kube.RbacV1().ClusterRoleBindings().Get(ctx, binding.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			existing.Subjects = subjects
			existing.RoleRef = binding.RoleRef
			_, err = kube.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{})
			return err
		})
	}
	return nil
}

func logicalClusterOf(ctx context.Context, cfg *rest.Config) (string, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("build dynamic client: %w", err)
	}
	lc, err := dyn.Resource(schema.GroupVersionResource{
		Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
	}).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read this workspace's LogicalCluster: %w", err)
	}
	name := lc.GetAnnotations()["kcp.io/cluster"]
	if name == "" {
		return "", fmt.Errorf("the LogicalCluster carries no kcp.io/cluster annotation")
	}
	return name, nil
}

// sliceURLs reports the URLs the endpoint slice has published, best effort.
func sliceURLs(ctx context.Context, cfg *rest.Config) []string {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil
	}
	var out []string
	_ = wait.PollUntilContextTimeout(ctx, time.Second, 10*time.Second, true,
		func(ctx context.Context) (bool, error) {
			slice, err := dyn.Resource(schema.GroupVersionResource{
				Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
			}).Get(ctx, ExportName, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			endpoints, found, _ := unstructured.NestedSlice(slice.Object, "status", "endpoints")
			if !found {
				return false, nil
			}
			out = nil
			for _, e := range endpoints {
				if m, ok := e.(map[string]interface{}); ok {
					if u, ok := m["url"].(string); ok && strings.TrimSpace(u) != "" {
						out = append(out, u)
					}
				}
			}
			return len(out) > 0, nil
		})
	return out
}
