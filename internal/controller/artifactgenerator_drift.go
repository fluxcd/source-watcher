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
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkstorage "github.com/fluxcd/pkg/artifact/storage"
	gotkconditions "github.com/fluxcd/pkg/runtime/conditions"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

// detectDrift checks if the actual state matches the desired and last reconciled state.
//
// Returns (drifted, reason) where reason can be one of:
//   - "NotReady" - object is not in a ready state
//   - "GenerationChanged" - object generation differs from observed generation
//   - "SourcesChanged" - sources digest differs from last observed sources digest
//   - "ArtifactsChanged" - number of artifacts in spec differs from inventory
//   - "ArtifactMissing" - artifact is missing from storage
//   - "ArtifactCorrupted" - artifact exists in storage but fails integrity verification
//   - "ExternalArtifactsNotFound" - failed to query in-cluster external artifacts
//   - "ExternalArtifactsChanged" - in-cluster external artifacts differ from inventory
//   - "NamespacesNotFound" - failed to query managed namespaces
//   - "NamespacesChanged" - managed namespaces differ from inventory
//   - "NoDriftDetected" - no drift detected and the storage is up to date
func (r *ArtifactGeneratorReconciler) detectDrift(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	currentSourcesDigest string,
	impersonated client.Client) (bool, string) {
	// Setup logger on debug level.
	log := ctrl.LoggerFrom(ctx).V(1)

	if gotkconditions.IsFalse(obj, gotkmeta.ReadyCondition) {
		log.Info("Drift detected, previous reconciliation failed")
		return true, "NotReady"
	}

	if obj.GetGeneration() != gotkconditions.GetObservedGeneration(obj, gotkmeta.ReadyCondition) {
		log.Info("Drift detected, generation has changed",
			"old", gotkconditions.GetObservedGeneration(obj, gotkmeta.ReadyCondition),
			"new", obj.GetGeneration())
		return true, "GenerationChanged"
	}

	// Namespace metadata sourced from artifacts can only be computed after the
	// sources are fetched, so always run the full reconciliation when it is
	// configured and the metadata drift can then be corrected.
	if obj.ManagesNamespaces() && obj.Spec.Namespaces.Metadata != nil &&
		obj.Spec.Namespaces.Metadata.FromSource != nil {
		log.Info("Drift detected, namespace metadata is sourced from artifacts")
		return true, "NamespaceMetadataFromSource"
	}

	if obj.Status.ObservedSourcesDigest != currentSourcesDigest {
		log.Info("Drift detected, sources have changed",
			"old", obj.Status.ObservedSourcesDigest,
			"new", currentSourcesDigest)
		return true, "SourcesChanged"
	}

	// Count only the artifact entries, as the inventory also tracks
	// managed namespaces.
	artifactCount := 0
	for _, ref := range obj.Status.Inventory {
		if ref.Kind != swapi.NamespaceKind {
			artifactCount++
		}
	}
	if artifactCount != len(obj.Spec.OutputArtifacts) && obj.Spec.PathPattern == "" {
		log.Info("Drift detected, number of output artifacts has changed",
			"old", artifactCount,
			"new", len(obj.Spec.OutputArtifacts))
		return true, "ArtifactsChanged"
	}

	for _, eaRef := range obj.Status.Inventory {
		if eaRef.Kind == swapi.NamespaceKind {
			continue
		}
		storagePath := gotkstorage.ArtifactPath(sourcev1.ExternalArtifactKind, eaRef.Namespace, eaRef.Name, eaRef.Filename)
		artifact := gotkmeta.Artifact{
			Digest: eaRef.Digest,
			Path:   storagePath,
		}
		if !r.Storage.ArtifactExist(artifact) {
			log.Info("Drift detected, artifact missing from storage",
				"artifact", fmt.Sprintf("%s/%s/%s", sourcev1.ExternalArtifactKind, eaRef.Namespace, eaRef.Name),
				"path", storagePath)
			return true, "ArtifactMissing"
		}
		if err := r.Storage.VerifyArtifact(artifact); err != nil {
			log.Error(err, "Artifact integrity verification failed, deleting corrupted artifact",
				"artifact", fmt.Sprintf("%s/%s/%s", sourcev1.ExternalArtifactKind, eaRef.Namespace, eaRef.Name))
			if err := r.Storage.Remove(artifact); err != nil {
				log.Error(err, "Failed to remove corrupted artifact from storage",
					"artifact", fmt.Sprintf("%s/%s/%s", sourcev1.ExternalArtifactKind, eaRef.Namespace, eaRef.Name))
			}
			return true, "ArtifactCorrupted"
		}
	}

	eaDrift, err := r.detectExternalArtifactsDrift(ctx, obj, impersonated)
	if err != nil {
		log.Error(err, "Failed to verify in-cluster external artifacts for drift")
		return true, "ExternalArtifactsNotFound"
	}
	if eaDrift {
		log.Info("Drift detected, in-cluster external artifacts have changed")
		return true, "ExternalArtifactsChanged"
	}

	nsDrift, err := r.detectNamespacesDrift(ctx, obj, impersonated)
	if err != nil {
		log.Error(err, "Failed to verify managed namespaces for drift")
		return true, "NamespacesNotFound"
	}
	if nsDrift {
		log.Info("Drift detected, managed namespaces have changed")
		return true, "NamespacesChanged"
	}

	return false, "NoDriftDetected"
}

// detectExternalArtifactsDrift checks if any ExternalArtifact objects
// managed by the ArtifactGenerator have been modified or deleted.
func (r *ArtifactGeneratorReconciler) detectExternalArtifactsDrift(ctx context.Context,
	obj *swapi.ArtifactGenerator,
	impersonated client.Client) (bool, error) {

	// Group the artifact inventory references by namespace, as the generated
	// ExternalArtifacts may live in namespaces other than the one of
	// the ArtifactGenerator.
	namespaces := make(map[string]struct{})
	artifactCount := 0
	for _, ref := range obj.Status.Inventory {
		if ref.Kind == swapi.NamespaceKind {
			continue
		}
		namespaces[ref.Namespace] = struct{}{}
		artifactCount++
	}

	// Check if the number of ExternalArtifacts in the cluster matches the inventory.
	total := 0
	for namespace := range namespaces {
		// Select the client based on the namespace of the ExternalArtifacts.
		kubeClient := r.clientForNamespace(obj, impersonated, namespace)

		eaList := &sourcev1.ExternalArtifactList{}
		if err := kubeClient.List(ctx, eaList, client.InNamespace(namespace),
			client.MatchingLabels{
				swapi.ArtifactGeneratorLabel: string(obj.GetUID()),
			}); err != nil {
			return true, fmt.Errorf("error listing external artifacts: %w", err)
		}
		total += len(eaList.Items)

		// Check if the ExternalArtifacts in the cluster match the inventory.
		for _, ea := range eaList.Items {
			if ea.Status.Artifact == nil ||
				!obj.HasArtifactInInventory(ea.Name, ea.Namespace, ea.Status.Artifact.Digest) {
				return true, nil
			}
		}
	}

	if total != artifactCount {
		return true, nil
	}

	return false, nil
}
