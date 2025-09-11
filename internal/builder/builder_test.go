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
	. "github.com/onsi/gomega"

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

				setupDirs(t, srcDir, workspaceDir)
				createFile(t, srcDir, "config.yaml", "apiVersion: v1\nkind: ConfigMap")

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

				setupDirs(t, gitDir, ociDir, workspaceDir)
				createFile(t, gitDir, "app.yaml", "apiVersion: apps/v1")
				createFile(t, ociDir, "config.json", `{"version": "1.0"}`)

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

				setupDirs(t, srcDir, workspaceDir)
				for _, file := range []string{"deployment.yaml", "service.yaml", "configmap.yaml"} {
					createFile(t, srcDir, file, "apiVersion: v1")
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

				setupDirs(t, filepath.Join(srcDir, "subdir"), workspaceDir)
				createFile(t, srcDir, "root.yaml", "root: content")
				createFile(t, filepath.Join(srcDir, "subdir"), "nested.yaml", "nested: content")

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

				setupDirs(t, filepath.Join(srcDir, "subdir"), workspaceDir)
				createFile(t, srcDir, "root.yaml", "root: content")
				createFile(t, filepath.Join(srcDir, "subdir"), "nested.yaml", "nested: content")

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

				setupDirs(t, filepath.Join(src1Dir, "config"), filepath.Join(src2Dir, "config"), workspaceDir)
				createFile(t, filepath.Join(src1Dir, "config"), "app.yaml", "source1: app")
				createFile(t, filepath.Join(src1Dir, "config"), "database.yaml", "source1: db")
				createFile(t, filepath.Join(src2Dir, "config"), "app.yaml", "source2: app")
				createFile(t, filepath.Join(src2Dir, "config"), "network.yaml", "source2: net")

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
				configDir := filepath.Join(srcDir, "config")

				setupDirs(t, configDir, workspaceDir)
				createFile(t, configDir, "app.yaml", "app: config")
				createFile(t, configDir, "db.yaml", "db: config")

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
				configDir := filepath.Join(srcDir, "config")

				setupDirs(t, configDir, workspaceDir)
				createFile(t, configDir, "app.yaml", "app: config")
				createFile(t, configDir, "db.yaml", "db: config")

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

				setupDirs(t, srcDir, workspaceDir)
				createFile(t, srcDir, "config.yaml", "a: test")

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
				configDir := filepath.Join(srcDir, "config")
				subDir := filepath.Join(configDir, "subdir")

				setupDirs(t, subDir, workspaceDir)
				createFile(t, configDir, "app.yaml", "app: config")
				createFile(t, subDir, "db.yaml", "db: config")

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
				workspaceDir := filepath.Join(tmpDir, "workspace")

				repoDir := filepath.Join(tmpDir, "repo")
				chartDir := filepath.Join(tmpDir, "chart")
				chartsDir := filepath.Join(repoDir, "charts", "podinfo")

				setupDirs(t, chartsDir, chartDir, workspaceDir)
				createFile(t, chartsDir, "values-prod.yaml", "env: production")
				createFile(t, chartDir, "Chart.yaml", "name: podinfo")
				createFile(t, chartDir, "values.yaml", "name: podinfo")

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

				setupDirs(t, srcDir, filepath.Join(srcDir, "subdir"), workspaceDir)
				createFile(t, srcDir, "file1.txt", "content1")
				createFile(t, filepath.Join(srcDir, "subdir"), "file2.txt", "content2")

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
			g := NewWithT(t)
			spec, sources, workspace := tt.setupFunc(t)
			artifact, err := testBuilder.Build(context.Background(), spec, sources, "test-namespace", workspace)
			g.Expect(err).ToNot(HaveOccurred())
			stagingDir := filepath.Join(workspace, spec.Name)
			tt.validateFunc(t, artifact, stagingDir)
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

				setupDirs(t, srcDir, workspaceDir)

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

				setupDirs(t, srcDir, workspaceDir)

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

				setupDirs(t, srcDir, workspaceDir)

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
				tmpDir := t.TempDir()
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
					"source": tmpDir,
				}

				return spec, sources, workspaceDir
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			spec, sources, workspace := tt.setupFunc(t)

			_, err := testBuilder.Build(context.Background(), spec, sources, "test-namespace", workspace)
			if err == nil {
				t.Logf("Staging directory contents:")
				walkErr := filepath.Walk(filepath.Join(workspace, spec.Name), func(path string, info os.FileInfo, err error) error {
					g.Expect(err).To(BeNil())
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
				return
			}

			g.Expect(err.Error()).To(ContainSubstring(tt.expectedError))
		})
	}
}

