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

// Package e2e deploys this server the way a user does -- as a kcp-operator VirtualWorkspace behind
// kcp's front-proxy, alongside the access virtual workspace it depends on -- and then speaks MCP to
// it as an ordinary human would.
//
// The point of the test is the seams, which no unit test in this repository can reach. There are
// three: kcp-operator has to render a Deployment this binary accepts; the front-proxy has to route
// /services/mcp and turn a client certificate into the identity headers this server authenticates;
// and this server has to reach the access virtual workspace as that caller, by impersonation,
// because the caller's own token was consumed by the proxy and never arrives here. Every one of
// those is a contract with another component, and each is exactly the kind of thing that breaks
// when the other side changes.
package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	// The same client types this server uses, from the module version go.mod pins -- deliberately
	// not the access repository's working tree. The image under test there is built from its main
	// branch, so asserting against the pinned types is what checks that the wire contract this
	// server compiles against still matches the server that answers.
	accessv1alpha1 "github.com/kcp-dev/contrib-access-virtual-workspace/pkg/apis/access/v1alpha1"
)

const (
	// The VirtualWorkspace names, which are also the prefix of the Deployment and Service
	// kcp-operator creates for each, and therefore part of the front-proxy path mappings' backends.
	accessVirtualWorkspaceName = "access"
	mcpVirtualWorkspaceName    = "mcp"

	// The workspace the APIExport is installed into, split the way access-vw-init takes it.
	workspacePrefix          = "root:access"
	controllersWorkspaceName = "controllers"
	controllersWorkspace     = workspacePrefix + ":" + controllersWorkspaceName

	// apiExportName is the APIExport, the APIExportEndpointSlice and the consumer APIBinding, all
	// of which access-vw-init names after the API group it serves.
	apiExportName = "access.contrib.kcp.io"

	// accessServerUsername is the identity the access virtual workspace runs as.
	accessServerUsername = "access-vw"

	// mcpServerUsername is the identity this server runs as: a client certificate with no groups,
	// holding nothing but impersonation rights.
	mcpServerUsername = "mcp-virtual-workspace"

	// consumerUsername is an ordinary user with access to one workspace and not the other. What MCP
	// reports for this identity is the test's main functional assertion.
	consumerUsername = "alice"

	// The agent: a bearer-token identity from the front-proxy's static token file, holding rights
	// in the workspace alice does not have and none in the one she does.
	//
	// Deliberately the mirror image of alice rather than a copy. Both identities ask the same
	// server the same question through the same path, so if the answer followed anything other than
	// the caller -- a cached list, the server's own rights, the last caller seen -- the two would
	// agree, and the test would notice. The uid is only there because kcp's token CSV format has a
	// column for it.
	agentUsername = "agent"
	agentUID      = "e2e-agent"
	agentToken    = "e2e-static-token-not-a-secret"

	// mcpPath is where the front-proxy's additionalPathMappings entry sends requests, and the path
	// this server registers as a raw handler.
	mcpPath = "/services/mcp"

	// scarPath is the same for the access virtual workspace, down to the resource: the test asks it
	// directly so that a broken dependency is reported as one.
	scarPath = "/services/access/apis/access.contrib.kcp.io/v1alpha1/selfclusteraccessreviews"

	// listWorkspacesTool is the only tool this server advertises today. When the handlers from the
	// proof of concept land (see internal/mcp/server.go), this test is where they get their
	// end-to-end coverage.
	listWorkspacesTool = "list_workspaces"

	mcpPodSelector = "app.kubernetes.io/component=virtual-workspace,app.kubernetes.io/instance=" + mcpVirtualWorkspaceName
)

// templateData is what every embedded manifest is rendered against.
type templateData struct {
	Namespace                  string
	FrontProxyHostname         string
	AccessVirtualWorkspaceName string
	MCPVirtualWorkspaceName    string
	AccessImageRepository      string
	AccessImageTag             string
	MCPImageRepository         string
	MCPImageTag                string
	WorkspacePrefix            string
	ControllersWorkspaceName   string
	ControllersWorkspace       string
	APIExportName              string
	AccessServerUsername       string
	MCPServerUsername          string
	ConsumerUsername           string
	AgentUsername              string
	AgentUID                   string
	AgentToken                 string
}

// workspaceEntry is one element of the list_workspaces tool's structured output, mirroring the
// anonymous type internal/mcp/server.go returns.
type workspaceEntry struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
}

