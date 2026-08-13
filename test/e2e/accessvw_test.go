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

// Package e2e deploys the access virtual workspace the way a user does -- as a kcp-operator
// VirtualWorkspace object, with access-vw-init as an init container -- and checks that the result
// serves.
//
// The point of the test is the seam between this component and kcp-operator. kcp-operator grew
// support for custom virtual workspace servers with init containers, and covers that support with
// a mock server of its own; nothing there runs the real access-vw binaries. Meanwhile this
// repository's unit tests never leave the process. The wiring in between -- the operator rendering
// a Deployment this image accepts, the init container reaching kcp with one identity and the
// server with another, the front-proxy routing to the result -- was covered by neither, and it is
// exactly what breaks when either side changes.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	accessv1alpha1 "github.com/kcp-dev/contrib-access-virtual-workspace/sdk/apis/access/v1alpha1"
)

const (
	// virtualWorkspaceName is also the prefix of the Deployment and Service the operator creates,
	// and therefore part of the front-proxy path mapping's backend.
	virtualWorkspaceName = "access"

	// The workspace the APIExport is installed into, split the way access-vw-init takes it.
	workspacePrefix          = "root:access"
	controllersWorkspaceName = "controllers"
	controllersWorkspace     = workspacePrefix + ":" + controllersWorkspaceName

	// apiExportName is the APIExport, the APIExportEndpointSlice and the consumer APIBinding, all
	// of which access-vw-init names after the API group it serves.
	apiExportName = "access.contrib.kcp.io"

	// serverUsername is the identity the server container runs as: a client certificate with no
	// groups, holding only what the test binds it to in the controllers workspace.
	serverUsername = "access-vw"

	// consumerUsername is an ordinary user with access to one workspace and not the other. What
	// SCAR reports for this identity is the test's main functional assertion.
	consumerUsername = "alice"

	// scarPath is where the front-proxy's additionalPathMappings entry sends requests, and the
	// path the APIExport's virtual workspace serves SelfClusterAccessReview under.
	scarPath = "/services/access/apis/access.contrib.kcp.io/v1alpha1/selfclusteraccessreviews"
)

// templateData is what every embedded manifest is rendered against.
type templateData struct {
	Namespace                string
	FrontProxyHostname       string
	VirtualWorkspaceName     string
	ImageRepository          string
	ImageTag                 string
	WorkspacePrefix          string
	ControllersWorkspaceName string
	ControllersWorkspace     string
	APIExportName            string
	ServerUsername           string
	ConsumerUsername         string
}

