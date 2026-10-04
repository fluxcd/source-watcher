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

package v1beta1

import (
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
)

const (
	ArtifactGeneratorKind            = "ArtifactGenerator"
	Finalizer                        = "source.extensions.fluxcd.io/finalizer"
	ArtifactGeneratorLabel           = "source.extensions.fluxcd.io/generator"
	ArtifactOriginRevisionAnnotation = "org.opencontainers.image.revision"
	ReconcileAnnotation              = "source.extensions.fluxcd.io/reconcile"
	ReconcileEveryAnnotation         = "source.extensions.fluxcd.io/reconcileEvery"
	PruneAnnotation                  = "source.extensions.fluxcd.io/prune"
	SSAAnnotation                    = "source.extensions.fluxcd.io/ssa"
	ReconciliationDisabledReason     = "ReconciliationDisabled"
	AccessDeniedReason               = "AccessDenied"
	ValidationFailedReason           = "ValidationFailed"
	SourceFetchFailedReason          = "SourceFetchFailed"
	OwnershipConflictReason          = "OwnershipConflict"
	OverwriteStrategy                = "Overwrite"
	MergeStrategy                    = "Merge"
	ExtractStrategy                  = "Extract"
	OverrideStrategy                 = "Override"
	EnabledValue                     = "Enabled"
	DisabledValue                    = "Disabled"
	MergeValue                       = "Merge"
	IfNotPresentValue                = "IfNotPresent"
	IgnoreValue                      = "Ignore"
	NamespaceStrategyUnmanaged       = "Unmanaged"
	NamespaceStrategyManaged         = "Managed"
	NamespaceKind                    = "Namespace"
	NamespaceAdoptedReason           = "NamespaceAdopted"
	NamespaceMetadataKind            = "NamespaceMetadata"
	NamespaceMetadataResetStrategy   = "Reset"
	ArtifactAlias                    = "artifact"
)