type listWorkspacesOutput struct {
	Workspaces []workspaceEntry `json:"workspaces"`
}

// TestMCPVirtualWorkspace stands up kcp through kcp-operator, deploys the access virtual workspace
// and this one into it, and then asks MCP a question only a correctly wired deployment can answer.
//
// The assertions build on each other, so they run as subtests of a single deployment rather than as
// separate tests: standing up kcp takes minutes, and a failure in an early stage makes the later
// ones meaningless anyway.
func TestMCPVirtualWorkspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	workload := workloadCluster(t)
	namespace := createNamespace(t, ctx, workload, "e2e-mcp")

	mcpRepository, mcpTag := imageUnderTest(t, "MCP_VW_IMG")
	accessRepository, accessTag := imageUnderTest(t, "ACCESS_VW_IMG")

	data := templateData{
		Namespace: namespace,
		// The front-proxy's own in-cluster Service name. Using it as the external hostname means
		// the credentials the operator mints resolve from inside the cluster without host aliases
		// or a hard-coded Service IP -- and this server calls the front-proxy from inside the
		// cluster, so it has to.
		FrontProxyHostname:         fmt.Sprintf("front-proxy-front-proxy.%s.svc.cluster.local", namespace),
		AccessVirtualWorkspaceName: accessVirtualWorkspaceName,
		MCPVirtualWorkspaceName:    mcpVirtualWorkspaceName,
		AccessImageRepository:      accessRepository,
		AccessImageTag:             accessTag,
		MCPImageRepository:         mcpRepository,
		MCPImageTag:                mcpTag,
		WorkspacePrefix:            workspacePrefix,
		ControllersWorkspaceName:   controllersWorkspaceName,
		ControllersWorkspace:       controllersWorkspace,
		APIExportName:              apiExportName,
		AccessServerUsername:       accessServerUsername,
		MCPServerUsername:          mcpServerUsername,
		ConsumerUsername:           consumerUsername,
		AgentUsername:              agentUsername,
		AgentUID:                   agentUID,
		AgentToken:                 agentToken,
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

	// The workspace path and the access server's rights inside it are created before the
	// VirtualWorkspace exists, not after.
	//
	// The init container creates the same path and is idempotent, so this is not a duplicate of its
	// work -- it is what removes a race. The server container starts the moment the init container
	// exits, and its very first request needs the ClusterRole to already be in place; binding it
	// afterwards would mean waiting out a crash loop and calling that success.
	t.Log("Preparing the workspace the APIExport will live in...")
	createWorkspace(t, ctx, root, "access")

	accessWorkspace := inWorkspace(t, adminConfig, workspacePrefix)
	createWorkspace(t, ctx, accessWorkspace, controllersWorkspaceName)

	controllers := inWorkspace(t, adminConfig, controllersWorkspace)
	controllers.apply(t, ctx, "access-rbac.yaml.tmpl", data)

	// Waited on only now, because the access server's credential is scoped to the workspace that
	// has just been created. Waiting for it earlier would be asking the operator to mint a
	// kubeconfig for a workspace this test has not created yet.
	waitForSecret(t, ctx, workload, namespace, "access-vw-server-kubeconfig")
	waitForSecret(t, ctx, workload, namespace, "mcp-vw-server-kubeconfig")
	consumerSecret := waitForSecret(t, ctx, workload, namespace, consumerUsername+"-kubeconfig")

	t.Log("Deploying the access virtual workspace...")
	workload.apply(t, ctx, "access-virtualworkspace.yaml.tmpl", data)
	waitForPods(t, ctx, workload, namespace,
		"app.kubernetes.io/component=virtual-workspace,app.kubernetes.io/instance="+accessVirtualWorkspaceName,
		10*time.Minute)

	// Readiness is the first real assertion about this component. The pod is ready only once the
	// server survived flag parsing -- including the flags kcp-operator generated rather than this
	// repository -- and its own TLS and authentication setup.
	t.Log("Deploying the MCP virtual workspace...")
	workload.apply(t, ctx, "mcp-virtualworkspace.yaml.tmpl", data)
	waitForPods(t, ctx, workload, namespace, mcpPodSelector, 10*time.Minute)

	// Two consumer workspaces, both bound to the export, with the two caller identities granted in
	// one each. A server that reported every workspace it knows about -- rather than the ones the
	// caller can reach -- would return both to both.
	t.Log("Creating the consumer workspaces...")
	granted := createConsumerWorkspace(t, ctx, adminConfig, data, "granted", consumerUsername)
	withheld := createConsumerWorkspace(t, ctx, adminConfig, data, "withheld", agentUsername)

	t.Logf("Consumer workspaces: granted=%s (%s) withheld=%s (%s)",
		granted, consumerUsername, withheld, agentUsername)

	waitForEndpoints(t, ctx, controllers)

	consumerConfig := kcpConfig(t, consumerSecret, frontProxyPort, "root")

	t.Run("Deployment", func(t *testing.T) {
		assertDeploymentShape(t, ctx, workload, namespace)
	})

	// Run before anything MCP, so that a failure in the dependency is reported as a failure in the
	// dependency. If this passes and the MCP subtests do not, the fault is in this repository.
	t.Run("AccessVirtualWorkspace", func(t *testing.T) {
		assertSCAR(t, ctx, consumerConfig, frontProxyPort, granted, withheld)
	})

	t.Run("Tools", func(t *testing.T) {
		assertToolsAdvertised(t, ctx, workload, namespace, consumerConfig, frontProxyPort)
	})

	t.Run("ListWorkspaces", func(t *testing.T) {
		assertListWorkspaces(t, ctx, workload, namespace, consumerConfig, frontProxyPort, data,
			consumerUsername, granted, withheld)
	})

	// The same question over the same path with a bearer token instead of a client certificate,
	// which is what an MCP client actually holds. The expectations are the mirror image of alice's,
	// so this only passes if the answer really followed the credential.
	t.Run("BearerToken", func(t *testing.T) {
		assertListWorkspaces(t, ctx, workload, namespace, tokenConfig(t, adminConfig, agentToken), frontProxyPort, data,
			agentUsername, withheld, granted)
	})

	t.Run("Unauthenticated", func(t *testing.T) {
		assertAnonymousRefused(t, ctx, adminConfig, frontProxyPort)
	})
}

