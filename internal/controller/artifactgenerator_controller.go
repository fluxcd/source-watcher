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
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	eventv1 "github.com/fluxcd/pkg/apis/event/v1"
	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkstroage "github.com/fluxcd/pkg/artifact/storage"
	gotkfetch "github.com/fluxcd/pkg/http/fetch"
	gotkclient "github.com/fluxcd/pkg/runtime/client"
	gotkconditions "github.com/fluxcd/pkg/runtime/conditions"
	gotkevents "github.com/fluxcd/pkg/runtime/events"
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
	gotkevents.Recorder

	ControllerName            string
	Scheme                    *runtime.Scheme
	Storage                   *gotkstroage.Storage
	APIReader                 client.Reader
	ArtifactFetchRetries      int
	DependencyRequeueInterval time.Duration
	NoCrossNamespaceRefs      bool
	DirectSourceFetch         bool
	DefaultServiceAccount     string
	DisallowedFieldManagers   []string
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

	// Finalize the reconciliation and release resources if the object is being
	// deleted. Finalization builds its own impersonation client, so a missing
	// ServiceAccount does not block the deletion of the ArtifactGenerator.
	if !obj.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, obj)
	}

	// Build the impersonation client only when there is work that requires it:
	// an explicit .spec.serviceAccountName, an ExternalArtifact targeting a
	// namespace other than the ArtifactGenerator namespace, or a managed
	// namespace tracked in the inventory.
	var impersonated client.Client
	if r.needsImpersonation(obj) {
		var err error
		impersonated, err = r.newImpersonatedClient(ctx, obj)
		if err != nil {
			err = fmt.Errorf("failed to build the impersonation client: %w", err)
			gotkconditions.MarkFalse(obj,
				gotkmeta.ReadyCondition,
				swapi.AccessDeniedReason,
				"%s", err.Error())
			r.Eventf(obj, nil, corev1.EventTypeWarning, swapi.AccessDeniedReason, swapi.ActionPublish.String(), "%s", err.Error())
			return ctrl.Result{}, err
		}
	}

	// Namespace management is always performed with an impersonated
	// ServiceAccount and never with the controller client, so the
	// ArtifactGenerator must configure one.
	if obj.ManagesNamespaces() && obj.Spec.ServiceAccountName == "" && r.DefaultServiceAccount == "" {
		return ctrl.Result{}, r.newTerminalErrorFor(obj,
			swapi.AccessDeniedReason,
			"namespace management requires .spec.serviceAccountName or the --default-service-account flag to be set")
	}

	// Add the finalizer if it does not exist.
	if !controllerutil.ContainsFinalizer(obj, swapi.Finalizer) {
		log.Info("Adding finalizer", "finalizer", swapi.Finalizer)
		return r.addFinalizer(obj)
	}

	// Pause reconciliation if the object has the reconcile annotation set to 'disabled'.
	if obj.IsDisabled() {
		log.Error(errors.New("can't reconcile"), msgReconciliationDisabled)
		r.Eventf(obj, nil, eventv1.EventTypeTrace, swapi.ReconciliationDisabledReason, swapi.ActionReconcile.String(), "%s", msgReconciliationDisabled)
		return ctrl.Result{}, nil
	}

	// Compile the namespace CEL expressions once per reconciliation.
	// Each expression is compiled at most once.
	policies, err := r.compileNamespacePolicies(obj)
	if err != nil {
		return ctrl.Result{}, r.newTerminalErrorFor(obj, swapi.ValidationFailedReason, "%s", err.Error())
	}

	// Validate the ArtifactGenerator spec and mark the object as Stalled if invalid.
	if err := r.validateSpec(obj); err != nil {
		return ctrl.Result{}, err
	}

	// Run drift detection and reconciliation.
	return r.reconcile(ctx, obj, patcher, impersonated, policies)
}

