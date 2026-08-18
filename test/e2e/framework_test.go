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
	"embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"text/template"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

//go:embed testdata/*.yaml.tmpl
var testdata embed.FS

// cluster is a connection to one API server -- either the Kubernetes cluster the operator runs in,
// or a workspace of the kcp it deploys. Both are addressed the same way here: render a manifest,
// apply it, wait for something to appear.
type cluster struct {
	config  *rest.Config
	dynamic dynamic.Interface
	kube    kubernetes.Interface
	mapper  meta.RESTMapper
	cache   discovery.CachedDiscoveryInterface
}

// Client-side throttling defaults to 5 queries per second, which is far too low here. Applying a
// manifest re-reads discovery, and a test that polls several objects while kcp is coming up spends
// its whole budget waiting on the limiter rather than on the server -- the failure then surfaces
// as "client rate limiter Wait returned an error", which reads like a server problem and is not
// one. These are single-tenant throwaway clusters, so the server is the only limit worth having.
const (
	clientQPS   = 100
	clientBurst = 200
)

func newCluster(config *rest.Config) (*cluster, error) {
	config = rest.CopyConfig(config)
	config.QPS = clientQPS
	config.Burst = clientBurst

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("build dynamic client: %w", err)
	}

	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("build kubernetes client: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("build discovery client: %w", err)
	}

	cache := memory.NewMemCacheClient(discoveryClient)

	return &cluster{
		config:  config,
		dynamic: dynamicClient,
		kube:    kubeClient,
		mapper:  restmapper.NewDeferredDiscoveryRESTMapper(cache),
		cache:   cache,
	}, nil
}

// workloadCluster connects to the Kubernetes cluster named by $KUBECONFIG, where kcp-operator and
// everything it deploys run.
func workloadCluster(t *testing.T) *cluster {
	t.Helper()

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		t.Fatal("KUBECONFIG is not set; run the e2e tests through hack/ci/run-e2e-tests.sh.")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("Failed to load %s: %v", kubeconfig, err)
	}

	c, err := newCluster(config)
	if err != nil {
		t.Fatalf("Failed to connect to the workload cluster: %v", err)
	}

	return c
}

// render expands one of the embedded templates.
func render(t *testing.T, name string, data any) []byte {
	t.Helper()

	raw, err := testdata.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("Failed to read %s: %v", name, err)
	}

	tmpl, err := template.New(name).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		t.Fatalf("Failed to parse %s: %v", name, err)
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("Failed to render %s: %v", name, err)
	}

	return out.Bytes()
}

// apply renders a template and creates every document in it, retrying while the API server does
// not yet know a type. CRDs and kcp's own APIBindings both become available asynchronously, so a
// first attempt failing to map a kind is expected rather than fatal.
//
// Documents are applied in order and the attempt stops at the first failure: a later document
// usually depends on an earlier one, so carrying on produces a pile of errors whose first entry is
// the only real one.
func (c *cluster) apply(t *testing.T, ctx context.Context, name string, data any) {
	t.Helper()

	docs := splitYAML(t, render(t, name, data))

	var (
		lastErr  error
		attempts int
	)

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		attempts++

		for _, doc := range docs {
			err := c.applyOne(ctx, doc)
			if err == nil {
				continue
			}

			lastErr = err

			// A manifest the API server rejects as malformed will be rejected identically for the
			// next five minutes. Failing immediately reports the field that is wrong; retrying
			// reports a timeout, which is how a missing required field comes to look like a slow
			// cluster.
			if apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) {
				return false, err
			}

			// Only a mapping failure is worth re-reading discovery for, and only then: discovery
			// is dozens of requests, so invalidating on every error turns a slow API server into a
			// self-inflicted request storm. errors.As rather than meta.IsNoMatchError because
			// applyOne wraps the mapping error, which the latter does not unwrap.
			if errors.As(err, new(*meta.NoKindMatchError)) || errors.As(err, new(*meta.NoResourceMatchError)) {
				c.cache.Invalidate()
			}

			// Logged rather than accumulated, so a run that eventually times out shows how the
			// error changed instead of only its last form.
			if attempts == 1 || attempts%10 == 0 {
				t.Logf("Applying %s, attempt %d: %v", name, attempts, err)
			}

			return false, nil
		}

		return true, nil
	})
	if err != nil {
		t.Fatalf("Failed to apply %s after %d attempts: %v (last error: %v)", name, attempts, err, lastErr)
	}
}

