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

package catalog

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/directory"
	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

// OfferingsREST is the read-only catalog: every published APIExport joined
// with the Producer record that owns its workspace, named
// "<producer>.<export>". Reading it is reading the consent form — Claims is
// exactly what enabling accepts, and Revision identifies that surface.
type OfferingsREST struct {
	directory *directory.Directory
	tables    rest.TableConvertor
}

var (
	_ rest.Storage              = &OfferingsREST{}
	_ rest.Getter               = &OfferingsREST{}
	_ rest.Lister               = &OfferingsREST{}
	_ rest.Scoper               = &OfferingsREST{}
	_ rest.SingularNameProvider = &OfferingsREST{}
)

// NewOfferingsREST returns the catalog storage backed by the directory.
func NewOfferingsREST(d *directory.Directory) *OfferingsREST {
	return &OfferingsREST{
		directory: d,
		tables:    rest.NewDefaultTableConvertor(marketplacev1alpha1.Resource("offerings")),
	}
}

func (r *OfferingsREST) New() runtime.Object     { return &marketplacev1alpha1.CatalogOffering{} }
func (r *OfferingsREST) NewList() runtime.Object { return &marketplacev1alpha1.CatalogOfferingList{} }
func (r *OfferingsREST) Destroy()                {}
func (r *OfferingsREST) NamespaceScoped() bool   { return false }
func (r *OfferingsREST) GetSingularName() string { return "offering" }

func (r *OfferingsREST) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return r.tables.ConvertToTable(ctx, object, tableOptions)
}

func (r *OfferingsREST) Get(_ context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	if !r.directory.Ready() {
		return nil, apierrors.NewServiceUnavailable("marketplace directory is not ready; try again shortly")
	}
	entry, ok := r.directory.Lookup(name)
	if !ok {
		return nil, apierrors.NewNotFound(marketplacev1alpha1.Resource("offerings"), name)
	}
	return toCatalogOffering(entry), nil
}

func (r *OfferingsREST) List(_ context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	if !r.directory.Ready() {
		return nil, apierrors.NewServiceUnavailable("marketplace directory is not ready; try again shortly")
	}
	list := &marketplacev1alpha1.CatalogOfferingList{}
	for _, entry := range r.directory.Catalog() {
		list.Items = append(list.Items, *toCatalogOffering(entry))
	}
	return list, nil
}

func toCatalogOffering(entry directory.Entry) *marketplacev1alpha1.CatalogOffering {
	return &marketplacev1alpha1.CatalogOffering{
		ObjectMeta:    metav1.ObjectMeta{Name: entry.Name()},
		Producer:      entry.Producer,
		Offering:      entry.Offering,
		DisplayName:   entry.Detail.DisplayName,
		Description:   entry.Detail.Description,
		Export:        entry.Detail.Export,
		WorkspacePath: entry.WorkspacePath,
		Revision:      entry.Detail.Revision,
		Claims:        entry.Detail.Claims,
		Ready:         entry.Detail.Ready,
		DocsURL:       entry.Detail.DocsURL,
		Instructions:  entry.Instructions,
		Resources:     entry.Resources,
	}
}
