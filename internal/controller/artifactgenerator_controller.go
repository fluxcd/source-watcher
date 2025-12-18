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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	kuberecorder "k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkstroage "github.com/fluxcd/pkg/artifact/storage"
	gotkfetch "github.com/fluxcd/pkg/http/fetch"
	gotkconditions "github.com/fluxcd/pkg/runtime/conditions"
	gotkjitter "github.com/fluxcd/pkg/runtime/jitter"
	gotkpatch "github.com/fluxcd/pkg/runtime/patch"
	gotktar "github.com/fluxcd/pkg/tar"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	"github.com/fluxcd/source-watcher/v2/internal/builder"
)

// ArtifactGeneratorReconciler reconciles a ArtifactGenerator object.
type ArtifactGeneratorReconciler struct {
	client.Client
	kuberecorder.EventRecorder

	ControllerName            string
	Scheme                    *runtime.Scheme
	Storage                   *gotkstroage.Storage
	APIReader                 client.Reader
	ArtifactFetchRetries      int
	DependencyRequeueInterval time.Duration
	NoCrossNamespaceRefs      bool
}

// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators,verbs=get;list;watch;create;update;patchStatus;delete
// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators/status,verbs=get;update;patchStatus
// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators/finalizers,verbs=update
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=*,verbs=get;list;watch;create;update;patchStatus;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ArtifactGeneratorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	log := ctrl.LoggerFrom(ctx)

	obj := &swapi.ArtifactGenerator{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Initialize the runtime patcher with the current version of the object.
	patcher := gotkpatch.NewSerialPatcher(obj, r.Client)

	// Update the status at the end of the reconciliation.
	defer func() {
		if err := r.summarizeStatus(ctx, obj, patcher); err != nil {
			log.Error(err, "failed to update status")
			retErr = kerrors.NewAggregate([]error{retErr, err})
		}
	}()

	// Finalize the reconciliation and release resources if the object is being deleted.
	if !obj.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, obj)
	}

	// Add the finalizer if it does not exist.
	if !controllerutil.ContainsFinalizer(obj, swapi.Finalizer) {
		log.Info("Adding finalizer", "finalizer", swapi.Finalizer)
		return r.addFinalizer(obj)
	}

	// Pause reconciliation if the object has the reconcile annotation set to 'disabled'.
	if obj.IsDisabled() {
		log.Error(errors.New("can't reconcile"), msgReconciliationDisabled)
		r.Event(obj, corev1.EventTypeWarning, swapi.ReconciliationDisabledReason, msgReconciliationDisabled)
		return ctrl.Result{}, nil
	}

	// Validate the ArtifactGenerator spec and mark the object as Stalled if invalid.
	if err := r.validateSpec(obj); err != nil {
		return ctrl.Result{}, err
	}

	// Run drift detection and reconciliation.
	return r.reconcile(ctx, obj, patcher)
}