func (c *cluster) applyOne(ctx context.Context, doc []byte) error {
	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(doc, &u.Object); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	gvk := u.GroupVersionKind()
	if gvk.Kind == "" {
		return errors.New("manifest has no kind")
	}

	mapping, err := c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("REST mapping for %s: %w", gvk, err)
	}

	resource := c.dynamic.Resource(mapping.Resource).Namespace(u.GetNamespace())

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

func splitYAML(t *testing.T, raw []byte) [][]byte {
	t.Helper()

	reader := kubeyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))

	var docs [][]byte
	for {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Failed to split manifest: %v", err)
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		docs = append(docs, doc)
	}

	return docs
}

// get fetches one object by group/version/resource, for assertions on types this module has no Go
// types for.
func (c *cluster) get(ctx context.Context, group, version, resource, namespace, name string) (*unstructured.Unstructured, error) {
	gvr := schemaGVR(group, version, resource)

	return c.dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// createNamespace creates a namespace that is removed when the test ends, unless $NO_TEARDOWN says
// to leave it behind for inspection.
//
// The delete is not waited on: the namespace holds the front-proxy every other object's finalizers
// talk to, so termination can outlive the test, and a test that has already reported its result
// should not fail because cleanup was slow.
func createNamespace(t *testing.T, ctx context.Context, c *cluster, prefix string) string {
	t.Helper()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{GenerateName: prefix + "-"},
	}

	created, err := c.kube.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create namespace: %v", err)
	}

	t.Logf("Created namespace %s.", created.Name)

	t.Cleanup(func() {
		if os.Getenv("NO_TEARDOWN") == "true" {
			t.Logf("NO_TEARDOWN is set, keeping namespace %s.", created.Name)

			return
		}

		deleteCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := c.kube.CoreV1().Namespaces().Delete(deleteCtx, created.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Logf("Failed to delete namespace %s: %v", created.Name, err)
		}
	})

	return created.Name
}

// waitForPods waits until at least one pod matching the selector exists and all of them are ready,
// then reports what each container was doing if that never happened.
//
// A pod that does not start is how a missing image, a rejected command line flag and a failed
// bootstrap all present themselves, and only the container states tell them apart.
func waitForPods(t *testing.T, ctx context.Context, c *cluster, namespace, selector string, timeout time.Duration) {
	t.Helper()

	// Held across attempts rather than reassigned, because the attempt that trips the deadline is
	// usually the one that fails: overwriting it with nil there would throw away the only
	// description of what the pods were doing, which is the whole point of this function.
	var pods *corev1.PodList

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		list, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false, nil
		}

		pods = list

		if len(pods.Items) == 0 {
			return false, nil
		}

		for _, pod := range pods.Items {
			if !podReady(pod) {
				return false, nil
			}
		}

		return true, nil
	})
	if err == nil {
		return
	}

	if pods != nil {
		for _, pod := range pods.Items {
			t.Logf("Pod %s is %s.", pod.Name, pod.Status.Phase)

			statuses := append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
			for _, status := range statuses {
				switch {
				case status.State.Waiting != nil:
					t.Logf("  %s: waiting (%s) %s", status.Name, status.State.Waiting.Reason, status.State.Waiting.Message)
				case status.State.Terminated != nil:
					t.Logf("  %s: terminated with exit code %d (%s) %s",
						status.Name, status.State.Terminated.ExitCode, status.State.Terminated.Reason, status.State.Terminated.Message)
				case status.State.Running != nil:
					t.Logf("  %s: running, ready=%t", status.Name, status.Ready)
				}
			}

			logContainers(t, ctx, c, pod)
		}
	}

	t.Fatalf("Pods matching %q in %s never became ready: %v", selector, namespace, err)
}

func podReady(pod corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}

	return false
}