// TestAccessVirtualWorkspace stands up kcp through kcp-operator, deploys this component as a
// VirtualWorkspace, and then asks it a question only a correctly wired deployment can answer.
//
// The assertions build on each other, so they run as subtests of a single deployment rather than
// as separate tests: standing up kcp takes minutes, and a failure in an early stage makes the
// later ones meaningless anyway.
func TestAccessVirtualWorkspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	workload := workloadCluster(t)
	namespace := createNamespace(t, ctx, workload, "e2e-access")

	repository, tag := accessVWImage(t)

	data := templateData{
		Namespace: namespace,
		// The front-proxy's own in-cluster Service name. Using it as the external hostname means
		// the credentials the operator mints resolve from inside the cluster without host aliases
		// or a hard-coded Service IP.
		FrontProxyHostname:       fmt.Sprintf("front-proxy-front-proxy.%s.svc.cluster.local", namespace),
		VirtualWorkspaceName:     virtualWorkspaceName,
		ImageRepository:          repository,
		ImageTag:                 tag,
		WorkspacePrefix:          workspacePrefix,
		ControllersWorkspaceName: controllersWorkspaceName,
		ControllersWorkspace:     controllersWorkspace,
		APIExportName:            apiExportName,
		ServerUsername:           serverUsername,
		ConsumerUsername:         consumerUsername,
	}

	t.Log("Deploying etcd...")
	workload.apply(t, ctx, "etcd.yaml.tmpl", data)
	waitForPods(t, ctx, workload, namespace, "app.kubernetes.io/name=etcd", 5*time.Minute)

	t.Log("Deploying kcp and minting credentials...")
	workload.apply(t, ctx, "kcp.yaml.tmpl", data)
	waitForPods(t, ctx, workload, namespace, "app.kubernetes.io/component=rootshard", 10*time.Minute)
	waitForPods(t, ctx, workload, namespace, "app.kubernetes.io/component=front-proxy", 10*time.Minute)

	adminSecret := waitForSecret(t, ctx, workload, namespace, "admin-kubeconfig")

	frontProxyPort := portForward(t, ctx, namespace, "svc/front-proxy-front-proxy", 6443)
	adminConfig := kcpConfig(t, adminSecret, frontProxyPort, "root")

	root, err := newCluster(adminConfig)
	if err != nil {
		t.Fatalf("Failed to connect to kcp: %v", err)
	}
	waitForKcp(t, ctx, root)

	// The workspace path and the server's rights inside it are created before the VirtualWorkspace
	// exists, not after.
	//
	// The init container creates the same path and is idempotent, so this is not a duplicate of
	// its work -- it is what removes a race. The server container starts the moment the init
	// container exits, and its very first request needs the ClusterRole below to already be in
	// place; binding it afterwards would mean waiting out a crash loop and calling that success.
	t.Log("Preparing the workspace the APIExport will live in...")
	createWorkspace(t, ctx, root, "access")

	accessWorkspace := inWorkspace(t, adminConfig, workspacePrefix)
	createWorkspace(t, ctx, accessWorkspace, controllersWorkspaceName)

	controllers := inWorkspace(t, adminConfig, controllersWorkspace)
	controllers.apply(t, ctx, "server-rbac.yaml.tmpl", data)

	// Waited on only now, because the server's credential is scoped to the workspace that has just
	// been created. Waiting for it earlier would be asking the operator to mint a kubeconfig for a
	// workspace this test has not created yet.
	waitForSecret(t, ctx, workload, namespace, "access-vw-server-kubeconfig")
	consumerSecret := waitForSecret(t, ctx, workload, namespace, consumerUsername+"-kubeconfig")

	t.Log("Deploying the access virtual workspace...")
	workload.apply(t, ctx, "virtualworkspace.yaml.tmpl", data)

	// Readiness is the first real assertion. The pod is ready only once the init container exited
	// 0 -- meaning it reached kcp with the administrative identity and installed the APIExport --
	// and the server survived flag parsing and its own connection to kcp with the scoped identity.
	t.Log("Waiting for the virtual workspace to become ready...")
	waitForPods(t, ctx, workload, namespace,
		"app.kubernetes.io/component=virtual-workspace,app.kubernetes.io/instance="+virtualWorkspaceName,
		10*time.Minute)

	t.Run("Deployment", func(t *testing.T) {
		assertDeploymentShape(t, ctx, workload, namespace)
	})

	t.Run("Bootstrap", func(t *testing.T) {
		assertBootstrapped(t, ctx, controllers)
	})

	t.Run("SelfClusterAccessReview", func(t *testing.T) {
		assertSCAR(t, ctx, adminConfig, consumerSecret, frontProxyPort, data)
	})
}

// accessVWImage is the image under test. hack/ci/run-e2e-tests.sh builds it from the working tree
// and loads it into the cluster, so what runs here is this checkout rather than a published tag.
func accessVWImage(t *testing.T) (repository, tag string) {
	t.Helper()

	image := os.Getenv("ACCESS_VW_IMG")
	if image == "" {
		t.Fatal("ACCESS_VW_IMG is not set; run the e2e tests through hack/ci/run-e2e-tests.sh.")
	}

	repository, tag, found := splitImage(image)
	if !found {
		t.Fatalf("ACCESS_VW_IMG %q has no tag.", image)
	}

	return repository, tag
}