// reconcile contains the main reconciliation logic for the ArtifactGenerator.
func (r *ArtifactGeneratorReconciler) reconcile(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	patcher *gotkpatch.SerialPatcher) (ctrl.Result, error) {
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

	// Get the current observed state of the sources,
	// including their artifact URLs, digests, and revisions.
	remoteSources, err := r.observeSources(ctx, obj)
	if err != nil {
		msg := fmt.Sprintf("get sources failed: %s", err.Error())
		gotkconditions.MarkFalse(obj,
			gotkmeta.ReadyCondition,
			swapi.SourceFetchFailedReason,
			"%s", msg)
		r.Event(obj, corev1.EventTypeWarning, swapi.SourceFetchFailedReason, msg)
		log.Error(err, "failed to get sources, retrying")
		return ctrl.Result{RequeueAfter: r.DependencyRequeueInterval}, nil
	}

	// Calculate the hash of the observed sources.
	observedSourcesDigest := swapi.HashObservedSources(remoteSources)

	// Detect drift between the actual state and the desired state.
	// If no drift is detected in sources and the stored artifacts pass the
	// integrity verification, the reconciliation is complete and we can exit early.
	hasDrifted, reason := r.detectDrift(ctx, obj, observedSourcesDigest)
	if !hasDrifted {
		msg := fmt.Sprintf("No drift detected, %d artifact(s) up to date", len(obj.Status.Inventory))
		log.Info(msg)
		r.Event(obj, corev1.EventTypeNormal, gotkmeta.ReadyCondition, msg)
		return ctrl.Result{RequeueAfter: obj.GetRequeueAfter()}, nil
	}

	// Mark the object as reconciling and remove any previous error conditions.
	gotkconditions.MarkReconciling(obj,
		gotkmeta.ProgressingReason,
		"%s", msgInProgress)
	gotkconditions.MarkUnknown(obj,
		gotkmeta.ReadyCondition,
		gotkmeta.ProgressingReason,
		"%s", msgInProgress)
	log.Info(msgInProgress, "reason", reason)
	if err := r.patchStatus(ctx, obj, patcher); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	// Download and extract the sources artifacts into the tmpDir.
	// The contents will be placed in subdirectories named after the source alias:
	// <tmpDir>/<source-alias>/<source-files>
	localSources, err := r.fetchSources(ctx, remoteSources, tmpDir)
	if err != nil {
		msg := fmt.Sprintf("fetch sources failed: %s", err.Error())
		gotkconditions.MarkFalse(obj,
			gotkmeta.ReadyCondition,
			swapi.SourceFetchFailedReason,
			"%s", msg)
		r.Event(obj, corev1.EventTypeWarning, swapi.SourceFetchFailedReason, msg)
		log.Error(err, "failed to fetch sources, retrying")
		return ctrl.Result{RequeueAfter: r.DependencyRequeueInterval}, nil
	}

	// Prepare a slice to hold the references to the created ExternalArtifact objects.
	eaRefs := make([]swapi.ExternalArtifactReference, 0, len(obj.Spec.OutputArtifacts))

	// Build the artifacts and reconcile the ExternalArtifact objects.
	// The artifacts will be stored in the storage under the following path:
	// <storage-root>/<kind>/<namespace>/<name>/<contents-hash>.tar.gz
	artifactBuilder := builder.New(r.Storage)
	for _, oa := range obj.Spec.OutputArtifacts {
		// Build the artifact using the local sources.
		artifact, err := artifactBuilder.Build(ctx, &oa, localSources, obj.Namespace, tmpDir)
		if err != nil {
			msg := fmt.Sprintf("%s build failed: %s", oa.Name, err.Error())
			gotkconditions.MarkFalse(obj,
				gotkmeta.ReadyCondition,
				gotkmeta.BuildFailedReason,
				"%s", msg)
			r.Event(obj, corev1.EventTypeWarning, gotkmeta.BuildFailedReason, msg)
			return ctrl.Result{}, err
		}

		// Set the revision and origin revision metadata on the artifact.
		r.setArtifactRevisions(artifact, oa, remoteSources)

		// Reconcile the ExternalArtifact corresponding to the built artifact.
		// The ExternalArtifact will reference the artifact stored in the storage backend.
		// If the ExternalArtifact already exists, its status will be updated with the new artifact details.
		eaRef, err := r.reconcileExternalArtifact(ctx, obj, &oa, artifact)
		if err != nil {
			msg := fmt.Sprintf("%s reconcile failed: %s", oa.Name, err.Error())
			gotkconditions.MarkFalse(obj,
				gotkmeta.ReadyCondition,
				gotkmeta.ReconciliationFailedReason,
				"%s", msg)
			r.Event(obj, corev1.EventTypeWarning, gotkmeta.ReconciliationFailedReason, msg)
			return ctrl.Result{}, err
		}
		eaRefs = append(eaRefs, *eaRef)
	}

	// Garbage collect orphaned ExternalArtifacts and their associated artifacts in gotkstroage.
	if orphans := r.findOrphanedReferences(obj.Status.Inventory, eaRefs); len(orphans) > 0 {
		r.finalizeExternalArtifacts(ctx, orphans)
	}

	// Garbage collect old artifacts in storage according to the retention policy.
	for _, eaRef := range eaRefs {
		storagePath := gotkstroage.ArtifactPath(sourcev1.ExternalArtifactKind, eaRef.Namespace, eaRef.Name, "*")
		delFiles, err := r.Storage.GarbageCollect(ctx, gotkmeta.Artifact{Path: storagePath}, 5*time.Minute)
		if err != nil {
			log.Error(err, "failed to garbage collect artifacts", "path", storagePath)
		} else if len(delFiles) > 0 {
			log.Info(fmt.Sprintf("garbage collected %d old artifact(s)", len(delFiles)), "artifacts", delFiles)
		}
	}

	// Update the status with to reflect the successful reconciliation.
	obj.Status.Inventory = eaRefs
	obj.Status.ObservedSourcesDigest = observedSourcesDigest
	msg := fmt.Sprintf("reconciliation succeeded, generated %d artifact(s)", len(eaRefs))
	gotkconditions.MarkTrue(obj,
		gotkmeta.ReadyCondition,
		gotkmeta.SucceededReason,
		"%s", msg)
	r.Event(obj, corev1.EventTypeNormal, gotkmeta.ReadyCondition, msg)

	return ctrl.Result{RequeueAfter: gotkjitter.JitteredIntervalDuration(obj.GetRequeueAfter())}, nil
}

