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

package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"

	ephemeralv1alpha1 "github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/apis/ephemeral/v1alpha1"
	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/config"
	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/webhook"
)

var (
	testGVR = schema.GroupVersionResource{Group: "s3.example.com", Version: "v1alpha1", Resource: "bucketinfos"}
	testGVK = schema.GroupVersionKind{Group: "s3.example.com", Version: "v1alpha1", Kind: "BucketInfo"}
)

// testValidator builds a schema validator requiring status.sizeBytes to be an
// integer, so that a webhook returning nonsense is rejected.
func testValidator(t *testing.T) apiservervalidation.SchemaValidator {
	t.Helper()

	v1Schema := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"spec": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"bucketName": {Type: "string"},
				},
			},
			"status": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"sizeBytes": {Type: "integer"},
				},
			},
		},
	}

	internal := &apiextensionsinternal.JSONSchemaProps{}
	require.NoError(t, apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v1Schema, internal, nil))

	validator, _, err := apiservervalidation.NewSchemaValidator(internal)
	require.NoError(t, err)

	return validator
}

// newTestREST spins up a webhook at handler and returns storage wired to it.
func newTestREST(t *testing.T, handler http.HandlerFunc) (*REST, *httptest.Server) {
	t.Helper()

	// A plain HTTP test server: config.Validate is what enforces https on
	// operator input, the client itself speaks whatever the URL says. And
	// httptest listens on 127.0.0.1, which the SSRF guard refuses by design, so
	// tests opt out of that explicitly.
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	factory := webhook.NewFactory(webhook.Options{AllowPrivateAddresses: true})
	client, err := factory.New(config.WebhookConfiguration{URL: server.URL, TimeoutSeconds: 5})
	require.NoError(t, err)

	return New(testGVR, testGVK, "bucketinfo", false, nil, testValidator(t), client), server
}

func testContext() context.Context {
	ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: "consumer-ws"})
	return genericapirequest.WithUser(ctx, &user.DefaultInfo{
		Name:   "alice",
		UID:    "alice-uid",
		Groups: []string{"team-a"},
		Extra:  map[string][]string{"scopes": {"read"}},
	})
}

func submittedObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "s3.example.com/v1alpha1",
		"kind":       "BucketInfo",
		"spec":       map[string]interface{}{"bucketName": "my-bucket"},
	}}
}

func respondWith(t *testing.T, w http.ResponseWriter, review *ephemeralv1alpha1.EphemeralReview) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(review))
}

func decodeRequest(t *testing.T, r *http.Request) *ephemeralv1alpha1.EphemeralReview {
	t.Helper()
	review := &ephemeralv1alpha1.EphemeralReview{}
	require.NoError(t, json.NewDecoder(r.Body).Decode(review))
	return review
}

func TestCreateReturnsWebhookAnswer(t *testing.T) {
	var got *ephemeralv1alpha1.EphemeralReview

	rest, _ := newTestREST(t, func(w http.ResponseWriter, r *http.Request) {
		got = decodeRequest(t, r)

		answer, err := json.Marshal(map[string]interface{}{
			"apiVersion": "s3.example.com/v1alpha1",
			"kind":       "BucketInfo",
			"spec":       map[string]interface{}{"bucketName": "my-bucket"},
			"status":     map[string]interface{}{"sizeBytes": 4096},
		})
		require.NoError(t, err)

		respondWith(t, w, &ephemeralv1alpha1.EphemeralReview{
			Response: &ephemeralv1alpha1.EphemeralResponse{
				UID:     got.Request.UID,
				Allowed: true,
				Object:  &runtime.RawExtension{Raw: answer},
			},
		})
	})

	out, err := rest.Create(testContext(), submittedObject(), nil, &metav1.CreateOptions{})
	require.NoError(t, err)

	result, ok := out.(*unstructured.Unstructured)
	require.True(t, ok)

	size, found, err := unstructured.NestedInt64(result.Object, "status", "sizeBytes")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(4096), size)

	// The request carried everything the webhook needs to authorize.
	require.NotNil(t, got.Request)
	require.Equal(t, "consumer-ws", got.Request.Cluster)
	require.Equal(t, "alice", got.Request.UserInfo.Username)
	require.Equal(t, []string{"team-a"}, got.Request.UserInfo.Groups)
	require.Equal(t, "bucketinfos", got.Request.Resource.Resource)
	require.False(t, got.Request.DryRun)
}

