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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/artifact/storage"
	"github.com/fluxcd/pkg/tar"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

func TestBuild(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string)
		validateFunc func(t *testing.T, artifact *meta.Artifact, workspace string)
	}{
		{
			name: "build artifact with single file copy",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				for _, dir := range []string{srcDir, workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				testFile := filepath.Join(srcDir, "config.yaml")
				if err := os.WriteFile(testFile, []byte("apiVersion: v1\nkind: ConfigMap"), 0o644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}

				spec := &swapi.OutputArtifact{
					Name: "test-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@source/config.yaml",
							To:   "@artifact/",
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, workspace string) {
				if artifact == nil {
					t.Fatal("Expected artifact to be returned")
				}
				if artifact.Path == "" {
					t.Error("Expected artifact path to be set")
				}
				if artifact.Revision == "" {
					t.Error("Expected revision to be set")
				}
				if artifact.URL == "" {
					t.Error("Expected artifact URL to be set")
				}

				// Verify staging directory files and tar.gz archive contents
				stagingDir := filepath.Join(workspace, "test-artifact")
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "config.yaml"): "apiVersion: v1\nkind: ConfigMap",
				}
				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "build artifact with multiple copy operations",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				gitDir := filepath.Join(tmpDir, "git")
				ociDir := filepath.Join(tmpDir, "oci")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				for _, dir := range []string{gitDir, ociDir, workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				if err := os.WriteFile(filepath.Join(gitDir, "app.yaml"), []byte("apiVersion: apps/v1"), 0o644); err != nil {
					t.Fatalf("Failed to create git file: %v", err)
				}
				if err := os.WriteFile(filepath.Join(ociDir, "config.json"), []byte(`{"version": "1.0"}`), 0o644); err != nil {
					t.Fatalf("Failed to create oci file: %v", err)
				}

				spec := &swapi.OutputArtifact{
					Name: "multi-source-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@git/app.yaml",
							To:   "@artifact/manifests/",
						},
						{
							From: "@oci/config.json",
							To:   "@artifact/config/",
						},
					},
				}

				sources := map[string]string{
					"git": gitDir,
					"oci": ociDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, workspace string) {
				expectedRevision := fmt.Sprintf("latest@%s", artifact.Digest)
				if artifact.Revision != expectedRevision {
					t.Errorf("Expected revision '%s', got '%s'", expectedRevision, artifact.Revision)
				}
				stagingDir := filepath.Join(workspace, "multi-source-artifact")
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "manifests", "app.yaml"): "apiVersion: apps/v1",
					filepath.Join(stagingDir, "config", "config.json"): `{"version": "1.0"}`,
				}
				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "build artifact with glob patterns",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				for _, dir := range []string{srcDir, workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				yamlFiles := []string{"deployment.yaml", "service.yaml", "configmap.yaml"}
				for _, file := range yamlFiles {
					if err := os.WriteFile(filepath.Join(srcDir, file), []byte("apiVersion: v1"), 0o644); err != nil {
						t.Fatalf("Failed to create file %s: %v", file, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name:     "glob-artifact",
					Revision: "@source",
					Copy: []swapi.CopyOperation{
						{
							From: "@source/*.yaml",
							To:   "@artifact/manifests/",
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, workspace string) {
				stagingDir := filepath.Join(workspace, "glob-artifact")
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "manifests", "deployment.yaml"): "apiVersion: v1",
					filepath.Join(stagingDir, "manifests", "service.yaml"):    "apiVersion: v1",
					filepath.Join(stagingDir, "manifests", "configmap.yaml"):  "apiVersion: v1",
				}
				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "build artifact with root directory copy",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				for _, dir := range []string{filepath.Join(srcDir, "subdir"), workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				if err := os.WriteFile(filepath.Join(srcDir, "root.yaml"), []byte("root content"), 0o644); err != nil {
					t.Fatalf("Failed to create root file: %v", err)
				}
				if err := os.WriteFile(filepath.Join(srcDir, "subdir", "nested.yaml"), []byte("nested content"), 0o644); err != nil {
					t.Fatalf("Failed to create nested file: %v", err)
				}

				spec := &swapi.OutputArtifact{
					Name: "root-to-root",
					Copy: []swapi.CopyOperation{
						{
							From: "@source/",
							To:   "@artifact/",
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, workspace string) {
				stagingDir := filepath.Join(workspace, "recursive-artifact")
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "root.yaml"):             "root content",
					filepath.Join(stagingDir, "subdir", "nested.yaml"): "nested content",
				}
				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "build artifact with recursive directory copy",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				for _, dir := range []string{filepath.Join(srcDir, "subdir"), workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				if err := os.WriteFile(filepath.Join(srcDir, "root.yaml"), []byte("root content"), 0o644); err != nil {
					t.Fatalf("Failed to create root file: %v", err)
				}
				if err := os.WriteFile(filepath.Join(srcDir, "subdir", "nested.yaml"), []byte("nested content"), 0o644); err != nil {
					t.Fatalf("Failed to create nested file: %v", err)
				}

				spec := &swapi.OutputArtifact{
					Name: "recursive-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@source/**",
							To:   "@artifact/",
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, workspace string) {
				stagingDir := filepath.Join(workspace, "recursive-artifact")
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "root.yaml"):             "root content",
					filepath.Join(stagingDir, "subdir", "nested.yaml"): "nested content",
				}
				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "copy operations overwrite entire subdirectories",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				src1Dir := filepath.Join(tmpDir, "source1")
				src2Dir := filepath.Join(tmpDir, "source2")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				// Create subdirectories in both sources
				for _, dir := range []string{
					filepath.Join(src1Dir, "config"),
					filepath.Join(src2Dir, "config"),
					workspaceDir,
				} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				// Create files in source1/config/
				if err := os.WriteFile(filepath.Join(src1Dir, "config", "app.yaml"), []byte("source1 app config"), 0o644); err != nil {
					t.Fatalf("Failed to create source1 config file: %v", err)
				}
				if err := os.WriteFile(filepath.Join(src1Dir, "config", "database.yaml"), []byte("source1 database config"), 0o644); err != nil {
					t.Fatalf("Failed to create source1 database file: %v", err)
				}

				// Create different files in source2/config/ (some overlapping, some new)
				if err := os.WriteFile(filepath.Join(src2Dir, "config", "app.yaml"), []byte("source2 app config - OVERWRITTEN"), 0o644); err != nil {
					t.Fatalf("Failed to create source2 app file: %v", err)
				}
				if err := os.WriteFile(filepath.Join(src2Dir, "config", "network.yaml"), []byte("source2 network config - NEW"), 0o644); err != nil {
					t.Fatalf("Failed to create source2 network file: %v", err)
				}

				spec := &swapi.OutputArtifact{
					Name: "subdir-overwrite-artifact",
					Copy: []swapi.CopyOperation{
						{
							// Copy entire config directory from source1
							From: "@source1/**",
							To:   "@artifact/",
						},
						{
							// Copy entire config directory from source2 - should merge/overwrite
							From: "@source2/**",
							To:   "@artifact/",
						},
					},
				}

				sources := map[string]string{
					"source1": src1Dir,
					"source2": src2Dir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, workspace string) {
				stagingDir := filepath.Join(workspace, "subdir-overwrite-artifact")
				expectedFiles := map[string]string{
					// app.yaml should be overwritten by source2
					filepath.Join(stagingDir, "config", "app.yaml"): "source2 app config - OVERWRITTEN",
					// database.yaml should remain from source1 (not overwritten since source2 doesn't have it)
					filepath.Join(stagingDir, "config", "database.yaml"): "source1 database config",
					// network.yaml should be new from source2
					filepath.Join(stagingDir, "config", "network.yaml"): "source2 network config - NEW",
				}

				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "cp-like directory copy without trailing slash creates subdirectory",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				// Create source directory with files
				configDir := filepath.Join(srcDir, "config")
				for _, dir := range []string{configDir, workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				// Create files in config directory
				if err := os.WriteFile(filepath.Join(configDir, "app.yaml"), []byte("app config"), 0o644); err != nil {
					t.Fatalf("Failed to create app.yaml: %v", err)
				}
				if err := os.WriteFile(filepath.Join(configDir, "db.yaml"), []byte("db config"), 0o644); err != nil {
					t.Fatalf("Failed to create db.yaml: %v", err)
				}

				spec := &swapi.OutputArtifact{
					Name: "cp-dir-test",
					Copy: []swapi.CopyOperation{
						{
							From: "@source/config",
							To:   "@artifact/dest",
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, workspace string) {
				// Since destination always exists, cp creates config as subdirectory
				stagingDir := filepath.Join(workspace, "cp-dir-test")
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "dest", "config", "app.yaml"): "app config",
					filepath.Join(stagingDir, "dest", "config", "db.yaml"):  "db config",
				}
				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "cp-like directory copy with trailing slash creates subdirectory",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				// Create source directory with files
				configDir := filepath.Join(srcDir, "config")
				for _, dir := range []string{configDir, workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				// Create files in config directory
				if err := os.WriteFile(filepath.Join(configDir, "app.yaml"), []byte("app config"), 0o644); err != nil {
					t.Fatalf("Failed to create app.yaml: %v", err)
				}
				if err := os.WriteFile(filepath.Join(configDir, "db.yaml"), []byte("db config"), 0o644); err != nil {
					t.Fatalf("Failed to create db.yaml: %v", err)
				}

				spec := &swapi.OutputArtifact{
					Name: "cp-dir-contents-test",
					Copy: []swapi.CopyOperation{
						{
							// cp -r config/ dest/ (should create dest/config/)
							From: "@source/config/",
							To:   "@artifact/dest/",
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, workspace string) {
				// Should create dest/config/ with contents
				stagingDir := filepath.Join(workspace, "cp-dir-contents-test")
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "dest", "config", "app.yaml"): "app config",
					filepath.Join(stagingDir, "dest", "config", "db.yaml"):  "db config",
				}
				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "cp-like file copy with trailing slash uses source filename",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				for _, dir := range []string{srcDir, workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				// Create source file
				if err := os.WriteFile(filepath.Join(srcDir, "config.yaml"), []byte("config content"), 0o644); err != nil {
					t.Fatalf("Failed to create config.yaml: %v", err)
				}

				spec := &swapi.OutputArtifact{
					Name: "cp-file-to-dir-test",
					Copy: []swapi.CopyOperation{
						{
							// cp config.yaml dest/ (should create dest/config.yaml)
							From: "@source/config.yaml",
							To:   "@artifact/dest/",
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, workspace string) {
				// Should create dest/config.yaml
				stagingDir := filepath.Join(workspace, "cp-file-to-dir-test")
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "dest", "config.yaml"): "config content",
				}
				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "recursive glob pattern strips directory prefix like cp",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				// Create config directory structure
				configDir := filepath.Join(srcDir, "config")
				subDir := filepath.Join(configDir, "subdir")
				for _, dir := range []string{subDir, workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				// Create files in config structure
				if err := os.WriteFile(filepath.Join(configDir, "app.yaml"), []byte("app config"), 0o644); err != nil {
					t.Fatalf("Failed to create app.yaml: %v", err)
				}
				if err := os.WriteFile(filepath.Join(subDir, "db.yaml"), []byte("db config"), 0o644); err != nil {
					t.Fatalf("Failed to create db.yaml: %v", err)
				}

				spec := &swapi.OutputArtifact{
					Name: "recursive-glob-test",
					Copy: []swapi.CopyOperation{
						{
							// Should behave like: cp -r ./config/** ./dest/
							// This strips the "config/" prefix from results
							From: "@source/config/**",
							To:   "@artifact/dest/",
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, workspace string) {
				stagingDir := filepath.Join(workspace, "recursive-glob-test")
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "dest", "app.yaml"):          "app config",
					filepath.Join(stagingDir, "dest", "subdir", "db.yaml"): "db config",
				}

				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "file-to-file copy should not treat destination as directory",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				repoDir := filepath.Join(tmpDir, "repo")
				chartDir := filepath.Join(tmpDir, "chart")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				chartsDir := filepath.Join(repoDir, "charts", "podinfo")
				for _, dir := range []string{chartsDir, chartDir, workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				// Create the source file that should be copied
				valuesFile := filepath.Join(chartsDir, "values-prod.yaml")
				if err := os.WriteFile(valuesFile, []byte("env: production\nreplicas: 3"), 0o644); err != nil {
					t.Fatalf("Failed to create values file: %v", err)
				}

				// Create chart content
				if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("name: podinfo"), 0o644); err != nil {
					t.Fatalf("Failed to create Chart.yaml: %v", err)
				}
				if err := os.WriteFile(filepath.Join(chartDir, "values.yaml"), []byte("name: podinfo"), 0o644); err != nil {
					t.Fatalf("Failed to create Chart.yaml: %v", err)
				}

				spec := &swapi.OutputArtifact{
					Name:     "podinfo",
					Revision: "@chart",
					Copy: []swapi.CopyOperation{
						{
							From: "@chart/**",
							To:   "@artifact/",
						},
						{
							From: "@repo/charts/podinfo/values-prod.yaml",
							To:   "@artifact/podinfo/values.yaml",
						},
					},
				}

				sources := map[string]string{
					"chart": chartDir,
					"repo":  repoDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, workspace string) {
				stagingDir := filepath.Join(workspace, "podinfo")
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "Chart.yaml"):             "name: podinfo",
					filepath.Join(stagingDir, "podinfo", "values.yaml"): "env: production\nreplicas: 3",
				}
				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, sources, workspace := tt.setupFunc(t)

			artifact, err := testBuilder.Build(context.Background(), spec, sources, "test-namespace", workspace)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			} else {
				tt.validateFunc(t, artifact, workspace)
			}
		})
	}
}

// verifyContents extracts and verifies the contents of a tar.gz artifact
// It takes the expected files from the staging directory and verifies they exist in the tar.gz
func verifyContents(t *testing.T, testStorage *storage.Storage, artifact *meta.Artifact, stagingDir string, expectedFiles map[string]string) {
	t.Helper()

	// Create a temporary directory for extraction
	extractDir := t.TempDir()

	// Get the full path to the artifact
	artifactPath := filepath.Join(testStorage.BasePath, artifact.Path)

	// Open the tar.gz file
	file, err := os.Open(artifactPath)
	if err != nil {
		t.Fatalf("Failed to open tar.gz %s: %v", artifactPath, err)
	}
	defer file.Close()

	// Extract the tar.gz file
	if err := tar.Untar(file, extractDir, tar.WithMaxUntarSize(-1)); err != nil {
		t.Fatalf("Failed to extract tar.gz %s: %v", artifactPath, err)
	}

	// Verify expected files exist with correct content by reading from staging directory
	for stagingPath, expectedContent := range expectedFiles {
		// Convert staging path to relative path within the tar
		relPath, err := filepath.Rel(stagingDir, stagingPath)
		if err != nil {
			t.Errorf("Failed to get relative path for %s: %v", stagingPath, err)
			continue
		}

		fullPath := filepath.Join(extractDir, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			t.Errorf("Expected file %s to exist in tar.gz but it doesn't", relPath)
			continue
		}

		content, err := os.ReadFile(fullPath)
		if err != nil {
			t.Errorf("Failed to read extracted file %s: %v", relPath, err)
			continue
		}

		if string(content) != expectedContent {
			t.Errorf("File %s: expected content '%s', got '%s'", relPath, expectedContent, string(content))
		}
	}
}

func TestBuildErrors(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string)
	}{
		{
			name: "error when copy operation fails",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				workspaceDir := filepath.Join(tmpDir, "workspace")

				if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
					t.Fatalf("Failed to create dir %s: %v", workspaceDir, err)
				}

				spec := &swapi.OutputArtifact{
					Name: "error-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@nonexistent/file.yaml",
							To:   "@artifact/",
						},
					},
				}

				sources := map[string]string{
					"source": "/nonexistent/path",
				}

				return spec, sources, workspaceDir
			},
		},
		{
			name: "error when destination file is a directory",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				for _, dir := range []string{srcDir, filepath.Join(srcDir, "subdir"), workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				// Create source files
				if err := os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("content1"), 0o644); err != nil {
					t.Fatalf("Failed to create source file: %v", err)
				}
				if err := os.WriteFile(filepath.Join(srcDir, "subdir", "file2.txt"), []byte("content2"), 0o644); err != nil {
					t.Fatalf("Failed to create source file: %v", err)
				}

				spec := &swapi.OutputArtifact{
					Name: "error-is-dir-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@source/subdir/",
							To:   "@artifact/",
						},
						{
							From: "@source/file1.txt",
							To:   "@artifact/subdir", // Error: destination is a directory
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
		},
		{
			name: "error when staging directory creation fails",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				// Use a non-existent parent directory to cause mkdir failure
				workspaceDir := "/nonexistent/workspace"

				spec := &swapi.OutputArtifact{
					Name: "mkdir-error-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@source/file.yaml",
							To:   "@artifact/",
						},
					},
				}

				sources := map[string]string{
					"source": "/tmp",
				}

				return spec, sources, workspaceDir
			},
		},
		{
			name: "error when source alias not found",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				workspaceDir := filepath.Join(tmpDir, "workspace")

				if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
					t.Fatalf("Failed to create dir %s: %v", workspaceDir, err)
				}

				spec := &swapi.OutputArtifact{
					Name: "missing-alias-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@missing/file.yaml",
							To:   "@artifact/",
						},
					},
				}

				sources := map[string]string{
					"source": tmpDir,
				}

				return spec, sources, workspaceDir
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, sources, workspace := tt.setupFunc(t)

			_, err := testBuilder.Build(context.Background(), spec, sources, "test-namespace", workspace)
			if err == nil {
				t.Logf("Staging directory contents:")
				err := filepath.Walk(filepath.Join(workspace, spec.Name), func(path string, info os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					relPath, _ := filepath.Rel(workspace, path)
					if info.IsDir() {
						t.Logf("+ %s/", relPath)
					} else {
						t.Logf("- %s", relPath)
					}
					return nil
				})
				if err != nil {
					t.Logf("Error walking staging directory: %v", err)
				}

				t.Errorf("Expected error but got none")
			}
		})
	}
}
