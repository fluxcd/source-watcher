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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/artifact/storage"
	"github.com/fluxcd/pkg/runtime/conditions"
	"github.com/fluxcd/pkg/runtime/patch"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

const (
	msgInProgress             = "Reconciliation in progress"
	msgInitSuspended          = "Initialized with reconciliation suspended"
	msgReconciliationDisabled = "Reconciliation is disabled"
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
	refs []meta.NamespacedObjectKindReference) {
	log := ctrl.LoggerFrom(ctx)

	for _, eaRef := range refs {
		// Delete from storage.
		storagePath := storage.ArtifactPath(eaRef.Kind, eaRef.Namespace, eaRef.Name, "*")
		rmDir, err := r.Storage.RemoveAll(meta.Artifact{Path: storagePath})
		if err != nil {
			log.Error(err, "Failed to delete artifact from storage", "path", storagePath)
		} else if rmDir != "" {
			log.Info(fmt.Sprintf("%s/%s/%s deleted from storage", eaRef.Kind, eaRef.Namespace, eaRef.Name), "path", rmDir)
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
			log.Info(fmt.Sprintf("%s/%s/%s deleted from cluster", eaRef.Kind, eaRef.Namespace, eaRef.Name))
		}
	}
}

// finalizeStatus updates the status of the object after reconciliation
// by setting the last handled reconcile time and removing stale conditions.
func (r *ArtifactGeneratorReconciler) finalizeStatus(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	patcher *patch.SerialPatcher) error {
	// Set the value of the reconciliation request in status.
	if v, ok := meta.ReconcileAnnotationValue(obj.GetAnnotations()); ok {
		obj.SetLastHandledReconcileAt(v)
	}

	// Set the Reconciling reason to ProgressingWithRetry if the
	// reconciliation has failed.
	if conditions.IsFalse(obj, meta.ReadyCondition) &&
		conditions.Has(obj, meta.ReconcilingCondition) {
		rc := conditions.Get(obj, meta.ReconcilingCondition)
		rc.Reason = meta.ProgressingWithRetryReason
		conditions.Set(obj, rc)
	}

	// Remove the Reconciling condition.
	if conditions.IsTrue(obj, meta.ReadyCondition) || conditions.IsTrue(obj, meta.StalledCondition) {
		conditions.Delete(obj, meta.ReconcilingCondition)
	}

	// Patch finalizers, status and conditions.
	return r.patch(ctx, obj, patcher)
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

// patch updates the object status, conditions and finalizers.
func (r *ArtifactGeneratorReconciler) patch(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	patcher *patch.SerialPatcher) (retErr error) {
	// Configure the runtime patcher.
	ownedConditions := []string{
		meta.ReadyCondition,
		meta.ReconcilingCondition,
		meta.StalledCondition,
	}
	patchOpts := []patch.Option{
		patch.WithOwnedConditions{Conditions: ownedConditions},
		patch.WithForceOverwriteConditions{},
		patch.WithFieldOwner(r.ControllerName),
	}

	// Patch the object status, conditions and finalizers.
	if err := patcher.Patch(ctx, obj, patchOpts...); err != nil {
		if !obj.GetDeletionTimestamp().IsZero() {
			err = kerrors.FilterOut(err, func(e error) bool { return apierrors.IsNotFound(e) })
		}
		retErr = kerrors.NewAggregate([]error{retErr, err})
		if retErr != nil {
			return retErr
		}
	}

	return nil
}

// newTerminalErrorFor returns a terminal error, sets the Ready condition to False
// with the provided reason and message, marks the object as Stalled, and records
// a warning event.
func (r *ArtifactGeneratorReconciler) newTerminalErrorFor(obj *swapi.ArtifactGenerator,
	reason string, messageFormat string, messageArgs ...any) error {
	terminalErr := fmt.Errorf(messageFormat, messageArgs...)
	conditions.MarkFalse(obj, meta.ReadyCondition, reason, "%s", terminalErr.Error())
	conditions.MarkStalled(obj, reason, "%s", terminalErr.Error())
	r.Event(obj, corev1.EventTypeWarning, reason, terminalErr.Error())
	return reconcile.TerminalError(terminalErr)
}
