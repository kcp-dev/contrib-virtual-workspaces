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

package operator

import (
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// Object names are prefixed with the API group so that a workspace admin
// can tell at a glance which objects the tenancy operator owns, and so the
// operator never fights over names with anything else in the workspace.
const (
	roleNamePrefix    = tenancyv1alpha1.GroupName + ":role:"
	bindingNamePrefix = tenancyv1alpha1.GroupName + ":membership:"

	// managedByLabel marks objects the operator writes, so cleanup and
	// audits can find them without guessing from names.
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "tenancy-operator"
)

// RoleClusterRoleName is the name of the ClusterRole materialized for a
// role in a target workspace.
func RoleClusterRoleName(role tenancyv1alpha1.Role) string {
	return roleNamePrefix + string(role)
}

// MembershipBindingName is the name of the ClusterRoleBinding materialized
// for a Membership. Deterministic in the membership's name so reconciles
// converge and deletion knows what to remove.
func MembershipBindingName(membershipName string) string {
	return bindingNamePrefix + membershipName
}

// DesiredClusterRole returns the ClusterRole for a role. Every role also
// carries kcp's workspace-content `access` verb: without it a subject can
// hold resource permissions in a workspace it cannot enter.
func DesiredClusterRole(role tenancyv1alpha1.Role) (*rbacv1.ClusterRole, error) {
	accessRule := rbacv1.PolicyRule{
		Verbs:           []string{"access"},
		NonResourceURLs: []string{"/"},
	}

	var resourceRules []rbacv1.PolicyRule
	switch role {
	case tenancyv1alpha1.RoleView:
		resourceRules = []rbacv1.PolicyRule{{
			APIGroups: []string{"*"},
			Resources: []string{"*"},
			Verbs:     []string{"get", "list", "watch"},
		}}
	case tenancyv1alpha1.RoleEdit:
		// Everything except RBAC: an editor works in the workspace but does
		// not hand out access to it.
		resourceRules = []rbacv1.PolicyRule{{
			APIGroups: []string{"*"},
			Resources: []string{"*"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		}, {
			APIGroups: []string{"rbac.authorization.k8s.io"},
			Resources: []string{"*"},
			Verbs:     []string{"get", "list", "watch"},
		}}
	case tenancyv1alpha1.RoleAdmin:
		resourceRules = []rbacv1.PolicyRule{{
			APIGroups: []string{"*"},
			Resources: []string{"*"},
			Verbs:     []string{"*"},
		}, {
			Verbs:           []string{"*"},
			NonResourceURLs: []string{"*"},
		}}
	default:
		return nil, fmt.Errorf("unknown role %q", role)
	}

	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   RoleClusterRoleName(role),
			Labels: map[string]string{managedByLabel: managedByValue},
		},
		Rules: append(resourceRules, accessRule),
	}, nil
}

// Kubernetes RBAC has its own subject kinds; the tenancy API deliberately
// uses the same two names, so the mapping is a straight cast — but it is
// still validated here so a bad object cannot silently produce a binding
// for nobody.
func rbacSubject(s tenancyv1alpha1.Subject) (rbacv1.Subject, error) {
	switch s.Kind {
	case tenancyv1alpha1.SubjectKindUser, tenancyv1alpha1.SubjectKindGroup:
	default:
		return rbacv1.Subject{}, fmt.Errorf("unknown subject kind %q", s.Kind)
	}
	if s.Name == "" {
		return rbacv1.Subject{}, fmt.Errorf("subject name must not be empty")
	}
	return rbacv1.Subject{
		APIGroup: rbacv1.GroupName,
		Kind:     string(s.Kind),
		Name:     s.Name,
	}, nil
}

// DesiredBinding returns the ClusterRoleBinding for a Membership.
func DesiredBinding(membership *tenancyv1alpha1.Membership) (*rbacv1.ClusterRoleBinding, error) {
	subject, err := rbacSubject(membership.Spec.Subject)
	if err != nil {
		return nil, err
	}
	if _, err := DesiredClusterRole(membership.Spec.Role); err != nil {
		return nil, err
	}

	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   MembershipBindingName(membership.Name),
			Labels: map[string]string{managedByLabel: managedByValue},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     RoleClusterRoleName(membership.Spec.Role),
		},
		Subjects: []rbacv1.Subject{subject},
	}, nil
}
