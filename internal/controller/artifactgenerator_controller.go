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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/artifact/storage"
	"github.com/fluxcd/pkg/http/fetch"
	"github.com/fluxcd/pkg/runtime/conditions"
	"github.com/fluxcd/pkg/runtime/patch"
	"github.com/fluxcd/pkg/tar"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	kuberecorder "k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
	"github.com/fluxcd/source-watcher/internal/builder"
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
	NoCrossNamespaceRefs      bool
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
		r.addFinalizer(obj)
		return ctrl.Result{RequeueAfter: time.Millisecond}, nil
	}

	// Pause reconciliation if the object has the reconcile annotation set to 'disabled'.
	if obj.IsDisabled() {
		log.Error(errors.New("can't reconcile"), msgReconciliationDisabled)
		r.Event(obj, corev1.EventTypeWarning, swapi.ReconciliationDisabledReason, msgReconciliationDisabled)
		return ctrl.Result{}, nil
	}

	// Validate the ArtifactGenerator spec
	if err := r.validateSpec(obj); err != nil {
		return ctrl.Result{}, err
	}

	return r.reconcile(ctx, obj, patcher)
}

func (r *ArtifactGeneratorReconciler) reconcile(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	patcher *patch.SerialPatcher) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Create a temporary directory to fetch sources and build artifacts.
	tmpDir, err := builder.MkdirTempAbs("", "ag-")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create tmp dir: %w", err)
	}

	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			log.Error(err, "failed to remove tmp dir", "dir", tmpDir)
		}
	}()

	// Fetch all sources into the tmpDir.
	// The sources will be placed in subdirectories named after the source alias:
	// <tmpDir>/<source-alias>/<source-files>
	localSources, revisions, err := r.fetchSources(ctx, obj, tmpDir)
	if err != nil {
		msg := fmt.Sprintf("fetch sources failed: %s", err.Error())
		conditions.MarkFalse(obj,
			meta.ReadyCondition,
			swapi.SourceFetchFailedReason,
			"%s", msg)
		r.Event(obj, corev1.EventTypeWarning, swapi.SourceFetchFailedReason, msg)
		log.Error(err, "failed to fetch sources, retrying")
		return ctrl.Result{RequeueAfter: r.DependencyRequeueInterval}, nil
	}

	// Prepare a slice to hold the references to the created ExternalArtifact objects.
	extRefs := make([]meta.NamespacedObjectKindReference, 0, len(obj.Spec.OutputArtifacts))

	// Build the artifacts and reconcile the ExternalArtifact objects.
	// The artifacts will be stored in the storage under the following path:
	// <storage-root>/<kind>/<namespace>/<name>/<artifact-uuid>.tar.gz
	artifactBuilder := builder.New(r.Storage)
	for _, oa := range obj.Spec.OutputArtifacts {
		// Build the artifact using the local sources.
		artifact, err := artifactBuilder.Build(ctx, &oa, localSources, obj.Namespace, tmpDir)
		if err != nil {
			msg := fmt.Sprintf("%s build failed: %s", oa.Name, err.Error())
			conditions.MarkFalse(obj,
				meta.ReadyCondition,
				meta.BuildFailedReason,
				"%s", msg)
			r.Event(obj, corev1.EventTypeWarning, meta.BuildFailedReason, msg)
			return ctrl.Result{}, err
		}

		// Override the revision with the one from the source if specified.
		if oa.Revision != "" {
			if rev, ok := revisions[strings.TrimPrefix(oa.Revision, "@")]; ok {
				artifact.Revision = rev
			}
		}

		// Reconcile the ExternalArtifact corresponding to the built artifact.
		// The ExternalArtifact will reference the artifact stored in the storage backend.
		// If the ExternalArtifact already exists, its status will be updated with the new artifact details.
		extRef, err := r.reconcileExternalArtifact(ctx, obj, &oa, artifact)
		if err != nil {
			msg := fmt.Sprintf("%s reconcile failed: %s", oa.Name, err.Error())
			conditions.MarkFalse(obj,
				meta.ReadyCondition,
				meta.ReconciliationFailedReason,
				"%s", msg)
			r.Event(obj, corev1.EventTypeWarning, meta.ReconciliationFailedReason, msg)
			return ctrl.Result{}, err
		}
		extRefs = append(extRefs, *extRef)
	}

	// Garbage collect orphaned ExternalArtifacts and their associated artifacts in storage.
	if orphans := r.findOrphanedReferences(obj.Status.Inventory, extRefs); len(orphans) > 0 {
		r.finalizeExternalArtifacts(ctx, orphans)
	}

	// Garbage collect old artifacts in storage according to the retention policy.
	for _, eaRef := range extRefs {
		storagePath := storage.ArtifactPath(eaRef.Kind, eaRef.Namespace, eaRef.Name, "*")
		delFiles, err := r.Storage.GarbageCollect(ctx, meta.Artifact{Path: storagePath}, 5*time.Minute)
		if err != nil {
			log.Error(err, "failed to garbage collect artifacts", "path", storagePath)
		} else if len(delFiles) > 0 {
			log.Info(fmt.Sprintf("garbage collected %d old artifact(s)", len(delFiles)), "artifacts", delFiles)
		}
	}

	// Update the status with the list of ExternalArtifact references.
	obj.Status.Inventory = extRefs
	msg := fmt.Sprintf("reconciliation succeeded, generated %d artifact(s)", len(extRefs))
	conditions.MarkTrue(obj,
		meta.ReadyCondition,
		meta.SucceededReason,
		"%s", msg)
	r.Event(obj, corev1.EventTypeNormal, meta.ReadyCondition, msg)

	return ctrl.Result{RequeueAfter: obj.GetRequeueAfter()}, nil
}

