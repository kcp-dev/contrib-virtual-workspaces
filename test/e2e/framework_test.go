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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

// examplesDir is docs/example, relative to this package.
//
// The manifests the tests apply are the ones the README walks a reader through,
// read from disk rather than copied into testdata. A copy would drift, and the
// first thing to break when it did would be the documentation rather than this
// test -- which is the wrong way round.
const examplesDir = "../../docs/example"

const (
	// A cold kcp answers discovery slowly, and this harness applies a CRD and
	// then immediately uses the kind it defines. The default 5 QPS turns that
	// into rate limiting rather than into waiting on the server.
	clientQPS   = 100
	clientBurst = 200
)

// workspace is a connection to one logical cluster of the kcp under test.
//
// kcp addresses a workspace in the URL rather than in a header or a context, so
// switching workspaces means a new client against a rewritten host. Everything
// here is unstructured and dynamic: the interesting kinds belong to kcp or to a
// provider, and this repository has generated clients for neither.
type workspace struct {
	path    string
	config  *rest.Config
	dynamic dynamic.Interface
	mapper  meta.RESTMapper
	cache   discovery.CachedDiscoveryInterface
}

func newWorkspace(path string, config *rest.Config) (*workspace, error) {
	config = rest.CopyConfig(config)
	config.QPS = clientQPS
	config.Burst = clientBurst

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("discovery client: %w", err)
	}

	cache := memory.NewMemCacheClient(discoveryClient)

	return &workspace{
		path:    path,
		config:  config,
		dynamic: dynamicClient,
		mapper:  restmapper.NewDeferredDiscoveryRESTMapper(cache),
		cache:   cache,
	}, nil
}

// root connects to the kcp named by $KUBECONFIG, at the root workspace.
//
// The kubeconfig is the one kcp writes on first start. hack/ci/run-e2e-tests.sh
// points $KUBECONFIG at it after standing the stack up; there is no in-process
// kcp here, because the virtual workspace under test is reached by kcp dialling
// back out to it, which needs a real listener on a real port.
func root(t *testing.T) *workspace {
	t.Helper()

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		t.Fatal("KUBECONFIG is not set; run the e2e tests through hack/ci/run-e2e-tests.sh.")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("Failed to load %s: %v", kubeconfig, err)
	}

	// Independent of what any test asserts, and low enough that a wedged shard
	// shows up as a failure rather than as a suite that hangs until its own
	// timeout.
	config.Timeout = 30 * time.Second

	return at(t, config, "root")
}

// at returns a connection to another workspace of the same kcp, reusing the
// credentials and replacing only the path.
func at(t *testing.T, config *rest.Config, path string) *workspace {
	t.Helper()

	parsed, err := url.Parse(config.Host)
	if err != nil {
		t.Fatalf("Unparseable host %q: %v", config.Host, err)
	}

	scoped := rest.CopyConfig(config)
	scoped.Host = fmt.Sprintf("%s://%s/clusters/%s", parsed.Scheme, parsed.Host, path)

	ws, err := newWorkspace(path, scoped)
	if err != nil {
		t.Fatalf("Failed to connect to workspace %s: %v", path, err)
	}

	return ws
}

// in returns a connection to a workspace below this one.
func (w *workspace) in(t *testing.T, name string) *workspace {
	t.Helper()

	return at(t, w.config, w.path+":"+name)
}

// readExample reads one of the manifests from docs/example.
func readExample(t *testing.T, name string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(examplesDir, name))
	if err != nil {
		t.Fatalf("Failed to read %s: %v", name, err)
	}

	return raw
}

// splitYAML separates a multi-document manifest.
func splitYAML(t *testing.T, manifest []byte) [][]byte {
	t.Helper()

	reader := utilyaml.NewYAMLReader(bufioReader(manifest))

	var docs [][]byte

	for {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Failed to split manifest: %v", err)
		}
		// A document that is only comments parses to nothing. docs/example
		// opens with a block of them above the first separator, so this is the
		// common case rather than a corner one.
		var probe map[string]any
		if err := yaml.Unmarshal(doc, &probe); err != nil {
			t.Fatalf("Failed to parse a document of the manifest: %v", err)
		}
		if len(probe) == 0 {
			continue
		}

		docs = append(docs, doc)
	}

	return docs
}

// apply creates or updates every object in one of the example manifests.
//
// Retried, because these are applied in the order a reader would apply them and
// each one races the machinery the previous one set going: a CRD has to be
// established before an object of its kind is accepted, and an APIExport has to
// be admitted before a binding to it resolves.
func (w *workspace) apply(t *testing.T, ctx context.Context, name string) {
	t.Helper()

	docs := splitYAML(t, readExample(t, name))

	var (
		lastErr  error
		attempts int
	)

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		attempts++

		for _, doc := range docs {
			err := w.applyOne(ctx, doc)
			if err == nil {
				continue
			}

			lastErr = err

			// A manifest the server calls malformed will be malformed for the
			// next three minutes too. Failing now names the field; retrying
			// reports a timeout, which is how a missing required field comes to
			// look like a slow server.
			if apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) || errors.Is(err, errNoKind) {
				return false, err
			}

			// A CRD applied a moment ago is not in this client's discovery yet,
			// and that is the one error worth re-reading discovery for.
			// Invalidating on every error would turn a slow shard into a
			// self-inflicted request storm.
			if errors.As(err, new(*meta.NoKindMatchError)) || errors.As(err, new(*meta.NoResourceMatchError)) {
				w.cache.Invalidate()
			}

			if attempts == 1 || attempts%10 == 0 {
				t.Logf("Applying %s in %s, attempt %d: %v", name, w.path, attempts, err)
			}

			return false, nil
		}

		return true, nil
	})
	if err != nil {
		t.Fatalf("Failed to apply %s in %s after %d attempts: %v (last error: %v)", name, w.path, attempts, err, lastErr)
	}
}

