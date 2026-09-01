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

// Package e2e drives the whole stack the way a consumer does.
//
// It asserts nothing about this repository's internals: everything goes through
// kcp, which is the point. A unit test can prove the storage refuses `get`; only
// this can prove the shard resolved a provider-published URL, forwarded the
// request to a server kcp does not run, and returned an answer that was never
// stored.
//
// hack/ci/run-e2e-tests.sh stands the stack up and points $KUBECONFIG at it.
package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	// Fixed rather than generated, because two things outside this test already
	// name it: the virtual workspace's --ephemeral-config, and the workspace the
	// endpoint slice controller is scoped to. A generated path would mean
	// generating those too, and then the test would no longer be exercising the
	// configuration the README documents.
	providersWorkspace = "providers"
	providerWorkspace  = "s3"
	providerPath       = "root:" + providersWorkspace + ":" + providerWorkspace

	exportName = "s3.example.com"
	apiGroup   = "s3.example.com"
	apiVersion = "v1alpha1"
)

var (
	apiExportsGVR     = gvr("apis.kcp.io", "v1alpha2", "apiexports")
	apiBindingsGVR    = gvr("apis.kcp.io", "v1alpha2", "apibindings")
	endpointSlicesGVR = gvr("ephemeral.contrib.kcp.io", "v1alpha1", "ephemeralresourceendpointslices")
	bucketInfosGVR    = gvr(apiGroup, apiVersion, "bucketinfos")
	bucketsGVR        = gvr(apiGroup, apiVersion, "buckets")
)

// TestEphemeralResources runs the documented walkthrough and then asks the API
// questions only a correctly wired deployment can answer.
//
// The stages build on each other and standing kcp up takes minutes, so they are
// subtests of one deployment rather than separate tests: once the export fails
// to resolve, everything after it is measuring the same failure.
func TestEphemeralResources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	rootWS := root(t)

	t.Log("Creating the provider workspace...")
	providers := rootWS.createWorkspace(t, ctx, providersWorkspace)
	provider := providers.createWorkspace(t, ctx, providerWorkspace)

	t.Log("Applying the provider manifests...")
	provider.apply(t, ctx, "00-endpointslice-crd.yaml")
	provider.apply(t, ctx, "01-apiresourceschema.yaml")
	applyExport(t, ctx, provider)
	provider.apply(t, ctx, "03-endpointslice.yaml")

	// The URL is not in the manifest and no kcp controller writes it. The
	// endpoint slice controller does, from outside kcp, which is the whole
	// argument for a provider-owned slice kind: only the provider knows where
	// their virtual workspace runs.
	t.Log("Waiting for the endpoint slice controller to publish the address...")
	published := waitForPublishedEndpoint(t, ctx, provider)
	t.Logf("Endpoint published: %s", published)

	t.Log("Creating the consumer workspace and binding the export...")
	consumer := rootWS.createWorkspace(t, ctx, "consumer")
	t.Cleanup(func() { rootWS.deleteWorkspace(t, "consumer") })

	consumer.apply(t, ctx, "04-apibinding.yaml")
	waitForBound(t, ctx, consumer)

	// Bound is not servable. This waits for the shard to be able to resolve the
	// slice through the cache server, which lags the binding.
	t.Log("Waiting for bucketinfos to become servable...")
	verbs := consumer.resourceIsServed(t, ctx, apiGroup, apiVersion, "bucketinfos", 5*time.Minute)

	t.Run("Discovery", func(t *testing.T) {
		assertDiscovery(t, ctx, consumer, verbs)
	})

	t.Run("Create", func(t *testing.T) {
		assertCreateIsAnswered(t, ctx, consumer)
	})

	t.Run("NotStored", func(t *testing.T) {
		assertNothingWasStored(t, ctx, consumer)
	})

	t.Run("Identity", func(t *testing.T) {
		assertCallerReachedTheWebhook(t, ctx, consumer)
	})

	t.Run("DryRun", func(t *testing.T) {
		assertDryRunIsAnswered(t, ctx, consumer)
	})

	t.Run("WebhookDenial", func(t *testing.T) {
		assertWebhookDenialBecomesAnAPIError(t, ctx, consumer)
	})

	t.Run("CRDStorageIsUnaffected", func(t *testing.T) {
		assertBucketsAreStoredNormally(t, ctx, consumer)
	})
}