// needsImpersonation returns true when the controller must impersonate a
// ServiceAccount to reconcile the ArtifactGenerator. An explicit
// .spec.serviceAccountName is authoritative for every generated
// ExternalArtifact, including those in the ArtifactGenerator namespace, so it
// always requires impersonation. Namespace management always uses an
// impersonated ServiceAccount, so it requires impersonation as soon as it is
// enabled. The --default-service-account flag is otherwise only used for
// ExternalArtifacts targeting another namespace, so it requires impersonation
// only when there is such work.
func (r *ArtifactGeneratorReconciler) needsImpersonation(obj *swapi.ArtifactGenerator) bool {
	if r.DefaultServiceAccount == "" && obj.Spec.ServiceAccountName == "" {
		return false
	}

	// An explicit ServiceAccount applies to every generated ExternalArtifact,
	// including those in the ArtifactGenerator namespace.
	if obj.Spec.ServiceAccountName != "" {
		return true
	}

	// Namespace management always uses an impersonated ServiceAccount, never
	// the controller credentials, even when there is no managed namespace yet.
	if obj.ManagesNamespaces() {
		return true
	}

	for i := range obj.Spec.OutputArtifacts {
		if obj.GetArtifactNamespace(&obj.Spec.OutputArtifacts[i]) != obj.Namespace {
			return true
		}
	}

	for _, ref := range obj.Status.Inventory {
		if ref.Kind == swapi.NamespaceKind {
			continue
		}
		if ref.Namespace != obj.Namespace {
			return true
		}
	}

	return false
}

// newImpersonatedClient returns a client that impersonates the ServiceAccount
// configured on the ArtifactGenerator, or the one from the
// --default-service-account flag when the object does not specify one.
func (r *ArtifactGeneratorReconciler) newImpersonatedClient(ctx context.Context,
	obj *swapi.ArtifactGenerator) (client.Client, error) {
	impersonator := gotkclient.NewImpersonator(r.Client,
		gotkclient.WithScheme(r.Scheme),
		gotkclient.WithServiceAccount(r.DefaultServiceAccount, obj.Spec.ServiceAccountName, obj.Namespace))
	kubeClient, _, err := impersonator.GetClient(ctx)
	if err != nil {
		return nil, err
	}
	return kubeClient, nil
}

// canImpersonate returns true when the ServiceAccount configured on the
// ArtifactGenerator, or the one from the --default-service-account flag, exists
// in the cluster. It is used during finalization to detect a ServiceAccount
// that was deleted before the ArtifactGenerator, so that the finalization can
// proceed instead of being blocked indefinitely.
func (r *ArtifactGeneratorReconciler) canImpersonate(ctx context.Context,
	obj *swapi.ArtifactGenerator) bool {
	impersonator := gotkclient.NewImpersonator(r.Client,
		gotkclient.WithScheme(r.Scheme),
		gotkclient.WithServiceAccount(r.DefaultServiceAccount, obj.Spec.ServiceAccountName, obj.Namespace))
	return impersonator.CanImpersonate(ctx)
}

// clientForNamespace returns the client that must be used to reconcile
// ExternalArtifacts in the given namespace. The impersonated client is used
// when an explicit .spec.serviceAccountName is set, or when the target
// namespace differs from the ArtifactGenerator namespace; otherwise the
// controller client is returned.
func (r *ArtifactGeneratorReconciler) clientForNamespace(obj *swapi.ArtifactGenerator,
	impersonated client.Client, namespace string) client.Client {
	if impersonated != nil && (obj.Spec.ServiceAccountName != "" || namespace != obj.Namespace) {
		return impersonated
	}
	return r.Client
}