// imageUnderTest reads one of the images hack/ci/run-e2e-tests.sh built and loaded into the
// cluster, so what runs here is a working tree rather than a published tag.
func imageUnderTest(t *testing.T, variable string) (repository, tag string) {
	t.Helper()

	image := os.Getenv(variable)
	if image == "" {
		t.Fatalf("%s is not set; run the e2e tests through hack/ci/run-e2e-tests.sh.", variable)
	}

	repository, tag, found := splitImage(image)
	if !found {
		t.Fatalf("%s %q has no tag.", variable, image)
	}

	return repository, tag
}

// waitForKcp waits until kcp answers through the port-forward. The front-proxy's pod is ready
// before it can serve the root workspace, and connecting too early fails in ways that read like a
// broken certificate.
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

	deployment, err := workload.kube.AppsV1().Deployments(namespace).Get(ctx, mcpVirtualWorkspaceName+"-virtual-workspace", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get the Deployment: %v", err)
	}

	podSpec := deployment.Spec.Template.Spec

	if len(podSpec.InitContainers) != 0 {
		t.Errorf("Expected no init containers, got %d: this server bootstraps nothing.", len(podSpec.InitContainers))
	}
	if len(podSpec.Containers) != 1 {
		t.Fatalf("Expected exactly one container, got %d.", len(podSpec.Containers))
	}

	server := podSpec.Containers[0]

	// The subcommand has to be part of command rather than extraArgs: the operator appends only
	// flags, and the root command with no subcommand prints its help and exits, which surfaces as a
	// pod that never becomes ready rather than as anything about MCP.
	if want := []string{"/mcp-virtual-workspace", "serve"}; !slices.Equal(server.Command, want) {
		t.Errorf("Server runs %v, expected %v.", server.Command, want)
	}

	// Flags only kcp's own virtual-workspace server understands must never be generated for a
	// custom server: this binary exits on an unknown flag, so it would have shown up as a pod that
	// never starts. Asserting it directly names the cause.
	for _, arg := range server.Args {
		for _, forbidden := range []string{"--shard-external-url", "--cache-kubeconfig"} {
			if strings.HasPrefix(arg, forbidden) {
				t.Errorf("Server was passed %q, which only kcp's own virtual-workspace server accepts.", arg)
			}
		}
	}

	// What this server needs in order to run behind the front-proxy, generated by the operator
	// rather than by this repository. Without the requestheader CA it refuses to start at all --
	// every caller would be anonymous; without a serving certificate the front-proxy would not
	// trust it; without a kubeconfig it has no identity to impersonate from.
	for _, required := range []string{"--requestheader-client-ca-file", "--tls-cert-file", "--kubeconfig"} {
		if !hasFlag(server.Args, required) {
			t.Errorf("Server was not passed %s; it has %v.", required, server.Args)
		}
	}

	// Ours rather than the operator's, and the one flag without which the server exits during
	// validation.
	if !hasFlag(server.Args, "--access-url") {
		t.Errorf("Server was not passed --access-url; it has %v.", server.Args)
	}
}