// fetchSources fetches the sources defined in the ArtifactGenerator spec
// into the provided tmpDir under a subdirectory named after the source alias.
// It returns a map of source alias to the absolute path where the source was fetched.
func (r *ArtifactGeneratorReconciler) fetchSources(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	tmpDir string) (dirs map[string]string, revisions map[string]string, err error) {
	// Map of source alias to local path.
	dirs = make(map[string]string)
	// Map of source alias to revision.
	revisions = make(map[string]string)

	for _, src := range obj.Spec.Sources {
		namespacedName := client.ObjectKey{
			Name:      src.Name,
			Namespace: obj.Namespace,
		}

		if src.Namespace != "" {
			namespacedName.Namespace = src.Namespace
		}

		var source sourcev1.Source
		switch src.Kind {
		case sourcev1.OCIRepositoryKind:
			var repository sourcev1.OCIRepository
			err := r.Client.Get(ctx, namespacedName, &repository)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, nil, err
				}
				return nil, nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &repository
		case sourcev1.GitRepositoryKind:
			var repository sourcev1.GitRepository
			err := r.Client.Get(ctx, namespacedName, &repository)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, nil, err
				}
				return nil, nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &repository
		case sourcev1.BucketKind:
			var bucket sourcev1.Bucket
			err := r.Client.Get(ctx, namespacedName, &bucket)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, nil, err
				}
				return nil, nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &bucket
		default:
			return nil, nil, fmt.Errorf("source `%s` kind '%s' not supported",
				src.Name, src.Kind)
		}

		if source.GetArtifact() == nil {
			return nil, nil, fmt.Errorf("source '%s/%s' is not ready", src.Kind, namespacedName)
		}

		// Create a dir for the source alias.
		srcDir := filepath.Join(tmpDir, src.Alias)
		if err := os.MkdirAll(srcDir, 0o755); err != nil {
			return nil, nil, fmt.Errorf("failed to create source dir: %w", err)
		}

		// Download artifact and extract files to the source alias dir.
		fetcher := fetch.New(
			fetch.WithLogger(ctrl.LoggerFrom(ctx)),
			fetch.WithRetries(r.ArtifactFetchRetries),
			fetch.WithMaxDownloadSize(tar.UnlimitedUntarSize),
			fetch.WithUntar(tar.WithMaxUntarSize(tar.UnlimitedUntarSize)),
			fetch.WithHostnameOverwrite(os.Getenv("SOURCE_CONTROLLER_LOCALHOST")),
		)
		if err := fetcher.Fetch(source.GetArtifact().URL, source.GetArtifact().Digest, srcDir); err != nil {
			return nil, nil, err
		}
		dirs[src.Alias] = srcDir
		revisions[src.Alias] = source.GetArtifact().Revision
	}

	return dirs, revisions, nil
}

