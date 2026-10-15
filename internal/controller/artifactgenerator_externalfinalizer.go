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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

// externalFinalizerRef identifies the object a managed namespace delegates its
// finalization to, parsed from the source.extensions.fluxcd.io/externalFinalizer
// annotation. The reference is fully qualified (group, version, kind,
// namespace, name and UID) so that the controller can look the object up
// without first resolving the kind API version in the cluster.
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
	return strings.Join([]string{r.Group, r.Version, r.Kind, r.Namespace, r.Name, string(r.UID)}, "/")
}

// parseExternalFinalizerRef parses the value of the externalFinalizer
// annotation, which has the format
// "group/version/kind/namespace/name/uid". The group and the namespace may be
// empty for core and cluster-scoped resources respectively.
func parseExternalFinalizerRef(value string) (externalFinalizerRef, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 6 {
		return externalFinalizerRef{}, fmt.Errorf("expected 6 slash-separated segments in 'group/version/kind/namespace/name/uid', got %d", len(parts))
	}
	ref := externalFinalizerRef{
		Group:     parts[0],
		Version:   parts[1],
		Kind:      parts[2],
		Namespace: parts[3],
		Name:      parts[4],
		UID:       types.UID(parts[5]),
	}
	switch {
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

// namespaceExternallyFinalized returns true when the given managed namespace
// delegates its finalization to an external object. This is the case when the
// namespace carries the externalFinalizer annotation and the referenced object
// exists in the cluster with the same UID.
//
// A missing object, or an object with a different UID (a recreated
// incarnation), does not hand over the finalization: the controller remains
// accountable for deleting the namespace, so a failed handoff cannot leak it.
// The same applies to a malformed annotation value, which is ignored so the
// controller stays accountable.
func (r *ArtifactGeneratorReconciler) namespaceExternallyFinalized(ctx context.Context,
	name string,
	kubeClient client.Client) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	ns := &corev1.Namespace{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: name}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to get Namespace for external finalizer check: %w", err)
	}

	value, ok := ns.Annotations[swapi.ExternalFinalizerAnnotation]
	if !ok || value == "" {
		return false, nil
	}

	ref, err := parseExternalFinalizerRef(value)
	if err != nil {
		log.Error(err, "Ignoring malformed external finalizer annotation, the controller remains accountable",
			"namespace", name, "annotation", swapi.ExternalFinalizerAnnotation, "value", value)
		return false, nil
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(ref.GroupVersionKind())
	if err := kubeClient.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, obj); err != nil {
		switch {
		case apierrors.IsNotFound(err), apimeta.IsNoMatchError(err):
			log.Info("External finalizer object not found, the controller remains accountable",
				"namespace", name, "object", ref.String())
			return false, nil
		default:
			return false, fmt.Errorf("failed to get external finalizer object %s: %w", ref.String(), err)
		}
	}

	if string(obj.GetUID()) != string(ref.UID) {
		log.Info("External finalizer object UID mismatch, the controller remains accountable",
			"namespace", name, "object", ref.String(), "uid", obj.GetUID())
		return false, nil
	}

	log.Info("Namespace finalization is delegated to an external object", "namespace", name, "object", ref.String())
	return true, nil
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
