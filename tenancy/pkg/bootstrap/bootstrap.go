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
// needs: the workspace that hosts the APIExport, the APIResourceSchemas,
// the export itself, the bind RBAC and the endpoint slice — plus the RBAC
// the server identity needs to follow that slice. Workspace-path plumbing
// is shared with the access component (access/pkg/bootstrap); this package
// only owns the tenancy assets.
package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/config/apiexport"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// ExportName is the name of the tenancy APIExport and of its endpoint
// slice.
const ExportName = tenancyv1alpha1.GroupName

// Defaults for where the export lives.
const (
	DefaultWorkspacePrefix      = "root:tenancy"
	DefaultControllersWorkspace = "controllers"
)

// Options configures Bootstrap.
type Options struct {
	// ServerUsers and ServerGroups are additional identities granted the
	// server role in the controllers workspace — the identities the
	// operator and the virtual workspace authenticate as when they are not
	// this bootstrap's own credential.
	ServerUsers  []string
	ServerGroups []string
}

// Result reports what Bootstrap installed.
type Result struct {
	APIExportEndpointSlice string
	VirtualWorkspaceURLs   []string
}

// Bootstrap applies the tenancy assets into the workspace cfg points at
// and reports any virtual workspace URLs the endpoint slice has published
// (none is normal before the first consumer binds). Idempotent: safe to
// run on every pod start and every upgrade.
func Bootstrap(ctx context.Context, cfg *rest.Config, opts Options) (*Result, error) {
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

	// Permission claims on kcp's own exported APIs (tenancy.kcp.io
	// workspaces) must carry that export's identity hash, and the hash is
	// per-deployment — kcp computes it from the export's identity secret.
	// It is discovered here rather than baked into the asset.
	workspacesHash, err := exportIdentityHash(ctx, cfg, "root", "tenancy.kcp.io")
	if err != nil {
		return nil, fmt.Errorf("discover the identity hash of kcp's tenancy.kcp.io export: %w", err)
	}

	for _, filename := range apiexport.Order {
		raw, err := apiexport.FS.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("read embedded asset %s: %w", filename, err)
		}
		if err := applyManifests(ctx, dyn, mapper, cache, raw, workspacesHash); err != nil {
			return nil, fmt.Errorf("apply %s: %w", filename, err)
		}
		logger.V(2).Info("applied", "asset", filename)
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

	urls := waitForEndpointSlice(ctx, dyn)

	return &Result{
		APIExportEndpointSlice: ExportName,
		VirtualWorkspaceURLs:   urls,
	}, nil
}

// exportIdentityHash reads status.identityHash of an APIExport in the
// given workspace, reached by retargeting cfg's host.
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
// APIExport documents get workspacesHash injected into their
// tenancy.kcp.io permission claims on the way through.
func applyManifests(ctx context.Context, dyn dynamic.Interface, mapper *restmapper.DeferredDiscoveryRESTMapper, cache discovery.CachedDiscoveryInterface, raw []byte, workspacesHash string) error {
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

		if obj.GetKind() == "APIExport" {
			if err := injectClaimIdentityHash(&obj, "tenancy.kcp.io", workspacesHash); err != nil {
				return fmt.Errorf("inject identity hash into %s: %w", obj.GetName(), err)
			}
		}

		gvk := obj.GroupVersionKind()
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			// A kind this bootstrap just installed (the APIExport makes the
			// endpoint slice mappable) may not be in the cached discovery
			// yet; refresh once and retry.
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
		// Subjects may have changed between runs; bindings are replaceable.
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

var endpointSliceGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
}

var apiExportGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports",
}

// injectClaimIdentityHash sets identityHash on every permission claim for
// the given group that does not carry one yet.
func injectClaimIdentityHash(obj *unstructured.Unstructured, group, hash string) error {
	claims, found, err := unstructured.NestedSlice(obj.Object, "spec", "permissionClaims")
	if err != nil || !found {
		return err
	}
	for i, c := range claims {
		claim, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if claim["group"] != group {
			continue
		}
		if existing, _ := claim["identityHash"].(string); existing == "" {
			claim["identityHash"] = hash
			claims[i] = claim
		}
	}
	return unstructured.SetNestedSlice(obj.Object, claims, "spec", "permissionClaims")
}

// waitForEndpointSlice reports the virtual workspace URLs the endpoint
// slice has published. kcp fills the slice only once a workspace actually
// binds the export, so an empty answer is normal on a fresh install — the
// wait is short and its outcome informational, never fatal. The operator
// and the virtual workspace follow the slice and pick URLs up as they
// appear.
func waitForEndpointSlice(ctx context.Context, dyn dynamic.Interface) []string {
	logger := klog.FromContext(ctx)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var urls []string
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		slice, err := dyn.Resource(endpointSliceGVR).Get(ctx, ExportName, metav1.GetOptions{})
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
		logger.Info("APIExportEndpointSlice has no virtual workspace endpoints yet; "+
			"this is expected until a workspace binds the export, and the servers pick them up as they appear",
			"name", ExportName)
		return nil
	}
	return urls
}