// waitForKcp waits until kcp answers through the port-forward. The front-proxy's pod is ready
// before it can serve the root workspace, and connecting too early fails in ways that read like
// a broken certificate.
func waitForKcp(t *testing.T, ctx context.Context, c *cluster) {
	t.Helper()

	var lastErr error

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := c.get(ctx, "core.kcp.io", "v1alpha1", "logicalclusters", "", "cluster")
		lastErr = err

		return err == nil, nil
	})
	if err != nil {
		t.Fatalf("kcp never became reachable: %v (last error: %v)", err, lastErr)
	}

	t.Log("kcp is reachable.")
}

// assertDeploymentShape checks what the operator rendered, which is the part of the contract the
// running server cannot observe about itself.
func assertDeploymentShape(t *testing.T, ctx context.Context, workload *cluster, namespace string) {
	t.Helper()

	deployment, err := workload.kube.AppsV1().Deployments(namespace).Get(ctx, virtualWorkspaceName+"-virtual-workspace", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get the Deployment: %v", err)
	}

	podSpec := deployment.Spec.Template.Spec

	if len(podSpec.InitContainers) != 1 {
		t.Fatalf("Expected exactly one init container, got %d.", len(podSpec.InitContainers))
	}
	if len(podSpec.Containers) != 1 {
		t.Fatalf("Expected exactly one container, got %d.", len(podSpec.Containers))
	}

	init := podSpec.InitContainers[0]
	server := podSpec.Containers[0]

	if got := init.Command; len(got) != 1 || got[0] != "/access-vw-init" {
		t.Errorf("Init container runs %v, expected /access-vw-init.", got)
	}
	if got := server.Command; len(got) != 1 || got[0] != "/access-vw" {
		t.Errorf("Server runs %v, expected the image's own entrypoint /access-vw.", got)
	}

	// Flags only kcp's own virtual-workspace server understands must never be generated for a
	// custom server: access-vw exits on an unknown flag, so this would have shown up as a pod
	// that never starts. Asserting it directly names the cause.
	for _, arg := range server.Args {
		for _, forbidden := range []string{"--shard-external-url", "--cache-kubeconfig"} {
			if strings.HasPrefix(arg, forbidden) {
				t.Errorf("Server was passed %q, which only kcp's own virtual-workspace server accepts.", arg)
			}
		}
	}

	// The flags access-vw does need in order to run behind the front-proxy. Without the
	// requestheader CA it refuses to start at all; without a serving certificate the front-proxy
	// would not trust it.
	for _, required := range []string{"--requestheader-client-ca-file", "--tls-cert-file", "--kubeconfig"} {
		if !hasFlag(server.Args, required) {
			t.Errorf("Server was not passed %s; it has %v.", required, server.Args)
		}
	}

	// Each container sees its own credential and not the other's. This is what makes the
	// privilege split real rather than nominal: the server never has the administrative
	// kubeconfig on disk.
	assertMounts(t, "server", server, "/etc/kcp/server-kubeconfig", "/etc/kcp/init-kubeconfig")
	assertMounts(t, "init", init, "/etc/kcp/init-kubeconfig", "/etc/kcp/server-kubeconfig")

	assertContainerSucceeded(t, ctx, workload, namespace, "init")
}

func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}

	return false
}

func assertMounts(t *testing.T, what string, container corev1.Container, want, unwanted string) {
	t.Helper()

	paths := map[string]bool{}
	for _, mount := range container.VolumeMounts {
		paths[mount.MountPath] = true
	}

	if !paths[want] {
		t.Errorf("The %s container is missing %q; it has %v.", what, want, container.VolumeMounts)
	}
	if paths[unwanted] {
		t.Errorf("The %s container has %q mounted, so the two identities are not separated.", what, unwanted)
	}
}

