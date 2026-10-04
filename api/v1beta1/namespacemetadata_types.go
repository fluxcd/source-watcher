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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NamespaceMetadata is a KRM-style configuration object that allows tenants to
// provide additional metadata for the namespaces managed by an
// ArtifactGenerator. It is read from a file inside a source artifact, as
// configured through .spec.namespaces.metadata.fromSource, and is not a
// cluster-scoped Kubernetes resource.
//
// Example:
//
//	apiVersion: source.extensions.fluxcd.io/v1beta1
//	kind: NamespaceMetadata
//	metadata:
//	  annotations:
//	    foo: bar
//	  labels:
//	    baz: qux
type NamespaceMetadata struct {
	metav1.TypeMeta `json:",inline"`

	// Metadata holds the labels and annotations provided for the namespace.
	// +optional
	Metadata NamespaceMetadataMetadata `json:"metadata,omitempty"`
}

// NamespaceMetadataMetadata holds the labels and annotations provided for a
// namespace through a NamespaceMetadata file.
type NamespaceMetadataMetadata struct {
	// Annotations to be applied to the namespace.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Labels to be applied to the namespace.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}