// logContainers dumps the tail of every container's log, so a failed run explains itself without
// requiring the cluster to still exist.
func logContainers(t *testing.T, ctx context.Context, c *cluster, pod corev1.Pod) {
	t.Helper()

	// A container that is crash-looping has usually just been restarted, so its current log is
	// empty or truncated and the failure is in the previous attempt. Both are fetched, because
	// which one holds the error depends on where in the backoff cycle the pod happens to be.
	restarts := map[string]int32{}
	for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
		restarts[status.Name] = status.RestartCount
	}

	containers := make([]string, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	for _, container := range pod.Spec.InitContainers {
		containers = append(containers, container.Name)
	}
	for _, container := range pod.Spec.Containers {
		containers = append(containers, container.Name)
	}

	for _, name := range containers {
		dumpLog(t, ctx, c, pod, name, false)

		if restarts[name] > 0 {
			dumpLog(t, ctx, c, pod, name, true)
		}
	}
}

func dumpLog(t *testing.T, ctx context.Context, c *cluster, pod corev1.Pod, container string, previous bool) {
	t.Helper()

	which := "current"
	if previous {
		which = "previous"
	}

	tail := int64(50)

	stream, err := c.kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: container,
		TailLines: &tail,
		Previous:  previous,
	}).Stream(ctx)
	if err != nil {
		t.Logf("  no %s logs for %s/%s: %v", which, pod.Name, container, err)

		return
	}
	defer stream.Close()

	body, err := io.ReadAll(stream)
	if err != nil {
		return
	}

	if len(bytes.TrimSpace(body)) > 0 {
		t.Logf("  %s logs of %s/%s:\n%s", which, pod.Name, container, string(body))
	}
}

// waitForSecret waits for a Secret to appear, which is how the operator reports that a Kubeconfig
// object has been minted.
func waitForSecret(t *testing.T, ctx context.Context, c *cluster, namespace, name string) *corev1.Secret {
	t.Helper()

	var secret *corev1.Secret

	err := wait.PollUntilContextTimeout(ctx, time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		var err error
		secret, err = c.kube.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}

		return err == nil, err
	})
	if err != nil {
		t.Fatalf("Secret %s/%s never appeared: %v", namespace, name, err)
	}

	return secret
}

// portForward forwards a local port to a Service in the cluster for the rest of the test.
//
// This shells out to kubectl rather than using the port-forward client library: kubectl restores
// the tunnel when the target pod is replaced, which happens routinely here because the front-proxy
// is restarted whenever its configuration changes.
func portForward(t *testing.T, ctx context.Context, namespace, target string, remotePort int) int {
	t.Helper()

	localPort := freePort(t)

	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	kubectl := os.Getenv("KUBECTL_BINARY")
	if kubectl == "" {
		kubectl = "kubectl"
	}

	//nolint:gosec // the arguments are all test-controlled.
	cmd := exec.CommandContext(ctx, kubectl,
		"--kubeconfig", os.Getenv("KUBECONFIG"),
		"port-forward",
		"--namespace", namespace,
		target,
		fmt.Sprintf("%d:%d", localPort, remotePort),
	)

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start port-forward to %s: %v", target, err)
	}

	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	// The tunnel is not usable the moment kubectl starts, and connecting too early produces a
	// confusing TLS error rather than a connection refused.
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(context.Context) (bool, error) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), time.Second)
		if err != nil {
			return false, nil
		}
		conn.Close()

		return true, nil
	})
	if err != nil {
		t.Fatalf("Port-forward to %s never became usable: %v\n%s", target, err, output.String())
	}

	t.Logf("Forwarding localhost:%d to %s:%d in %s.", localPort, target, remotePort, namespace)

	return localPort
}

func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find a free port: %v", err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