func TestCreatePropagatesDryRun(t *testing.T) {
	var got *ephemeralv1alpha1.EphemeralReview

	rest, _ := newTestREST(t, func(w http.ResponseWriter, r *http.Request) {
		got = decodeRequest(t, r)
		answer, err := json.Marshal(map[string]interface{}{
			"apiVersion": "s3.example.com/v1alpha1",
			"kind":       "BucketInfo",
		})
		require.NoError(t, err)
		respondWith(t, w, &ephemeralv1alpha1.EphemeralReview{
			Response: &ephemeralv1alpha1.EphemeralResponse{
				UID:     got.Request.UID,
				Allowed: true,
				Object:  &runtime.RawExtension{Raw: answer},
			},
		})
	})

	_, err := rest.Create(testContext(), submittedObject(), nil, &metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	require.NoError(t, err)
	require.True(t, got.Request.DryRun)
}

func TestCreateSurfacesWebhookDenialAsAPIError(t *testing.T) {
	rest, _ := newTestREST(t, func(w http.ResponseWriter, r *http.Request) {
		review := decodeRequest(t, r)
		respondWith(t, w, &ephemeralv1alpha1.EphemeralReview{
			Response: &ephemeralv1alpha1.EphemeralResponse{
				UID:     review.Request.UID,
				Allowed: false,
				Status: &metav1.Status{
					Code:    http.StatusNotFound,
					Reason:  metav1.StatusReasonNotFound,
					Message: "bucket does not exist",
				},
			},
		})
	})

	_, err := rest.Create(testContext(), submittedObject(), nil, &metav1.CreateOptions{})
	require.Error(t, err)
	require.True(t, apierrors.IsNotFound(err), "expected a NotFound error, got %v", err)
	require.Contains(t, err.Error(), "bucket does not exist")
}

func TestCreateRejectsNonConformingAnswer(t *testing.T) {
	rest, _ := newTestREST(t, func(w http.ResponseWriter, r *http.Request) {
		review := decodeRequest(t, r)
		// status.sizeBytes must be an integer per the schema.
		answer := []byte(`{"apiVersion":"s3.example.com/v1alpha1","kind":"BucketInfo","status":{"sizeBytes":"not-a-number"}}`)
		respondWith(t, w, &ephemeralv1alpha1.EphemeralReview{
			Response: &ephemeralv1alpha1.EphemeralResponse{
				UID:     review.Request.UID,
				Allowed: true,
				Object:  &runtime.RawExtension{Raw: answer},
			},
		})
	})

	_, err := rest.Create(testContext(), submittedObject(), nil, &metav1.CreateOptions{})
	require.Error(t, err)
	require.True(t, apierrors.IsInternalError(err), "expected an InternalError, got %v", err)
	require.Contains(t, err.Error(), "does not conform to the schema")
}

func TestCreateRejectsMismatchedUID(t *testing.T) {
	rest, _ := newTestREST(t, func(w http.ResponseWriter, r *http.Request) {
		_ = decodeRequest(t, r)
		respondWith(t, w, &ephemeralv1alpha1.EphemeralReview{
			Response: &ephemeralv1alpha1.EphemeralResponse{
				UID:     "some-other-uid",
				Allowed: true,
				Object:  &runtime.RawExtension{Raw: []byte(`{}`)},
			},
		})
	})

	_, err := rest.Create(testContext(), submittedObject(), nil, &metav1.CreateOptions{})
	require.Error(t, err)
	require.True(t, apierrors.IsServiceUnavailable(err), "expected ServiceUnavailable, got %v", err)
}

func TestCreateReturns503WhenWebhookIsUnreachable(t *testing.T) {
	rest, server := newTestREST(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	server.Close()

	_, err := rest.Create(testContext(), submittedObject(), nil, &metav1.CreateOptions{})
	require.Error(t, err)
	require.True(t, apierrors.IsServiceUnavailable(err), "expected ServiceUnavailable, got %v", err)
}

func TestCreateRejectsWildcardCluster(t *testing.T) {
	rest, _ := newTestREST(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the webhook must not be called for a wildcard request")
	})

	ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Wildcard: true})
	ctx = genericapirequest.WithUser(ctx, &user.DefaultInfo{Name: "alice"})

	_, err := rest.Create(ctx, submittedObject(), nil, &metav1.CreateOptions{})
	require.Error(t, err)
	require.True(t, apierrors.IsBadRequest(err), "expected BadRequest, got %v", err)
}

// The set of interfaces the storage implements is what the virtual workspace
// framework turns into discovery verbs, and discovery is in turn what the shard
// copies into the consumer-visible API. Implementing one more interface here
// would silently add a verb that ephemeral resources cannot serve, so the
// negative case is asserted rather than left to review.
func TestStorageOnlyServesCreate(t *testing.T) {
	var storage rest.Storage = &REST{}

	_, isCreater := storage.(rest.Creater)
	require.True(t, isCreater, "storage must implement rest.Creater")

	_, isGetter := storage.(rest.Getter)
	require.False(t, isGetter, "storage must not implement rest.Getter")

	_, isLister := storage.(rest.Lister)
	require.False(t, isLister, "storage must not implement rest.Lister")

	_, isWatcher := storage.(rest.Watcher)
	require.False(t, isWatcher, "storage must not implement rest.Watcher")

	_, isUpdater := storage.(rest.Updater)
	require.False(t, isUpdater, "storage must not implement rest.Updater")

	_, isPatcher := storage.(rest.Patcher)
	require.False(t, isPatcher, "storage must not implement rest.Patcher")

	_, isDeleter := storage.(rest.GracefulDeleter)
	require.False(t, isDeleter, "storage must not implement rest.GracefulDeleter")

	_, isCollectionDeleter := storage.(rest.CollectionDeleter)
	require.False(t, isCollectionDeleter, "storage must not implement rest.CollectionDeleter")
}
