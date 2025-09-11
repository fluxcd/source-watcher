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
	"strings"
	"testing"

	"github.com/fluxcd/pkg/apis/meta"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

func TestBuild(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string)
		validateFunc func(t *testing.T, artifact *meta.Artifact, stagingDir string)
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
					Name: "cp-single-file",
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
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
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

				files := map[string]string{
					filepath.Join(gitDir, "app.yaml"):    "apiVersion: apps/v1",
					filepath.Join(ociDir, "config.json"): `{"version": "1.0"}`,
				}
				for filePath, content := range files {
					if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
						t.Fatalf("Failed to create file %s: %v", filePath, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name: "cp-multi-source",
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
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				expectedRevision := fmt.Sprintf("latest@%s", artifact.Digest)
				if artifact.Revision != expectedRevision {
					t.Errorf("Expected revision '%s', got '%s'", expectedRevision, artifact.Revision)
				}
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

				for _, file := range []string{"deployment.yaml", "service.yaml", "configmap.yaml"} {
					if err := os.WriteFile(filepath.Join(srcDir, file), []byte("apiVersion: v1"), 0o644); err != nil {
						t.Fatalf("Failed to create file %s: %v", file, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name:     "cp-glob-pattern",
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
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
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

				files := map[string]string{
					filepath.Join(srcDir, "root.yaml"):             "root: content",
					filepath.Join(srcDir, "subdir", "nested.yaml"): "nested: content",
				}
				for filePath, content := range files {
					if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
						t.Fatalf("Failed to create file %s: %v", filePath, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name: "cp-root-to-root",
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
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "root.yaml"):             "root: content",
					filepath.Join(stagingDir, "subdir", "nested.yaml"): "nested: content",
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

				files := map[string]string{
					filepath.Join(srcDir, "root.yaml"):             "root: content",
					filepath.Join(srcDir, "subdir", "nested.yaml"): "nested: content",
				}
				for filePath, content := range files {
					if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
						t.Fatalf("Failed to create file %s: %v", filePath, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name: "cp-recursive",
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
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "root.yaml"):             "root: content",
					filepath.Join(stagingDir, "subdir", "nested.yaml"): "nested: content",
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

				// Create files in both sources
				files := map[string]string{
					filepath.Join(src1Dir, "config", "app.yaml"):      "source1: app",
					filepath.Join(src1Dir, "config", "database.yaml"): "source1: db",
					filepath.Join(src2Dir, "config", "app.yaml"):      "source2: app",
					filepath.Join(src2Dir, "config", "network.yaml"):  "source2: net",
				}
				for filePath, content := range files {
					if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
						t.Fatalf("Failed to create file %s: %v", filePath, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name: "cp-subdir-overwrite",
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
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				expectedFiles := map[string]string{
					// app.yaml should be overwritten by source2
					filepath.Join(stagingDir, "config", "app.yaml"): "source2: app",
					// database.yaml should remain from source1 (not overwritten since source2 doesn't have it)
					filepath.Join(stagingDir, "config", "database.yaml"): "source1: db",
					// network.yaml should be new from source2
					filepath.Join(stagingDir, "config", "network.yaml"): "source2: net",
				}

				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "directory copy without trailing slash creates subdirectory",
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
				files := map[string]string{
					filepath.Join(configDir, "app.yaml"): "app: config",
					filepath.Join(configDir, "db.yaml"):  "db: config",
				}
				for filePath, content := range files {
					if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
						t.Fatalf("Failed to create file %s: %v", filePath, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name: "cp-dir-no-slash",
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
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				// Since destination always exists, cp creates config as subdirectory
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "dest", "config", "app.yaml"): "app: config",
					filepath.Join(stagingDir, "dest", "config", "db.yaml"):  "db: config",
				}
				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "directory copy with trailing slash creates subdirectory",
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
				files := map[string]string{
					filepath.Join(configDir, "app.yaml"): "app: config",
					filepath.Join(configDir, "db.yaml"):  "db: config",
				}
				for filePath, content := range files {
					if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
						t.Fatalf("Failed to create file %s: %v", filePath, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name: "cp-dir-slash",
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
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				// Should create dest/config/ with contents
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "dest", "config", "app.yaml"): "app: config",
					filepath.Join(stagingDir, "dest", "config", "db.yaml"):  "db: config",
				}
				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "file copy with trailing slash uses source filename",
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
				if err := os.WriteFile(filepath.Join(srcDir, "config.yaml"), []byte("a: test"), 0o644); err != nil {
					t.Fatalf("Failed to create config.yaml: %v", err)
				}

				spec := &swapi.OutputArtifact{
					Name: "cp-file-to-dir",
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
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "dest", "config.yaml"): "a: test",
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
				files := map[string]string{
					filepath.Join(configDir, "app.yaml"): "app: config",
					filepath.Join(subDir, "db.yaml"):     "db: config",
				}
				for filePath, content := range files {
					if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
						t.Fatalf("Failed to create file %s: %v", filePath, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name: "cp-glob-prefix-strip",
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
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "dest", "app.yaml"):          "app: config",
					filepath.Join(stagingDir, "dest", "subdir", "db.yaml"): "db: config",
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
				if err := os.WriteFile(valuesFile, []byte("env: production"), 0o644); err != nil {
					t.Fatalf("Failed to create values file: %v", err)
				}

				// Create chart content
				files := map[string]string{
					filepath.Join(chartDir, "Chart.yaml"):  "name: podinfo",
					filepath.Join(chartDir, "values.yaml"): "name: podinfo",
				}
				for filePath, content := range files {
					if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
						t.Fatalf("Failed to create file %s: %v", filePath, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name:     "cp-file-to-file",
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
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "Chart.yaml"):             "name: podinfo",
					filepath.Join(stagingDir, "podinfo", "values.yaml"): "env: production",
				}
				verifyContents(t, testStorage, artifact, stagingDir, expectedFiles)
			},
		},
		{
			name: "file copy should detect when destination is a directory",
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
					Name: "cp-file-to-existing-dir",
					Copy: []swapi.CopyOperation{
						{
							From: "@source/subdir/",
							To:   "@artifact/",
						},
						{
							From: "@source/file1.txt",
							To:   "@artifact/subdir",
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "subdir", "file1.txt"): "content1",
					filepath.Join(stagingDir, "subdir", "file2.txt"): "content2",
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
				stagingDir := filepath.Join(workspace, spec.Name)
				tt.validateFunc(t, artifact, stagingDir)
			}
		})
	}
}

func TestBuildErrors(t *testing.T) {
	tests := []struct {
		name          string
		expectedError string
		setupFunc     func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string)
	}{
		{
			name:          "error when source file does not exist",
			expectedError: "does not exist",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				for _, dir := range []string{srcDir, workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name: "error-source-file",
					Copy: []swapi.CopyOperation{
						{
							From: "@source/test.yaml",
							To:   "@artifact/",
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
			name:          "error when glob pattern matches no files",
			expectedError: "no files match pattern",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				for _, dir := range []string{srcDir, workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name: "error-glob-match",
					Copy: []swapi.CopyOperation{
						{
							From: "@source/*.yaml",
							To:   "@artifact/",
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
			name:          "error when invalid glob pattern is used",
			expectedError: "syntax error in pattern",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				for _, dir := range []string{srcDir, workspaceDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				spec := &swapi.OutputArtifact{
					Name: "error-glob-pattern",
					Copy: []swapi.CopyOperation{
						{
							// Invalid glob pattern with unmatched bracket
							From: "@source/[",
							To:   "@artifact/",
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
			name:          "error when source alias not found",
			expectedError: "source alias 'missing' not found",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				workspaceDir := filepath.Join(tmpDir, "workspace")

				if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
					t.Fatalf("Failed to create dir %s: %v", workspaceDir, err)
				}

				spec := &swapi.OutputArtifact{
					Name: "error-alias",
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
		{
			name:          "error when staging directory creation fails",
			expectedError: "failed to create staging dir",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				// Use a non-existent parent directory to cause mkdir failure
				workspaceDir := "/nonexistent/workspace"

				spec := &swapi.OutputArtifact{
					Name: "error-mkdir",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, sources, workspace := tt.setupFunc(t)

			_, err := testBuilder.Build(context.Background(), spec, sources, "test-namespace", workspace)
			if err == nil {
				t.Logf("Staging directory contents:")
				walkErr := filepath.Walk(filepath.Join(workspace, spec.Name), func(path string, info os.FileInfo, err error) error {
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
				if walkErr != nil {
					t.Logf("Error walking staging directory: %v", walkErr)
				}

				t.Errorf("Expected error containing '%s' but got none", tt.expectedError)
				return
			}

			if !strings.Contains(err.Error(), tt.expectedError) {
				t.Errorf("Expected error containing '%s', but got: %v", tt.expectedError, err)
			}
		})
	}
}