func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}

	return false
}

// assertSCAR asks the access virtual workspace directly, as the consumer, for the answer this
// server is about to ask for on their behalf.
//
// It asserts nothing about MCP. It exists to split one failure into two: if the access VW cannot
// answer, every MCP assertion below fails too, and without this subtest the obvious reading would
// be that the MCP server is broken.
func assertSCAR(t *testing.T, ctx context.Context, consumerConfig *rest.Config, frontProxyPort int, granted, withheld string) {
	t.Helper()

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

	// The access VW learns about a workspace through informers over the APIExport's virtual
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

	for _, cluster := range review.Status.Clusters {
		if cluster.ClusterName == withheld {
			t.Errorf("SelfClusterAccessReview reported %s, which %s has no rights in.", withheld, consumerUsername)
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

// assertToolsAdvertised completes an MCP handshake as the consumer and lists the tools.
//
// This is already an end-to-end assertion rather than a formality: the handler resolves the
// caller's workspaces on every request, including this one, so a server that could not reach the
// access VW as the caller replaces its whole tool set with a single "error" tool. Getting
// list_workspaces back means the front-proxy routed, the identity headers were trusted, and the
// impersonated round trip to the access VW succeeded.
func assertToolsAdvertised(t *testing.T, ctx context.Context, workload *cluster, namespace string, consumerConfig *rest.Config, frontProxyPort int) {
	t.Helper()

	var lastNote string

	err := wait.PollUntilContextTimeout(ctx, 3*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		session, err := mcpSession(ctx, consumerConfig, frontProxyPort)
		if err != nil {
			lastNote = fmt.Sprintf("connecting: %v", err)

			return false, nil
		}
		defer session.Close()

		tools, err := session.ListTools(ctx, nil)
		if err != nil {
			lastNote = fmt.Sprintf("listing tools: %v", err)

			return false, nil
		}

		names := make([]string, 0, len(tools.Tools))
		for _, tool := range tools.Tools {
			names = append(names, tool.Name)
		}

		if slices.Contains(names, listWorkspacesTool) {
			t.Logf("The server advertises %v.", names)

			return true, nil
		}

		// The server reports why it is unavailable as a tool rather than as a transport error, so
		// calling it is how the reason gets into the test log.
		lastNote = fmt.Sprintf("tools are %v: %s", names, errorToolMessage(ctx, session))

		return false, nil
	})
	if err != nil {
		dumpMCPLogs(t, ctx, workload, namespace)
		t.Fatalf("The MCP server never advertised %s: %v (last: %s)", listWorkspacesTool, err, lastNote)
	}
}

// assertListWorkspaces is the functional half of the test: a client asks which workspaces the
// person it acts for can work on, and the answer has to match what RBAC actually says.
//
// Everything else here could pass with a server that starts and answers nothing. This is the only
// assertion that requires the whole path -- the front-proxy routing MCP, the server trusting the
// forwarded identity, the impersonated SelfClusterAccessReview, and the answer being the caller's
// rather than the server's own.
//
// Run once per credential. want and unwanted are swapped between them, so an answer that ignored
// the caller could not satisfy both.
func assertListWorkspaces(t *testing.T, ctx context.Context, workload *cluster, namespace string, callerConfig *rest.Config, frontProxyPort int, data templateData, who, want, unwanted string) {
	t.Helper()

	var (
		result   listWorkspacesOutput
		lastNote string
	)

	err := wait.PollUntilContextTimeout(ctx, 3*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		session, err := mcpSession(ctx, callerConfig, frontProxyPort)
		if err != nil {
			lastNote = fmt.Sprintf("connecting: %v", err)

			return false, nil
		}
		defer session.Close()

		// An empty object rather than no arguments at all: the tool takes an empty struct, and its
		// schema is an object.
		call, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
			Name:      listWorkspacesTool,
			Arguments: map[string]any{},
		})
		if err != nil {
			lastNote = fmt.Sprintf("calling %s: %v", listWorkspacesTool, err)

			return false, nil
		}
		if call.IsError {
			lastNote = toolText(call)

			return false, nil
		}

		result = listWorkspacesOutput{}
		if err := decodeStructured(call, &result); err != nil {
			lastNote = err.Error()

			return false, nil
		}

		return slices.ContainsFunc(result.Workspaces, func(w workspaceEntry) bool {
			return w.Name == want
		}), nil
	})
	if err != nil {
		dumpMCPLogs(t, ctx, workload, namespace)
		t.Fatalf("%s never reported the workspace %s can reach: %v (last: %s)", listWorkspacesTool, who, err, lastNote)
	}

	names := make([]string, 0, len(result.Workspaces))
	for _, workspace := range result.Workspaces {
		names = append(names, workspace.Name)
	}

	t.Logf("%s reports %v for %s.", listWorkspacesTool, names, who)

	if slices.Contains(names, unwanted) {
		t.Errorf("%s reported %s, which %s has no rights in; the answer is not scoped to the caller.",
			listWorkspacesTool, unwanted, who)
	}

	// The endpoint is what the agent's next call would go to, so an empty or in-cluster one would
	// make the answer useless even though the workspace list is right.
	wantPrefix := fmt.Sprintf("https://%s:6443/clusters/", data.FrontProxyHostname)
	for _, workspace := range result.Workspaces {
		if !strings.HasPrefix(workspace.Endpoint, wantPrefix) {
			t.Errorf("Workspace %s has endpoint %q, expected it to start with %q.", workspace.Name, workspace.Endpoint, wantPrefix)
		}
	}
}