// applyExport applies the APIExport and fills in the identity its virtual
// storage entry needs.
//
// The manifest carries a placeholder because the value it wants is the export's
// own status.identityHash, which does not exist until the export is admitted.
// On a kcp where the field has been removed the placeholder is pruned on
// admission, and there is nothing to patch -- so the patch is driven by what
// came back rather than by what was sent.
func applyExport(t *testing.T, ctx context.Context, provider *workspace) {
	t.Helper()

	manifest := readExample(t, "02-apiexport.yaml")
	manifest = []byte(strings.ReplaceAll(string(manifest),
		"REPLACE_WITH_APIEXPORT_STATUS_IDENTITYHASH", "pending"))

	provider.applyBytes(t, ctx, "02-apiexport.yaml", manifest)

	var identity string

	err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		export, err := provider.get(ctx, apiExportsGVR, exportName)
		if err != nil {
			return false, nil
		}

		identity, _, _ = unstructured.NestedString(export.Object, "status", "identityHash")

		return identity != "", nil
	})
	if err != nil {
		t.Fatalf("APIExport %s never got an identity hash: %v", exportName, err)
	}

	export, err := provider.get(ctx, apiExportsGVR, exportName)
	if err != nil {
		t.Fatalf("Failed to re-read APIExport %s: %v", exportName, err)
	}

	resources, _, err := unstructured.NestedSlice(export.Object, "spec", "resources")
	if err != nil {
		t.Fatalf("APIExport %s has an unreadable spec.resources: %v", exportName, err)
	}

	// Found by name rather than by position: this export mixes storage kinds,
	// and an index assumed here fails as "the request is invalid" -- because the
	// path does not exist, not because the value is wrong.
	patched := false

	for _, raw := range resources {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		virtual, found, _ := unstructured.NestedMap(entry, "storage", "virtual")
		if !found {
			continue
		}

		if _, hasField := virtual["identityHash"]; !hasField {
			// The field was pruned, so this kcp no longer has it and the
			// identity is derived from the resource's own name and group.
			t.Log("The APIExport has no storage.virtual.identityHash; this kcp derives it.")

			return
		}

		virtual["identityHash"] = identity
		_ = unstructured.SetNestedMap(entry, virtual, "storage", "virtual")
		patched = true
	}

	if !patched {
		t.Fatalf("APIExport %s has no virtually stored resource to patch.", exportName)
	}

	if err := unstructured.SetNestedSlice(export.Object, resources, "spec", "resources"); err != nil {
		t.Fatalf("Failed to set spec.resources: %v", err)
	}

	if _, err := provider.dynamic.Resource(apiExportsGVR).Update(ctx, export, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Failed to set the virtual storage identity on %s: %v", exportName, err)
	}

	t.Logf("Set storage.virtual.identityHash to %s.", identity)
}

// waitForPublishedEndpoint waits for the endpoint slice controller to write the
// virtual workspace's address, and returns it.
func waitForPublishedEndpoint(t *testing.T, ctx context.Context, provider *workspace) string {
	t.Helper()

	var url string

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		slice, err := provider.get(ctx, endpointSlicesGVR, exportName)
		if err != nil {
			return false, nil
		}

		endpoints, found, _ := unstructured.NestedSlice(slice.Object, "status", "endpoints")
		if !found || len(endpoints) == 0 {
			return false, nil
		}

		first, ok := endpoints[0].(map[string]any)
		if !ok {
			return false, nil
		}

		url, _, _ = unstructured.NestedString(first, "url")

		// matchAll is what makes one virtual workspace serve every shard. Without
		// it the URL is matched by prefix against the shard's own address, which
		// is exactly what a provider-run virtual workspace cannot satisfy.
		matchAll, _, _ := unstructured.NestedBool(first, "shards", "matchAll")
		if !matchAll {
			t.Errorf("The published endpoint does not set shards.matchAll: %v", first)
		}

		return url != "", nil
	})
	if err != nil {
		t.Fatalf("The endpoint slice never got a URL: %v\n"+
			"That is the endpoint slice controller's job; check that it is running against %s.", err, providerPath)
	}

	return url
}

