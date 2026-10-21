/*
Copyright 2026 The Flux authors

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
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	eventv1 "github.com/fluxcd/pkg/apis/event/v1"
	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/cel"
	"github.com/fluxcd/pkg/ssa"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

// deniedNamespaces are namespaces the controller never manages, regardless of
// the expressions in .spec.managedNamespaces.allowedNames.
var deniedNamespaces = map[string]struct{}{
	"default":     {},
	"kube-system": {},
	"kube-public": {},
	"flux-system": {},
}

// namespacePolicies holds the CEL expressions compiled once per reconciliation
// that constrain the namespaces an ArtifactGenerator may manage and the
// metadata it may apply to them.
type namespacePolicies struct {
	allowedNamespaces  []*cel.Expression
	allowedLabels      map[string]*cel.Expression
	allowedAnnotations map[string]*cel.Expression
}

// terminalNamespaceMetadataError marks an error that can only be resolved by a
// change to the ArtifactGenerator spec or to the source content. Both changes
// trigger a reconcile through a watch event (the generation predicate or the
// source revision predicate), so the error is terminal. Local read failures
// that can resolve without a watch event are returned as-is and stay retryable.
type terminalNamespaceMetadataError struct{ err error }

// Error implements the error interface.
func (e *terminalNamespaceMetadataError) Error() string { return e.err.Error() }

// Unwrap returns the wrapped error.
func (e *terminalNamespaceMetadataError) Unwrap() error { return e.err }

// newTerminalNamespaceMetadataError returns a terminal namespace metadata error.
func newTerminalNamespaceMetadataError(format string, args ...any) error {
	return &terminalNamespaceMetadataError{err: fmt.Errorf(format, args...)}
}

// isTerminalNamespaceMetadataError returns true when the error is a terminal
// namespace metadata error.
func isTerminalNamespaceMetadataError(err error) bool {
	var terminalErr *terminalNamespaceMetadataError
	return errors.As(err, &terminalErr)
}

// compileNamespacePolicies compiles the CEL expressions declared in
// .spec.managedNamespaces once per reconciliation.
func (r *ArtifactGeneratorReconciler) compileNamespacePolicies(obj *swapi.ArtifactGenerator) (*namespacePolicies, error) {
	ns := obj.Spec.ManagedNamespaces
	if ns == nil {
		return &namespacePolicies{}, nil
	}

	policies := &namespacePolicies{}

	if len(ns.AllowedNames) == 0 {
		return nil, fmt.Errorf(".spec.managedNamespaces.allowedNames must contain at least one expression")
	}
	for _, expr := range ns.AllowedNames {
		e, err := cel.NewStringAssertion("ns", expr)
		if err != nil {
			return nil, fmt.Errorf("invalid .spec.managedNamespaces.allowedNames expression %q: %w", expr, err)
		}
		policies.allowedNamespaces = append(policies.allowedNamespaces, e)
	}

	if ns.Metadata != nil && ns.Metadata.FromSource != nil {
		src := ns.Metadata.FromSource
		var err error
		policies.allowedLabels, err = compileMetadataAssertions(src.AllowedLabels, "label")
		if err != nil {
			return nil, err
		}
		policies.allowedAnnotations, err = compileMetadataAssertions(src.AllowedAnnotations, "annotation")
		if err != nil {
			return nil, err
		}
	}

	return policies, nil
}

// compileMetadataAssertions compiles the allowed metadata CEL expressions. The
// expression receives the metadata value through the 'value' string variable.
func compileMetadataAssertions(exprs map[string]string, kind string) (map[string]*cel.Expression, error) {
	if len(exprs) == 0 {
		return nil, nil
	}
	compiled := make(map[string]*cel.Expression, len(exprs))
	for key, expr := range exprs {
		e, err := cel.NewStringAssertion("value", expr)
		if err != nil {
			return nil, fmt.Errorf("invalid allowed %s expression for key %q: %w", kind, key, err)
		}
		compiled[key] = e
	}
	return compiled, nil
}

// validateManagedNamespaces rejects namespaces that the ArtifactGenerator is
// not allowed to manage, either because they are always denied or because no
// expression in .spec.managedNamespaces.allowedNames accepts them.
func (r *ArtifactGeneratorReconciler) validateManagedNamespaces(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	reqs []artifactRequest,
	policies *namespacePolicies) error {
	seen := make(map[string]struct{})
	for _, req := range reqs {
		name := req.Namespace
		if name == "" || name == obj.Namespace {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}

		if _, denied := deniedNamespaces[name]; denied {
			return fmt.Errorf("namespace %q cannot be managed by an ArtifactGenerator", name)
		}
		allowed, err := namespaceAllowed(ctx, policies.allowedNamespaces, name)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("namespace %q is not allowed by .spec.managedNamespaces.allowedNames", name)
		}
	}
	return nil
}

// namespaceAllowed returns true when at least one of the given expressions
// accepts the namespace name.
func namespaceAllowed(ctx context.Context, exprs []*cel.Expression, name string) (bool, error) {
	for _, e := range exprs {
		ok, err := e.EvaluateBoolean(ctx, map[string]any{"ns": name})
		if err != nil {
			return false, fmt.Errorf("failed to evaluate .spec.managedNamespaces.allowedNames expression %q: %w", e.String(), err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// namespaceConflict describes a managed namespace that is being adopted or
// taken over from another ArtifactGenerator.
type namespaceConflict struct {
	Namespace string `json:"namespace"`
	Owner     string `json:"owner,omitempty"`
	Adopted   bool   `json:"adopted"`
}

// reconcileNamespaces creates or updates the namespaces explicitly targeted by
// the rendered output artifacts, returning their inventory entries and the
// namespaces that were adopted or taken over.
func (r *ArtifactGeneratorReconciler) reconcileNamespaces(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	reqs []artifactRequest,
	localSources map[string]string,
	workDir string,
	auth *accessChecker,
	policies *namespacePolicies) ([]swapi.InventoryEntry, []namespaceConflict, error) {
	// Validate the namespace metadata source path before touching the cluster.
	if err := validateNamespaceMetadataPaths(obj, reqs); err != nil {
		return nil, nil, err
	}

	// Resolve and validate the metadata of every targeted namespace before
	// applying any of them, so that a metadata error does not leave a partial
	// set of managed namespaces behind.
	type plannedNamespace struct {
		name        string
		labels      map[string]string
		annotations map[string]string
	}
	seen := make(map[string]struct{})
	var planned []plannedNamespace
	for _, req := range reqs {
		// Only namespaces explicitly targeted by .spec.artifacts[].namespace
		// are managed, the ArtifactGenerator namespace is never managed
		// implicitly through the default.
		name := req.Namespace
		if name == "" {
			continue
		}
		// Never manage the ArtifactGenerator namespace itself, even when
		// explicitly targeted, to avoid deleting the ArtifactGenerator and
		// its dependencies when pruning.
		if name == obj.Namespace {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}

		// The metadata of a namespace is built from the captures of the first
		// artifact that targets it. validateNamespaceMetadataPaths guarantees
		// that every artifact targeting the namespace resolves the namespace
		// metadata source to the same file, so the choice is not ambiguous.
		artifactDir := filepath.Join(workDir, name, req.Name)
		labels, annotations, err := r.namespaceMetadata(ctx, obj, name, req.Captures, localSources, artifactDir, policies)
		if err != nil {
			return nil, nil, err
		}
		planned = append(planned, plannedNamespace{name: name, labels: labels, annotations: annotations})
	}

	var refs []swapi.InventoryEntry
	var conflicts []namespaceConflict
	for _, p := range planned {
		ref, conflict, err := r.reconcileNamespace(ctx, obj, p.name, p.labels, p.annotations, auth)
		if err != nil {
			return nil, nil, err
		}
		refs = append(refs, *ref)
		if conflict != nil {
			conflicts = append(conflicts, *conflict)
		}
	}
	return refs, conflicts, nil
}

// validateNamespaceMetadataPaths ensures that the NamespaceMetadata source path
// resolves to the same file for every artifact targeting a given namespace.
// The path may only use captures that are constant for the namespace; a
// module-level capture would resolve to a different file per artifact, making
// the metadata to apply ambiguous. The captures of the first artifact targeting
// the namespace are used for the metadata, so a mismatch is a terminal error.
func validateNamespaceMetadataPaths(obj *swapi.ArtifactGenerator, reqs []artifactRequest) error {
	if obj.Spec.ManagedNamespaces == nil || obj.Spec.ManagedNamespaces.Metadata == nil ||
		obj.Spec.ManagedNamespaces.Metadata.FromSource == nil {
		return nil
	}
	tmpl := obj.Spec.ManagedNamespaces.Metadata.FromSource.Path

	paths := make(map[string]string)
	for _, req := range reqs {
		name := req.Namespace
		if name == "" || name == obj.Namespace {
			continue
		}
		path, err := renderTemplateString(tmpl, req.Captures)
		if err != nil {
			return newTerminalNamespaceMetadataError("invalid namespace metadata path %q: %w", tmpl, err)
		}
		prev, ok := paths[name]
		if !ok {
			paths[name] = path
			continue
		}
		if prev != path {
			return newTerminalNamespaceMetadataError(
				"namespace metadata path %q resolves to %q and %q for different artifacts targeting namespace %q, it must only use namespace-level captures",
				tmpl, prev, path, name)
		}
	}
	return nil
}

// reconcileNamespace ensures the Namespace exists and is managed by the
// ArtifactGenerator, applying the common metadata and the controller labels.
// Existing namespaces that are not owned by another ArtifactGenerator are
// adopted with a warning event, while namespaces owned by another
// ArtifactGenerator are taken over.
//
// The lifecycle follows the kustomize-controller annotations: 'reconcile=disabled'
// and 'ssa=Ignore' exclude the namespace from apply, 'ssa=IfNotPresent' only
// creates it when missing, and 'ssa=Merge' skips the metadata cleanup.
func (r *ArtifactGeneratorReconciler) reconcileNamespace(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	name string,
	labels, annotations map[string]string,
	auth *accessChecker) (*swapi.InventoryEntry, *namespaceConflict, error) {
	log := ctrl.LoggerFrom(ctx)

	digest := namespaceMetadataDigest(labels, annotations)
	desired := newNamespace(name, labels, annotations)

	// Detect adoption or ownership conflict before applying.
	var conflict *namespaceConflict
	existing := &corev1.Namespace{}
	if err := auth.ensureNamespaceVerb(ctx, "get"); err != nil {
		return nil, nil, err
	}
	err := r.reader().Get(ctx, client.ObjectKey{Name: name}, existing)
	switch {
	case apierrors.IsNotFound(err):
		// The namespace will be created.
	case err != nil:
		return nil, nil, fmt.Errorf("failed to get Namespace: %w", err)
	default:
		owner := existing.Labels[swapi.ArtifactGeneratorLabel]
		switch {
		case owner == "":
			conflict = &namespaceConflict{Namespace: name, Adopted: true}
		case owner != string(obj.GetUID()):
			conflict = &namespaceConflict{Namespace: name, Owner: owner}
		}
	}

	// The apply library selects the IfNotPresent policy from the desired
	// object only, but the annotations are set on the in-cluster namespaces,
	// so handle the existing namespace here.
	if existing.GetUID() != "" &&
		matchesNamespaceMetadata(existing.Labels, existing.Annotations,
			swapi.SSAAnnotation, swapi.IfNotPresentValue) {
		log.Info("Namespace apply skipped, IfNotPresent policy is set", "namespace", name)
		return &swapi.InventoryEntry{
			Kind:   swapi.NamespaceKind,
			Name:   name,
			Digest: digest,
		}, nil, nil
	}

	// The apply library skips namespaces with the exclusion annotations. Check
	// them here to avoid requesting apply permissions that will not be used.
	if namespaceApplyExcluded(labels, annotations, existing) {
		log.Info("Namespace apply skipped, reconciliation is disabled", "namespace", name)
		return &swapi.InventoryEntry{
			Kind:   swapi.NamespaceKind,
			Name:   name,
			Digest: digest,
		}, nil, nil
	}

	if existing.GetUID() == "" {
		if err := auth.ensureNamespaceVerb(ctx, "create"); err != nil {
			return nil, nil, err
		}
	}
	if err := auth.ensureNamespaceVerb(ctx, "patch"); err != nil {
		return nil, nil, err
	}

	manager := r.newNamespaceManager()
	entry, err := manager.Apply(ctx, desired, r.namespaceApplyOptions())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to apply Namespace: %w", err)
	}

	// Skipped namespaces are still tracked in the inventory, so that pruning
	// can settle them when they are no longer targeted.
	if entry.Action == ssa.SkippedAction {
		log.Info("Namespace apply skipped, reconciliation is disabled", "namespace", name)
		return &swapi.InventoryEntry{
			Kind:   swapi.NamespaceKind,
			Name:   name,
			Digest: digest,
		}, nil, nil
	}

	// Emit the adoption or ownership conflict events only when the namespace
	// is actually applied.
	if conflict != nil {
		if conflict.Adopted {
			msg := fmt.Sprintf("Namespace %s is not managed by any ArtifactGenerator and is being adopted by %s/%s",
				name, obj.Namespace, obj.Name)
			log.Info("Adopting unmanaged namespace", "namespace", name)
			r.Eventf(existing, nil, corev1.EventTypeWarning, swapi.AdoptedReason, swapi.ActionPublish.String(), "%s", msg)
		} else {
			msg := fmt.Sprintf("Namespace %s is managed by ArtifactGenerator %s and is being taken over by %s/%s",
				name, conflict.Owner, obj.Namespace, obj.Name)
			log.Info("Taking over managed namespace", "namespace", name, "owner", conflict.Owner)
			r.Eventf(existing, nil, corev1.EventTypeWarning, swapi.OwnershipConflictReason, swapi.ActionPublish.String(), "%s", msg)
		}
	}

	// Emit the per-namespace event on the Namespace itself, so that the
	// ArtifactGenerator only carries the aggregate count.
	switch entry.Action {
	case ssa.CreatedAction:
		log.Info(fmt.Sprintf("Namespace %s created", name))
		created := &corev1.Namespace{}
		if err := r.reader().Get(ctx, client.ObjectKey{Name: name}, created); err != nil {
			log.Error(err, "failed to get Namespace for event", "namespace", name)
		} else {
			r.Eventf(created, nil, eventv1.EventTypeTrace, gotkmeta.ReadyCondition, swapi.ActionPublish.String(),
				"Namespace %s created", name)
		}
	case ssa.ConfiguredAction:
		log.Info(fmt.Sprintf("Namespace %s reconciled", name))
		r.Eventf(existing, nil, eventv1.EventTypeTrace, gotkmeta.ReadyCondition, swapi.ActionPublish.String(),
			"Namespace %s reconciled", name)
	default:
		log.Info("Namespace is up to date", "namespace", name)
	}

	return &swapi.InventoryEntry{
		Kind:   swapi.NamespaceKind,
		Name:   name,
		Digest: digest,
	}, conflict, nil
}

// newNamespaceManager returns a server-side apply resource manager for
// cluster-scoped Namespace objects.
func (r *ArtifactGeneratorReconciler) newNamespaceManager() *ssa.ResourceManager {
	return ssa.NewResourceManager(r.Client, nil, ssa.Owner{
		Field: r.ControllerName,
		Group: swapi.GroupVersion.Group,
	})
}

// namespaceApplyOptions returns the server-side apply options for managed
// namespaces, following the kustomize-controller annotation semantics.
func (r *ArtifactGeneratorReconciler) namespaceApplyOptions() ssa.ApplyOptions {
	opts := ssa.DefaultApplyOptions()
	opts.ExclusionSelector = map[string]string{
		swapi.ReconcileAnnotation: swapi.DisabledValue,
		swapi.SSAAnnotation:       swapi.IgnoreValue,
	}
	opts.IfNotPresentSelector = map[string]string{
		swapi.SSAAnnotation: swapi.IfNotPresentValue,
	}

	fieldManagers := []ssa.FieldManager{
		{Name: "kubectl", OperationType: metav1.ManagedFieldsOperationApply},
		{Name: "kubectl", OperationType: metav1.ManagedFieldsOperationUpdate},
		{Name: "before-first-apply", OperationType: metav1.ManagedFieldsOperationUpdate},
	}
	for _, manager := range r.DisallowedFieldManagers {
		fieldManagers = append(fieldManagers,
			ssa.FieldManager{Name: manager, OperationType: metav1.ManagedFieldsOperationApply, ExactMatch: true},
			ssa.FieldManager{Name: manager, OperationType: metav1.ManagedFieldsOperationUpdate, ExactMatch: true},
		)
	}

	opts.Cleanup = ssa.ApplyCleanupOptions{
		// Remove the annotation left behind by client-side apply.
		Annotations: []string{corev1.LastAppliedConfigAnnotation},
		// Only undo field ownership left by kubectl by default. Fields set by
		// Flux controllers and Helm are preserved, so adopting a namespace
		// does not revert metadata they manage. Users can protect manual
		// kubectl changes with --field-manager or the ssa=Merge annotation,
		// and admins can opt in to reverting more managers with
		// --override-manager.
		FieldManagers: fieldManagers,
		Exclusions: map[string]string{
			swapi.SSAAnnotation: swapi.MergeValue,
		},
	}
	return opts
}

// newNamespace returns the desired Namespace as an unstructured object.
func newNamespace(name string, labels, annotations map[string]string) *unstructured.Unstructured {
	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion(corev1.SchemeGroupVersion.String())
	ns.SetKind(swapi.NamespaceKind)
	ns.SetName(name)
	ns.SetLabels(labels)
	ns.SetAnnotations(annotations)
	return ns
}

// namespaceMetadata returns the labels and annotations to apply to the given
// managed namespace. The metadata starts from .spec.commonMetadata, then the
// metadata sourced from a NamespaceMetadata file (.fromSource) is applied, then
// the .from operations are applied in order, with the controller labels taking
// precedence.
func (r *ArtifactGeneratorReconciler) namespaceMetadata(
	ctx context.Context,
	obj *swapi.ArtifactGenerator,
	namespace string,
	captures map[string]string,
	sources map[string]string,
	artifactDir string,
	policies *namespacePolicies) (map[string]string, map[string]string, error) {
	var sourcedLabels, sourcedAnnotations map[string]string
	if obj.Spec.ManagedNamespaces != nil && obj.Spec.ManagedNamespaces.Metadata != nil &&
		obj.Spec.ManagedNamespaces.Metadata.FromSource != nil {
		var err error
		sourcedLabels, sourcedAnnotations, err = loadNamespaceMetadataFromSource(
			ctx, obj.Spec.ManagedNamespaces.Metadata.FromSource, captures, sources, artifactDir, policies)
		if err != nil {
			return nil, nil, err
		}
	}

	labels, annotations := r.namespaceMetadataFor(obj, namespace, sourcedLabels, sourcedAnnotations)
	if err := validateNamespaceMetadataExternalFinalizer(namespace, labels, annotations); err != nil {
		return nil, nil, newTerminalNamespaceMetadataError("%w", err)
	}
	// Validate the resolved metadata before the server-side apply, so that an
	// invalid label or annotation fails with an actionable error instead of a
	// cryptic apply error.
	if err := validateResolvedNamespaceMetadata(namespace, labels, annotations); err != nil {
		return nil, nil, newTerminalNamespaceMetadataError("%w", err)
	}
	return labels, annotations, nil
}

// validateResolvedNamespaceMetadata validates the resolved labels and
// annotations of a managed namespace against the Kubernetes object metadata
// rules.
func validateResolvedNamespaceMetadata(name string, labels, annotations map[string]string) error {
	meta := &metav1.ObjectMeta{
		Name:        name,
		Labels:      labels,
		Annotations: annotations,
	}
	errs := apivalidation.ValidateObjectMeta(meta, false, apivalidation.NameIsDNSLabel, field.NewPath("metadata"))
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid metadata for namespace %q: %s", name, errs.ToAggregate().Error())
}

// namespaceMetadataFor builds the desired labels and annotations of a managed
// namespace from the ArtifactGenerator spec, the sourced metadata (already
// filtered by the allowed schema), and the controller labels.
func (r *ArtifactGeneratorReconciler) namespaceMetadataFor(obj *swapi.ArtifactGenerator,
	namespace string,
	sourcedLabels, sourcedAnnotations map[string]string) (map[string]string, map[string]string) {
	labels := make(map[string]string)
	var annotations map[string]string
	if cm := obj.Spec.CommonMetadata; cm != nil {
		maps.Copy(labels, cm.Labels)
		if len(cm.Annotations) > 0 {
			annotations = make(map[string]string)
			maps.Copy(annotations, cm.Annotations)
		}
	}

	// Metadata sourced from a NamespaceMetadata file overrides the common
	// metadata.
	labels = mergeStringMap(labels, sourcedLabels)
	annotations = mergeStringMap(annotations, sourcedAnnotations)

	if obj.Spec.ManagedNamespaces != nil && obj.Spec.ManagedNamespaces.Metadata != nil {
		for _, op := range obj.Spec.ManagedNamespaces.Metadata.From {
			if op.Namespace != "*" && op.Namespace != namespace {
				continue
			}
			switch op.Strategy {
			case swapi.NamespaceMetadataResetStrategy:
				labels = maps.Clone(op.Labels)
				if labels == nil {
					labels = make(map[string]string)
				}
				annotations = maps.Clone(op.Annotations)
			case swapi.OverrideStrategy:
				labels = mergeStringMap(labels, op.Labels)
				annotations = mergeStringMap(annotations, op.Annotations)
			default: // swapi.MergeStrategy
				labels = mergeAbsentStringMap(labels, op.Labels)
				annotations = mergeAbsentStringMap(annotations, op.Annotations)
			}
		}
	}

	labels["app.kubernetes.io/managed-by"] = r.ControllerName
	labels[swapi.ArtifactGeneratorLabel] = string(obj.GetUID())
	return labels, annotations
}

// loadNamespaceMetadataFromSource reads and validates the NamespaceMetadata
// file referenced by .spec.managedNamespaces.metadata.fromSource, returning the labels
// and annotations that are allowed by the schema.
func loadNamespaceMetadataFromSource(
	ctx context.Context,
	src *swapi.NamespaceMetadataFromSource,
	captures map[string]string,
	sources map[string]string,
	artifactDir string,
	policies *namespacePolicies) (map[string]string, map[string]string, error) {
	path, err := renderTemplateString(src.Path, captures)
	if err != nil {
		return nil, nil, newTerminalNamespaceMetadataError("invalid namespace metadata path: %w", err)
	}

	localPath, err := resolveLocalSourcePath(path, sources, artifactDir)
	if err != nil {
		return nil, nil, newTerminalNamespaceMetadataError("invalid namespace metadata path %q: %w", path, err)
	}

	data, err := readFileLimited(localPath, maxInputsFileSize)
	if err != nil {
		// A missing or oversized file is resolved by changing the source
		// content (which triggers a reconcile), while any other local read
		// failure can resolve without a watch event and must stay retryable.
		if os.IsNotExist(err) || errors.Is(err, errFileTooLarge) {
			return nil, nil, newTerminalNamespaceMetadataError("failed to read namespace metadata file %q: %w", path, err)
		}
		return nil, nil, fmt.Errorf("failed to read namespace metadata file %q: %w", path, err)
	}

	var nm swapi.NamespaceMetadata
	if err := yaml.Unmarshal(data, &nm); err != nil {
		return nil, nil, newTerminalNamespaceMetadataError("failed to parse namespace metadata file %q: %w", path, err)
	}
	if nm.APIVersion != "" && nm.APIVersion != swapi.GroupVersion.String() {
		return nil, nil, newTerminalNamespaceMetadataError("namespace metadata file %q has unsupported apiVersion %q", path, nm.APIVersion)
	}
	if nm.Kind != "" && nm.Kind != swapi.NamespaceMetadataKind {
		return nil, nil, newTerminalNamespaceMetadataError("namespace metadata file %q has unsupported kind %q", path, nm.Kind)
	}

	var allowedLabels, allowedAnnotations map[string]*cel.Expression
	if policies != nil {
		allowedLabels = policies.allowedLabels
		allowedAnnotations = policies.allowedAnnotations
	}

	labels, err := filterNamespaceMetadata(ctx, nm.Metadata.Labels, allowedLabels, "label")
	if err != nil {
		return nil, nil, newTerminalNamespaceMetadataError("namespace metadata file %q: %w", path, err)
	}
	annotations, err := filterNamespaceMetadata(ctx, nm.Metadata.Annotations, allowedAnnotations, "annotation")
	if err != nil {
		return nil, nil, newTerminalNamespaceMetadataError("namespace metadata file %q: %w", path, err)
	}

	return labels, annotations, nil
}

// filterNamespaceMetadata filters the labels or annotations of a
// NamespaceMetadata file against the allowed schema. Keys that are not part of
// the allowed schema are ignored, while values that do not satisfy the allowed
// CEL expressions are rejected.
func filterNamespaceMetadata(ctx context.Context, values map[string]string, allowed map[string]*cel.Expression, kind string) (map[string]string, error) {
	if len(values) == 0 || len(allowed) == 0 {
		return nil, nil
	}
	filtered := make(map[string]string)
	for key, value := range values {
		expr, ok := allowed[key]
		if !ok {
			continue
		}
		ok, err := expr.EvaluateBoolean(ctx, map[string]any{"value": value})
		if err != nil {
			return nil, fmt.Errorf("failed to evaluate the allowed %s expression for key %q: %w", kind, key, err)
		}
		if !ok {
			return nil, fmt.Errorf("%s %q with value %q is rejected by the allowed expression %q", kind, key, value, expr.String())
		}
		filtered[key] = value
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	return filtered, nil
}

// mergeStringMap merges the source map into the destination, allocating the
// destination when needed. Empty sources are ignored.
func mergeStringMap(dst, src map[string]string) map[string]string {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]string)
	}
	maps.Copy(dst, src)
	return dst
}

// mergeAbsentStringMap merges the source map into the destination without
// overriding existing keys, allocating the destination when needed. Empty
// sources are ignored.
func mergeAbsentStringMap(dst, src map[string]string) map[string]string {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]string)
	}
	for key, value := range src {
		if _, ok := dst[key]; !ok {
			dst[key] = value
		}
	}
	return dst
}

// namespaceMetadataMatches returns true when the namespace carries all the
// expected labels and annotations.
func namespaceMetadataMatches(ns *corev1.Namespace, labels, annotations map[string]string) bool {
	for key, value := range labels {
		if ns.Labels[key] != value {
			return false
		}
	}
	for key, value := range annotations {
		if ns.Annotations[key] != value {
			return false
		}
	}
	return true
}

// namespaceMetadataDigest computes a deterministic digest of the metadata
// applied to a managed namespace.
func namespaceMetadataDigest(labels, annotations map[string]string) string {
	parts := make([]string, 0, len(labels)+len(annotations))
	for key, value := range labels {
		parts = append(parts, fmt.Sprintf("label:%s=%s", key, value))
	}
	for key, value := range annotations {
		parts = append(parts, fmt.Sprintf("annotation:%s=%s", key, value))
	}
	sort.Strings(parts)
	digest := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return fmt.Sprintf("sha256:%x", digest)
}

// detectNamespacesDrift checks if the managed namespaces recorded in the
// inventory still exist, are owned by the ArtifactGenerator and carry the
// expected common metadata. Namespaces excluded from apply and namespaces
// with the IfNotPresent policy are never reported as drifted.
func (r *ArtifactGeneratorReconciler) detectNamespacesDrift(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	auth *accessChecker) (bool, error) {
	if !obj.ManagesNamespaces() {
		return false, nil
	}

	for _, ref := range obj.Status.Inventory {
		if ref.Kind != swapi.NamespaceKind {
			continue
		}
		if err := auth.ensureNamespaceVerb(ctx, "get"); err != nil {
			return false, err
		}
		expectedLabels, expectedAnnotations := r.namespaceMetadataFor(obj, ref.Name, nil, nil)
		ns := &corev1.Namespace{}
		err := r.reader().Get(ctx, client.ObjectKey{Name: ref.Name}, ns)
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		exists := err == nil

		if namespaceApplyExcluded(expectedLabels, expectedAnnotations, ns) {
			continue
		}
		// Namespaces with the IfNotPresent policy are only created and never
		// updated once they exist.
		if exists && namespaceIfNotPresent(expectedLabels, expectedAnnotations, ns) {
			continue
		}
		if !exists {
			return true, nil
		}
		if !namespaceMetadataMatches(ns, expectedLabels, expectedAnnotations) {
			return true, nil
		}
	}
	return false, nil
}

// namespaceApplyExcluded returns true when the expected or actual namespace
// metadata matches the apply exclusion annotations.
func namespaceApplyExcluded(expectedLabels, expectedAnnotations map[string]string, ns *corev1.Namespace) bool {
	if matchesNamespaceMetadata(expectedLabels, expectedAnnotations, swapi.ReconcileAnnotation, swapi.DisabledValue) ||
		matchesNamespaceMetadata(expectedLabels, expectedAnnotations, swapi.SSAAnnotation, swapi.IgnoreValue) {
		return true
	}
	if ns != nil &&
		(matchesNamespaceMetadata(ns.Labels, ns.Annotations, swapi.ReconcileAnnotation, swapi.DisabledValue) ||
			matchesNamespaceMetadata(ns.Labels, ns.Annotations, swapi.SSAAnnotation, swapi.IgnoreValue)) {
		return true
	}
	return false
}

// matchesNamespaceMetadata returns true when the given metadata matches the
// annotation or label selector, mirroring the server-side apply library.
func matchesNamespaceMetadata(labels, annotations map[string]string, key, value string) bool {
	return strings.EqualFold(labels[key], value) || strings.EqualFold(annotations[key], value)
}

// namespaceDeleteExcluded returns true when the namespace metadata matches the
// delete exclusion annotations.
func namespaceDeleteExcluded(ns *corev1.Namespace) bool {
	return matchesNamespaceMetadata(ns.Labels, ns.Annotations, swapi.PruneAnnotation, swapi.DisabledValue) ||
		matchesNamespaceMetadata(ns.Labels, ns.Annotations, swapi.ReconcileAnnotation, swapi.DisabledValue) ||
		matchesNamespaceMetadata(ns.Labels, ns.Annotations, swapi.SSAAnnotation, swapi.IgnoreValue)
}

// namespaceIfNotPresent returns true when the expected or actual namespace
// metadata matches the IfNotPresent annotation.
func namespaceIfNotPresent(expectedLabels, expectedAnnotations map[string]string, ns *corev1.Namespace) bool {
	if matchesNamespaceMetadata(expectedLabels, expectedAnnotations, swapi.SSAAnnotation, swapi.IfNotPresentValue) {
		return true
	}
	return ns != nil &&
		matchesNamespaceMetadata(ns.Labels, ns.Annotations, swapi.SSAAnnotation, swapi.IfNotPresentValue)
}

// deleteNamespace deletes a managed namespace unless the namespace matches the
// delete exclusion annotations ('prune=disabled', 'reconcile=disabled' or
// 'ssa=Ignore'). It returns an error when the deletion was not confirmed by
// the API server.
func (r *ArtifactGeneratorReconciler) deleteNamespace(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	name string,
	auth *accessChecker) error {
	log := ctrl.LoggerFrom(ctx)

	if err := auth.ensureNamespaceVerb(ctx, "get"); err != nil {
		return err
	}

	// The namespace may delegate its finalization to an external object. When
	// the annotation is valid, the controller hands off the deletion and stops
	// tracking the namespace, so that the external finalizer can perform the
	// ordered garbage collection.
	existing, externallyFinalized, err := r.namespaceExternallyFinalized(ctx, name)
	if err != nil {
		return err
	}
	if existing == nil {
		return nil
	}
	if externallyFinalized {
		log.Info("Skipping managed namespace deletion, finalization is delegated to an external object", "namespace", name)
		return nil
	}
	if namespaceDeleteExcluded(existing) {
		log.Info("Skipping managed namespace deletion, deletion is disabled", "namespace", name)
		return nil
	}

	if err := auth.ensureNamespaceVerb(ctx, "delete"); err != nil {
		return err
	}

	ns := newNamespace(name, nil, nil)
	opts := ssa.DefaultDeleteOptions()
	opts.Exclusions = map[string]string{
		swapi.PruneAnnotation:     swapi.DisabledValue,
		swapi.ReconcileAnnotation: swapi.DisabledValue,
		swapi.SSAAnnotation:       swapi.IgnoreValue,
	}

	entry, err := r.newNamespaceManager().Delete(ctx, ns, opts)
	if err != nil {
		return err
	}

	if entry.Action == ssa.SkippedAction {
		log.Info("Skipping managed namespace deletion, deletion is disabled", "namespace", name)
		return nil
	}

	log.Info("Namespace deleted from cluster", "namespace", name)
	return nil
}
