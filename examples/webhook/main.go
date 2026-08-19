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

// Command example-webhook is a reference implementation of the EphemeralReview
// contract: a BucketInfo service that answers with live numbers.
//
// It exists to make the request path runnable end to end, and to show what a
// provider actually has to write. In a real deployment the numbers would come
// from an object store, and the handler would authorize request.userInfo before
// answering.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"sort"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"

	ephemeralv1alpha1 "github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/apis/ephemeral/v1alpha1"
)

func main() {
	var (
		// Not :8443, which is routinely already taken by a kubectl
		// port-forward. When something else answers there, the failure surfaces
		// as a TLS error from this server rather than as a port clash.
		addr     = flag.String("address", ":18443", "[address]:port to serve on")
		certFile = flag.String("tls-cert-file", "", "serving certificate; plain HTTP is served when empty")
		keyFile  = flag.String("tls-private-key-file", "", "serving key")
	)
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/ephemeral/bucketinfos", handleBucketInfo)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	server := &http.Server{Addr: *addr, Handler: mux}

	log.Printf("serving on %s", *addr)
	if *certFile != "" {
		log.Fatal(server.ListenAndServeTLS(*certFile, *keyFile))
	}
	log.Fatal(server.ListenAndServe())
}

// describeUser renders everything kcp said about the caller.
//
// Groups and extras are printed even when empty, because their absence is the
// signal. A forwarded caller arrives with at least `system:authenticated`;
// nothing but a username means nothing but a client certificate.
//
// `extra=none` is normal, though, and does not mean anything was lost. kcp puts
// `authentication.kcp.io/scopes` and `authentication.kcp.io/warrant` there when
// an identity is confined to one workspace or is acting on another's behalf --
// service accounts, front-proxy-scoped tokens. An installation-wide admin token
// is confined to nothing, so it carries neither.
func describeUser(u authenticationv1.UserInfo) string {
	parts := []string{fmt.Sprintf("user=%q", u.Username)}
	if u.UID != "" {
		parts = append(parts, "uid="+u.UID)
	}
	parts = append(parts, fmt.Sprintf("groups=%v", u.Groups))

	keys := make([]string, 0, len(u.Extra))
	for key := range u.Extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	if len(keys) == 0 {
		parts = append(parts, "extra=none")
	}
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("extra[%s]=%v", key, u.Extra[key]))
	}

	return strings.Join(parts, " ")
}

func handleBucketInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST is supported", http.StatusMethodNotAllowed)
		return
	}

	review := &ephemeralv1alpha1.EphemeralReview{}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 3<<20)).Decode(review); err != nil {
		http.Error(w, fmt.Sprintf("cannot decode EphemeralReview: %v", err), http.StatusBadRequest)
		return
	}
	if review.Request == nil {
		http.Error(w, "EphemeralReview has no request", http.StatusBadRequest)
		return
	}

	log.Printf("bucketinfo request uid=%s cluster=%s resource=%s name=%q dryRun=%t",
		review.Request.UID, review.Request.Cluster, review.Request.Resource.Resource,
		review.Request.Name, review.Request.DryRun)

	// The identity is on its own line because it is the interesting part, and
	// the part most likely to be wrong.
	//
	// A provider's whole reason for wanting this call is to decide what to
	// answer for *this* caller in *this* workspace, so both are repeated here:
	// authorizing one without the other answers the wrong question. The cluster
	// is not part of UserInfo -- kcp carries it alongside, because an identity
	// is installation-wide while the workspace it is acting in is not.
	//
	// `user=kcp-shard` with no groups would mean the shard's own client
	// certificate: the caller never made it across the proxy hop, and anything
	// decided here is about the wrong subject.
	log.Printf("  identity: cluster=%s %s",
		review.Request.Cluster, describeUser(review.Request.UserInfo))

	var submitted struct {
		Spec struct {
			BucketName string `json:"bucketName"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(review.Request.Object.Raw, &submitted); err != nil {
		respond(w, deny(review, http.StatusBadRequest, "Invalid",
			fmt.Sprintf("cannot decode the submitted object: %v", err)))
		return
	}

	if submitted.Spec.BucketName == "" {
		respond(w, deny(review, http.StatusBadRequest, "Invalid", "spec.bucketName is required"))
		return
	}

	// This is where a real provider would authorize review.Request.UserInfo
	// against review.Request.Cluster before answering, and where "no such
	// bucket" becomes a 404 rather than an opaque failure.
	if submitted.Spec.BucketName == "does-not-exist" {
		respond(w, deny(review, http.StatusNotFound, "NotFound",
			fmt.Sprintf("bucket %q does not exist", submitted.Spec.BucketName)))
		return
	}

	answer, err := json.Marshal(map[string]interface{}{
		"apiVersion": "s3.example.com/v1alpha1",
		"kind":       "BucketInfo",
		"spec":       map[string]interface{}{"bucketName": submitted.Spec.BucketName},
		"status": map[string]interface{}{
			// Stable fake numbers, so a demo is reproducible.
			"sizeBytes":   int64(hashOf(submitted.Spec.BucketName)%1_000_000) * 1024,
			"objectCount": int64(hashOf(submitted.Spec.BucketName) % 5000),

			// Echoed so that a test can ask the API who the webhook was told
			// about. A real provider authorizes on this and returns nothing
			// derived from it; here it is the only way to distinguish a
			// forwarded caller from the shard's own certificate identity
			// without reading this process's log.
			"observedUser": review.Request.UserInfo.Username,
		},
	})
	if err != nil {
		respond(w, deny(review, http.StatusInternalServerError, "InternalError", err.Error()))
		return
	}

	respond(w, &ephemeralv1alpha1.EphemeralReview{
		TypeMeta: review.TypeMeta,
		Response: &ephemeralv1alpha1.EphemeralResponse{
			UID:     review.Request.UID,
			Allowed: true,
			Object:  rawExtension(answer),
		},
	})
}

func hashOf(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}