// assertAnonymousRefused checks that a client with no credential is turned away.
//
// The server authorizes any authenticated caller, which is only safe if unauthenticated ones never
// get that far. Whether the refusal comes from the front-proxy or from this server's own authorizer
// is deliberately not asserted -- either is correct, and pinning it would break on a change to
// kcp's proxy that is none of this component's business.
func assertAnonymousRefused(t *testing.T, ctx context.Context, adminConfig *rest.Config, frontProxyPort int) {
	t.Helper()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(adminConfig.TLSClientConfig.CAData) {
		t.Fatal("The admin kubeconfig holds no usable CA bundle.")
	}

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		Timeout:   30 * time.Second,
	}

	// A bare initialize request: enough to be routed and authenticated, and rejected before any of
	// it is parsed.
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://localhost:%d%s", frontProxyPort, mcpPath), body)
	if err != nil {
		t.Fatalf("Failed to build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("Anonymous request failed at the transport: %v", err)
	}
	defer response.Body.Close()

	raw, _ := io.ReadAll(response.Body)

	if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
		t.Errorf("An anonymous request to %s returned HTTP %d, expected 401 or 403: %s", mcpPath, response.StatusCode, truncate(string(raw)))
	}
}

// mcpSession completes an MCP handshake through the front-proxy with the given credential.
//
// The transport is the caller's own rest.Config, so the client certificate the front-proxy
// authenticates is the consumer's -- the same thing an agent running on someone's laptop would
// present. The standalone SSE stream is disabled because the handler is stateless (SEP-1442): there
// is no session for the server to push notifications into.
func mcpSession(ctx context.Context, config *rest.Config, frontProxyPort int) (*mcpsdk.ClientSession, error) {
	transport, err := rest.TransportFor(config)
	if err != nil {
		return nil, fmt.Errorf("building a transport: %w", err)
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "mcp-vw-e2e", Version: "v1alpha1"}, nil)

	return client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:             fmt.Sprintf("https://localhost:%d%s", frontProxyPort, mcpPath),
		HTTPClient:           &http.Client{Transport: transport, Timeout: 60 * time.Second},
		DisableStandaloneSSE: true,
	}, nil)
}

// tokenConfig builds a credential that authenticates with nothing but a bearer token: no client
// certificate, only the CA needed to verify the front-proxy.
//
// That absence is the point. The front-proxy has to resolve this token against its static token
// file and forward the identity itself, so a server that quietly depended on certificate identity
// -- or on the caller's credential reaching it at all -- fails here and nowhere else.
func tokenConfig(t *testing.T, adminConfig *rest.Config, token string) *rest.Config {
	t.Helper()

	if len(adminConfig.TLSClientConfig.CAData) == 0 {
		t.Fatal("The admin kubeconfig holds no CA bundle to verify the front-proxy with.")
	}

	return &rest.Config{
		BearerToken:     token,
		TLSClientConfig: rest.TLSClientConfig{CAData: adminConfig.TLSClientConfig.CAData},
		Timeout:         30 * time.Second,
	}
}

