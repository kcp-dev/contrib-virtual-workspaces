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

package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	dbaasv1alpha1 "example.com/dbaas-producer/apis/v1alpha1"
)

// The reconcile logic is a function over a client, so this is the whole
// harness: a fake client with the status subresource, and two passes.
func TestReconcileProvisionsThenReady(t *testing.T) {
	sch := runtime.NewScheme()
	if err := dbaasv1alpha1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	db := &dbaasv1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Generation: 1},
		Spec:       dbaasv1alpha1.DatabaseSpec{Engine: "mysql", Size: "small"},
	}
	c := fake.NewClientBuilder().
		WithScheme(sch).
		WithStatusSubresource(&dbaasv1alpha1.Database{}).
		WithObjects(db).
		Build()
	key := types.NamespacedName{Name: "orders"}
	ctx := context.Background()

	res, err := Reconcile(ctx, c, key)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("first pass should requeue to finish provisioning, got %+v", res)
	}
	var got dbaasv1alpha1.Database
	if err := c.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != dbaasv1alpha1.PhasePending || got.Status.ObservedGeneration != 1 {
		t.Fatalf("after first pass: phase=%q observedGeneration=%d", got.Status.Phase, got.Status.ObservedGeneration)
	}

	if _, err := Reconcile(ctx, c, key); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if err := c.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != dbaasv1alpha1.PhaseReady {
		t.Fatalf("after second pass: phase=%q message=%q", got.Status.Phase, got.Status.Message)
	}
	if want := "mysql://orders.dbaas.example.com:3306"; got.Status.Endpoint != want {
		t.Fatalf("endpoint = %q, want %q", got.Status.Endpoint, want)
	}
	if len(got.Status.Conditions) != 1 || got.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("conditions = %+v", got.Status.Conditions)
	}

	// A third pass is a no-op: current generation, final phase.
	res, err = Reconcile(ctx, c, key)
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("settled object should not requeue: res=%+v err=%v", res, err)
	}
}

func TestReconcileIgnoresMissing(t *testing.T) {
	sch := runtime.NewScheme()
	if err := dbaasv1alpha1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(sch).Build()
	if _, err := Reconcile(context.Background(), c, types.NamespacedName{Name: "nope"}); err != nil {
		t.Fatalf("a missing object is not an error: %v", err)
	}
}