// observeSources retrieves the current state of sources,
// including their artifact URLs, digests, and revisions.
// It returns a map of source alias to observed state.
func (r *ArtifactGeneratorReconciler) observeSources(ctx context.Context,
	obj *swapi.ArtifactGenerator) (map[string]swapi.ObservedSource, error) {
	// Map of source alias to observed state.
	observedSources := make(map[string]swapi.ObservedSource)

	// Get the source objects referenced in the ArtifactGenerator spec.
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
			err := r.Get(ctx, namespacedName, &repository)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, err
				}
				return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &repository
		case sourcev1.GitRepositoryKind:
			var repository sourcev1.GitRepository
			err := r.Get(ctx, namespacedName, &repository)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, err
				}
				return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &repository
		case sourcev1.BucketKind:
			var bucket sourcev1.Bucket
			err := r.Get(ctx, namespacedName, &bucket)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, err
				}
				return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &bucket
		case sourcev1.HelmChartKind:
			var chart sourcev1.HelmChart
			err := r.Get(ctx, namespacedName, &chart)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, err
				}
				return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &chart
		case sourcev1.ExternalArtifactKind:
			var chart sourcev1.ExternalArtifact
			err := r.Get(ctx, namespacedName, &chart)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, err
				}
				return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &chart
		default:
			return nil, fmt.Errorf("source `%s` kind '%s' not supported",
				src.Name, src.Kind)
		}

		artifact := source.GetArtifact()
		if artifact == nil {
			return nil, fmt.Errorf("source '%s/%s' is not ready", src.Kind, namespacedName)
		}

		observedSource := swapi.ObservedSource{
			Digest:   artifact.Digest,
			Revision: artifact.Revision,
			URL:      artifact.URL,
		}

		// Capture the origin revision if present in the artifact metadata.
		if originRev, ok := artifact.Metadata[swapi.ArtifactOriginRevisionAnnotation]; ok {
			observedSource.OriginRevision = originRev
		}

		observedSources[src.Alias] = observedSource
	}

	return observedSources, nil
}

// fetchSources fetches the sources defined in the ArtifactGenerator spec
// into the provided tmpDir under a subdirectory named after the source alias.
// It returns a map of source alias to the absolute path where the source was fetched.
func (r *ArtifactGeneratorReconciler) fetchSources(ctx context.Context,
	sources map[string]swapi.ObservedSource,
	tmpDir string) (map[string]string, error) {
	// Map of source alias to local path.
	dirs := make(map[string]string)

	for alias, src := range sources {
		// Create a dir for the source alias.
		srcDir := filepath.Join(tmpDir, alias)
		if err := os.MkdirAll(srcDir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create source dir: %w", err)
		}

		// Download artifact and extract files to the source alias dir.
		fetcher := gotkfetch.New(
			gotkfetch.WithLogger(ctrl.LoggerFrom(ctx)),
			gotkfetch.WithRetries(r.ArtifactFetchRetries),
			gotkfetch.WithMaxDownloadSize(gotktar.UnlimitedUntarSize),
			gotkfetch.WithUntar(gotktar.WithMaxUntarSize(gotktar.UnlimitedUntarSize)),
			gotkfetch.WithHostnameOverwrite(os.Getenv("SOURCE_CONTROLLER_LOCALHOST")),
		)
		if err := fetcher.FetchWithContext(ctx, src.URL, src.Digest, srcDir); err != nil {
			return nil, err
		}
		dirs[alias] = srcDir
	}

	return dirs, nil
}

