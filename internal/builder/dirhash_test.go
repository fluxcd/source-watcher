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

package builder_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkstorage "github.com/fluxcd/pkg/artifact/storage"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

func TestBuild_DirHash(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	gitDir := filepath.Join(tmpDir, "git")
	ociDir := filepath.Join(tmpDir, "oci")
	workspaceDir := filepath.Join(tmpDir, "workspace")

	for _, dir := range []string{gitDir, ociDir, workspaceDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("Failed to create dir %s: %v", dir, err)
		}
	}

	gitSpec := &swapi.OutputArtifact{
		Name: "git-artifact",
		Copy: []swapi.CopyOperation{{From: "@git/**", To: "@artifact/"}},
	}
	ociSpec := &swapi.OutputArtifact{
		Name: "oci-artifact",
		Copy: []swapi.CopyOperation{{From: "@oci/**", To: "@artifact/"}},
	}
	sources := map[string]string{
		"git": gitDir,
		"oci": ociDir,
	}

	// Same content in both sources
	err := os.WriteFile(filepath.Join(gitDir, "1.yaml"), []byte("---"), 0o644)
	g.Expect(err).ToNot(HaveOccurred())
	err = os.WriteFile(filepath.Join(ociDir, "1.yaml"), []byte("---"), 0o644)
	g.Expect(err).ToNot(HaveOccurred())

	// Build both artifacts
	gitArtifact, err := testBuilder.Build(ctx, gitSpec, sources, "dirhash", workspaceDir)
	g.Expect(err).ToNot(HaveOccurred())
	ociArtifact, err := testBuilder.Build(ctx, ociSpec, sources, "dirhash", workspaceDir)
	g.Expect(err).ToNot(HaveOccurred())

	// Same content should have same digest
	g.Expect(gitArtifact.Digest).To(Equal(ociArtifact.Digest))

	// Different artifact names should have different filenames (name included in hash)
	g.Expect(filepath.Base(gitArtifact.Path)).ToNot(Equal(filepath.Base(ociArtifact.Path)))

	// Different names should have different storage paths
	g.Expect(gitArtifact.Path).ToNot(Equal(ociArtifact.Path))

	// Rebuild artifact to ensure it's reproducible
	gitArtifactRebuild, err := testBuilder.Build(ctx, gitSpec, sources, "dirhash", workspaceDir)
	g.Expect(err).ToNot(HaveOccurred())

	// Rebuild should yield same digest and filename
	g.Expect(gitArtifactRebuild.Digest).To(Equal(gitArtifact.Digest))
	g.Expect(filepath.Base(gitArtifactRebuild.Path)).To(Equal(filepath.Base(gitArtifact.Path)))
}

func TestBuild_DirHash_FileChanges(t *testing.T) {
	tests := []struct {
		name       string
		changeFunc func(dir string) error
	}{
		{
			name: "rename file changes digest and artifact filename",
			changeFunc: func(dir string) error {
				return os.Rename(filepath.Join(dir, "1.yaml"), filepath.Join(dir, "2.yaml"))
			},
		},
		{
			name: "change content changes digest and artifact filename",
			changeFunc: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "1.yaml"), []byte("---\n"), 0o644)
			},
		},
		{
			name: "add file changes digest and artifact filename",
			changeFunc: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "2.yaml"), []byte("---"), 0o644)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			ctx := context.Background()

			tmpDir := t.TempDir()
			sourceDir := filepath.Join(tmpDir, "source")
			workspaceDir := filepath.Join(tmpDir, "workspace")

			for _, dir := range []string{sourceDir, workspaceDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("Failed to create dir %s: %v", dir, err)
				}
			}

			err := os.WriteFile(filepath.Join(sourceDir, "1.yaml"), []byte("---"), 0o644)
			g.Expect(err).ToNot(HaveOccurred())

			spec := &swapi.OutputArtifact{
				Name: "test-artifact",
				Copy: []swapi.CopyOperation{{From: "@source/**", To: "@artifact/"}},
			}

			sources := map[string]string{"source": sourceDir}

			// Build original
			original, err := testBuilder.Build(ctx, spec, sources, "change", workspaceDir)
			g.Expect(err).ToNot(HaveOccurred())

			// Make change
			err = tt.changeFunc(sourceDir)
			g.Expect(err).ToNot(HaveOccurred())

			// Build after change
			changed, err := testBuilder.Build(ctx, spec, sources, "change", workspaceDir)
			g.Expect(err).ToNot(HaveOccurred())

			originalFilename := filepath.Base(original.Path)
			changedFilename := filepath.Base(changed.Path)

			// All changes should result in different digest and filename
			g.Expect(changed.Digest).ToNot(Equal(original.Digest))
			g.Expect(changedFilename).ToNot(Equal(originalFilename))
		})
	}
}

func TestBuild_GarbageCollection(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	workspaceDir := filepath.Join(tmpDir, "workspace")

	for _, dir := range []string{sourceDir, workspaceDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("Failed to create dir %s: %v", dir, err)
		}
	}

	spec := &swapi.OutputArtifact{
		Name: "gc-test-artifact",
		Copy: []swapi.CopyOperation{{From: "@source/**", To: "@artifact/"}},
	}
	sources := map[string]string{"source": sourceDir}

	// Build 3 different artifacts to trigger GC
	err := os.WriteFile(filepath.Join(sourceDir, "1.yaml"), []byte("---"), 0o644)
	g.Expect(err).ToNot(HaveOccurred())

	artifact1, err := testBuilder.Build(ctx, spec, sources, "gc", workspaceDir)
	g.Expect(err).ToNot(HaveOccurred())

	err = os.WriteFile(filepath.Join(sourceDir, "1.yaml"), []byte("---\n"), 0o644)
	g.Expect(err).ToNot(HaveOccurred())

	artifact2, err := testBuilder.Build(ctx, spec, sources, "gc", workspaceDir)
	g.Expect(err).ToNot(HaveOccurred())

	err = os.WriteFile(filepath.Join(sourceDir, "2.yaml"), []byte("---"), 0o644)
	g.Expect(err).ToNot(HaveOccurred())

	artifact3, err := testBuilder.Build(ctx, spec, sources, "gc", workspaceDir)
	g.Expect(err).ToNot(HaveOccurred())

	// GC should delete artifacts older than retention
	storagePath := gotkstorage.ArtifactPath(sourcev1.ExternalArtifactKind, "gc", spec.Name, "*")
	deleted, err := testStorage.GarbageCollect(ctx, gotkmeta.Artifact{Path: storagePath}, 2*time.Second)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(deleted).ToNot(BeEmpty())
	g.Expect(testStorage.ArtifactExist(*artifact1)).To(BeFalse())
	g.Expect(testStorage.ArtifactExist(*artifact2)).To(BeTrue())
	g.Expect(testStorage.ArtifactExist(*artifact3)).To(BeTrue())
}