// reconcileExternalArtifact ensures the ExternalArtifact object exists and is up to date
// with the provided artifact details. It returns a reference to the ExternalArtifact.
func (r *ArtifactGeneratorReconciler) reconcileExternalArtifact(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	outputArtifact *swapi.OutputArtifact,
	artifact *meta.Artifact) (*meta.NamespacedObjectKindReference, error) {
	log := ctrl.LoggerFrom(ctx)
	// Create the ExternalArtifact object.
	externalArtifact := &sourcev1.ExternalArtifact{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sourcev1.GroupVersion.String(),
			Kind:       sourcev1.ExternalArtifactKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      outputArtifact.Name,
			Namespace: obj.Namespace,
			Labels:    obj.GetLabels(),
		},
		Spec: sourcev1.ExternalArtifactSpec{
			SourceRef: &meta.NamespacedObjectKindReference{
				APIVersion: swapi.GroupVersion.String(),
				Kind:       swapi.ArtifactGeneratorKind,
				Name:       obj.Name,
				Namespace:  obj.Namespace,
			},
		},
	}

	// Apply the ExternalArtifact object.
	forceApply := true
	if err := r.Patch(ctx, externalArtifact, client.Apply, &client.PatchOptions{
		FieldManager: r.ControllerName,
		Force:        &forceApply,
	}); err != nil {
		return nil, fmt.Errorf("failed to apply ExternalArtifact: %w", err)
	}

	// Update the status of the ExternalArtifact with the artifact details.
	externalArtifact.ManagedFields = nil
	externalArtifact.Status = sourcev1.ExternalArtifactStatus{
		Artifact: artifact,
		Conditions: []metav1.Condition{
			{
				ObservedGeneration: externalArtifact.GetGeneration(),
				Type:               meta.ReadyCondition,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             meta.SucceededReason,
				Message:            "Artifact is ready",
			},
		},
	}
	statusOpts := &client.SubResourcePatchOptions{
		PatchOptions: client.PatchOptions{
			FieldManager: r.ControllerName,
		},
	}
	if err := r.Status().Patch(ctx, externalArtifact, client.Apply, statusOpts); err != nil {
		return nil, fmt.Errorf("failed to patch ExternalArtifact status: %w", err)
	}

	log.Info(fmt.Sprintf("ExternalArtifact/%s/%s reconciled with revision %s",
		externalArtifact.Namespace, externalArtifact.Name, artifact.Revision))

	return &meta.NamespacedObjectKindReference{
		APIVersion: sourcev1.GroupVersion.String(),
		Kind:       sourcev1.ExternalArtifactKind,
		Name:       externalArtifact.Name,
		Namespace:  externalArtifact.Namespace,
	}, nil
}

// findOrphanedReferences identifies ExternalArtifact references in the inventory
// that are not present in the current references, indicating they should be garbage collected.
func (r *ArtifactGeneratorReconciler) findOrphanedReferences(
	inventory []meta.NamespacedObjectKindReference,
	currentRefs []meta.NamespacedObjectKindReference) []meta.NamespacedObjectKindReference {
	// Create map of current references for O(1) lookup
	currentSet := make(map[meta.NamespacedObjectKindReference]struct{})
	for _, ref := range currentRefs {
		currentSet[ref] = struct{}{}
	}

	// Find inventory items not in current set
	var orphaned []meta.NamespacedObjectKindReference
	for _, ref := range inventory {
		if _, exists := currentSet[ref]; !exists {
			orphaned = append(orphaned, ref)
		}
	}

	return orphaned
}

// validateSpec validates the ArtifactGenerator spec for uniqueness and multi-tenancy constraints.
func (r *ArtifactGeneratorReconciler) validateSpec(obj *swapi.ArtifactGenerator) error {
	// Validate source aliases.
	aliasMap := make(map[string]bool)
	for _, src := range obj.Spec.Sources {
		// Check for duplicate aliases
		if aliasMap[src.Alias] {
			return r.newTerminalErrorFor(obj,
				swapi.ValidationFailedReason,
				"duplicate source alias '%s' found", src.Alias)
		}
		aliasMap[src.Alias] = true

		// Enforce multi-tenancy lockdown if configured.
		if r.NoCrossNamespaceRefs && src.Namespace != "" && src.Namespace != obj.Namespace {
			return r.newTerminalErrorFor(obj,
				swapi.AccessDeniedReason,
				"cross-namespace reference to source %s/%s/%s is not allowed",
				src.Kind, src.Namespace, src.Name)
		}
	}

	// Validate output artifact.
	nameMap := make(map[string]bool)
	for _, artifact := range obj.Spec.OutputArtifacts {
		// Check for duplicate artifact names.
		if nameMap[artifact.Name] {
			return r.newTerminalErrorFor(obj,
				swapi.ValidationFailedReason,
				"duplicate artifact name '%s' found", artifact.Name)
		}

		// Check that the revision source alias exists.
		if artifact.Revision != "" && !aliasMap[strings.TrimPrefix(artifact.Revision, "@")] {
			return r.newTerminalErrorFor(obj,
				swapi.ValidationFailedReason,
				"artifact %s revision source alias '%s' not found",
				artifact.Name, strings.TrimPrefix(artifact.Revision, "@"))
		}
		nameMap[artifact.Name] = true
	}

	return nil
}
