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

// Package authorizer gates access to the ephemeral virtual workspace.
package authorizer

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericoptions "k8s.io/apiserver/pkg/server/options"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	apisv1alpha2listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha2"

	"github.com/kcp-dev/contrib-virtual-workspaces/ephemeral/pkg/virtualworkspace/apidomainkey"
)

// ExportChecker reports whether this virtual workspace serves anything for an
// APIExport.
type ExportChecker interface {
	HasEntriesFor(export *apisv1alpha2.APIExport) bool
}

// Options tune the content authorizer.
type Options struct {
	// RequireExportContent makes the authorizer additionally verify that the
	// requesting identity holds the `create` verb on the APIExport's
	// `content` subresource in the export's own workspace, as the replication
	// virtual workspace does.
	//
	// Whether this is correct depends on how the shard authenticates when it
	// proxies a consumer's request: with a shard client certificate the
	// identity the virtual workspace sees is the shard, not the end user.
	// Verify against a real deployment before relying on it.
	RequireExportContent bool
}

type contentAuthorizer struct {
	opts Options

	checker ExportChecker

	kubeClusterClient kcpkubernetesclientset.ClusterInterface

	getAPIExport func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error)
	listBindings func(cluster logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error)
}

// New builds the authorizer guarding the ephemeral virtual workspace.
func New(
	opts Options,
	checker ExportChecker,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	localExports, globalExports apisv1alpha2listers.APIExportClusterLister,
	bindings apisv1alpha2listers.APIBindingClusterLister,
) authorizer.Authorizer {
	return &contentAuthorizer{
		opts:              opts,
		checker:           checker,
		kubeClusterClient: kubeClusterClient,
		getAPIExport: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error) {
			export, err := localExports.Cluster(cluster).Get(name)
			if err == nil {
				return export, nil
			}
			if !apierrors.IsNotFound(err) {
				return nil, err
			}
			return globalExports.Cluster(cluster).Get(name)
		},
		listBindings: func(cluster logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
			return bindings.Cluster(cluster).List(labels.Everything())
		},
	}
}

func (a *contentAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	// Discovery is served to any authenticated caller.
	//
	// The shard performs discovery against this endpoint with its own identity
	// in order to learn which verbs the resource supports, and it does so
	// before any consumer request. Gating that on the consumer-facing checks
	// below would make the resource undiscoverable. Discovery only exposes the
	// shape of the API, which is already public in the APIExport.
	if !attr.IsResourceRequest() {
		return authorizer.DecisionAllow, "discovery is allowed for authenticated users", nil
	}

	// Only create is ever served. Refusing the rest here, before any lookup,
	// keeps the storage's interface set from being the only thing standing
	// between a client and a verb the resource does not have.
	if attr.GetVerb() != "create" {
		return authorizer.DecisionDeny,
			fmt.Sprintf("verb %q is not served by ephemeral resources, only create is", attr.GetVerb()), nil
	}

	key, err := apidomainkey.FromContext(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "invalid API domain key", err
	}

	export, err := a.getAPIExport(key.Cluster, key.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return authorizer.DecisionDeny, "APIExport not found", nil
		}
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error getting APIExport %s|%s: %w", key.Cluster, key.Name, err)
	}

	if !a.checker.HasEntriesFor(export) {
		return authorizer.DecisionDeny,
			fmt.Sprintf("APIExport %s|%s has no ephemeral resources configured", key.Cluster, export.Name), nil
	}

	targetCluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error getting valid cluster from context: %w", err)
	}
	if targetCluster.Wildcard {
		// There is nothing to collect across clusters: ephemeral resources have
		// no collection at all.
		return authorizer.DecisionDeny, "wildcard requests are not served by ephemeral resources", nil
	}

	// The consumer must actually have bound the export. Without this, anyone
	// who can reach the endpoint could call a provider's webhook on behalf of
	// any workspace name they care to put in the URL.
	bound, err := a.hasBinding(targetCluster.Name, export, attr)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}
	if !bound {
		return authorizer.DecisionDeny,
			fmt.Sprintf("logical cluster %s has no APIBinding to APIExport %s|%s for %s",
				targetCluster.Name, key.Cluster, export.Name, attr.GetResource()), nil
	}

	if !a.opts.RequireExportContent {
		return authorizer.DecisionAllow, "bound APIExport found", nil
	}

	delegated, err := newDelegatedAuthorizer(logicalcluster.From(export), a.kubeClusterClient)
	if err != nil {
		return authorizer.DecisionNoOpinion, "",
			fmt.Errorf("error creating delegated authorizer for APIExport %s|%s: %w", key.Cluster, export.Name, err)
	}

	decision, reason, err := delegated.Authorize(ctx, authorizer.AttributesRecord{
		APIGroup:        apisv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      apisv1alpha1.SchemeGroupVersion.Version,
		User:            attr.GetUser(),
		Verb:            attr.GetVerb(),
		Resource:        "apiexports",
		ResourceRequest: true,
		Subresource:     "content",
		Name:            export.Name,
	})
	if err != nil {
		return authorizer.DecisionNoOpinion, "",
			fmt.Errorf("error authorizing RBAC in APIExport %s|%s: %w", key.Cluster, export.Name, err)
	}
	if decision != authorizer.DecisionAllow {
		return authorizer.DecisionDeny, reason, nil
	}

	return authorizer.DecisionAllow, "bound APIExport found and content access granted", nil
}

// hasBinding reports whether the target logical cluster binds the given export
// and has the requested resource among its bound resources.
func (a *contentAuthorizer) hasBinding(target logicalcluster.Name, export *apisv1alpha2.APIExport, attr authorizer.Attributes) (bool, error) {
	bindings, err := a.listBindings(target)
	if err != nil {
		return false, fmt.Errorf("error listing APIBindings in %s: %w", target, err)
	}

	exportCluster := logicalcluster.From(export).String()
	exportPath := export.Annotations[core.LogicalClusterPathAnnotationKey]

	for _, binding := range bindings {
		ref := binding.Spec.Reference.Export
		if ref == nil || ref.Name != export.Name {
			continue
		}

		// An empty path means "the binding's own workspace".
		path := ref.Path
		if path == "" {
			path = logicalcluster.From(binding).String()
		}
		if path != exportCluster && (exportPath == "" || path != exportPath) {
			continue
		}

		for _, br := range binding.Status.BoundResources {
			if br.Group == attr.GetAPIGroup() && br.Resource == attr.GetResource() {
				return true, nil
			}
		}
	}

	return false, nil
}

// newDelegatedAuthorizer returns an authorizer that asks kcp, in the export's
// own workspace, via SubjectAccessReview.
func newDelegatedAuthorizer(cluster logicalcluster.Name, client kcpkubernetesclientset.ClusterInterface) (authorizer.Authorizer, error) {
	cfg := &authorizerfactory.DelegatingAuthorizerConfig{
		SubjectAccessReviewClient: client.Cluster(cluster.Path()).AuthorizationV1(),
		AllowCacheTTL:             5 * time.Minute,
		DenyCacheTTL:              30 * time.Second,
		WebhookRetryBackoff:       genericoptions.DefaultAuthWebhookRetryBackoff(),
	}

	return cfg.New()
}
