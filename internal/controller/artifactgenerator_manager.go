/*
Copyright 2025 The Flux authors

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
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/fluxcd/pkg/runtime/predicates"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

// sourceRefIndexKey is the cache index key used to index
// ArtifactGenerators by their source references.
const sourceRefIndexKey string = ".metadata.sourceRef"

type ArtifactGeneratorReconcilerOptions struct {
	RateLimiter workqueue.TypedRateLimiter[reconcile.Request]
}

// SetupWithManager sets up the controller with the Manager and configures
// watches for sources referenced by ArtifactGenerators.
func (r *ArtifactGeneratorReconciler) SetupWithManager(ctx context.Context,
	mgr ctrl.Manager,
	opts ArtifactGeneratorReconcilerOptions) error {
	if err := mgr.GetCache().IndexField(ctx,
		&swapi.ArtifactGenerator{},
		sourceRefIndexKey,
		r.indexBySourceRef); err != nil {
		return fmt.Errorf("failed to set index field '%s': %w", sourceRefIndexKey, err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&swapi.ArtifactGenerator{},
			builder.WithPredicates(
				predicate.Or(
					predicate.GenerationChangedPredicate{},
					predicates.ReconcileRequestedPredicate{},
				),
			)).
		Watches(
			&sourcev1.GitRepository{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForSourceChange),
			builder.WithPredicates(sourceChangePredicate),
		).
		Watches(
			&sourcev1.OCIRepository{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForSourceChange),
			builder.WithPredicates(sourceChangePredicate),
		).
		Watches(
			&sourcev1.Bucket{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForSourceChange),
			builder.WithPredicates(sourceChangePredicate),
		).
		WithOptions(controller.Options{
			RateLimiter: opts.RateLimiter,
		}).
		Complete(r)
}

// requestsForSourceChange returns a list of reconcile requests for
// ArtifactGenerators that reference the given source object.
func (r *ArtifactGeneratorReconciler) requestsForSourceChange(ctx context.Context, obj client.Object) []reconcile.Request {
	log := ctrl.LoggerFrom(ctx)
	source, ok := obj.(sourcev1.Source)
	if !ok {
		log.Error(fmt.Errorf("expected object to implement the Source interface, but got a %T", obj),
			"failed to get source for revision change")
		return nil
	}

	// If we do not have an artifact, we have no requests to make
	if source.GetArtifact() == nil {
		return nil
	}

	sourceGVK, err := r.GroupVersionKindFor(obj)
	if err != nil {
		log.Error(err, "failed to get GVK of source for revision change")
		return nil
	}

	log.V(1).Info("processing source change",
		"kind", sourceGVK.Kind,
		"name", obj.GetName(),
		"namespace", obj.GetNamespace(),
		"revision", source.GetArtifact().Revision)

	var list swapi.ArtifactGeneratorList
	if err := r.List(ctx, &list, client.MatchingFields{
		sourceRefIndexKey: fmt.Sprintf("%s/%s", sourceGVK.Kind, client.ObjectKeyFromObject(obj).String()),
	}); err != nil {
		log.Error(err, "failed to list objects for source change")
		return nil
	}

	reqs := make([]reconcile.Request, len(list.Items))
	for i, ag := range list.Items {
		reqs[i].NamespacedName = types.NamespacedName{Name: ag.Name, Namespace: ag.Namespace}
	}

	return reqs
}

// indexBySourceRef indexes ArtifactGenerators by their source references
// in the format "<kind>/<namespace>/<name>".
func (r *ArtifactGeneratorReconciler) indexBySourceRef(o client.Object) []string {
	ag, ok := o.(*swapi.ArtifactGenerator)
	if !ok {
		panic(fmt.Sprintf("Expected to find ArtifactGenerator object, but got a %T", o))
	}
	indexers := make([]string, 0)
	for _, source := range ag.Spec.Sources {
		namespace := source.Namespace
		if namespace == "" {
			namespace = ag.Namespace
		}
		indexers = append(indexers, fmt.Sprintf("%s/%s/%s", source.Kind, namespace, source.Name))
	}
	return indexers
}

// sourceChangePredicate filters source changes to only those that
// represent a new artifact revision.
var sourceChangePredicate = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldObj, ok := e.ObjectOld.(sourcev1.Source)
		if !ok {
			return false
		}
		newObj, ok := e.ObjectNew.(sourcev1.Source)
		if !ok {
			return false
		}

		if newObj.GetArtifact() == nil {
			return false
		}

		if oldObj.GetArtifact() == nil && newObj.GetArtifact() != nil {
			return true
		}

		if !oldObj.GetArtifact().HasRevision(newObj.GetArtifact().Revision) {
			return true
		}

		return false
	},
}