// assertContainerSucceeded checks that the named init container really ran and exited cleanly,
// rather than being skipped.
func assertContainerSucceeded(t *testing.T, ctx context.Context, workload *cluster, namespace, container string) {
	t.Helper()

	pods, err := workload.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/component=virtual-workspace,app.kubernetes.io/instance=" + virtualWorkspaceName,
	})
	if err != nil {
		t.Fatalf("Failed to list virtual workspace pods: %v", err)
	}
	if len(pods.Items) == 0 {
		t.Fatal("No virtual workspace pods found.")
	}

	for _, pod := range pods.Items {
		found := false

		for _, status := range pod.Status.InitContainerStatuses {
			if status.Name != container {
				continue
			}

			found = true

			terminated := status.State.Terminated
			if terminated == nil {
				t.Errorf("Init container %s of %s has not terminated.", container, pod.Name)
			} else if terminated.ExitCode != 0 {
				t.Errorf("Init container %s of %s exited with %d (%s).", container, pod.Name, terminated.ExitCode, terminated.Reason)
			}
		}

		if !found {
			t.Errorf("Pod %s has no init container named %s.", pod.Name, container)
		}
	}
}

// assertBootstrapped checks that the init container installed what the server needs, in the
// workspace it was told to install it into.
//
// Deliberately not asserted here: that the APIExportEndpointSlice resolves to any URLs. kcp only
// advertises endpoints once a workspace has bound the export, so at this point an empty slice is
// the correct state, not a broken one. That it fills in is asserted in the SCAR stage below, where
// a consumer exists and the assertion means something.
func assertBootstrapped(t *testing.T, ctx context.Context, controllers *cluster) {
	t.Helper()

	export, err := controllers.get(ctx, "apis.kcp.io", "v1alpha2", "apiexports", "", apiExportName)
	if err != nil {
		t.Fatalf("The init container did not install the APIExport %q in %s: %v", apiExportName, controllersWorkspace, err)
	}

	// The schema's name carries a version prefix this test should not have to track, so it is
	// read off the export rather than guessed, and then looked up to confirm it really exists.
	schemas, _, _ := unstructured.NestedSlice(export.Object, "spec", "resources")
	if len(schemas) == 0 {
		t.Errorf("APIExport %q references no resource schemas.", apiExportName)
	}

	for _, entry := range schemas {
		resource, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		schemaName, _, _ := unstructured.NestedString(resource, "schema")
		if schemaName == "" {
			continue
		}

		if _, err := controllers.get(ctx, "apis.kcp.io", "v1alpha1", "apiresourceschemas", "", schemaName); err != nil {
			t.Errorf("APIExport %q references APIResourceSchema %q, which is not installed: %v", apiExportName, schemaName, err)
		}
	}

	// The slice has to exist and be accepted, which is as much as can be true before a consumer
	// binds. It is what --apiexport-endpointslice names, so a missing one means the server watches
	// an object that is not there and reports itself ready while serving an empty graph.
	slice, err := controllers.get(ctx, "apis.kcp.io", "v1alpha1", "apiexportendpointslices", "", apiExportName)
	if err != nil {
		t.Fatalf("No APIExportEndpointSlice %q in %s: %v", apiExportName, controllersWorkspace, err)
	}

	conditions, _, _ := unstructured.NestedSlice(slice.Object, "status", "conditions")
	for _, condition := range conditions {
		entry, ok := condition.(map[string]any)
		if !ok {
			continue
		}

		kind, _, _ := unstructured.NestedString(entry, "type")
		status, _, _ := unstructured.NestedString(entry, "status")

		if kind == "APIExportValid" && status != "True" {
			reason, _, _ := unstructured.NestedString(entry, "message")
			t.Errorf("APIExportEndpointSlice %q reports APIExportValid=%s: %s", apiExportName, status, reason)
		}
	}
}

