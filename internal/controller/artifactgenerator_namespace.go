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
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	eventv1 "github.com/fluxcd/pkg/apis/event/v1"
	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/ssa"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

// namespaceConflict describes a managed namespace that is being adopted or
// taken over from another ArtifactGenerator.
type namespaceConflict struct {
	Namespace string `json:"namespace"`
	Owner     string `json:"owner,omitempty"`
	Adopted   bool   `json:"adopted"`
}

// namespaceClient returns the client used to manage cluster-scoped Namespace
// objects. When a ServiceAccount is configured for impersonation, it is used,
// otherwise the controller client is returned.
func (r *ArtifactGeneratorReconciler) namespaceClient(impersonated client.Client) client.Client {
	if impersonated != nil {
		return impersonated
	}
	return r.Client
}

// reconcileNamespaces creates or updates the namespaces explicitly targeted by
// the rendered output artifacts, returning their inventory entries and the
// namespaces that were adopted or taken over.
func (r *ArtifactGeneratorReconciler) reconcileNamespaces(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	reqs []artifactRequest,
	localSources map[string]string,
	workDir string,
	impersonated client.Client) ([]swapi.InventoryEntry, []namespaceConflict, error) {
	kubeClient := r.namespaceClient(impersonated)
	seen := make(map[string]struct{})
	var refs []swapi.InventoryEntry
	var conflicts []namespaceConflict
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
		// artifact that targets it, which is sufficient for the common case
		// where the namespace metadata path only uses namespace-level
		// captures.
		artifactDir := filepath.Join(workDir, req.Name)
		labels, annotations, err := r.namespaceMetadata(obj, name, req.Captures, localSources, artifactDir)
		if err != nil {
			return nil, nil, err
		}

		ref, conflict, err := r.reconcileNamespace(ctx, obj, name, labels, annotations, kubeClient)
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
	kubeClient client.Client) (*swapi.InventoryEntry, *namespaceConflict, error) {
	log := ctrl.LoggerFrom(ctx)

	digest := namespaceMetadataDigest(labels, annotations)
	desired := newNamespace(name, labels, annotations)

	// Detect adoption or ownership conflict before applying.
	var conflict *namespaceConflict
	existing := &corev1.Namespace{}
	err := kubeClient.Get(ctx, client.ObjectKey{Name: name}, existing)
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

	manager := r.newNamespaceManager(kubeClient)
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
			r.Eventf(existing, nil, corev1.EventTypeWarning, swapi.NamespaceAdoptedReason, swapi.ActionPublish.String(), "%s", msg)
		} else {
			msg := fmt.Sprintf("Namespace %s is managed by ArtifactGenerator %s and is being taken over by %s/%s",
				name, conflict.Owner, obj.Namespace, obj.Name)
			log.Info("Taking over managed namespace", "namespace", name, "owner", conflict.Owner)
			r.Eventf(existing, nil, corev1.EventTypeWarning, swapi.OwnershipConflictReason, swapi.ActionPublish.String(), "%s", msg)
		}
	}

	switch entry.Action {
	case ssa.CreatedAction:
		msg := fmt.Sprintf("Namespace %s created", name)
		log.Info(msg)
		r.Eventf(obj, nil, eventv1.EventTypeTrace, gotkmeta.ReadyCondition, swapi.ActionPublish.String(), "%s", msg)
	case ssa.ConfiguredAction:
		msg := fmt.Sprintf("Namespace %s reconciled", name)
		log.Info(msg)
		r.Eventf(obj, nil, eventv1.EventTypeTrace, gotkmeta.ReadyCondition, swapi.ActionPublish.String(), "%s", msg)
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
func (r *ArtifactGeneratorReconciler) newNamespaceManager(kubeClient client.Client) *ssa.ResourceManager {
	return ssa.NewResourceManager(kubeClient, nil, ssa.Owner{
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
	obj *swapi.ArtifactGenerator,
	namespace string,
	captures map[string]string,
	sources map[string]string,
	artifactDir string) (map[string]string, map[string]string, error) {
	var sourcedLabels, sourcedAnnotations map[string]string
	if obj.Spec.Namespaces != nil && obj.Spec.Namespaces.Metadata != nil &&
		obj.Spec.Namespaces.Metadata.FromSource != nil {
		var err error
		sourcedLabels, sourcedAnnotations, err = loadNamespaceMetadataFromSource(
			obj.Spec.Namespaces.Metadata.FromSource, captures, sources, artifactDir)
		if err != nil {
			return nil, nil, err
		}
	}

	labels, annotations := r.namespaceMetadataFor(obj, namespace, sourcedLabels, sourcedAnnotations)
	if err := validateNamespaceMetadataExternalFinalizer(namespace, labels, annotations); err != nil {
		return nil, nil, err
	}
	return labels, annotations, nil
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

	if obj.Spec.Namespaces != nil && obj.Spec.Namespaces.Metadata != nil {
		for _, op := range obj.Spec.Namespaces.Metadata.From {
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
// file referenced by .spec.namespaces.metadata.fromSource, returning the labels
// and annotations that are allowed by the schema.
func loadNamespaceMetadataFromSource(
	src *swapi.NamespaceMetadataFromSource,
	captures map[string]string,
	sources map[string]string,
	artifactDir string) (map[string]string, map[string]string, error) {
	path, err := renderTemplateString(src.Path, captures)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid namespace metadata path: %w", err)
	}

	localPath, err := resolveLocalSourcePath(path, sources, artifactDir)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid namespace metadata path %q: %w", path, err)
	}

	data, err := readFileLimited(localPath, maxInputsFileSize)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read namespace metadata file %q: %w", path, err)
	}

	var nm swapi.NamespaceMetadata
	if err := yaml.Unmarshal(data, &nm); err != nil {
		return nil, nil, fmt.Errorf("failed to parse namespace metadata file %q: %w", path, err)
	}
	if nm.APIVersion != "" && nm.APIVersion != swapi.GroupVersion.String() {
		return nil, nil, fmt.Errorf("namespace metadata file %q has unsupported apiVersion %q", path, nm.APIVersion)
	}
	if nm.Kind != "" && nm.Kind != swapi.NamespaceMetadataKind {
		return nil, nil, fmt.Errorf("namespace metadata file %q has unsupported kind %q", path, nm.Kind)
	}

	labels, err := filterNamespaceMetadata(nm.Metadata.Labels, src.AllowedLabels, "label")
	if err != nil {
		return nil, nil, fmt.Errorf("namespace metadata file %q: %w", path, err)
	}
	annotations, err := filterNamespaceMetadata(nm.Metadata.Annotations, src.AllowedAnnotations, "annotation")
	if err != nil {
		return nil, nil, fmt.Errorf("namespace metadata file %q: %w", path, err)
	}

	return labels, annotations, nil
}

// filterNamespaceMetadata filters the labels or annotations of a
// NamespaceMetadata file against the allowed schema. Keys that are not part of
// the allowed schema are ignored, while values that do not match the allowed
// regular expressions are rejected.
func filterNamespaceMetadata(values, allowed map[string]string, kind string) (map[string]string, error) {
	if len(values) == 0 || len(allowed) == 0 {
		return nil, nil
	}
	filtered := make(map[string]string)
	for key, value := range values {
		pattern, ok := allowed[key]
		if !ok {
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid allowed %s pattern for key %q: %w", kind, key, err)
		}
		if !re.MatchString(value) {
			return nil, fmt.Errorf("%s %q with value %q does not match the allowed pattern %q", kind, key, value, pattern)
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
	impersonated client.Client) (bool, error) {
	if !obj.ManagesNamespaces() {
		return false, nil
	}

	kubeClient := r.namespaceClient(impersonated)
	for _, ref := range obj.Status.Inventory {
		if ref.Kind != swapi.NamespaceKind {
			continue
		}
		expectedLabels, expectedAnnotations := r.namespaceMetadataFor(obj, ref.Name, nil, nil)
		ns := &corev1.Namespace{}
		err := kubeClient.Get(ctx, client.ObjectKey{Name: ref.Name}, ns)
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
	name string,
	impersonated client.Client) error {
	log := ctrl.LoggerFrom(ctx)

	kubeClient := r.namespaceClient(impersonated)

	// The namespace may delegate its finalization to an external object. When
	// the referenced object exists, the controller hands off the deletion and
	// stops tracking the namespace, so that the external finalizer can perform
	// the ordered garbage collection.
	externallyFinalized, err := r.namespaceExternallyFinalized(ctx, name, kubeClient)
	if err != nil {
		return err
	}
	if externallyFinalized {
		log.Info("Skipping managed namespace deletion, finalization is delegated to an external object", "namespace", name)
		return nil
	}

	ns := newNamespace(name, nil, nil)
	opts := ssa.DefaultDeleteOptions()
	opts.Exclusions = map[string]string{
		swapi.PruneAnnotation:     swapi.DisabledValue,
		swapi.ReconcileAnnotation: swapi.DisabledValue,
		swapi.SSAAnnotation:       swapi.IgnoreValue,
	}

	entry, err := r.newNamespaceManager(kubeClient).Delete(ctx, ns, opts)
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