// reconcile contains the main reconciliation logic for the ArtifactGenerator.
func (r *ArtifactGeneratorReconciler) reconcile(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	patcher *gotkpatch.SerialPatcher,
	impersonated client.Client,
	policies *namespacePolicies) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	oldObj := obj.DeepCopy()

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
		r.Eventf(obj, nil, corev1.EventTypeWarning, swapi.SourceFetchFailedReason, swapi.ActionResolveSource.String(), "%s", msg)
		log.Error(err, "failed to get sources, retrying")
		return ctrl.Result{RequeueAfter: r.DependencyRequeueInterval}, nil
	}

	// Calculate the hash of the observed sources.
	observedSourcesDigest := swapi.HashObservedSources(remoteSources)

	// Detect drift between the actual state and the desired state.
	// If no drift is detected in sources and the stored artifacts pass the
	// integrity verification, the reconciliation is complete and we can exit early.
	hasDrifted, reason := r.detectDrift(ctx, obj, observedSourcesDigest, impersonated)
	if !hasDrifted {
		artifactCount, namespaceCount := 0, 0
		for _, ref := range obj.Status.Inventory {
			if ref.Kind == swapi.NamespaceKind {
				namespaceCount++
			} else {
				artifactCount++
			}
		}
		msg := fmt.Sprintf("No drift detected, %d artifact(s) up to date", artifactCount)
		if namespaceCount > 0 {
			msg = fmt.Sprintf("%s and %d namespace(s) up to date", msg, namespaceCount)
		}
		log.Info(msg)
		r.Eventf(obj, nil, eventv1.EventTypeTrace, gotkmeta.ReadyCondition, swapi.ActionReconcile.String(), "%s", msg)
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
		r.Eventf(obj, nil, corev1.EventTypeWarning, swapi.SourceFetchFailedReason, swapi.ActionFetchSource.String(), "%s", msg)
		log.Error(err, "failed to fetch sources, retrying")
		return ctrl.Result{RequeueAfter: r.DependencyRequeueInterval}, nil
	}

	// Build the artifacts and reconcile the ExternalArtifact objects.
	// The artifacts will be stored in the storage under the following path:
	// <storage-root>/<kind>/<namespace>/<name>/<contents-hash>.tar.gz
	artifactBuilder := builder.New(r.Storage)

	reqs, err := buildArtifactRequests(obj, localSources)
	if err != nil {
		msg := fmt.Sprintf("failed to expand path pattern: %s", err.Error())
		if isTerminalPathPatternError(err) {
			return ctrl.Result{}, r.newTerminalErrorFor(obj,
				gotkmeta.BuildFailedReason,
				"%s", msg)
		}
		gotkconditions.Delete(obj, gotkmeta.StalledCondition)
		gotkconditions.MarkFalse(obj,
			gotkmeta.ReadyCondition,
			gotkmeta.BuildFailedReason,
			"%s", msg)
		r.Eventf(obj, nil, corev1.EventTypeWarning, gotkmeta.BuildFailedReason, swapi.ActionBuild.String(), "%s", msg)
		return ctrl.Result{}, err
	}

	// Build all artifacts first. This stages the files, stores the artifacts
	// and extracts the exported inputs, but does not create the
	// ExternalArtifact objects yet: namespaces must exist before the
	// ExternalArtifacts that target them are created, and the namespace
	// metadata may be sourced from the built artifacts.
	type builtArtifact struct {
		req            artifactRequest
		artifact       *gotkmeta.Artifact
		exportedInputs map[string]*apiextensionsv1.JSON
	}
	built := make([]builtArtifact, 0, len(reqs))
	for i := range reqs {
		req := reqs[i]
		oa := req.OutputArtifact
		namespace := obj.GetArtifactNamespace(&oa)

		// Build the artifact using the local sources.
		artifact, err := artifactBuilder.Build(ctx, &oa, localSources, namespace, tmpDir)
		if err != nil {
			msg := fmt.Sprintf("%s build failed: %s", oa.Name, err.Error())
			gotkconditions.MarkFalse(obj,
				gotkmeta.ReadyCondition,
				gotkmeta.BuildFailedReason,
				"%s", msg)
			r.Eventf(obj, nil, corev1.EventTypeWarning, gotkmeta.BuildFailedReason, swapi.ActionBuild.String(), "%s", msg)
			return ctrl.Result{}, err
		}

		// Set the revision and origin revision metadata on the artifact.
		r.setArtifactRevisions(artifact, oa, remoteSources)

		// Load and parse the exported inputs. The staging directory is
		// namespaced to match the artifact builder.
		artifactDir := filepath.Join(tmpDir, namespace, oa.Name)
		exportedInputs, err := r.loadExportedInputs(ctx, &oa, localSources, artifactDir)
		if err != nil {
			msg := fmt.Sprintf("%s inputs failed: %s", oa.Name, err.Error())
			gotkconditions.MarkFalse(obj,
				gotkmeta.ReadyCondition,
				gotkmeta.BuildFailedReason,
				"%s", msg)
			r.Eventf(obj, nil, corev1.EventTypeWarning, gotkmeta.BuildFailedReason, swapi.ActionBuild.String(), "%s", msg)
			return ctrl.Result{}, err
		}

		built = append(built, builtArtifact{
			req:            req,
			artifact:       artifact,
			exportedInputs: exportedInputs,
		})
	}

	// Reconcile the namespaces managed by the ArtifactGenerator before the
	// ExternalArtifacts that target them. Only namespaces explicitly targeted
	// by .spec.artifacts[].namespace are managed, the ArtifactGenerator
	// namespace is never managed implicitly through the default.
	var nsRefs []swapi.InventoryEntry
	adoptedNS, conflictNS := 0, 0
	if obj.ManagesNamespaces() {
		// Enforce the namespace management policy before touching the
		// cluster, so that a disallowed target fails with an actionable
		// error instead of a cryptic apply error.
		if err := r.validateManagedNamespaces(ctx, obj, reqs, policies); err != nil {
			return ctrl.Result{}, r.newTerminalErrorFor(obj, swapi.ValidationFailedReason, "%s", err.Error())
		}

		var nsConflicts []namespaceConflict
		nsRefs, nsConflicts, err = r.reconcileNamespaces(ctx, obj, reqs, localSources, tmpDir, impersonated, policies)
		if err != nil {
			msg := fmt.Sprintf("namespace reconciliation failed: %s", err.Error())
			// Namespace metadata errors can only be resolved by a change to
			// the spec or the source content, both of which trigger a
			// reconcile through a watch event, so they are terminal.
			if isTerminalNamespaceMetadataError(err) {
				return ctrl.Result{}, r.newTerminalErrorFor(obj, gotkmeta.ReconciliationFailedReason, "%s", msg)
			}
			gotkconditions.MarkFalse(obj,
				gotkmeta.ReadyCondition,
				gotkmeta.ReconciliationFailedReason,
				"%s", msg)
			r.Eventf(obj, nil, corev1.EventTypeWarning, gotkmeta.ReconciliationFailedReason, swapi.ActionPublish.String(), "%s", msg)
			return ctrl.Result{}, err
		}
		// Summarize the adopted and taken over namespaces once per
		// reconciliation, mirroring the ExternalArtifact ownership conflicts.
		if len(nsConflicts) > 0 {
			for _, c := range nsConflicts {
				if c.Adopted {
					adoptedNS++
				} else {
					conflictNS++
				}
			}
			if adoptedNS > 0 {
				log.Error(fmt.Errorf("adopted %d unmanaged namespace(s)", adoptedNS),
					"adopting namespaces not managed by any ArtifactGenerator")
			}
			if conflictNS > 0 {
				log.Error(fmt.Errorf("ownership conflict detected for %d Namespace(s)", conflictNS),
					"taking over namespaces from other ArtifactGenerators")
			}
		}
	}

	// Prepare a slice to hold the references to the created ExternalArtifact objects.
	eaRefs := make([]swapi.InventoryEntry, 0, len(built))

	// Collect ExternalArtifacts whose ownership is being taken over from
	// another ArtifactGenerator to emit a single error log and a single
	// summary event for this reconciliation.
	var ownershipConflicts []ownershipConflict

	for _, b := range built {
		oa := b.req.OutputArtifact
		// Reconcile the ExternalArtifact corresponding to the built artifact.
		// The ExternalArtifact will reference the artifact stored in the storage backend.
		// If the ExternalArtifact already exists, its status will be updated with the new artifact details.
		eaRef, conflict, err := r.reconcileExternalArtifact(ctx, obj, &oa, b.artifact, b.req.Labels, b.exportedInputs, impersonated)
		if err != nil {
			msg := fmt.Sprintf("%s reconcile failed: %s", oa.Name, err.Error())
			gotkconditions.MarkFalse(obj,
				gotkmeta.ReadyCondition,
				gotkmeta.ReconciliationFailedReason,
				"%s", msg)
			r.Eventf(obj, nil, corev1.EventTypeWarning, gotkmeta.ReconciliationFailedReason, swapi.ActionPublish.String(), "%s", msg)
			return ctrl.Result{}, err
		}
		if conflict != nil {
			ownershipConflicts = append(ownershipConflicts, *conflict)
		}
		eaRefs = append(eaRefs, *eaRef)
	}

	// Count the adopted and taken over ExternalArtifacts once per
	// reconciliation, with the exact references logged.
	adoptedEA, conflictEA := 0, 0
	for _, c := range ownershipConflicts {
		if c.Adopted {
			adoptedEA++
		} else {
			conflictEA++
		}
	}
	if adoptedEA > 0 {
		log.Error(fmt.Errorf("adopted %d unmanaged ExternalArtifact(s)", adoptedEA),
			"adopting ExternalArtifacts not managed by any ArtifactGenerator")
	}
	if conflictEA > 0 {
		log.Error(fmt.Errorf("ownership conflict detected for %d ExternalArtifact(s)", conflictEA),
			"taking over ExternalArtifacts from other ArtifactGenerators",
			"artifacts", ownershipConflicts)
	}

	// Emit a single warning event on the ArtifactGenerator summarizing the
	// objects adopted or taken over. The message only carries the amounts, so
	// it does not grow with the number of objects.
	if adoptedNS > 0 || conflictNS > 0 || adoptedEA > 0 || conflictEA > 0 {
		reason := swapi.AdoptedReason
		parts := make([]string, 0, 2)
		if adoptedNS > 0 || adoptedEA > 0 {
			adopted := make([]string, 0, 2)
			if adoptedNS > 0 {
				adopted = append(adopted, fmt.Sprintf("%d Namespace(s)", adoptedNS))
			}
			if adoptedEA > 0 {
				adopted = append(adopted, fmt.Sprintf("%d ExternalArtifact(s)", adoptedEA))
			}
			parts = append(parts, fmt.Sprintf("adopted unmanaged %s", strings.Join(adopted, " and ")))
		}
		if conflictNS > 0 || conflictEA > 0 {
			reason = swapi.OwnershipConflictReason
			conflicts := make([]string, 0, 2)
			if conflictNS > 0 {
				conflicts = append(conflicts, fmt.Sprintf("%d Namespace(s)", conflictNS))
			}
			if conflictEA > 0 {
				conflicts = append(conflicts, fmt.Sprintf("%d ExternalArtifact(s)", conflictEA))
			}
			parts = append(parts, fmt.Sprintf("ownership conflict detected for %s", strings.Join(conflicts, " and ")))
		}
		r.Eventf(obj, nil, corev1.EventTypeWarning, reason, swapi.ActionPublish.String(), "%s", strings.Join(parts, ", "))
	}

	// Merge the artifact and namespace references into the inventory.
	inventory := append(eaRefs, nsRefs...)

	// Garbage collect orphaned ExternalArtifacts and managed namespaces.
	// Objects whose deletion was not confirmed are kept in the inventory so
	// the next reconciliation retries their deletion.
	if orphans := r.findOrphanedReferences(obj.Status.Inventory, inventory); len(orphans) > 0 {
		survivors := r.finalizeReferences(ctx, obj, orphans, impersonated)
		if len(survivors) > 0 {
			inventory = append(inventory, survivors...)
			obj.Status.Inventory = inventory
			msg := fmt.Sprintf("failed to prune %d object(s), retrying", len(survivors))
			gotkconditions.MarkFalse(obj, gotkmeta.ReadyCondition, gotkmeta.PruneFailedReason, "%s", msg)
			r.Eventf(obj, nil, corev1.EventTypeWarning, gotkmeta.PruneFailedReason, swapi.ActionReconcile.String(), "%s", msg)
			return ctrl.Result{}, fmt.Errorf("failed to prune %d object(s)", len(survivors))
		}
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
	obj.Status.Inventory = inventory
	obj.Status.ObservedSourcesDigest = observedSourcesDigest
	msg := fmt.Sprintf("reconciliation succeeded, generated %d artifact(s)", len(eaRefs))
	if len(nsRefs) > 0 {
		msg = fmt.Sprintf("%s and manages %d namespace(s)", msg, len(nsRefs))
	}
	gotkconditions.MarkTrue(obj,
		gotkmeta.ReadyCondition,
		gotkmeta.SucceededReason,
		"%s", msg)
	r.Eventf(obj, nil, eventv1.EventTypeTrace, gotkmeta.ReadyCondition, swapi.ActionReconcile.String(), "%s", msg)

	r.notify(oldObj, obj, inventory)

	return ctrl.Result{RequeueAfter: gotkjitter.JitteredIntervalDuration(obj.GetRequeueAfter())}, nil
}

