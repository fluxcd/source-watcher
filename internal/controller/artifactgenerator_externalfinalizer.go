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
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

// externalFinalizerRef identifies the object a managed namespace delegates its
// finalization to, parsed from the source.extensions.fluxcd.io/externalFinalizer
// annotation. The reference is fully qualified (group, version, kind,
// namespace, name and UID) so that it is unambiguous without resolving the kind
// in the cluster.
type externalFinalizerRef struct {
	Group     string
	Version   string
	Kind      string
	Namespace string
	Name      string
	UID       types.UID
}

// GroupVersionKind returns the GroupVersionKind of the referenced object.
func (r externalFinalizerRef) GroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: r.Group, Version: r.Version, Kind: r.Kind}
}

// String returns the fully qualified reference in the annotation format.
func (r externalFinalizerRef) String() string {
	group := r.Group
	if group == "" {
		group = "core"
	}
	parts := []string{group, r.Version, r.Kind}
	if r.Namespace != "" {
		parts = append(parts, r.Namespace)
	}
	parts = append(parts, r.Name, string(r.UID))
	return strings.Join(parts, "/")
}

// parseExternalFinalizerRef parses the value of the externalFinalizer
// annotation. Namespaced objects use
// "group/version/kind/namespace/name/uid", while cluster-scoped objects use
// "group/version/kind/name/uid". The core group is written as "core".
func parseExternalFinalizerRef(value string) (externalFinalizerRef, error) {
	parts := strings.Split(value, "/")
	var ref externalFinalizerRef
	namespaced := len(parts) == 6
	switch len(parts) {
	case 6:
		ref = externalFinalizerRef{
			Group:     parts[0],
			Version:   parts[1],
			Kind:      parts[2],
			Namespace: parts[3],
			Name:      parts[4],
			UID:       types.UID(parts[5]),
		}
	case 5:
		ref = externalFinalizerRef{
			Group:   parts[0],
			Version: parts[1],
			Kind:    parts[2],
			Name:    parts[3],
			UID:     types.UID(parts[4]),
		}
	default:
		return externalFinalizerRef{}, fmt.Errorf(
			"expected 5 or 6 slash-separated segments, got %d", len(parts))
	}
	switch ref.Group {
	case "core":
		ref.Group = ""
	case "":
		return externalFinalizerRef{}, errors.New("group must not be empty")
	}
	switch {
	case namespaced && ref.Namespace == "":
		return externalFinalizerRef{}, errors.New("namespace must not be empty")
	case ref.Version == "":
		return externalFinalizerRef{}, errors.New("version must not be empty")
	case ref.Kind == "":
		return externalFinalizerRef{}, errors.New("kind must not be empty")
	case ref.Name == "":
		return externalFinalizerRef{}, errors.New("name must not be empty")
	case ref.UID == "":
		return externalFinalizerRef{}, errors.New("uid must not be empty")
	}
	return ref, nil
}

// namespaceExternallyFinalized returns the managed Namespace and true when it
// delegates its finalization to an external object. This is the case when the
// namespace carries a valid externalFinalizer annotation.
//
// The referenced object is not looked up: the annotation is trusted as written,
// which keeps the controller RBAC narrow and avoids caching arbitrary kinds. A
// malformed annotation value is reported to the caller, which surfaces it to
// controller-runtime for a retry.
func (r *ArtifactGeneratorReconciler) namespaceExternallyFinalized(ctx context.Context,
	name string) (*corev1.Namespace, bool, error) {
	log := ctrl.LoggerFrom(ctx)

	ns := &corev1.Namespace{}
	if err := r.reader().Get(ctx, client.ObjectKey{Name: name}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to get Namespace for external finalizer check: %w", err)
	}

	value, ok := ns.Annotations[swapi.ExternalFinalizerAnnotation]
	if !ok || value == "" {
		log.Info("No external finalizer claimed the namespace, proceeding with cleanup", "namespace", name)
		return ns, false, nil
	}

	if _, err := parseExternalFinalizerRef(value); err != nil {
		return nil, false, fmt.Errorf("invalid %q annotation on namespace %q: %w",
			swapi.ExternalFinalizerAnnotation, name, err)
	}

	log.Info("Namespace finalization is delegated to an external object",
		"namespace", name, "externalFinalizer", value)
	return ns, true, nil
}

// validateNamespaceMetadataExternalFinalizer rejects the reserved
// externalFinalizer annotation in the metadata the controller computes for a
// managed namespace. The annotation must be set by an external actor, so that
// the controller cannot be tricked into handing over a namespace it is still
// accountable for.
func validateNamespaceMetadataExternalFinalizer(namespace string, labels, annotations map[string]string) error {
	if annotations[swapi.ExternalFinalizerAnnotation] == "" {
		return nil
	}
	return fmt.Errorf("metadata for namespace %q must not set the reserved %q annotation, it is reserved for external finalizers",
		namespace, swapi.ExternalFinalizerAnnotation)
}
