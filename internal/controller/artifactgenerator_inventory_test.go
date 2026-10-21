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
	"testing"

	. "github.com/onsi/gomega"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

func TestFindOrphanedReferences(t *testing.T) {
	g := NewWithT(t)
	r := &ArtifactGeneratorReconciler{}

	// The previous inventory may contain entries written before the type and
	// kind field was introduced.
	inventory := []swapi.InventoryEntry{
		{Name: "app", Namespace: "tenant", Digest: "sha256:abc", Filename: "app.tar.gz"},
		{Name: "stale", Namespace: "tenant", Digest: "sha256:def", Filename: "stale.tar.gz"},
		{Kind: swapi.NamespaceKind, Name: "old-ns"},
	}
	current := []swapi.InventoryEntry{
		{Kind: sourcev1.ExternalArtifactKind, Name: "app", Namespace: "tenant", Digest: "sha256:abc", Filename: "app.tar.gz"},
		{Kind: swapi.NamespaceKind, Name: "new-ns"},
	}

	orphans := r.findOrphanedReferences(inventory, current)
	g.Expect(orphans).To(HaveLen(2))
	g.Expect(orphans).To(ConsistOf(
		swapi.InventoryEntry{Name: "stale", Namespace: "tenant", Digest: "sha256:def", Filename: "stale.tar.gz"},
		swapi.InventoryEntry{Kind: swapi.NamespaceKind, Name: "old-ns"},
	))
}