// notify emits a single notification event when at least one external artifact
// or namespace was updated. The message only carries the number of updated
// objects, so that its size does not grow with the number of namespaces or
// artifacts.
func (r *ArtifactGeneratorReconciler) notify(oldObj, newObj *swapi.ArtifactGenerator, refs []swapi.InventoryEntry) {
	eaChanged, nsChanged := 0, 0

	for _, ref := range refs {
		switch ref.Kind {
		case swapi.NamespaceKind:
			if !oldObj.HasNamespaceInInventory(ref.Name, ref.Digest) {
				nsChanged++
			}
		default:
			if !oldObj.HasArtifactInInventory(ref.Name, ref.Namespace, ref.Digest) {
				eaChanged++
			}
		}
	}

	if eaChanged == 0 && nsChanged == 0 {
		return
	}

	parts := make([]string, 0, 2)
	if eaChanged > 0 {
		parts = append(parts, fmt.Sprintf("external artifacts reconciled: %d", eaChanged))
	}
	if nsChanged > 0 {
		parts = append(parts, fmt.Sprintf("namespaces reconciled: %d", nsChanged))
	}
	r.Eventf(newObj, nil, corev1.EventTypeNormal, gotkmeta.ReadyCondition, swapi.ActionPublish.String(),
		"%s", strings.Join(parts, ", "))
}

