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
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/testserver"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

func TestArtifactGeneratorReconciler_getSources(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func() (*swapi.ArtifactGenerator, func())
		expectError bool
		expectCount int
	}{
		{
			name: "successfully gets git and oci sources",
			setupFunc: func() (*swapi.ArtifactGenerator, func()) {
				gitKey := client.ObjectKey{Name: "test-git", Namespace: "default"}
				ociKey := client.ObjectKey{Name: "test-oci", Namespace: "default"}
				objKey := client.ObjectKey{Name: "test-generator", Namespace: "default"}

				gitFiles := []testserver.File{
					{Name: "file1.yaml", Body: "content1"},
					{Name: "file2.yaml", Body: "content2"},
				}
				ociFiles := []testserver.File{
					{Name: "file3.yaml", Body: "content3"},
					{Name: "file4.yaml", Body: "content4"},
				}

				if err := applyGitRepository(gitKey, "main@sha1:abcd", gitFiles); err != nil {
					t.Fatalf("Failed to apply git repository: %v", err)
				}
				if err := applyOCIRepository(ociKey, "latest@sha256:1234", ociFiles); err != nil {
					t.Fatalf("Failed to apply OCI repository: %v", err)
				}

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      objKey.Name,
						Namespace: objKey.Namespace,
					},
					Spec: swapi.ArtifactGeneratorSpec{
						Sources: []swapi.SourceReference{
							{
								Alias: gitKey.Name,
								Kind:  sourcev1.GitRepositoryKind,
								Name:  gitKey.Name,
							},
							{
								Alias: ociKey.Name,
								Kind:  sourcev1.OCIRepositoryKind,
								Name:  ociKey.Name,
							},
						},
					},
				}

				cleanup := func() {
					testClient.Delete(context.Background(), &sourcev1.GitRepository{
						ObjectMeta: metav1.ObjectMeta{Name: gitKey.Name, Namespace: gitKey.Namespace},
					})
					testClient.Delete(context.Background(), &sourcev1.OCIRepository{
						ObjectMeta: metav1.ObjectMeta{Name: ociKey.Name, Namespace: ociKey.Namespace},
					})
				}

				return generator, cleanup
			},
			expectError: false,
			expectCount: 2,
		},
		{
			name: "fails when git source not found",
			setupFunc: func() (*swapi.ArtifactGenerator, func()) {
				gitKey := client.ObjectKey{Name: "nonexistent-git", Namespace: "default"}
				ociKey := client.ObjectKey{Name: "nonexistent-oci", Namespace: "default"}
				objKey := client.ObjectKey{Name: "test-generator", Namespace: "default"}

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      objKey.Name,
						Namespace: objKey.Namespace,
					},
					Spec: swapi.ArtifactGeneratorSpec{
						Sources: []swapi.SourceReference{
							{
								Alias: gitKey.Name,
								Kind:  sourcev1.GitRepositoryKind,
								Name:  gitKey.Name,
							},
							{
								Alias: ociKey.Name,
								Kind:  sourcev1.OCIRepositoryKind,
								Name:  ociKey.Name,
							},
						},
					},
				}
				return generator, func() {}
			},
			expectError: true,
			expectCount: 0,
		},
		{
			name: "successfully gets single git source",
			setupFunc: func() (*swapi.ArtifactGenerator, func()) {
				gitKey := client.ObjectKey{Name: "single-git", Namespace: "default"}
				objKey := client.ObjectKey{Name: "single-generator", Namespace: "default"}

				gitFiles := []testserver.File{
					{Name: "config.yaml", Body: "apiVersion: v1\nkind: ConfigMap"},
				}

				if err := applyGitRepository(gitKey, "main@sha1:xyz123", gitFiles); err != nil {
					t.Fatalf("Failed to apply git repository: %v", err)
				}

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      objKey.Name,
						Namespace: objKey.Namespace,
					},
					Spec: swapi.ArtifactGeneratorSpec{
						Sources: []swapi.SourceReference{
							{
								Alias: gitKey.Name,
								Kind:  sourcev1.GitRepositoryKind,
								Name:  gitKey.Name,
							},
						},
					},
				}

				cleanup := func() {
					testClient.Delete(context.Background(), &sourcev1.GitRepository{
						ObjectMeta: metav1.ObjectMeta{Name: gitKey.Name, Namespace: gitKey.Namespace},
					})
				}

				return generator, cleanup
			},
			expectError: false,
			expectCount: 1,
		},
		{
			name: "handles explicit namespace in source reference",
			setupFunc: func() (*swapi.ArtifactGenerator, func()) {
				gitKey := client.ObjectKey{Name: "explicit-ns-git", Namespace: "default"}
				objKey := client.ObjectKey{Name: "explicit-ns-generator", Namespace: "default"}

				gitFiles := []testserver.File{
					{Name: "explicit-ns.yaml", Body: "explicit namespace content"},
				}

				if err := applyGitRepository(gitKey, "main@sha1:explicit", gitFiles); err != nil {
					t.Fatalf("Failed to apply git repository: %v", err)
				}

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      objKey.Name,
						Namespace: objKey.Namespace,
					},
					Spec: swapi.ArtifactGeneratorSpec{
						Sources: []swapi.SourceReference{
							{
								Alias:     gitKey.Name,
								Kind:      sourcev1.GitRepositoryKind,
								Name:      gitKey.Name,
								Namespace: gitKey.Namespace,
							},
						},
					},
				}

				cleanup := func() {
					testClient.Delete(context.Background(), &sourcev1.GitRepository{
						ObjectMeta: metav1.ObjectMeta{Name: gitKey.Name, Namespace: gitKey.Namespace},
					})
				}

				return generator, cleanup
			},
			expectError: false,
			expectCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generator, cleanup := tt.setupFunc()
			defer cleanup()

			reconciler := getArtifactGeneratorReconciler()
			tmpDir := t.TempDir()

			ctx := context.Background()
			result, err := reconciler.getSources(ctx, generator, tmpDir)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if len(result) != tt.expectCount {
					t.Errorf("Expected %d sources, got %d", tt.expectCount, len(result))
				}

				// Verify that the returned paths exist and correspond to the source aliases
				for alias, path := range result {
					if _, err := os.Stat(path); os.IsNotExist(err) {
						t.Errorf("Expected source path %s to exist for alias %s", path, alias)
					}

					// Verify the path structure matches expectations
					expectedPath := tmpDir + "/" + alias
					if path != expectedPath {
						t.Errorf("Expected path %s for alias %s, got %s", expectedPath, alias, path)
					}
				}
			}
		})
	}
}

