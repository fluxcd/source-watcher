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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkconditions "github.com/fluxcd/pkg/runtime/conditions"
	gotkpatch "github.com/fluxcd/pkg/runtime/patch"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

const (
	msgInProgress             = "Reconciliation in progress"
	msgInitSuspended          = "Initialized with reconciliation suspended"
	msgReconciliationDisabled = "Reconciliation is disabled"
)

// summarizeStatus updates the status of the object after reconciliation
// by setting the last handled reconcile time and removing stale conditions.
func (r *ArtifactGeneratorReconciler) summarizeStatus(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	patcher *gotkpatch.SerialPatcher) error {
	// Set the value of the reconciliation request in status.
	if v, ok := gotkmeta.ReconcileAnnotationValue(obj.GetAnnotations()); ok {
		obj.SetLastHandledReconcileAt(v)
	}

	// Set the Reconciling reason to ProgressingWithRetry if the
	// reconciliation has failed.
	if gotkconditions.IsFalse(obj, gotkmeta.ReadyCondition) &&
		gotkconditions.Has(obj, gotkmeta.ReconcilingCondition) {
		rc := gotkconditions.Get(obj, gotkmeta.ReconcilingCondition)
		rc.Reason = gotkmeta.ProgressingWithRetryReason
		gotkconditions.Set(obj, rc)
	}

	// Remove the Reconciling condition.
	if gotkconditions.IsTrue(obj, gotkmeta.ReadyCondition) || gotkconditions.IsTrue(obj, gotkmeta.StalledCondition) {
		gotkconditions.Delete(obj, gotkmeta.ReconcilingCondition)
	}

	// Patch finalizers, status and conditions.
	return r.patchStatus(ctx, obj, patcher)
}

// patchStatus patches the object status sub-resource and finalizers.
func (r *ArtifactGeneratorReconciler) patchStatus(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	patcher *gotkpatch.SerialPatcher) (retErr error) {
	// Configure the runtime patcher.
	ownedConditions := []string{
		gotkmeta.ReadyCondition,
		gotkmeta.ReconcilingCondition,
		gotkmeta.StalledCondition,
	}
	patchOpts := []gotkpatch.Option{
		gotkpatch.WithOwnedConditions{Conditions: ownedConditions},
		gotkpatch.WithForceOverwriteConditions{},
		gotkpatch.WithFieldOwner(r.ControllerName),
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
	gotkconditions.MarkFalse(obj, gotkmeta.ReadyCondition, reason, "%s", terminalErr.Error())
	gotkconditions.MarkStalled(obj, reason, "%s", terminalErr.Error())
	r.Event(obj, corev1.EventTypeWarning, reason, terminalErr.Error())
	return reconcile.TerminalError(terminalErr)
}
