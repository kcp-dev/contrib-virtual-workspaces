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

package main

import (
	"encoding/json"
	"log"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	ephemeralv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/ephemeral/pkg/apis/ephemeral/v1alpha1"
)

// respond writes the review back. The virtual workspace requires HTTP 200 with
// an EphemeralReview body even when the answer is a denial: the denial travels
// in response.status, not in the HTTP status code.
func respond(w http.ResponseWriter, review *ephemeralv1alpha1.EphemeralReview) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(review); err != nil {
		log.Printf("failed to write response: %v", err)
	}
}

// deny builds a response that turns into a proper API error for the client.
func deny(review *ephemeralv1alpha1.EphemeralReview, code int32, reason, message string) *ephemeralv1alpha1.EphemeralReview {
	return &ephemeralv1alpha1.EphemeralReview{
		TypeMeta: review.TypeMeta,
		Response: &ephemeralv1alpha1.EphemeralResponse{
			UID:     review.Request.UID,
			Allowed: false,
			Status: &metav1.Status{
				Code:    code,
				Reason:  metav1.StatusReason(reason),
				Message: message,
			},
		},
	}
}

func rawExtension(raw []byte) *runtime.RawExtension {
	return &runtime.RawExtension{Raw: raw}
}