// waitForEndpoints waits for kcp to advertise virtual workspace URLs on the endpoint slice.
//
// This only happens once a workspace has an APIBinding to the export, which is why it is checked
// after the consumers exist rather than as part of bootstrapping. Until it does, the RBAC provider
// has nothing to watch and every SCAR answer is empty, so this is the step that separates "the
// server started" from "the server can answer".
func waitForEndpoints(t *testing.T, ctx context.Context, controllers *cluster) {
	t.Helper()

	var urls []string

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		slice, err := controllers.get(ctx, "apis.kcp.io", "v1alpha1", "apiexportendpointslices", "", apiExportName)
		if err != nil {
			return false, nil
		}

		endpoints, found, err := unstructured.NestedSlice(slice.Object, "status", "endpoints")
		if err != nil || !found {
			return false, nil
		}

		urls = nil
		for _, endpoint := range endpoints {
			entry, ok := endpoint.(map[string]any)
			if !ok {
				continue
			}
			if url, ok := entry["url"].(string); ok && url != "" {
				urls = append(urls, url)
			}
		}

		return len(urls) > 0, nil
	})
	if err != nil {
		t.Fatalf("APIExportEndpointSlice %q resolved to no virtual workspace URLs even after a workspace bound the export: %v", apiExportName, err)
	}

	t.Logf("APIExportEndpointSlice %q resolves to %v.", apiExportName, urls)
}

// assertSCAR is the functional half of the test: an ordinary user asks the virtual workspace which
// workspaces they can reach, and the answer has to match what RBAC actually says.
//
// Everything else here could pass with a server that starts and answers nothing. This is the only
// assertion that requires the whole path to work -- the front-proxy routing the request, the
// server trusting the identity it forwards, and the RBAC provider having indexed the consumer
// workspaces through the APIExport's own virtual workspace.
func assertSCAR(t *testing.T, ctx context.Context, adminConfig *rest.Config, consumerSecret *corev1.Secret, frontProxyPort int, data templateData) {
	t.Helper()

	// Two consumer workspaces, both bound to the export, differing only in whether the consumer
	// has any rights there. A server that reported every workspace it indexes -- rather than the
	// ones this identity can reach -- would return both.
	granted := createConsumerWorkspace(t, ctx, adminConfig, data, "granted", true)
	withheld := createConsumerWorkspace(t, ctx, adminConfig, data, "withheld", false)

	t.Logf("Consumer workspaces: granted=%s withheld=%s", granted, withheld)

	waitForEndpoints(t, ctx, inWorkspace(t, adminConfig, controllersWorkspace))

	consumerConfig := kcpConfig(t, consumerSecret, frontProxyPort, "root")

	transport, err := rest.TransportFor(consumerConfig)
	if err != nil {
		t.Fatalf("Failed to build a client for %s: %v", consumerUsername, err)
	}

	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	url := fmt.Sprintf("https://localhost:%d%s", frontProxyPort, scarPath)

	var (
		review   accessv1alpha1.SelfClusterAccessReview
		lastNote string
	)

	// The provider learns about a workspace through informers over the APIExport's virtual
	// workspace, so a freshly bound workspace takes a moment to show up. Polling for the expected
	// answer rather than asserting the first one distinguishes "not yet" from "never".
	err = wait.PollUntilContextTimeout(ctx, 3*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		review = accessv1alpha1.SelfClusterAccessReview{}

		body, status, err := postSCAR(ctx, client, url)
		if err != nil {
			lastNote = err.Error()

			return false, nil
		}
		if status != http.StatusCreated && status != http.StatusOK {
			lastNote = fmt.Sprintf("HTTP %d: %s", status, truncate(string(body)))

			return false, nil
		}

		if err := json.Unmarshal(body, &review); err != nil {
			return false, fmt.Errorf("response is not a SelfClusterAccessReview: %q", truncate(string(body)))
		}

		return slices.ContainsFunc(review.Status.Clusters, func(c accessv1alpha1.AccessEndpointSlice) bool {
			return c.ClusterName == granted
		}), nil
	})
	if err != nil {
		t.Fatalf("SelfClusterAccessReview never reported the workspace %s can reach: %v (last: %s)", consumerUsername, err, lastNote)
	}

	names := make([]string, 0, len(review.Status.Clusters))
	for _, cluster := range review.Status.Clusters {
		names = append(names, cluster.ClusterName)
	}

	t.Logf("SelfClusterAccessReview for %s reports %v.", consumerUsername, names)

	if slices.Contains(names, withheld) {
		t.Errorf("SelfClusterAccessReview reported %s, which %s has no rights in; the answer is not scoped to the caller.", withheld, consumerUsername)
	}

	// The endpoint is what a client would connect to next, so an empty or in-cluster one would
	// make the answer useless even though the cluster list is right.
	wantPrefix := fmt.Sprintf("https://%s:6443/clusters/", data.FrontProxyHostname)
	for _, cluster := range review.Status.Clusters {
		if !strings.HasPrefix(cluster.Endpoint, wantPrefix) {
			t.Errorf("Cluster %s has endpoint %q, expected it to start with %q (--endpoint-base).", cluster.ClusterName, cluster.Endpoint, wantPrefix)
		}
	}
}