func waitForBound(t *testing.T, ctx context.Context, consumer *workspace) {
	t.Helper()

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		binding, err := consumer.get(ctx, apiBindingsGVR, exportName)
		if err != nil {
			return false, nil
		}

		phase, _, _ := unstructured.NestedString(binding.Object, "status", "phase")

		return phase == "Bound", nil
	})
	if err != nil {
		t.Fatalf("APIBinding %s never bound: %v", exportName, err)
	}
}

// assertDiscovery checks that the consumer is offered create and nothing else.
//
// This is not a restriction applied on top of a full resource: the shard
// forwards discovery to the virtual workspace, whose storage implements only
// rest.Creater. A regression here means the storage grew an interface.
func assertDiscovery(t *testing.T, ctx context.Context, consumer *workspace, verbs []string) {
	t.Helper()

	if len(verbs) != 1 || verbs[0] != "create" {
		t.Errorf("bucketinfos advertises %v, want exactly [create].", verbs)
	}

	// The other half of the same export is stored normally, which is what makes
	// this a mixed export rather than a special one.
	bucketVerbs := consumer.resourceIsServed(t, ctx, apiGroup, apiVersion, "buckets", time.Minute)
	for _, want := range []string{"get", "list", "create", "delete"} {
		if !contains(bucketVerbs, want) {
			t.Errorf("buckets does not advertise %q: %v", want, bucketVerbs)
		}
	}
}

// assertCreateIsAnswered submits a BucketInfo and checks that what comes back is
// the webhook's answer rather than the object that was sent.
func assertCreateIsAnswered(t *testing.T, ctx context.Context, consumer *workspace) {
	t.Helper()

	answer := createBucketInfo(t, ctx, consumer, "my-bucket", metav1.CreateOptions{})

	size, found, _ := unstructured.NestedInt64(answer.Object, "status", "sizeBytes")
	if !found {
		t.Fatalf("The answer carries no status.sizeBytes: %v", answer.Object)
	}
	if size <= 0 {
		t.Errorf("status.sizeBytes is %d, want a positive number from the webhook.", size)
	}

	count, found, _ := unstructured.NestedInt64(answer.Object, "status", "objectCount")
	if !found {
		t.Fatalf("The answer carries no status.objectCount: %v", answer.Object)
	}
	if count < 0 {
		t.Errorf("status.objectCount is %d, want a number from the webhook.", count)
	}

	// The submitted object had no status at all, so a status in the response can
	// only have come from the far end of the webhook call.
	t.Logf("The webhook answered: sizeBytes=%d objectCount=%d", size, count)
}

// assertNothingWasStored checks the defining property of an ephemeral resource.
//
// There is no verb to list them with, so this asks the only way a client can:
// a second create of the same bucket has to be accepted. A stored object would
// have made the name taken.
func assertNothingWasStored(t *testing.T, ctx context.Context, consumer *workspace) {
	t.Helper()

	first := createBucketInfo(t, ctx, consumer, "repeatable", metav1.CreateOptions{})
	second := createBucketInfo(t, ctx, consumer, "repeatable", metav1.CreateOptions{})

	if first.GetResourceVersion() != "" {
		t.Errorf("The answer has resourceVersion %q; an unstored object should have none.",
			first.GetResourceVersion())
	}

	// Same question, same answer: the numbers are derived from the bucket name,
	// so a differing answer would mean something stateful crept in.
	firstSize, _, _ := unstructured.NestedInt64(first.Object, "status", "sizeBytes")
	secondSize, _, _ := unstructured.NestedInt64(second.Object, "status", "sizeBytes")

	if firstSize != secondSize {
		t.Errorf("Two identical questions got different answers: %d and %d.", firstSize, secondSize)
	}
}