func TestArtifactGeneratorReconciler_buildArtifact(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(t *testing.T) (*swapi.ArtifactGenerator, *swapi.OutputArtifact, map[string]string, string)
		expectError  bool
		validateFunc func(t *testing.T, artifact *meta.Artifact)
	}{
		{
			name: "successfully builds artifact with single source",
			setupFunc: func(t *testing.T) (*swapi.ArtifactGenerator, *swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "git-source")

				if err := os.MkdirAll(srcDir, 0o755); err != nil {
					t.Fatalf("Failed to create source dir: %v", err)
				}

				// Create test files in the source directory
				testFiles := map[string]string{
					"config.yaml":     "apiVersion: v1\nkind: ConfigMap",
					"deployment.yaml": "apiVersion: apps/v1\nkind: Deployment",
				}

				for filename, content := range testFiles {
					if err := os.WriteFile(filepath.Join(srcDir, filename), []byte(content), 0o644); err != nil {
						t.Fatalf("Failed to create test file %s: %v", filename, err)
					}
				}

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-generator",
						Namespace: "default",
					},
				}

				outputArtifact := &swapi.OutputArtifact{
					Name: "test-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@git-source/*.yaml",
							To:   "@artifact/manifests/",
						},
					},
				}

				sources := map[string]string{
					"git-source": srcDir,
				}

				return generator, outputArtifact, sources, tmpDir
			},
			expectError: false,
			validateFunc: func(t *testing.T, artifact *meta.Artifact) {
				if artifact == nil {
					t.Error("Expected artifact to be non-nil")
					return
				}

				if artifact.Path == "" {
					t.Error("Expected artifact path to be set")
				}

				if artifact.URL == "" {
					t.Error("Expected artifact URL to be set")
				}

				if artifact.Digest == "" {
					t.Error("Expected artifact digest to be set")
				}

				if artifact.Revision == "" {
					t.Error("Expected artifact revision to be set")
				}

				// Verify artifact file exists in storage
				reconciler := getArtifactGeneratorReconciler()
				storagePath := reconciler.Storage.LocalPath(*artifact)
				if _, err := os.Stat(storagePath); os.IsNotExist(err) {
					t.Errorf("Expected artifact file to exist at %s", storagePath)
				}
			},
		},
		{
			name: "builds artifact with multiple sources and operations",
			setupFunc: func(t *testing.T) (*swapi.ArtifactGenerator, *swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				gitDir := filepath.Join(tmpDir, "git")
				ociDir := filepath.Join(tmpDir, "oci")

				for _, dir := range []string{gitDir, ociDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				// Create files in git source
				if err := os.WriteFile(filepath.Join(gitDir, "app.yaml"), []byte("apiVersion: v1\nkind: Service"), 0o644); err != nil {
					t.Fatalf("Failed to create git file: %v", err)
				}

				// Create files in oci source
				if err := os.WriteFile(filepath.Join(ociDir, "config.json"), []byte(`{"key": "value"}`), 0o644); err != nil {
					t.Fatalf("Failed to create oci file: %v", err)
				}

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "multi-source-generator",
						Namespace: "default",
					},
				}

				outputArtifact := &swapi.OutputArtifact{
					Name: "multi-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@git/*.yaml",
							To:   "@artifact/k8s/",
						},
						{
							From: "@oci/*.json",
							To:   "@artifact/config/",
						},
					},
				}

				sources := map[string]string{
					"git": gitDir,
					"oci": ociDir,
				}

				return generator, outputArtifact, sources, tmpDir
			},
			expectError: false,
			validateFunc: func(t *testing.T, artifact *meta.Artifact) {
				if artifact == nil {
					t.Error("Expected artifact to be non-nil")
					return
				}

				if artifact.Path == "" {
					t.Error("Expected artifact path to be set")
				}

				if artifact.Digest == "" {
					t.Error("Expected artifact digest to be set")
				}
			},
		},
		{
			name: "builds artifact with default revision",
			setupFunc: func(t *testing.T) (*swapi.ArtifactGenerator, *swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")

				if err := os.MkdirAll(srcDir, 0o755); err != nil {
					t.Fatalf("Failed to create source dir: %v", err)
				}

				if err := os.WriteFile(filepath.Join(srcDir, "test.txt"), []byte("test content"), 0o644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "custom-revision-generator",
						Namespace: "default",
					},
				}

				outputArtifact := &swapi.OutputArtifact{
					Name: "custom-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@source/test.txt",
							To:   "@artifact/",
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return generator, outputArtifact, sources, tmpDir
			},
			expectError: false,
			validateFunc: func(t *testing.T, artifact *meta.Artifact) {
				if artifact == nil {
					t.Error("Expected artifact to be non-nil")
					return
				}

				if !strings.HasPrefix(artifact.Revision, "latest@") {
					t.Errorf("Expected revision to start with 'latest@', got '%s'", artifact.Revision)
				}
			},
		},
		{
			name: "fails when copy operation source alias not found",
			setupFunc: func(t *testing.T) (*swapi.ArtifactGenerator, *swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "error-generator",
						Namespace: "default",
					},
				}

				outputArtifact := &swapi.OutputArtifact{
					Name: "error-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@nonexistent/file.txt",
							To:   "@artifact/",
						},
					},
				}

				sources := map[string]string{
					"existing": "/some/path",
				}

				return generator, outputArtifact, sources, tmpDir
			},
			expectError: true,
			validateFunc: func(t *testing.T, artifact *meta.Artifact) {
				// No validation needed for error case
			},
		},
		{
			name: "fails when no files match copy pattern",
			setupFunc: func(t *testing.T) (*swapi.ArtifactGenerator, *swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "empty-source")

				if err := os.MkdirAll(srcDir, 0o755); err != nil {
					t.Fatalf("Failed to create source dir: %v", err)
				}

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "empty-generator",
						Namespace: "default",
					},
				}

				outputArtifact := &swapi.OutputArtifact{
					Name: "empty-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@empty-source/*.nonexistent",
							To:   "@artifact/",
						},
					},
				}

				sources := map[string]string{
					"empty-source": srcDir,
				}

				return generator, outputArtifact, sources, tmpDir
			},
			expectError: true,
			validateFunc: func(t *testing.T, artifact *meta.Artifact) {
				// No validation needed for error case
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generator, outputArtifact, sources, tmpDir := tt.setupFunc(t)

			reconciler := getArtifactGeneratorReconciler()

			artifact, err := reconciler.buildArtifact(generator, outputArtifact, sources, tmpDir)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				} else {
					tt.validateFunc(t, artifact)
				}
			}
		})
	}
}