func postSCAR(ctx context.Context, client *http.Client, url string) ([]byte, int, error) {
	body := strings.NewReader(`{"apiVersion":"access.contrib.kcp.io/v1alpha1","kind":"SelfClusterAccessReview"}`)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, 0, err
	}

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, response.StatusCode, err
	}

	return raw, response.StatusCode, nil
}

// createConsumerWorkspace creates a workspace under the controllers workspace, binds the exported
// API in it, and optionally grants the consumer rights there. It returns the logical cluster name,
// which is what SCAR answers with.
func createConsumerWorkspace(t *testing.T, ctx context.Context, adminConfig *rest.Config, data templateData, name string, grant bool) string {
	t.Helper()

	controllers := inWorkspace(t, adminConfig, controllersWorkspace)
	createWorkspace(t, ctx, controllers, name)

	workspace := inWorkspace(t, adminConfig, controllersWorkspace+":"+name)
	workspace.apply(t, ctx, "consumer.yaml.tmpl", data)
	waitForAPIBinding(t, ctx, workspace)

	if grant {
		binding := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRoleBinding",
			"metadata":   map[string]any{"name": consumerUsername + "-admin"},
			"subjects": []any{map[string]any{
				"kind":     "User",
				"name":     consumerUsername,
				"apiGroup": "rbac.authorization.k8s.io",
			}},
			"roleRef": map[string]any{
				"kind":     "ClusterRole",
				"name":     "cluster-admin",
				"apiGroup": "rbac.authorization.k8s.io",
			},
		}}

		gvr := schemaGVR("rbac.authorization.k8s.io", "v1", "clusterrolebindings")
		if _, err := workspace.dynamic.Resource(gvr).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
			t.Fatalf("Failed to grant %s access to %s: %v", consumerUsername, name, err)
		}
	}

	return logicalClusterName(t, ctx, workspace)
}

// waitForAPIBinding waits until the binding's permission claims are accepted. Until then the
// provider cannot read the workspace's RBAC, and a SCAR answer would be empty for reasons that
// have nothing to do with the caller.
func waitForAPIBinding(t *testing.T, ctx context.Context, workspace *cluster) {
	t.Helper()

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		binding, err := workspace.get(ctx, "apis.kcp.io", "v1alpha2", "apibindings", "", apiExportName)
		if err != nil {
			return false, nil
		}

		phase, _, _ := unstructured.NestedString(binding.Object, "status", "phase")
		declared, _, _ := unstructured.NestedSlice(binding.Object, "spec", "permissionClaims")
		applied, _, _ := unstructured.NestedSlice(binding.Object, "status", "appliedPermissionClaims")

		return phase == "Bound" && len(applied) >= len(declared), nil
	})
	if err != nil {
		t.Fatalf("APIBinding %q never became bound with all claims accepted: %v", apiExportName, err)
	}
}

func truncate(s string) string {
	const limit = 512
	if len(s) <= limit {
		return s
	}

	return s[:limit] + "..."
}