// assertCallerReachedTheWebhook checks that the consumer's identity survived the
// proxy hop, rather than the shard's.
//
// The shard reaches the virtual workspace over its own client certificate, so
// without identity forwarding every request arrives as `kcp-shard` -- and a
// provider's webhook, whose entire reason for being called is to answer for a
// particular caller, is told the wrong subject.
func assertCallerReachedTheWebhook(t *testing.T, ctx context.Context, consumer *workspace) {
	t.Helper()

	answer := createBucketInfo(t, ctx, consumer, "who-am-i", metav1.CreateOptions{})

	observed, found, _ := unstructured.NestedString(answer.Object, "status", "observedUser")
	if !found {
		t.Skip("The webhook did not report the caller; examples/webhook is older than this test.")
	}

	if observed == "" {
		t.Fatal("The webhook reported an empty caller.")
	}

	if strings.HasPrefix(observed, "kcp-shard") {
		t.Errorf("The webhook was told the caller is %q, which is the shard's own certificate identity.\n"+
			"The caller did not survive the proxy hop: check that kcp forwards X-Remote-* and that this "+
			"server was started with --requestheader-client-ca-file and friends.", observed)
	}

	t.Logf("The webhook was told the caller is %q.", observed)
}

// assertDryRunIsAnswered checks that a dry run still produces an answer.
//
// Nothing is stored either way, so dry-run is not about persistence here: it is
// the flag that tells a provider not to perform side effects, and it has to
// reach them.
func assertDryRunIsAnswered(t *testing.T, ctx context.Context, consumer *workspace) {
	t.Helper()

	answer := createBucketInfo(t, ctx, consumer, "my-bucket",
		metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})

	if _, found, _ := unstructured.NestedInt64(answer.Object, "status", "sizeBytes"); !found {
		t.Errorf("A dry-run create returned no answer: %v", answer.Object)
	}
}

// assertWebhookDenialBecomesAnAPIError checks that a provider can refuse, and
// that the refusal reaches the client as the status it chose rather than as a
// generic failure.
func assertWebhookDenialBecomesAnAPIError(t *testing.T, ctx context.Context, consumer *workspace) {
	t.Helper()

	_, err := submitBucketInfo(ctx, consumer, "does-not-exist", metav1.CreateOptions{})
	if err == nil {
		t.Fatal("Creating a BucketInfo for a bucket the webhook refuses succeeded.")
	}

	if !apierrors.IsNotFound(err) {
		t.Errorf("The webhook's 404 arrived as %T (%v), want a NotFound.", err, truncate(err.Error(), 200))
	}
}

// assertBucketsAreStoredNormally checks the CRD-backed half of the same export.
//
// One export mixing both storage kinds is the interesting case: it proves the
// shard forwards only what is declared virtual, and serves the rest itself.
func assertBucketsAreStoredNormally(t *testing.T, ctx context.Context, consumer *workspace) {
	t.Helper()

	consumer.apply(t, ctx, "bucket.yaml")

	stored, err := consumer.get(ctx, bucketsGVR, "my-bucket")
	if err != nil {
		t.Fatalf("The Bucket was not stored: %v", err)
	}

	if stored.GetResourceVersion() == "" {
		t.Error("The stored Bucket has no resourceVersion.")
	}

	list, err := consumer.dynamic.Resource(bucketsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list Buckets: %v", err)
	}
	if len(list.Items) == 0 {
		t.Error("Listing Buckets returned nothing, though one was just created.")
	}
}

// createBucketInfo submits one and fails the test if it is refused.
func createBucketInfo(t *testing.T, ctx context.Context, consumer *workspace, bucket string, opts metav1.CreateOptions) *unstructured.Unstructured {
	t.Helper()

	answer, err := submitBucketInfo(ctx, consumer, bucket, opts)
	if err != nil {
		t.Fatalf("Failed to create a BucketInfo for %q: %v", bucket, err)
	}

	return answer
}

func submitBucketInfo(ctx context.Context, consumer *workspace, bucket string, opts metav1.CreateOptions) (*unstructured.Unstructured, error) {
	// Built here rather than read from docs/example, because the bucket name is
	// what each assertion varies.
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiGroup + "/" + apiVersion,
		"kind":       "BucketInfo",
		"metadata":   map[string]any{"name": bucket},
		"spec":       map[string]any{"bucketName": bucket},
	}}

	return consumer.dynamic.Resource(bucketInfosGVR).Create(ctx, obj, opts)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}

	return false
}
