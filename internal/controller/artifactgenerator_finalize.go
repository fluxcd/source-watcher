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
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkstorage "github.com/fluxcd/pkg/artifact/storage"
	gotkconditions "github.com/fluxcd/pkg/runtime/conditions"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

// finalize handles the finalization of the object during deletion.
func (r *ArtifactGeneratorReconciler) finalize(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	impersonated client.Client) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Delete the objects found in the inventory. Objects whose deletion is
	// not confirmed are kept in the inventory so the finalizer is retried on
	// the next reconciliation.
	survivors := r.finalizeReferences(ctx, obj, obj.Status.Inventory, impersonated)
	if len(survivors) > 0 {
		obj.Status.Inventory = survivors
		msg := fmt.Sprintf("failed to prune %d object(s), retrying", len(survivors))
		gotkconditions.MarkFalse(obj, gotkmeta.ReadyCondition, gotkmeta.PruneFailedReason, "%s", msg)
		r.Eventf(obj, nil, corev1.EventTypeWarning, gotkmeta.PruneFailedReason, swapi.ActionReconcile.String(), "%s", msg)
		return ctrl.Result{}, fmt.Errorf("failed to prune %d object(s)", len(survivors))
	}

	// Remove the finalizer.
	controllerutil.RemoveFinalizer(obj, swapi.Finalizer)
	log.Info("Removed finalizer", "finalizer", swapi.Finalizer)

	return ctrl.Result{}, nil
}

// finalizeReferences deletes the objects referenced in the provided list.
// ExternalArtifacts are deleted along with their associated artifacts in the
// storage backend. Managed namespaces are deleted only when pruning is enabled.
// It returns the references whose deletion was not confirmed by the API server,
// so the caller can keep them tracked and retry on the next reconciliation.
func (r *ArtifactGeneratorReconciler) finalizeReferences(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	refs []swapi.InventoryEntry,
	impersonated client.Client) []swapi.InventoryEntry {
	log := ctrl.LoggerFrom(ctx)
	var survivors []swapi.InventoryEntry

	for _, ref := range refs {
		if ref.Kind == swapi.NamespaceKind {
			if !obj.ManagesNamespaces() || !obj.NamespacePrune(r.DefaultToPruneNamespaces) {
				log.Info("Skipping managed namespace deletion, pruning is disabled", "namespace", ref.Name)
				continue
			}
			if err := r.deleteNamespace(ctx, ref.Name, impersonated); err != nil {
				log.Error(err, "Failed to delete Namespace, will retry", "namespace", ref.Name)
				survivors = append(survivors, ref)
			}
			continue
		}

		if err := r.finalizeExternalArtifact(ctx, obj, ref, impersonated); err != nil {
			log.Error(err, "Failed to delete ExternalArtifact, will retry",
				"artifact", fmt.Sprintf("%s/%s/%s", sourcev1.ExternalArtifactKind, ref.Namespace, ref.Name))
			survivors = append(survivors, ref)
		}
	}
	return survivors
}

// finalizeExternalArtifact deletes the ExternalArtifact referenced in the
// provided entry, along with its associated artifact in the storage backend.
// It returns an error when the deletion was not confirmed by the API server.
func (r *ArtifactGeneratorReconciler) finalizeExternalArtifact(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	ref swapi.InventoryEntry,
	impersonated client.Client) error {
	log := ctrl.LoggerFrom(ctx)

	// Delete from storage.
	var retErr error
	storagePath := gotkstorage.ArtifactPath(sourcev1.ExternalArtifactKind, ref.Namespace, ref.Name, "*")
	if rmDir, err := r.Storage.RemoveAll(gotkmeta.Artifact{Path: storagePath}); err != nil {
		retErr = fmt.Errorf("failed to delete artifact from storage: %w", err)
	} else if rmDir != "" {
		log.Info(fmt.Sprintf("%s/%s/%s deleted from storage", sourcev1.ExternalArtifactKind, ref.Namespace, ref.Name), "path", rmDir)
	}

	// Delete from cluster.
	ea := &sourcev1.ExternalArtifact{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ref.Name,
			Namespace: ref.Namespace,
		},
	}
	if err := r.clientForNamespace(obj, impersonated, ref.Namespace).Delete(ctx, ea); err != nil && !apierrors.IsNotFound(err) {
		retErr = errors.Join(retErr, fmt.Errorf("failed to delete ExternalArtifact: %w", err))
	} else {
		log.Info(fmt.Sprintf("%s/%s/%s deleted from cluster", sourcev1.ExternalArtifactKind, ref.Namespace, ref.Name))
	}

	return retErr
}

// addFinalizer sets the initial status conditions, adds the finalizer
// and requests an immediate requeue.
func (r *ArtifactGeneratorReconciler) addFinalizer(obj *swapi.ArtifactGenerator) (ctrl.Result, error) {
	controllerutil.AddFinalizer(obj, swapi.Finalizer)
	if obj.IsDisabled() {
		gotkconditions.MarkTrue(obj,
			gotkmeta.ReadyCondition,
			swapi.ReconciliationDisabledReason,
			"%s", msgInitSuspended)
	} else {
		gotkconditions.MarkUnknown(obj,
			gotkmeta.ReadyCondition,
			gotkmeta.ProgressingReason,
			"%s", msgInProgress)
		gotkconditions.MarkReconciling(obj,
			gotkmeta.ProgressingReason,
			"%s", msgInProgress)
	}

	return ctrl.Result{RequeueAfter: time.Millisecond}, nil
}
