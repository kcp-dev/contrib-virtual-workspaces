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

// Package v1alpha1 is the API this producer publishes: one kind, Database,
// in the group dbaas.example.com.
//
// The Go types are the source of truth. `make generate` renders them into
// the APIResourceSchema in config/ (via controller-gen and kcp's apigen),
// so the schema kcp serves, the deepcopy code the controller needs, and
// the printer columns the console shows all come from the markers here.
//
// +kubebuilder:object:generate=true
// +groupName=dbaas.example.com
package v1alpha1