func TestBuildWithExcludes(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string)
		validateFunc  func(t *testing.T, artifact *meta.Artifact, stagingDir string)
		expectError   bool
		expectedError string
	}{
		{
			name: "exclude single file extension",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, srcDir, workspaceDir)

				// Create files with different extensions
				createFile(t, srcDir, "app.yaml", "yaml content")
				createFile(t, srcDir, "config.json", "json content")
				createFile(t, srcDir, "README.md", "markdown content")
				createFile(t, srcDir, "notes.md", "more markdown")

				spec := &swapi.OutputArtifact{
					Name:     "test-artifact",
					Revision: "v1.0.0",
					Copy: []swapi.CopyOperation{
						{
							From:    "@source/**",
							To:      "@artifact/",
							Exclude: []string{"*.md"},
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				g := NewWithT(t)
				artifactDir := filepath.Join(stagingDir, "test-artifact")

				// Should have yaml and json files
				g.Expect(filepath.Join(artifactDir, "app.yaml")).To(BeAnExistingFile())
				g.Expect(filepath.Join(artifactDir, "config.json")).To(BeAnExistingFile())

				// Should NOT have markdown files
				g.Expect(filepath.Join(artifactDir, "README.md")).ToNot(BeAnExistingFile())
				g.Expect(filepath.Join(artifactDir, "notes.md")).ToNot(BeAnExistingFile())
			},
		},
		{
			name: "exclude directory recursively",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, srcDir, workspaceDir)

				// Create files in various directories
				createFile(t, srcDir, "app.yaml", "main app config")
				createDir(t, srcDir, "testdata")
				createFile(t, filepath.Join(srcDir, "testdata"), "test.yaml", "test config")
				createFile(t, filepath.Join(srcDir, "testdata"), "fixture.json", "test fixture")

				createDir(t, srcDir, "configs")
				createFile(t, filepath.Join(srcDir, "configs"), "prod.yaml", "prod config")

				spec := &swapi.OutputArtifact{
					Name:     "test-artifact",
					Revision: "v1.0.0",
					Copy: []swapi.CopyOperation{
						{
							From:    "@source/**",
							To:      "@artifact/",
							Exclude: []string{"**/testdata/**"},
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				g := NewWithT(t)
				artifactDir := filepath.Join(stagingDir, "test-artifact")

				// Should have main app and configs
				g.Expect(filepath.Join(artifactDir, "app.yaml")).To(BeAnExistingFile())
				g.Expect(filepath.Join(artifactDir, "configs", "prod.yaml")).To(BeAnExistingFile())

				// Should NOT have testdata directory or its contents
				g.Expect(filepath.Join(artifactDir, "testdata")).ToNot(BeADirectory())
				g.Expect(filepath.Join(artifactDir, "testdata", "test.yaml")).ToNot(BeAnExistingFile())
				g.Expect(filepath.Join(artifactDir, "testdata", "fixture.json")).ToNot(BeAnExistingFile())
			},
		},
		{
			name: "multiple exclude patterns",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, srcDir, workspaceDir)

				// Create various files and directories
				createFile(t, srcDir, "app.yaml", "app config")
				createFile(t, srcDir, "README.md", "documentation")
				createFile(t, srcDir, "temp.tmp", "temporary file")
				createFile(t, srcDir, ".hidden", "hidden file")

				createDir(t, srcDir, "docs")
				createFile(t, filepath.Join(srcDir, "docs"), "guide.md", "user guide")

				createDir(t, srcDir, "tests")
				createFile(t, filepath.Join(srcDir, "tests"), "test.go", "test file")

				spec := &swapi.OutputArtifact{
					Name: "test-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@source/**",
							To:   "@artifact/",
							Exclude: []string{
								"*.md",        // Exclude markdown files
								"*.tmp",       // Exclude temp files
								"**/.*",       // Exclude hidden files
								"**/tests/**", // Exclude tests directory
							},
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				g := NewWithT(t)
				artifactDir := filepath.Join(stagingDir, "test-artifact")

				// Should have app.yaml
				g.Expect(filepath.Join(artifactDir, "app.yaml")).To(BeAnExistingFile())

				// Should NOT have excluded files
				g.Expect(filepath.Join(artifactDir, "README.md")).ToNot(BeAnExistingFile())
				g.Expect(filepath.Join(artifactDir, "temp.tmp")).ToNot(BeAnExistingFile())
				g.Expect(filepath.Join(artifactDir, ".hidden")).ToNot(BeAnExistingFile())
				g.Expect(filepath.Join(artifactDir, "tests")).ToNot(BeADirectory())
				g.Expect(filepath.Join(artifactDir, "tests", "test.go")).ToNot(BeAnExistingFile())

				// docs directory should exist but be empty (guide.md was excluded)
				g.Expect(filepath.Join(artifactDir, "docs")).To(BeADirectory())
				g.Expect(filepath.Join(artifactDir, "docs", "guide.md")).ToNot(BeAnExistingFile())
			},
		},
		{
			name: "exclude with glob patterns",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, srcDir, workspaceDir)

				// Create files matching glob pattern
				createFile(t, srcDir, "app.yaml", "app config")
				createFile(t, srcDir, "config.yaml", "config")
				createFile(t, srcDir, "secret.yaml", "secret")
				createFile(t, srcDir, "other.json", "other config")

				spec := &swapi.OutputArtifact{
					Name:     "test-artifact",
					Revision: "v1.0.0",
					Copy: []swapi.CopyOperation{
						{
							From:    "@source/*.yaml",
							To:      "@artifact/configs/",
							Exclude: []string{"secret.yaml"},
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				g := NewWithT(t)
				artifactDir := filepath.Join(stagingDir, "test-artifact")

				// Should have non-secret yaml files
				g.Expect(filepath.Join(artifactDir, "configs", "app.yaml")).To(BeAnExistingFile())
				g.Expect(filepath.Join(artifactDir, "configs", "config.yaml")).To(BeAnExistingFile())

				// Should NOT have secret.yaml
				g.Expect(filepath.Join(artifactDir, "configs", "secret.yaml")).ToNot(BeAnExistingFile())

				// Should NOT have json file (not matched by glob)
				g.Expect(filepath.Join(artifactDir, "configs", "other.json")).ToNot(BeAnExistingFile())
			},
		},
		{
			name: "exclude single file copy",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, srcDir, workspaceDir)

				createFile(t, srcDir, "secret.yaml", "secret content")

				spec := &swapi.OutputArtifact{
					Name:     "test-artifact",
					Revision: "v1.0.0",
					Copy: []swapi.CopyOperation{
						{
							From:    "@source/secret.yaml",
							To:      "@artifact/config.yaml",
							Exclude: []string{"secret.yaml"},
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				g := NewWithT(t)
				artifactDir := filepath.Join(stagingDir, "test-artifact")

				// Should NOT have any files (excluded single file)
				g.Expect(filepath.Join(artifactDir, "config.yaml")).ToNot(BeAnExistingFile())

				// The artifact directory should be empty or contain only directories
				entries, err := os.ReadDir(artifactDir)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(entries).To(HaveLen(0))
			},
		},
		{
			name: "all files excluded - error",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, srcDir, workspaceDir)

				createFile(t, srcDir, "test.md", "test file")
				createFile(t, srcDir, "other.md", "other file")

				spec := &swapi.OutputArtifact{
					Name:     "test-artifact",
					Revision: "v1.0.0",
					Copy: []swapi.CopyOperation{
						{
							From:    "@source/*.md",
							To:      "@artifact/",
							Exclude: []string{"*.md"},
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				// This test expects an error, so validateFunc won't be called
			},
			expectError:   true,
			expectedError: "all files matching pattern",
		},
		{
			name: "invalid exclude - error",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				spec := &swapi.OutputArtifact{
					Name:     "test-artifact",
					Revision: "v1.0.0",
					Copy: []swapi.CopyOperation{
						{
							From:    "@source/*.md",
							To:      "@artifact/",
							Exclude: []string{"[*.md"},
						},
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *meta.Artifact, stagingDir string) {
				// This test expects an error, so validateFunc won't be called
			},
			expectError:   true,
			expectedError: "invalid exclude pattern",
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Setup test data
			spec, sources, workspaceDir := tt.setupFunc(t)

			// Use the global test builder
			artifact, err := testBuilder.Build(context.TODO(), spec, sources, fmt.Sprintf("exclude-%d", i), workspaceDir)

			if tt.expectError {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring(tt.expectedError))
				return
			}

			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(artifact).ToNot(BeNil())

			// Validate the result
			tt.validateFunc(t, artifact, workspaceDir)
		})
	}
}