// observeSources retrieves the current state of sources,
// including their artifact URLs, digests, and revisions.
// It returns a map of source alias to observed state.
func (r *ArtifactGeneratorReconciler) observeSources(ctx context.Context,
	obj *swapi.ArtifactGenerator) (map[string]swapi.ObservedSource, error) {
	// Map of source alias to observed state.
	observedSources := make(map[string]swapi.ObservedSource)

	// Use APIReader to bypass the cache when DirectSourceFetch is enabled.
	var reader client.Reader = r.Client
	if r.DirectSourceFetch {
		reader = r.APIReader
	}

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
			err := reader.Get(ctx, namespacedName, &repository)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, err
				}
				return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &repository
		case sourcev1.GitRepositoryKind:
			var repository sourcev1.GitRepository
			err := reader.Get(ctx, namespacedName, &repository)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, err
				}
				return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &repository
		case sourcev1.BucketKind:
			var bucket sourcev1.Bucket
			err := reader.Get(ctx, namespacedName, &bucket)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, err
				}
				return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &bucket
		case sourcev1.HelmChartKind:
			var chart sourcev1.HelmChart
			err := reader.Get(ctx, namespacedName, &chart)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, err
				}
				return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &chart
		case sourcev1.ExternalArtifactKind:
			var ea sourcev1.ExternalArtifact
			err := reader.Get(ctx, namespacedName, &ea)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, err
				}
				return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
			}
			source = &ea
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
// It returns a reference to the ExternalArtifact and, when the ExternalArtifact
// was taken over from another ArtifactGenerator, the detected conflict.
func (r *ArtifactGeneratorReconciler) reconcileExternalArtifact(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	outputArtifact *swapi.OutputArtifact,
	artifact *gotkmeta.Artifact,
	dynamicLabels map[string]string,
	exportedInputs map[string]*apiextensionsv1.JSON,
	impersonated client.Client) (*swapi.InventoryEntry, *ownershipConflict, error) {
	log := ctrl.LoggerFrom(ctx)

	// Select the client.
	kubeClient := r.clientForNamespace(obj, impersonated, obj.GetArtifactNamespace(outputArtifact))

	// Prepare labels for the ExternalArtifact with the managed-by and generator labels.
	labels := make(map[string]string)
	var annotations map[string]string

	if cm := obj.Spec.CommonMetadata; cm != nil {
		maps.Copy(labels, cm.Labels)
		if len(cm.Annotations) > 0 {
			annotations = make(map[string]string)
			maps.Copy(annotations, cm.Annotations)
		}
	}

	maps.Copy(labels, dynamicLabels)

	labels["app.kubernetes.io/managed-by"] = r.ControllerName
	labels[swapi.ArtifactGeneratorLabel] = string(obj.GetUID())

	// Create the ExternalArtifact object.
	ea := &sourcev1.ExternalArtifact{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sourcev1.GroupVersion.String(),
			Kind:       sourcev1.ExternalArtifactKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        outputArtifact.Name,
			Namespace:   obj.GetArtifactNamespace(outputArtifact),
			Labels:      labels,
			Annotations: annotations,
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

	// Detect ownership conflicts before applying the ExternalArtifact.
	conflict, err := r.detectOwnershipConflict(ctx, obj, kubeClient, ea)
	if err != nil {
		return nil, nil, err
	}

	// Apply the ExternalArtifact object.
	forceApply := true
	if err := kubeClient.Patch(ctx, ea, client.Apply, &client.PatchOptions{
		FieldManager: r.ControllerName,
		Force:        &forceApply,
	}); err != nil {
		return nil, nil, fmt.Errorf("failed to apply ExternalArtifact: %w", err)
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
		ExportedInputs: exportedInputs,
	}
	statusOpts := &client.SubResourcePatchOptions{
		PatchOptions: client.PatchOptions{
			FieldManager: r.ControllerName,
		},
	}
	if err := kubeClient.Status().Patch(ctx, ea, client.Apply, statusOpts); err != nil {
		return nil, nil, fmt.Errorf("failed to patchStatus ExternalArtifact status: %w", err)
	}

	// Log if the artifact is up to date or has changed. The per-artifact event
	// is emitted on the ExternalArtifact itself; the ArtifactGenerator only
	// carries the aggregate count.
	if obj.HasArtifactInInventory(ea.Name, ea.Namespace, artifact.Digest) {
		log.Info(fmt.Sprintf("%s/%s/%s is up to date",
			ea.Kind, ea.Namespace, ea.Name))
	} else {
		msg := fmt.Sprintf("%s/%s/%s reconciled with revision %s",
			ea.Kind, ea.Namespace, ea.Name, artifact.Revision)
		log.Info(msg)
		r.Eventf(ea, nil, eventv1.EventTypeTrace, gotkmeta.ReadyCondition, swapi.ActionPublish.String(), "%s", msg)
	}

	return &swapi.InventoryEntry{
		Kind:      sourcev1.ExternalArtifactKind,
		Name:      ea.Name,
		Namespace: ea.Namespace,
		Digest:    artifact.Digest,
		Filename:  filepath.Base(artifact.Path),
	}, conflict, nil
}

// ownershipConflict describes an ExternalArtifact that is being adopted (no
// previous owner) or taken over from another ArtifactGenerator.
type ownershipConflict struct {
	// ExternalArtifact is the namespace/name of the ExternalArtifact.
	ExternalArtifact string `json:"externalArtifact"`
	// ArtifactGenerator identifies the previous owner, as namespace/name when
	// known, otherwise the generator label value. Empty for adopted artifacts.
	ArtifactGenerator string `json:"artifactGenerator,omitempty"`
	// Adopted is true when the ExternalArtifact had no owner.
	Adopted bool `json:"adopted"`
}

// detectOwnershipConflict checks whether the ExternalArtifact is currently
// unmanaged or owned by a different ArtifactGenerator. An existing
// ExternalArtifact without a generator label is about to be adopted, and one
// owned by another ArtifactGenerator is about to be taken over: a warning event
// is emitted on the ExternalArtifact in both cases, so that accidental overlaps
// are visible to users. The ownership transfer is allowed to proceed to support
// moving ExternalArtifacts from one ArtifactGenerator to another. The caller
// aggregates the returned changes into a single log line and a single
// ArtifactGenerator event.
func (r *ArtifactGeneratorReconciler) detectOwnershipConflict(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	kubeClient client.Client,
	ea *sourcev1.ExternalArtifact) (*ownershipConflict, error) {
	existing := &sourcev1.ExternalArtifact{}
	err := kubeClient.Get(ctx, client.ObjectKeyFromObject(ea), existing)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get ExternalArtifact: %w", err)
	}

	owner := existing.Labels[swapi.ArtifactGeneratorLabel]
	if owner == string(obj.GetUID()) {
		return nil, nil
	}

	change := &ownershipConflict{
		ExternalArtifact: fmt.Sprintf("%s/%s", existing.Namespace, existing.Name),
	}
	if owner == "" {
		change.Adopted = true
		msg := fmt.Sprintf("ExternalArtifact %s is not managed by any ArtifactGenerator and is being adopted by %s/%s",
			change.ExternalArtifact, obj.Namespace, obj.Name)
		r.Eventf(existing, nil, corev1.EventTypeWarning, swapi.AdoptedReason, swapi.ActionPublish.String(), "%s", msg)
		return change, nil
	}

	previous := owner
	if ref := existing.Spec.SourceRef; ref != nil {
		previous = fmt.Sprintf("%s/%s", ref.Namespace, ref.Name)
	}
	change.ArtifactGenerator = previous
	msg := fmt.Sprintf("ExternalArtifact %s is owned by ArtifactGenerator %s and is being taken over by %s/%s",
		change.ExternalArtifact, change.ArtifactGenerator, obj.Namespace, obj.Name)
	r.Eventf(existing, nil, corev1.EventTypeWarning, swapi.OwnershipConflictReason, swapi.ActionPublish.String(), "%s", msg)

	return change, nil
}

// findOrphanedReferences identifies references in the inventory that are not
// present in the current references, indicating they should be garbage
// collected.
func (r *ArtifactGeneratorReconciler) findOrphanedReferences(
	inventory []swapi.InventoryEntry,
	currentRefs []swapi.InventoryEntry) []swapi.InventoryEntry {
	// Create map of current references for O(1) lookup
	currentSet := make(map[string]struct{})
	for _, ref := range currentRefs {
		currentSet[inventoryKey(ref)] = struct{}{}
	}

	// Find inventory items not in current set
	var orphaned []swapi.InventoryEntry
	for _, ref := range inventory {
		if _, exists := currentSet[inventoryKey(ref)]; !exists {
			orphaned = append(orphaned, ref)
		}
	}

	return orphaned
}

// inventoryKey returns a unique key for an inventory entry. Entries written
// before the type and kind fields were introduced are treated as
// ExternalArtifacts.
func inventoryKey(ref swapi.InventoryEntry) string {
	kind := ref.Kind
	if kind == "" {
		kind = sourcev1.ExternalArtifactKind
	}
	return fmt.Sprintf("%s/%s/%s", kind, ref.Namespace, ref.Name)
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