// kcpConfig turns a kubeconfig Secret minted by the operator into a rest.Config that reaches kcp
// through a local port-forward.
//
// Only the scheme and authority are replaced: the embedded CA and client certificate are kept, and
// the front-proxy's serving certificate carries "localhost" precisely so that verification still
// holds after the rewrite. Everything this test asserts about identity would be worthless against
// a connection that skipped verification.
func kcpConfig(t *testing.T, secret *corev1.Secret, localPort int, workspace string) *rest.Config {
	t.Helper()

	raw, ok := secret.Data["kubeconfig"]
	if !ok {
		t.Fatalf("Secret %s/%s has no kubeconfig key.", secret.Namespace, secret.Name)
	}

	config, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		t.Fatalf("Secret %s/%s does not hold a usable kubeconfig: %v", secret.Namespace, secret.Name, err)
	}

	parsed, err := url.Parse(config.Host)
	if err != nil {
		t.Fatalf("Kubeconfig in %s/%s has an unparseable server %q: %v", secret.Namespace, secret.Name, config.Host, err)
	}

	config.Host = fmt.Sprintf("https://localhost:%d%s", localPort, parsed.Path)
	if workspace != "" {
		config.Host = fmt.Sprintf("https://localhost:%d/clusters/%s", localPort, workspace)
	}

	// Independent of what the test asserts, and low enough that a wedged front-proxy shows up as
	// a failure rather than as a test that hangs until the suite times out.
	config.Timeout = 30 * time.Second

	return config
}

// inWorkspace returns a connection to another workspace of the same kcp, reusing the credentials.
func inWorkspace(t *testing.T, config *rest.Config, workspace string) *cluster {
	t.Helper()

	scoped := rest.CopyConfig(config)

	parsed, err := url.Parse(config.Host)
	if err != nil {
		t.Fatalf("Unparseable host %q: %v", config.Host, err)
	}

	scoped.Host = fmt.Sprintf("%s://%s/clusters/%s", parsed.Scheme, parsed.Host, workspace)

	c, err := newCluster(scoped)
	if err != nil {
		t.Fatalf("Failed to connect to workspace %s: %v", workspace, err)
	}

	return c
}

// createWorkspace creates a kcp Workspace in the workspace c points at and waits for it to become
// Ready, which is when its own logical cluster can be addressed.
//
// Creating one that already exists is not an error: the init container creates the same path, and
// which of the two gets there first is not something this test should depend on.
func createWorkspace(t *testing.T, ctx context.Context, c *cluster, name string) {
	t.Helper()

	// The same type access-vw-init uses (bootstrap.DefaultWorkspaceType), so a workspace this test
	// pre-creates is indistinguishable from one the init container would have created itself.
	workspace := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"type": map[string]any{"name": "universal"}},
	}}

	gvr := schemaGVR("tenancy.kcp.io", "v1alpha1", "workspaces")

	var lastErr error

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		current, err := c.dynamic.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if err := createErr(c.dynamic.Resource(gvr).Create(ctx, workspace, metav1.CreateOptions{})); err != nil {
				lastErr = err
			}

			return false, nil
		}
		if err != nil {
			lastErr = err

			return false, nil
		}

		lastErr = nil

		phase, _, _ := unstructured.NestedString(current.Object, "status", "phase")

		return phase == "Ready", nil
	})
	if err != nil {
		t.Fatalf("Workspace %q never became ready: %v (last error: %v)", name, err, lastErr)
	}

	t.Logf("Workspace %q is ready.", name)
}

func schemaGVR(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
}

// createErr discards the created object and the AlreadyExists error, which every create in this
// package treats as success: the init container creates some of the same objects, and which of the
// two gets there first is not something a test should depend on.
func createErr(_ *unstructured.Unstructured, err error) error {
	if apierrors.IsAlreadyExists(err) {
		return nil
	}

	return err
}

// logicalClusterName reads the logical cluster identifier of the workspace c points at, which is
// the name SCAR responses report.
func logicalClusterName(t *testing.T, ctx context.Context, c *cluster) string {
	t.Helper()

	var name string

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		lc, err := c.get(ctx, "core.kcp.io", "v1alpha1", "logicalclusters", "", "cluster")
		if err != nil {
			return false, nil
		}

		name = lc.GetAnnotations()["kcp.io/cluster"]

		return name != "", nil
	})
	if err != nil {
		t.Fatalf("Failed to read the logical cluster name: %v", err)
	}

	return name
}

// splitImage splits "repository:tag" on the tag separator, ignoring a colon that is part of a
// registry's port.
func splitImage(image string) (repository, tag string, found bool) {
	index := strings.LastIndex(image, ":")
	if index < 0 || strings.Contains(image[index:], "/") {
		return "", "", false
	}

	return image[:index], image[index+1:], true
}
