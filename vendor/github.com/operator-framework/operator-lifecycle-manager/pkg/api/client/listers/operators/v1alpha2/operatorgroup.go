/*
Copyright Red Hat, Inc.

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

// Code generated by lister-gen. DO NOT EDIT.

package v1alpha2

import (
	operatorsv1alpha2 "github.com/operator-framework/api/pkg/operators/v1alpha2"
	labels "k8s.io/apimachinery/pkg/labels"
	listers "k8s.io/client-go/listers"
	cache "k8s.io/client-go/tools/cache"
)

// OperatorGroupLister helps list OperatorGroups.
// All objects returned here must be treated as read-only.
type OperatorGroupLister interface {
	// List lists all OperatorGroups in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*operatorsv1alpha2.OperatorGroup, err error)
	// OperatorGroups returns an object that can list and get OperatorGroups.
	OperatorGroups(namespace string) OperatorGroupNamespaceLister
	OperatorGroupListerExpansion
}

// operatorGroupLister implements the OperatorGroupLister interface.
type operatorGroupLister struct {
	listers.ResourceIndexer[*operatorsv1alpha2.OperatorGroup]
}

// NewOperatorGroupLister returns a new OperatorGroupLister.
func NewOperatorGroupLister(indexer cache.Indexer) OperatorGroupLister {
	return &operatorGroupLister{listers.New[*operatorsv1alpha2.OperatorGroup](indexer, operatorsv1alpha2.Resource("operatorgroup"))}
}

// OperatorGroups returns an object that can list and get OperatorGroups.
func (s *operatorGroupLister) OperatorGroups(namespace string) OperatorGroupNamespaceLister {
	return operatorGroupNamespaceLister{listers.NewNamespaced[*operatorsv1alpha2.OperatorGroup](s.ResourceIndexer, namespace)}
}

// OperatorGroupNamespaceLister helps list and get OperatorGroups.
// All objects returned here must be treated as read-only.
type OperatorGroupNamespaceLister interface {
	// List lists all OperatorGroups in the indexer for a given namespace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*operatorsv1alpha2.OperatorGroup, err error)
	// Get retrieves the OperatorGroup from the indexer for a given namespace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*operatorsv1alpha2.OperatorGroup, error)
	OperatorGroupNamespaceListerExpansion
}

// operatorGroupNamespaceLister implements the OperatorGroupNamespaceLister
// interface.
type operatorGroupNamespaceLister struct {
	listers.ResourceIndexer[*operatorsv1alpha2.OperatorGroup]
}
