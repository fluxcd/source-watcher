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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/artifact/storage"
	"github.com/fluxcd/pkg/runtime/conditions"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

// finalize handles the finalization of the object during deletion.
func (r *ArtifactGeneratorReconciler) finalize(ctx context.Context,
	obj *swapi.ArtifactGenerator) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Delete ExternalArtifacts found in the inventory.
	r.finalizeExternalArtifacts(ctx, obj.Status.Inventory)

	// Remove the finalizer.
	controllerutil.RemoveFinalizer(obj, swapi.Finalizer)
	log.Info("Removed finalizer", "finalizer", swapi.Finalizer)

	return ctrl.Result{}, nil
}

// finalizeExternalArtifacts deletes the ExternalArtifact resources
// referenced in the provided list, along with their associated
// artifacts in the storage backend.
func (r *ArtifactGeneratorReconciler) finalizeExternalArtifacts(ctx context.Context,
	refs []swapi.ExternalArtifactReference) {
	log := ctrl.LoggerFrom(ctx)

	for _, eaRef := range refs {
		// Delete from storage.
		storagePath := storage.ArtifactPath(sourcev1.ExternalArtifactKind, eaRef.Namespace, eaRef.Name, "*")
		rmDir, err := r.Storage.RemoveAll(meta.Artifact{Path: storagePath})
		if err != nil {
			log.Error(err, "Failed to delete artifact from storage", "path", storagePath)
		} else if rmDir != "" {
			log.Info(fmt.Sprintf("%s/%s/%s deleted from storage", sourcev1.ExternalArtifactKind, eaRef.Namespace, eaRef.Name), "path", rmDir)
		}

		// Delete from cluster.
		ea := &sourcev1.ExternalArtifact{
			ObjectMeta: metav1.ObjectMeta{
				Name:      eaRef.Name,
				Namespace: eaRef.Namespace,
			},
		}
		err = r.Client.Delete(ctx, ea)
		if err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete ExternalArtifact")
		} else {
			log.Info(fmt.Sprintf("%s/%s/%s deleted from cluster", sourcev1.ExternalArtifactKind, eaRef.Namespace, eaRef.Name))
		}
	}
}

// addFinalizer sets the initial status conditions, adds the finalizer
// and requests an immediate requeue.
func (r *ArtifactGeneratorReconciler) addFinalizer(obj *swapi.ArtifactGenerator) (ctrl.Result, error) {
	controllerutil.AddFinalizer(obj, swapi.Finalizer)
	if obj.IsDisabled() {
		conditions.MarkTrue(obj,
			meta.ReadyCondition,
			swapi.ReconciliationDisabledReason,
			"%s", msgInitSuspended)
	} else {
		conditions.MarkUnknown(obj,
			meta.ReadyCondition,
			meta.ProgressingReason,
			"%s", msgInProgress)
		conditions.MarkReconciling(obj,
			meta.ProgressingReason,
			"%s", msgInProgress)
	}

	return ctrl.Result{RequeueAfter: time.Millisecond}, nil
}