// CommonMetadata defines the common labels and annotations.
type CommonMetadata struct {
	// Annotations to be added to the object's metadata.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Labels to be added to the object's metadata.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// ArtifactGeneratorSpec defines the desired state of ArtifactGenerator.
// +kubebuilder:validation:XValidation:rule="has(self.pathPattern) && size(self.pathPattern) > 0 || self.artifacts.all(a, a.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$') && a.name.size() <= 253)",message="artifact names must be valid Kubernetes object names when pathPattern is not set"
// +kubebuilder:validation:XValidation:rule="has(self.pathPattern) && size(self.pathPattern) > 0 || self.artifacts.all(a, !has(a.__namespace__) || (a.__namespace__.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$') && a.__namespace__.size() <= 63))",message="artifact namespaces must be valid Kubernetes namespaces when pathPattern is not set"
// +kubebuilder:validation:XValidation:rule="!has(self.commonMetadata) || !has(self.commonMetadata.annotations) || (!('source.extensions.fluxcd.io/ssa' in self.commonMetadata.annotations) && !('source.extensions.fluxcd.io/prune' in self.commonMetadata.annotations) && !('source.extensions.fluxcd.io/reconcile' in self.commonMetadata.annotations))",message="commonMetadata must not set source.extensions.fluxcd.io/ssa, source.extensions.fluxcd.io/prune or source.extensions.fluxcd.io/reconcile; set these annotations on individual objects"
type ArtifactGeneratorSpec struct {
	// CommonMetadata specifies the common labels and annotations that are
	// applied to all resources. Any existing label or annotation will be
	// overridden if its key matches a common one.
	// +optional
	CommonMetadata *CommonMetadata `json:"commonMetadata,omitempty"`

	// Sources is a list of references to the Flux source-controller
	// resources that will be used to generate the artifact.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1000
	// +required
	Sources []SourceReference `json:"sources"`

	// ServiceAccountName is the name of the ServiceAccount used to reconcile
	// the generated ExternalArtifacts and the managed Namespaces. The
	// ServiceAccount must exist in the ArtifactGenerator namespace. When
	// specified, the controller impersonates this ServiceAccount for all
	// generated ExternalArtifacts, including those in the ArtifactGenerator
	// namespace, and its RBAC bindings determine the namespaces in which they
	// can be created, updated and deleted.
	// When not specified, the controller uses its own credentials for
	// ExternalArtifacts in the ArtifactGenerator namespace, and the default
	// ServiceAccount configured by the cluster administrator (when set) for
	// artifacts targeting another namespace.
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// PathPattern specifies a directory traversal pattern to match within the sources.
	// The format is "@<alias>/<pattern>". Named captures in the pattern (e.g. "{app}")
	// can be used as placeholders in OutputArtifacts fields.
	// +kubebuilder:validation:Pattern="^@([a-z0-9]([a-z0-9_-]*[a-z0-9])?)/(.*)$"
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	PathPattern string `json:"pathPattern,omitempty"`

	// Namespaces defines how the controller manages the namespaces
	// targeted by the generated artifacts.
	// +optional
	Namespaces *Namespaces `json:"namespaces,omitempty"`

	// OutputArtifacts is a list of output artifacts to be generated.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1000
	// +required
	OutputArtifacts []OutputArtifact `json:"artifacts"`
}

// Namespaces defines how the controller manages the namespaces targeted by
// the generated artifacts.
type Namespaces struct {
	// Strategy specifies the namespace management strategy.
	// 'Unmanaged' leaves the target namespaces untouched, they must exist and
	// are not modified by the controller.
	// 'Managed' makes the controller the manager of the target namespaces: it
	// creates them when missing and applies the common metadata to them.
	// When .spec.namespaces is omitted, namespaces are unmanaged.
	// +kubebuilder:validation:Enum=Unmanaged;Managed
	// +required
	Strategy string `json:"strategy"`

	// Prune specifies whether the controller deletes managed namespaces that
	// are no longer targeted by any generated artifact, or when the
	// ArtifactGenerator is deleted. Pruning only occurs when
	// .spec.namespaces.strategy is 'Managed'. Defaults to false.
	// When unset and the DefaultToPruneNamespaces feature gate is enabled,
	// the field is considered set to true (pruning still only takes place if
	// the strategy is explicitly set to Managed). When set, the feature gate
	// is ignored.
	// +optional
	Prune *bool `json:"prune,omitempty"`

	// Metadata defines how the metadata of the desired namespaces is built,
	// on top of .spec.commonMetadata.
	// +optional
	Metadata *NamespacesMetadata `json:"metadata,omitempty"`
}

// NamespacesMetadata defines how the metadata of the desired namespaces is
// built. The metadata is constructed by applying .spec.commonMetadata first,
// then the metadata sourced from a NamespaceMetadata file, then the metadata
// operations in .from, in order.
type NamespacesMetadata struct {
	// FromSource sources namespace metadata from a NamespaceMetadata file
	// inside a source artifact.
	// +optional
	FromSource *NamespaceMetadataFromSource `json:"fromSource,omitempty"`

	// From is a list of metadata operations applied in order to the desired
	// namespaces, on top of .spec.commonMetadata and .fromSource.
	// +optional
	From []NamespaceMetadataFrom `json:"from,omitempty"`
}

// NamespaceMetadataFromSource sources namespace metadata from a
// NamespaceMetadata file inside a source artifact. The file may contain
// labels and annotations for the desired namespace, constrained by the
// allowedAnnotations and allowedLabels schemas.
type NamespaceMetadataFromSource struct {
	// Path is the path to the NamespaceMetadata file inside a source artifact,
	// in the format "@<alias>/<path>". When pathPattern is set, the path may
	// use capture placeholders such as "{namespace}".
	// +kubebuilder:validation:Pattern="^@([a-z0-9]([a-z0-9_-]*[a-z0-9])?)/(.*)$"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	// +required
	Path string `json:"path"`

	// AllowedAnnotations is a schema for the annotations that the
	// NamespaceMetadata file is allowed to set. Each key is an annotation key
	// and each value is a regular expression that the annotation value must
	// match. Annotations whose key is not present in this map are ignored,
	// while annotations whose value does not match the respective regular
	// expression are rejected.
	// +optional
	AllowedAnnotations map[string]string `json:"allowedAnnotations,omitempty"`

	// AllowedLabels is a schema for the labels that the NamespaceMetadata
	// file is allowed to set. Each key is a label key and each value is a
	// regular expression that the label value must match. Labels whose key is
	// not present in this map are ignored, while labels whose value does not
	// match the respective regular expression are rejected.
	// +optional
	AllowedLabels map[string]string `json:"allowedLabels,omitempty"`
}

// NamespaceMetadataFrom defines a metadata operation applied to a desired
// namespace.
type NamespaceMetadataFrom struct {
	// Strategy specifies how the labels and annotations are applied.
	// 'Reset' clears all the existing labels and annotations before applying
	// the ones defined in the operation (fields that are not set clear the
	// corresponding metadata), 'Override' merges the ones defined in the
	// operation into the existing values, overriding existing keys, while
	// 'Merge' merges only the absent keys, preserving existing values.
	// +kubebuilder:validation:Enum=Reset;Override;Merge
	// +required
	Strategy string `json:"strategy"`

	// Namespace is the name of the desired namespace the metadata applies to,
	// or '*' to apply the metadata to all desired namespaces.
	// +kubebuilder:validation:Pattern="^(\\*|[a-z0-9]([-a-z0-9]*[a-z0-9])?)$"
	// +kubebuilder:validation:MaxLength=63
	// +required
	Namespace string `json:"namespace"`

	// Annotations to be applied to the namespace.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Labels to be applied to the namespace.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// SourceReference contains the reference to a Flux source-controller resource.
type SourceReference struct {
	// Alias of the source within the ArtifactGenerator context.
	// The alias must be unique per ArtifactGenerator, and must consist
	// of lower case alphanumeric characters, underscores, and hyphens.
	// It must start and end with an alphanumeric character.
	// +kubebuilder:validation:Pattern="^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$"
	// +kubebuilder:validation:MaxLength=63
	// +required
	Alias string `json:"alias"`

	// Name of the source.
	// +kubebuilder:validation:Pattern="^[a-z0-9]([a-z0-9-]*[a-z0-9])?$"
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`

	// Namespace of the source.
	// If not provided, defaults to the same namespace as the ArtifactGenerator.
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Kind of the source.
	// +kubebuilder:validation:Enum=Bucket;GitRepository;OCIRepository;HelmChart;ExternalArtifact
	// +required
	Kind string `json:"kind"`
}

// OutputArtifact defines the desired state of an ExternalArtifact
// generated by the ArtifactGenerator.
type OutputArtifact struct {
	// Name is the name of the generated artifact.
	// When pathPattern is set, this field may use capture placeholders such as "{app}".
	// The maximum length accommodates capture placeholders; the effective
	// limits are enforced by the CEL validation when pathPattern is not set.
	// +kubebuilder:validation:MaxLength=1024
	// +required
	Name string `json:"name"`

	// Namespace is the namespace of the generated artifact.
	// If not provided, defaults to the same namespace as the ArtifactGenerator.
	// When pathPattern is set, this field may use capture placeholders such as "{app}".
	// When set to a different namespace, the controller reconciles the artifact
	// with the credentials of .spec.serviceAccountName or the controller default.
	// The maximum length accommodates capture placeholders; the effective
	// limits are enforced by the CEL validation when pathPattern is not set.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// InputsFrom specifies a source path to a YAML file whose contents are
	// exported in the ExternalArtifact .status.exportedInputs field.
	// The format is "@<alias>/<path>", where <alias> references a source or
	// "artifact" for the generated artifact itself. When pathPattern is set,
	// the path may use capture placeholders such as "{module}".
	// +kubebuilder:validation:Pattern="^@([a-z0-9]([a-z0-9_-]*[a-z0-9])?)/(.*)$"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	InputsFrom string `json:"inputsFrom,omitempty"`

	// Revision is the revision of the generated artifact.
	// If specified, it must point to an existing source alias in the format "@<alias>".
	// If not specified, the revision is automatically set to the digest of the artifact content.
	// +kubebuilder:validation:Pattern="^@([a-z0-9]([a-z0-9_-]*[a-z0-9])?)$"
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Revision string `json:"revision,omitempty"`

	// OriginRevision is used to set the 'org.opencontainers.image.revision'
	// annotation on the generated artifact metadata.
	// If specified, it must point to an existing source alias in the format "@<alias>".
	// If the referenced source has an origin revision (e.g. a Git commit SHA),
	// it will be used to set the annotation on the generated artifact.
	// If the referenced source does not have an origin revision, the field is ignored.
	// +kubebuilder:validation:Pattern="^@([a-z0-9]([a-z0-9_-]*[a-z0-9])?)$"
	// +kubebuilder:validation:MaxLength=64
	// +optional
	OriginRevision string `json:"originRevision,omitempty"`

	// Copy defines a list of copy operations to perform from the sources to the generated artifact.
	// The copy operations are performed in the order they are listed with existing files
	// being overwritten by later copy operations.
	// +kubebuilder:validation:MinItems=1
	// +required
	Copy []CopyOperation `json:"copy"`
}

type CopyOperation struct {
	// From specifies the source (by alias) and the glob pattern to match files.
	// The format is "@<alias>/<glob-pattern>". When pathPattern is set,
	// the path may use capture placeholders such as "{app}".
	// +kubebuilder:validation:Pattern="^@([a-z0-9]([a-z0-9_-]*[a-z0-9])?)/(.*)$"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	// +required
	From string `json:"from"`

	// To specifies the destination path within the artifact.
	// The format is "@artifact/path", the alias "artifact"
	// refers to the root path of the generated artifact. When pathPattern
	// is set, the path may use capture placeholders such as "{app}".
	// +kubebuilder:validation:Pattern="^@artifact/([^/]{0,1}|[^./][^/]|[.][^./]|[^/]{3,})(/([^/]{0,1}|[^./][^/]|[.][^./]|[^/]{3,}))*$"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	// +required
	To string `json:"to"`

	// Exclude specifies a list of glob patterns to exclude
	// files and dirs matched by the 'From' field. Patterns are matched
	// against paths relative to the source alias root or to the non-glob
	// prefix of 'From'. Patterns without a separator (e.g. "*.md") match
	// the file name at any depth.
	// +kubebuilder:validation:MaxItems=100
	// +kubebuilder:validation:items:MaxLength=1024
	// +optional
	Exclude []string `json:"exclude,omitempty"`

	// Strategy specifies the copy strategy to use.
	// 'Overwrite' will overwrite existing files in the destination.
	// 'Merge' is for merging YAML files using Helm values merge strategy.
	// 'Extract' is for extracting the contents of tarball archives (.tar.gz, .tgz)
	// When using glob patterns, non-tarball files are silently skipped. For single file sources,
	// the file must be a tarball or an error is returned. Directories are not supported.
	// If not specified, defaults to 'Overwrite'.
	// +optional
	// +kubebuilder:validation:Enum=Overwrite;Merge;Extract
	Strategy string `json:"strategy,omitempty"`

	// Optional, when set to true, allows the copy operation to silently
	// skip when the source path or glob pattern matches no files.
	// When false (default), the reconciliation fails with an error if
	// no files are matched.
	// +optional
	Optional bool `json:"optional,omitempty"`
}

// ArtifactGeneratorStatus defines the observed state of ArtifactGenerator.
type ArtifactGeneratorStatus struct {
	gotkmeta.ReconcileRequestStatus `json:",inline"`

	// Conditions holds the conditions for the ArtifactGenerator.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Inventory contains the list of objects managed by the ArtifactGenerator,
	// such as the generated ExternalArtifacts and the managed Namespaces.
	// +optional
	Inventory []InventoryEntry `json:"inventory,omitempty"`

	// ObservedSourcesDigest is a hash representing the current state of
	// all the sources referenced by the ArtifactGenerator.
	// +optional
	ObservedSourcesDigest string `json:"observedSourcesDigest,omitempty"`
}

// InventoryEntry contains a reference to an object managed by the
// ArtifactGenerator, such as a generated ExternalArtifact or a managed
// Namespace.
type InventoryEntry struct {
	// Kind is the kind of the referent object.
	// +kubebuilder:validation:Enum=ExternalArtifact;Namespace
	// +optional
	Kind string `json:"kind,omitempty"`

	// Name of the referent object.
	// +required
	Name string `json:"name"`

	// Namespace of the referent object. Empty for cluster-scoped objects.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Digest of the referent object. For generated artifacts this is the
	// artifact content digest; for managed namespaces this is a digest of the
	// metadata applied by the controller.
	// +required
	Digest string `json:"digest"`

	// Filename is the name of the artifact file.
	// +optional
	Filename string `json:"filename,omitempty"`
}

// GetConditions returns the status conditions of the object.
func (in *ArtifactGenerator) GetConditions() []metav1.Condition {
	return in.Status.Conditions
}

// SetConditions sets the status conditions on the object.
func (in *ArtifactGenerator) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}

// GetRequeueAfter returns the duration after which the ArtifactGenerator
// must be reconciled again. It defaults to one hour and can be overridden
// with the reconcileEvery annotation using a Go duration string.
func (in *ArtifactGenerator) GetRequeueAfter() time.Duration {
	if v, ok := in.GetAnnotations()[ReconcileEveryAnnotation]; ok {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return time.Hour
}

// SetLastHandledReconcileAt sets the last handled reconcile time in the status.
func (in *ArtifactGenerator) SetLastHandledReconcileAt(value string) {
	in.Status.LastHandledReconcileAt = value
}

// IsDisabled returns true if the object has the reconcile annotation set to 'disabled'.
func (in *ArtifactGenerator) IsDisabled() bool {
	val, ok := in.GetAnnotations()[ReconcileAnnotation]
	return ok && strings.EqualFold(val, DisabledValue)
}

// GetArtifactNamespace returns the namespace where the ExternalArtifact
// generated for the given OutputArtifact is created. It defaults to the
// ArtifactGenerator namespace.
func (in *ArtifactGenerator) GetArtifactNamespace(outputArtifact *OutputArtifact) string {
	if outputArtifact.Namespace != "" {
		return outputArtifact.Namespace
	}
	return in.Namespace
}

// ManagesNamespaces returns true when the controller manages the target
// namespaces of the generated artifacts.
func (in *ArtifactGenerator) ManagesNamespaces() bool {
	return in.Spec.Namespaces != nil && in.Spec.Namespaces.Strategy == NamespaceStrategyManaged
}

// NamespacePrune returns whether the controller prunes managed namespaces
// that are no longer targeted by any generated artifact, or when the
// ArtifactGenerator is deleted. When .spec.namespaces.prune is unset, the
// provided default is returned; callers pass the state of the
// DefaultToPruneNamespaces feature gate. The setting only applies when
// ManagesNamespaces() returns true.
func (in *ArtifactGenerator) NamespacePrune(defaultToPrune bool) bool {
	if in.Spec.Namespaces == nil || in.Spec.Namespaces.Prune == nil {
		return defaultToPrune
	}
	return *in.Spec.Namespaces.Prune
}

// HasArtifactInInventory returns true if the artifact with the given
// name, namespace, and digest exists in the inventory.
func (in *ArtifactGenerator) HasArtifactInInventory(name, namespace, digest string) bool {
	for _, ref := range in.Status.Inventory {
		if ref.Kind == NamespaceKind {
			continue
		}
		if ref.Name == name && ref.Namespace == namespace && ref.Digest == digest {
			return true
		}
	}
	return false
}

// HasNamespaceInInventory returns true if the namespace with the given
// name and metadata digest exists in the inventory.
func (in *ArtifactGenerator) HasNamespaceInInventory(name, digest string) bool {
	for _, ref := range in.Status.Inventory {
		if ref.Kind == NamespaceKind && ref.Name == name && ref.Digest == digest {
			return true
		}
	}
	return false
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ag,categories=all;fluxcd;fluxcd-sources
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message",description=""
// +kubebuilder:metadata:annotations="kustomize.toolkit.fluxcd.io/substitute=disabled"

// ArtifactGenerator is the Schema for the artifactgenerators API.
type ArtifactGenerator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArtifactGeneratorSpec   `json:"spec,omitempty"`
	Status ArtifactGeneratorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ArtifactGeneratorList contains a list of ArtifactGenerator.
type ArtifactGeneratorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ArtifactGenerator `json:"items"`
}