// applyBytes is apply for a manifest the caller has already produced.
func (w *workspace) applyBytes(t *testing.T, ctx context.Context, what string, manifest []byte) {
	t.Helper()

	docs := splitYAML(t, manifest)

	var lastErr error

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		for _, doc := range docs {
			if err := w.applyOne(ctx, doc); err != nil {
				lastErr = err
				if apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) || errors.Is(err, errNoKind) {
					return false, err
				}

				return false, nil
			}
		}

		return true, nil
	})
	if err != nil {
		t.Fatalf("Failed to apply %s in %s: %v (last error: %v)", what, w.path, err, lastErr)
	}
}

// errNoKind marks a manifest that will never be applicable, however long it is
// retried for.
var errNoKind = errors.New("manifest has no kind")

func (w *workspace) applyOne(ctx context.Context, doc []byte) error {
	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(doc, &u.Object); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	gvk := u.GroupVersionKind()
	if gvk.Kind == "" {
		return fmt.Errorf("%w: %s", errNoKind, truncate(string(doc), 120))
	}

	mapping, err := w.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("REST mapping for %s: %w", gvk, err)
	}

	resource := w.dynamic.Resource(mapping.Resource).Namespace(u.GetNamespace())

	existing, err := resource.Get(ctx, u.GetName(), metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := resource.Create(ctx, u, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create %s %s: %w", gvk.Kind, u.GetName(), err)
		}

		return nil
	case err != nil:
		return fmt.Errorf("get %s %s: %w", gvk.Kind, u.GetName(), err)
	}

	u.SetResourceVersion(existing.GetResourceVersion())
	if _, err := resource.Update(ctx, u, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update %s %s: %w", gvk.Kind, u.GetName(), err)
	}

	return nil
}

func gvr(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
}

func (w *workspace) get(ctx context.Context, r schema.GroupVersionResource, name string) (*unstructured.Unstructured, error) {
	return w.dynamic.Resource(r).Get(ctx, name, metav1.GetOptions{})
}

var workspacesGVR = gvr("tenancy.kcp.io", "v1alpha1", "workspaces")

// createWorkspace creates a workspace below w and waits for it to be usable.
//
// It is idempotent, so a run with NO_TEARDOWN=true can be repeated against the
// same kcp without cleaning up first.
func (w *workspace) createWorkspace(t *testing.T, ctx context.Context, name string) *workspace {
	t.Helper()

	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"type": map[string]any{"name": "universal"}},
	}}

	var lastErr error

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		current, err := w.dynamic.Resource(workspacesGVR).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if _, err := w.dynamic.Resource(workspacesGVR).Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
				lastErr = err
			}

			return false, nil
		}
		if err != nil {
			lastErr = err

			return false, nil
		}

		lastErr = nil

		// Ready rather than merely created: a workspace that is still
		// initializing accepts no objects, and the failure that produces is a
		// 404 on the kind rather than anything mentioning initialization.
		phase, _, _ := unstructured.NestedString(current.Object, "status", "phase")

		return phase == "Ready", nil
	})
	if err != nil {
		t.Fatalf("Workspace %s:%s never became ready: %v (last error: %v)", w.path, name, err, lastErr)
	}

	t.Logf("Workspace %s:%s is ready.", w.path, name)

	return w.in(t, name)
}

// deleteWorkspace removes a workspace unless the run asked to keep it.
func (w *workspace) deleteWorkspace(t *testing.T, name string) {
	t.Helper()

	if os.Getenv("NO_TEARDOWN") == "true" {
		t.Logf("NO_TEARDOWN is set, keeping workspace %s:%s.", w.path, name)

		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := w.dynamic.Resource(workspacesGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		t.Logf("Failed to delete workspace %s:%s: %v", w.path, name, err)
	}
}

// resourceIsServed waits until a resource appears in this workspace's
// discovery, and returns the verbs it advertises.
//
// This is the gate everything else waits behind, and it is not the same as the
// APIBinding being Bound. A virtually stored resource only becomes servable
// once the shard can resolve the endpoint slice the APIExport references, which
// happens through the cache server and therefore lags the binding. Before that,
// discovery reports the resource as temporarily unavailable and a create fails
// with `no matches for kind`, which reads like a broken setup rather than an
// early one.
func (w *workspace) resourceIsServed(t *testing.T, ctx context.Context, group, version, resource string, timeout time.Duration) []string {
	t.Helper()

	var verbs []string

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		// Uncached, because the answer this is waiting for is precisely the one
		// that changes.
		w.cache.Invalidate()

		list, err := w.cache.ServerResourcesForGroupVersion(group + "/" + version)
		if err != nil {
			return false, nil
		}

		for _, r := range list.APIResources {
			if r.Name == resource {
				verbs = r.Verbs

				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("%s.%s never became servable in %s: %v\n"+
			"Look for \"failed to retrieve virtual workspace URL\" in the kcp log: that is the shard "+
			"being unable to resolve the endpoint slice the APIExport references.", resource, group, w.path, err)
	}

	return verbs
}

func bufioReader(b []byte) *bufio.Reader {
	return bufio.NewReader(bytes.NewReader(b))
}

// truncate keeps a failure message readable when a server returns a wall of
// YAML.
func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}

	return s[:max] + "..."
}