// errorToolMessage calls the "error" tool, which is what the server advertises instead of its real
// tools when it could not resolve the caller's workspaces.
func errorToolMessage(ctx context.Context, session *mcpsdk.ClientSession) string {
	call, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "error", Arguments: map[string]any{}})
	if err != nil {
		return fmt.Sprintf("no error tool either: %v", err)
	}

	return toolText(call)
}

// toolText renders a tool result for a log line, preferring the structured output and falling back
// to the text content the SDK derives from it.
func toolText(call *mcpsdk.CallToolResult) string {
	if call.StructuredContent != nil {
		if raw, err := json.Marshal(call.StructuredContent); err == nil {
			return truncate(string(raw))
		}
	}

	parts := make([]string, 0, len(call.Content))
	for _, content := range call.Content {
		if text, ok := content.(*mcpsdk.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}

	return truncate(strings.Join(parts, " "))
}

// decodeStructured re-marshals a tool's structured output into a typed value. The SDK hands it back
// as `any`, and a round trip through JSON is both shorter and closer to what a real client does
// than reaching into the map.
func decodeStructured(call *mcpsdk.CallToolResult, into any) error {
	if call.StructuredContent == nil {
		return fmt.Errorf("tool returned no structured content: %s", toolText(call))
	}

	raw, err := json.Marshal(call.StructuredContent)
	if err != nil {
		return fmt.Errorf("re-marshalling structured content: %w", err)
	}

	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("structured content %s does not match the expected shape: %w", truncate(string(raw)), err)
	}

	return nil
}

// dumpMCPLogs prints the MCP server's own logs, which is where the reason for an unhelpful MCP
// answer actually is -- the protocol only carries "unavailable".
func dumpMCPLogs(t *testing.T, ctx context.Context, workload *cluster, namespace string) {
	t.Helper()

	pods, err := workload.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: mcpPodSelector})
	if err != nil {
		t.Logf("Failed to list the MCP server's pods: %v", err)

		return
	}

	for _, pod := range pods.Items {
		logContainers(t, ctx, workload, pod)
	}
}

// waitForEndpoints waits for kcp to advertise virtual workspace URLs on the access VW's endpoint
// slice.
//
// This only happens once a workspace has an APIBinding to the export. Until it does, the access VW
// has nothing to watch and every answer it gives is empty, so this is the step that separates "the
// servers started" from "the servers can answer".
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

// createConsumerWorkspace creates a workspace under the controllers workspace, binds the exported
// API in it, applies the impersonation RBAC this server is documented to need, and grants each
// named user rights there. It returns the logical cluster name, which is what both
// SelfClusterAccessReview and list_workspaces answer with.
func createConsumerWorkspace(t *testing.T, ctx context.Context, adminConfig *rest.Config, data templateData, name string, grantees ...string) string {
	t.Helper()

	controllers := inWorkspace(t, adminConfig, controllersWorkspace)
	createWorkspace(t, ctx, controllers, name)

	workspace := inWorkspace(t, adminConfig, controllersWorkspace+":"+name)
	workspace.apply(t, ctx, "consumer.yaml.tmpl", data)
	waitForAPIBinding(t, ctx, workspace)

	gvr := schemaGVR("rbac.authorization.k8s.io", "v1", "clusterrolebindings")

	for _, grantee := range grantees {
		binding := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRoleBinding",
			"metadata":   map[string]any{"name": grantee + "-admin"},
			"subjects": []any{map[string]any{
				"kind":     "User",
				"name":     grantee,
				"apiGroup": "rbac.authorization.k8s.io",
			}},
			"roleRef": map[string]any{
				"kind":     "ClusterRole",
				"name":     "cluster-admin",
				"apiGroup": "rbac.authorization.k8s.io",
			},
		}}

		if err := createErr(workspace.dynamic.Resource(gvr).Create(ctx, binding, metav1.CreateOptions{})); err != nil {
			t.Fatalf("Failed to grant %s access to %s: %v", grantee, name, err)
		}
	}

	return logicalClusterName(t, ctx, workspace)
}

// waitForAPIBinding waits until the binding's permission claims are accepted. Until then the access
// VW cannot read the workspace's RBAC, and the answer would be empty for reasons that have nothing
// to do with the caller.
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