// reconcileExternalArtifact ensures the ExternalArtifact object
// exists and is up to date with the provided artifact details.
// It returns a reference to the ExternalArtifact.
func (r *ArtifactGeneratorReconciler) reconcileExternalArtifact(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	outputArtifact *swapi.OutputArtifact,
	artifact *gotkmeta.Artifact) (*swapi.ExternalArtifactReference, error) {
	log := ctrl.LoggerFrom(ctx)

	// Prepare labels for the ExternalArtifact with the managed-by and generator labels.
	labels := make(map[string]string)
	labels["app.kubernetes.io/managed-by"] = r.ControllerName
	labels[swapi.ArtifactGeneratorLabel] = string(obj.GetUID())

	// Create the ExternalArtifact object.
	ea := &sourcev1.ExternalArtifact{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sourcev1.GroupVersion.String(),
			Kind:       sourcev1.ExternalArtifactKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      outputArtifact.Name,
			Namespace: obj.Namespace,
			Labels:    labels,
		},
		Spec: sourcev1.ExternalArtifactSpec{
			SourceRef: &gotkmeta.NamespacedObjectKindReference{
				APIVersion: swapi.GroupVersion.String(),
				Kind:       swapi.ArtifactGeneratorKind,
				Name:       obj.Name,
				Namespace:  obj.Namespace,
			},
		},
	}

	// Apply the ExternalArtifact object.
	forceApply := true
	if err := r.Patch(ctx, ea, client.Apply, &client.PatchOptions{
		FieldManager: r.ControllerName,
		Force:        &forceApply,
	}); err != nil {
		return nil, fmt.Errorf("failed to apply ExternalArtifact: %w", err)
	}

	// Update the status of the ExternalArtifact with the artifact details.
	ea.ManagedFields = nil
	ea.Status = sourcev1.ExternalArtifactStatus{
		Artifact: artifact,
		Conditions: []metav1.Condition{
			{
				ObservedGeneration: ea.GetGeneration(),
				Type:               gotkmeta.ReadyCondition,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             gotkmeta.SucceededReason,
				Message:            "Artifact is ready",
			},
		},
	}
	statusOpts := &client.SubResourcePatchOptions{
		PatchOptions: client.PatchOptions{
			FieldManager: r.ControllerName,
		},
	}
	if err := r.Status().Patch(ctx, ea, client.Apply, statusOpts); err != nil {
		return nil, fmt.Errorf("failed to patchStatus ExternalArtifact status: %w", err)
	}

	// Log if the artifact is up to date or emit an event if it is new or has changed.
	if obj.HasArtifactInInventory(ea.Name, ea.Namespace, artifact.Digest) {
		log.Info(fmt.Sprintf("%s/%s/%s is up to date",
			ea.Kind, ea.Namespace, ea.Name))
	} else {
		msg := fmt.Sprintf("%s/%s/%s reconciled with revision %s",
			ea.Kind, ea.Namespace, ea.Name, artifact.Revision)
		log.Info(msg)
		r.Event(obj, corev1.EventTypeNormal, gotkmeta.ReadyCondition, msg)
	}

	return &swapi.ExternalArtifactReference{
		Name:      ea.Name,
		Namespace: ea.Namespace,
		Digest:    artifact.Digest,
		Filename:  filepath.Base(artifact.Path),
	}, nil
}

// findOrphanedReferences identifies ExternalArtifact references
// in the inventory that are not present in the current references,
// indicating they should be garbage collected.
func (r *ArtifactGeneratorReconciler) findOrphanedReferences(
	inventory []swapi.ExternalArtifactReference,
	currentRefs []swapi.ExternalArtifactReference) []swapi.ExternalArtifactReference {
	// Create map of current references for O(1) lookup
	currentSet := make(map[string]struct{})
	for _, ref := range currentRefs {
		key := fmt.Sprintf("%s/%s/%s", sourcev1.ExternalArtifactKind, ref.Namespace, ref.Name)
		currentSet[key] = struct{}{}
	}

	// Find inventory items not in current set
	var orphaned []swapi.ExternalArtifactReference
	for _, ref := range inventory {
		key := fmt.Sprintf("%s/%s/%s", sourcev1.ExternalArtifactKind, ref.Namespace, ref.Name)
		if _, exists := currentSet[key]; !exists {
			orphaned = append(orphaned, ref)
		}
	}

	return orphaned
}

// setArtifactRevisions sets the revision and origin revision
// metadata on the artifact based on the output artifact
// configuration and available remote sources.
func (r *ArtifactGeneratorReconciler) setArtifactRevisions(artifact *gotkmeta.Artifact,
	oa swapi.OutputArtifact,
	remoteSources map[string]swapi.ObservedSource) {
	// Override the revision with the one from the source if specified.
	if oa.Revision != "" {
		if rs, ok := remoteSources[strings.TrimPrefix(oa.Revision, "@")]; ok {
			artifact.Revision = rs.Revision
		}
	}

	// Set the origin revision in the artifact metadata if available from the source.
	if oa.OriginRevision != "" {
		if artifact.Metadata == nil {
			artifact.Metadata = make(map[string]string)
		}
		if rs, ok := remoteSources[strings.TrimPrefix(oa.OriginRevision, "@")]; ok {
			if rs.OriginRevision != "" {
				artifact.Metadata[swapi.ArtifactOriginRevisionAnnotation] = rs.OriginRevision
			} else {
				artifact.Metadata[swapi.ArtifactOriginRevisionAnnotation] = rs.Revision
			}
		}
	}
}
