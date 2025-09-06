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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	kuberecorder "k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/fluxcd/pkg/artifact/storage"
	"github.com/fluxcd/pkg/runtime/patch"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

// ArtifactGeneratorReconciler reconciles a ArtifactGenerator object.
type ArtifactGeneratorReconciler struct {
	client.Client
	kuberecorder.EventRecorder

	ControllerName            string
	Scheme                    *runtime.Scheme
	Storage                   *storage.Storage
	APIReader                 client.Reader
	ArtifactFetchRetries      int
	DependencyRequeueInterval time.Duration
}

// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ArtifactGeneratorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	log := ctrl.LoggerFrom(ctx)

	obj := &swapi.ArtifactGenerator{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Initialize the runtime patcher with the current version of the object.
	patcher := patch.NewSerialPatcher(obj, r.Client)

	// Finalize the reconciliation and report the results.
	defer func() {
		if err := r.finalizeStatus(ctx, obj, patcher); err != nil {
			log.Error(err, "failed to update status")
			retErr = kerrors.NewAggregate([]error{retErr, err})
		}
	}()

	// Finalize the reconciliation and release resources
	// if the object is being deleted.
	if !obj.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, obj)
	}

	// Add the finalizer if it does not exist.
	if !controllerutil.ContainsFinalizer(obj, swapi.Finalizer) {
		log.Info("Adding finalizer", "finalizer", swapi.Finalizer)
		r.initializeObjectStatus(obj)
		return ctrl.Result{RequeueAfter: time.Millisecond}, nil
	}

	// Pause reconciliation if the object has the reconcile annotation set to 'disabled'.
	if obj.IsDisabled() {
		log.Error(errors.New("can't reconcile"), msgReconciliationDisabled)
		r.Event(obj, corev1.EventTypeWarning, swapi.ReconciliationDisabledReason, msgReconciliationDisabled)
		return ctrl.Result{}, nil
	}

	log.Info("Reconciling")
	return ctrl.Result{RequeueAfter: obj.GetRequeueAfter()}, nil
}
