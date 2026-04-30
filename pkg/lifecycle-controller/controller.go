/*
Copyright 2025.

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

package controllers

import (
	"context"
	"strings"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	catalogLabelKey = "olm.catalogSource"
)

// LifecycleReconciler watches CatalogSources, pulls their images, extracts
// lifecycle metadata, and caches it for serving via the HTTP API.
type LifecycleReconciler struct {
	client.Client
	Log                        logr.Logger
	Cache                      *LifecycleCache
	Puller                     *ImagePuller
	CatalogSourceLabelSelector labels.Selector
	CatalogSourceFieldSelector fields.Selector
}

// matchesCatalogSource checks if a CatalogSource matches both label and field selectors.
func (r *LifecycleReconciler) matchesCatalogSource(cs *operatorsv1alpha1.CatalogSource) bool {
	if !r.CatalogSourceLabelSelector.Matches(labels.Set(cs.Labels)) {
		return false
	}
	fieldSet := fields.Set{
		"metadata.name":      cs.Name,
		"metadata.namespace": cs.Namespace,
	}
	return r.CatalogSourceFieldSelector.Matches(fieldSet)
}

func (r *LifecycleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("catalogSource", req.NamespacedName)

	log.Info("handling reconciliation request")
	defer log.Info("finished reconciliation")

	var cs operatorsv1alpha1.CatalogSource
	if err := r.Get(ctx, req.NamespacedName, &cs); err != nil {
		if errors.IsNotFound(err) {
			log.Info("catalog source deleted, removing from cache")
			r.Cache.Delete(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		log.Error(err, "failed to get catalog source")
		return ctrl.Result{}, err
	}

	if !r.matchesCatalogSource(&cs) {
		log.Info("catalog source does not match selectors, removing from cache")
		r.Cache.Delete(cs.Namespace, cs.Name)
		return ctrl.Result{}, nil
	}

	imageRef, err := r.getCatalogImageRef(ctx, &cs)
	if err != nil {
		log.Error(err, "failed to get catalog image ref")
		return ctrl.Result{}, err
	}
	if imageRef == "" {
		log.Info("no valid image ref for catalog source, skipping")
		return ctrl.Result{}, nil
	}

	// Check cache: skip pull if digest hasn't changed
	if entry, found := r.Cache.Get(cs.Namespace, cs.Name); found && entry.ImageDigest == imageRef {
		log.V(1).Info("cache hit, image unchanged", "imageRef", imageRef)
		return ctrl.Result{}, nil
	}

	log.Info("pulling image for lifecycle data", "imageRef", imageRef)
	data, err := r.Puller.PullAndExtractLifecycleData(ctx, imageRef, cs.Spec.Secrets, cs.Namespace)
	if err != nil {
		log.Error(err, "failed to pull and extract lifecycle data", "imageRef", imageRef)
		return ctrl.Result{}, err
	}

	if data.CountBlobs() == 0 {
		log.Info("no lifecycle data found in image, caching negative result", "imageRef", imageRef)
		r.Cache.Set(cs.Namespace, cs.Name, imageRef, nil)
	} else {
		log.Info("cached lifecycle data", "imageRef", imageRef, "blobs", data.CountBlobs(), "packages", data.CountPackages())
		r.Cache.Set(cs.Namespace, cs.Name, imageRef, data)
	}

	return ctrl.Result{}, nil
}

// getCatalogImageRef extracts the resolved image digest from the running catalog pod.
func (r *LifecycleReconciler) getCatalogImageRef(ctx context.Context, cs *operatorsv1alpha1.CatalogSource) (string, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(cs.Namespace),
		client.MatchingLabels{catalogLabelKey: cs.Name},
	); err != nil {
		return "", err
	}

	var best *corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase != corev1.PodRunning || !podReady(p) {
			continue
		}
		if imageID(p) == "" {
			continue
		}
		if best == nil || podStartedAfter(p, best) {
			best = p
		}
	}

	if best == nil {
		return "", nil
	}
	return imageID(best), nil
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podStartedAfter(a, b *corev1.Pod) bool {
	aTime := a.CreationTimestamp.Time
	if a.Status.StartTime != nil {
		aTime = a.Status.StartTime.Time
	}
	bTime := b.CreationTimestamp.Time
	if b.Status.StartTime != nil {
		bTime = b.Status.StartTime.Time
	}
	return aTime.After(bTime)
}

// imageID extracts the resolved image digest from pod status.
func imageID(pod *corev1.Pod) string {
	for i := range pod.Status.InitContainerStatuses {
		if pod.Status.InitContainerStatuses[i].Name == "extract-content" {
			return pod.Status.InitContainerStatuses[i].ImageID
		}
	}
	if len(pod.Status.ContainerStatuses) > 0 {
		return pod.Status.ContainerStatuses[0].ImageID
	}
	return ""
}

// catalogPodPredicate filters pod events to only meaningful changes.
func catalogPodPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return true },
		DeleteFunc: func(e event.DeleteEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, ok := e.ObjectOld.(*corev1.Pod)
			if !ok {
				return false
			}
			newPod, ok := e.ObjectNew.(*corev1.Pod)
			if !ok {
				return false
			}
			if oldPod.Status.Phase != newPod.Status.Phase {
				return true
			}
			if podReady(oldPod) != podReady(newPod) {
				return true
			}
			if imageID(oldPod) != imageID(newPod) {
				return true
			}
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool { return true },
	}
}

func mapPodToCatalogSource(_ context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	catalogName := pod.Labels[catalogLabelKey]
	if catalogName == "" {
		return nil
	}
	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      catalogName,
				Namespace: pod.Namespace,
			},
		},
	}
}

// CatalogPodLabelSelector returns a label selector matching catalog pods.
func CatalogPodLabelSelector() labels.Selector {
	req, _ := labels.NewRequirement(catalogLabelKey, "exists", nil)
	return labels.NewSelector().Add(*req)
}

// SetupWithManager sets up the controller with the Manager.
func (r *LifecycleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&operatorsv1alpha1.CatalogSource{}, builder.WithPredicates(
			predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{}),
		)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(mapPodToCatalogSource), builder.WithPredicates(catalogPodPredicate())).
		Complete(r)
}

// isDigest returns true if the image reference contains a digest (@sha256:...).
func isDigest(ref string) bool {
	return strings.Contains(ref, "@")
}
