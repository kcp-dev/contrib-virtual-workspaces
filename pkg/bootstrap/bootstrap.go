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

// Package bootstrap installs the kcp-side objects the access virtual workspace
// needs and verifies they came up.
package bootstrap

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	configapiexport "github.com/kcp-dev/contrib-access-virtual-workspace/config/apiexport"
	configcontroller "github.com/kcp-dev/contrib-access-virtual-workspace/config/controller"
)

const (
	APIExportName = "access.contrib.kcp.io"

	// ControllerNamespace is the namespace inside the APIExport's
	// workspace holding the server's identity and generated kubeconfig.
	ControllerNamespace = "default"

	// ControllerServiceAccount is the identity the server runs as.
	ControllerServiceAccount = "access-vw-controller"

	// ControllerTokenSecret is the ServiceAccount token secret kcp
	// populates.
	ControllerTokenSecret = "access-vw-controller-token"

	// ControllerKubeconfigSecret holds the kubeconfig built from that
	// token, for the server to mount.
	ControllerKubeconfigSecret = "access-vw-kubeconfig"
)

var apiExportEndpointSliceGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
}

// Result reports what a successful bootstrap produced, so callers can
// print the values the server needs.
type Result struct {
	// WorkspacePath is the absolute path of the workspace the objects
	// were installed into.
	WorkspacePath string

	// APIExportEndpointSlice is the name to pass to the server's
	// --apiexport-endpointslice flag.
	APIExportEndpointSlice string

	// VirtualWorkspaceURLs are the per-shard URLs the slice resolved to.
	// Empty means the provider will discover nothing.
	VirtualWorkspaceURLs []string

	// KubeconfigSecret is the Secret, in the APIExport's workspace, holding
	// the kubeconfig the server should run with.
	KubeconfigSecret string
}

// Options configures Bootstrap.
type Options struct {
	// WorkspacePath is the absolute path the objects were installed
	// into, used only for reporting.
	WorkspacePath string

	// HostOverride replaces the scheme://host[:port] of the generated
	// kubeconfig, keeping the workspace path. Needed when init runs with
	// an externally reachable URL but the server will connect from inside
	// the cluster (or the reverse).
	HostOverride string

	// Timeout bounds the apply-and-verify loop.
	Timeout time.Duration
}

// Bootstrap applies the embedded manifests to the workspace cfg points at,
// then waits for the APIExport and its endpoint slice to become usable.
func Bootstrap(ctx context.Context, cfg *rest.Config, opts Options) (*Result, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 3 * time.Minute
	}

	logger := klog.FromContext(ctx)
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build dynamic client: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build discovery client: %w", err)
	}
	cache := memory.NewMemCacheClient(discoveryClient)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cache)

	logger.Info("applying access virtual workspace assets", "workspace", opts.WorkspacePath)
	if err := applyFS(ctx, dynamicClient, mapper, cache, configapiexport.FS); err != nil {
		return nil, fmt.Errorf("apply assets: %w", err)
	}

	logger.Info("applying controller identity", "serviceAccount", ControllerServiceAccount)
	if err := applyFS(ctx, dynamicClient, mapper, cache, configcontroller.FS); err != nil {
		return nil, fmt.Errorf("apply controller identity: %w", err)
	}

	urls, err := waitForEndpointSlice(ctx, dynamicClient)
	if err != nil {
		return nil, err
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build kubernetes client: %w", err)
	}
	if err := createControllerKubeconfig(ctx, kubeClient, cfg, opts.HostOverride); err != nil {
		return nil, err
	}

	return &Result{
		WorkspacePath:          opts.WorkspacePath,
		APIExportEndpointSlice: APIExportName,
		VirtualWorkspaceURLs:   urls,
		KubeconfigSecret:       ControllerKubeconfigSecret,
	}, nil
}

func createControllerKubeconfig(ctx context.Context, client kubernetes.Interface, cfg *rest.Config, hostOverride string) error {
	logger := klog.FromContext(ctx)

	var token []byte
	var caCert []byte
	err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		secret, err := client.CoreV1().Secrets(ControllerNamespace).Get(ctx, ControllerTokenSecret, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		if len(secret.Data["token"]) == 0 {
			logger.V(2).Info("waiting for ServiceAccount token to be populated", "secret", ControllerTokenSecret)
			return false, nil
		}
		token = secret.Data["token"]
		caCert = secret.Data["ca.crt"]
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for ServiceAccount token %q: %w", ControllerTokenSecret, err)
	}

	server := cfg.Host
	if hostOverride != "" {
		if u, parseErr := url.Parse(cfg.Host); parseErr == nil && u.Path != "" {
			server = strings.TrimSuffix(hostOverride, "/") + u.Path
		} else {
			server = hostOverride
		}
		logger.Info("applying host override to generated kubeconfig", "server", server)
	}

	kubeconfig := clientcmdapi.Config{
		Kind:       "Config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			"workspace": {Server: server, CertificateAuthorityData: caCert},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"controller": {Token: string(token)},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"workspace": {Cluster: "workspace", AuthInfo: "controller"},
		},
		CurrentContext: "workspace",
	}

	raw, err := clientcmd.Write(kubeconfig)
	if err != nil {
		return fmt.Errorf("marshal kubeconfig: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ControllerKubeconfigSecret,
			Namespace: ControllerNamespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"kubeconfig": raw},
	}

	if _, err := client.CoreV1().Secrets(ControllerNamespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create kubeconfig secret: %w", err)
		}
		existing, getErr := client.CoreV1().Secrets(ControllerNamespace).Get(ctx, secret.Name, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("get existing kubeconfig secret: %w", getErr)
		}
		secret.ResourceVersion = existing.ResourceVersion
		if _, err := client.CoreV1().Secrets(ControllerNamespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update kubeconfig secret: %w", err)
		}
		logger.Info("updated controller kubeconfig secret", "secret", secret.Name)
		return nil
	}

	logger.Info("created controller kubeconfig secret", "secret", secret.Name)
	return nil
}

func applyFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, cache discovery.CachedDiscoveryInterface, fs embed.FS) error {
	logger := klog.FromContext(ctx)

	var lastErr error
	attempt := 0
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		attempt++
		if err := applyResources(ctx, client, mapper, fs); err != nil {
			lastErr = err
			logger.V(2).Info("assets not applied yet, retrying", "attempt", attempt, "err", err)
			cache.Invalidate()
			return false, nil
		}
		logger.Info("assets applied", "attempts", attempt)
		return true, nil
	})
	if err != nil && lastErr != nil {
		return fmt.Errorf("%w (last error: %v)", err, lastErr)
	}
	return err
}

func applyResources(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, fs embed.FS) error {
	entries, err := fs.ReadDir(".")
	if err != nil {
		return fmt.Errorf("read embedded assets: %w", err)
	}

	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if err := applyFile(ctx, client, mapper, fs, e.Name()); err != nil {
			errs = append(errs, err)
		}
	}
	return utilerrors.NewAggregate(errs)
}

func applyFile(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, fs embed.FS, filename string) error {
	raw, err := fs.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read %s: %w", filename, err)
	}

	reader := kubeyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	var errs []error
	for i := 1; ; i++ {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read document %d of %s: %w", i, filename, err)
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		if err := applyResource(ctx, client, mapper, doc); err != nil {
			errs = append(errs, fmt.Errorf("%s document %d: %w", filename, i, err))
		}
	}
	return utilerrors.NewAggregate(errs)
}

func applyResource(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, raw []byte) error {
	logger := klog.FromContext(ctx)

	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(raw, &u.Object); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	gvk := u.GroupVersionKind()
	if gvk.Kind == "" {
		return errors.New("manifest has no kind")
	}

	m, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("REST mapping for %s: %w", gvk, err)
	}

	resource := client.Resource(m.Resource).Namespace(u.GetNamespace())

	existing, err := resource.Get(ctx, u.GetName(), metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := resource.Create(ctx, u, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create %s %s: %w", gvk.Kind, u.GetName(), err)
		}
		logger.Info("created", "kind", gvk.Kind, "name", u.GetName())
		return nil
	case err != nil:
		return fmt.Errorf("get %s %s: %w", gvk.Kind, u.GetName(), err)
	}

	u.SetResourceVersion(existing.GetResourceVersion())
	if _, err := resource.Update(ctx, u, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update %s %s: %w", gvk.Kind, u.GetName(), err)
	}
	logger.V(2).Info("updated", "kind", gvk.Kind, "name", u.GetName())
	return nil
}

func waitForEndpointSlice(ctx context.Context, client dynamic.Interface) ([]string, error) {
	logger := klog.FromContext(ctx)

	var urls []string
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		slice, err := client.Resource(apiExportEndpointSliceGVR).Get(ctx, APIExportName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}

		endpoints, found, err := unstructured.NestedSlice(slice.Object, "status", "endpoints")
		if err != nil || !found || len(endpoints) == 0 {
			logger.V(2).Info("waiting for APIExportEndpointSlice endpoints", "name", APIExportName)
			return false, nil
		}

		urls = nil
		for _, e := range endpoints {
			entry, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if u, ok := entry["url"].(string); ok && u != "" {
				urls = append(urls, u)
			}
		}
		return len(urls) > 0, nil
	})
	if err != nil {
		return nil, fmt.Errorf("APIExportEndpointSlice %q has no virtual workspace endpoints: %w "+
			"(is the APIExport valid, and is a shard serving it?)", APIExportName, err)
	}

	return urls, nil
}

// VerifyAPIBinding reports whether the given workspace has an APIBinding to
// this APIExport with all of its permission claims accepted.
func VerifyAPIBinding(ctx context.Context, cfg *rest.Config, bindingName string) error {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build dynamic client: %w", err)
	}

	binding, err := client.Resource(schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings",
	}).Get(ctx, bindingName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get APIBinding %q: %w", bindingName, err)
	}

	declared, _, _ := unstructured.NestedSlice(binding.Object, "spec", "permissionClaims")
	applied, _, _ := unstructured.NestedSlice(binding.Object, "status", "appliedPermissionClaims")

	if len(applied) < len(declared) {
		return fmt.Errorf("APIBinding %q has %d permission claims declared but only %d applied; "+
			"the virtual workspace cannot see RBAC objects until they are accepted",
			bindingName, len(declared), len(applied))
	}
	return nil
}
