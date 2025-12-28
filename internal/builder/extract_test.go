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
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

func TestBuild_ExtractStrategy(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string)
		validateFunc  func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string)
		expectedError string
	}{
		{
			name: "Extract single referenced archive successfully",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, sourceDir, workspaceDir)

				// create a simple archive file with a single file
				tarballlPath := filepath.Join(sourceDir, "manifests.tgz")
				createTestTarball(tarballlPath)

				spec := &swapi.OutputArtifact{
					Name: "extract-simple-archive",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/manifests.tgz",
							To:       "@artifact/manifests",
							Strategy: swapi.ExtractStrategy,
						},
					},
				}
				sources := map[string]string{
					"source": sourceDir,
				}
				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string) {
				g := NewWithT(t)
				g.Expect(artifact).ToNot(BeNil())

				// Read the extracted config from staging directory
				stagingDir := filepath.Join(workspaceDir, "extract-simple-archive")

				configPath := filepath.Join(stagingDir, "manifests", "config.yaml")

				configContent, err := os.ReadFile(configPath)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(configContent).ToNot(BeEmpty())

				// Verify the merged YAML contains expected content
				g.Expect(configContent).To(MatchYAML(`
name: app
ports: [8080]
labels:
  env: dev
  keep: me
version: 1.0.1
replicas: 2
env: development
`))
				configPath = filepath.Join(stagingDir, "manifests", "prod", "config.yaml")
				configContent, err = os.ReadFile(configPath)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(configContent).ToNot(BeEmpty())

				// Verify the merged YAML contains expected content
				g.Expect(configContent).To(MatchYAML(`
name: app
ports: [8081]
labels:
  env: prod
  keep: me
version: 1.0.0
replicas: 5
env: production
`))
			},
			expectedError: "",
		},
		{
			name: "Extract multiple referenced archives using same source pattern",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, sourceDir, workspaceDir)

				tarballlPath := filepath.Join(sourceDir, "manifests1.tgz")
				createTestTarball(tarballlPath)

				tarballlPath2 := filepath.Join(sourceDir, "manifests2.tgz")
				createTestTarballForInt(tarballlPath2)

				spec := &swapi.OutputArtifact{
					Name: "extract-simple-archive",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/*.tg*",
							To:       "@artifact/",
							Strategy: swapi.ExtractStrategy,
						},
					},
				}
				sources := map[string]string{
					"source": sourceDir,
				}
				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string) {
				g := NewWithT(t)
				g.Expect(artifact).ToNot(BeNil())

				// Read the extracted config from staging directory
				stagingDir := filepath.Join(workspaceDir, "extract-simple-archive")
				configPath := filepath.Join(stagingDir, "config.yaml")

				configContent, err := os.ReadFile(configPath)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(configContent).ToNot(BeEmpty())

				// Verify the merged YAML contains expected content
				g.Expect(configContent).To(MatchYAML(`
name: app
ports: [8080]
labels:
  env: dev
  keep: me
version: 1.0.1
replicas: 2
env: development
`))
				configPath = filepath.Join(stagingDir, "prod", "config.yaml")
				configContent, err = os.ReadFile(configPath)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(configContent).ToNot(BeEmpty())

				// Verify the merged YAML contains expected content
				g.Expect(configContent).To(MatchYAML(`
name: app
ports: [8081]
labels:
  env: prod
  keep: me
version: 1.0.0
replicas: 5
env: production
`))

				// Verify files from second tarball in int/ subdirectory
				// The int/manifests2.tgz should extract to int/ preserving directory structure
				intConfigPath := filepath.Join(stagingDir, "int", "config.yaml")
				intConfigContent, err := os.ReadFile(intConfigPath)
				g.Expect(err).ToNot(HaveOccurred(), "int/config.yaml should exist from manifests2.tgz")
				g.Expect(intConfigContent).ToNot(BeEmpty())
				// Verify the merged YAML contains expected content
				g.Expect(intConfigContent).To(MatchYAML(`
name: app
ports: [8082]
labels:
  env: int
  keep: me
version: 1.0.0
replicas: 3
env: int
`))
			},
			expectedError: "",
		},
		{
			name: "Extract multiple referenced archives using recursive glob pattent matching",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				tarballDir1 := filepath.Join(sourceDir, "prod")
				tarballDir2 := filepath.Join(sourceDir, "int")
				workspaceDir := filepath.Join(tmpDir, "workspace")
				setupDirs(t, tarballDir1, tarballDir2, workspaceDir)

				tarballlPath := filepath.Join(tarballDir1, "manifests.tgz")
				createTestTarball(tarballlPath)

				tarballlPath2 := filepath.Join(tarballDir2, "manifests.tgz")
				createTestTarballForInt(tarballlPath2)

				spec := &swapi.OutputArtifact{
					Name: "extract-simple-archive",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/**",
							To:       "@artifact/",
							Strategy: swapi.ExtractStrategy,
						},
					},
				}
				sources := map[string]string{
					"source": sourceDir,
				}
				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string) {
				g := NewWithT(t)
				g.Expect(artifact).ToNot(BeNil())

				// Read the extracted config from staging directory
				stagingDir := filepath.Join(workspaceDir, "extract-simple-archive")
				configPath := filepath.Join(stagingDir, "config.yaml")

				configContent, err := os.ReadFile(configPath)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(configContent).ToNot(BeEmpty())

				// Verify the merged YAML contains expected content
				g.Expect(configContent).To(MatchYAML(`
name: app
ports: [8080]
labels:
  env: dev
  keep: me
version: 1.0.1
replicas: 2
env: development
`))
				configPath = filepath.Join(stagingDir, "prod", "config.yaml")
				configContent, err = os.ReadFile(configPath)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(configContent).ToNot(BeEmpty())

				// Verify the merged YAML contains expected content
				g.Expect(configContent).To(MatchYAML(`
name: app
ports: [8081]
labels:
  env: prod
  keep: me
version: 1.0.0
replicas: 5
env: production
`))

				// Verify files from second tarball in int/ subdirectory
				// The int/manifests2.tgz should extract to int/ preserving directory structure
				intConfigPath := filepath.Join(stagingDir, "int", "config.yaml")
				intConfigContent, err := os.ReadFile(intConfigPath)
				g.Expect(err).ToNot(HaveOccurred(), "int/config.yaml should exist from manifests2.tgz")
				g.Expect(intConfigContent).ToNot(BeEmpty())
				// Verify the merged YAML contains expected content
				g.Expect(intConfigContent).To(MatchYAML(`
name: app
ports: [8082]
labels:
  env: int
  keep: me
version: 1.0.0
replicas: 3
env: int
`))
			},
			expectedError: "",
		},
		{
			name: "Extract with invalid tarball fails gracefully",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")
				setupDirs(t, sourceDir, workspaceDir)

				// Create an invalid tarball (text file with .tgz extension)
				createFile(t, sourceDir, "invalid.tgz", "this is not a tarball")

				spec := &swapi.OutputArtifact{
					Name: "extract-invalid-tarball",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/invalid.tgz",
							To:       "@artifact/",
							Strategy: swapi.ExtractStrategy,
						},
					},
				}
				sources := map[string]string{"source": sourceDir}
				return spec, sources, workspaceDir
			},
			validateFunc:  nil,
			expectedError: "failed to extract tarball",
		},
		{
			name: "Extract to subdirectory",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")
				setupDirs(t, sourceDir, workspaceDir)

				createTestTarball(filepath.Join(sourceDir, "manifests.tgz"))

				spec := &swapi.OutputArtifact{
					Name: "extract-to-subdir",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/manifests.tgz",
							To:       "@artifact/extracted/configs/",
							Strategy: swapi.ExtractStrategy,
						},
					},
				}
				sources := map[string]string{"source": sourceDir}
				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string) {
				g := NewWithT(t)
				stagingDir := filepath.Join(workspaceDir, "extract-to-subdir")

				// Files should be in the nested directory
				g.Expect(filepath.Join(stagingDir, "extracted", "configs", "config.yaml")).To(BeAnExistingFile())
				g.Expect(filepath.Join(stagingDir, "extracted", "configs", "prod", "config.yaml")).To(BeAnExistingFile())
			},
			expectedError: "",
		},
		{
			name: "Extract with .tar.gz extension",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")
				setupDirs(t, sourceDir, workspaceDir)

				// Create tarball with .tar.gz extension instead of .tgz
				createTestTarball(filepath.Join(sourceDir, "manifests.tar.gz"))

				spec := &swapi.OutputArtifact{
					Name: "extract-tar-gz",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/manifests.tar.gz",
							To:       "@artifact/",
							Strategy: swapi.ExtractStrategy,
						},
					},
				}
				sources := map[string]string{"source": sourceDir}
				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string) {
				g := NewWithT(t)
				stagingDir := filepath.Join(workspaceDir, "extract-tar-gz")
				g.Expect(filepath.Join(stagingDir, "config.yaml")).To(BeAnExistingFile())
				g.Expect(filepath.Join(stagingDir, "prod", "config.yaml")).To(BeAnExistingFile())
			},
			expectedError: "",
		},
		{
			name: "Extract with exclude patterns",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")
				setupDirs(t, sourceDir, workspaceDir)

				createTestTarball(filepath.Join(sourceDir, "app.tgz"))
				createTestTarballForInt(filepath.Join(sourceDir, "int.tgz"))

				spec := &swapi.OutputArtifact{
					Name: "extract-with-exclude",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/*.tgz",
							To:       "@artifact/",
							Strategy: swapi.ExtractStrategy,
							Exclude:  []string{"int.tgz"},
						},
					},
				}
				sources := map[string]string{"source": sourceDir}
				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string) {
				g := NewWithT(t)
				stagingDir := filepath.Join(workspaceDir, "extract-with-exclude")

				// app.tgz contents should exist
				g.Expect(filepath.Join(stagingDir, "config.yaml")).To(BeAnExistingFile())

				// int.tgz contents should NOT exist (was excluded)
				g.Expect(filepath.Join(stagingDir, "int", "config.yaml")).ToNot(BeAnExistingFile())
			},
			expectedError: "",
		},
		{
			name: "Extract pattern matches no tarballs",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")
				setupDirs(t, sourceDir, workspaceDir)

				// Create only non-tarball files
				createFile(t, sourceDir, "config.yaml", "key: value")

				spec := &swapi.OutputArtifact{
					Name: "extract-no-tarballs",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/*.tgz",
							To:       "@artifact/",
							Strategy: swapi.ExtractStrategy,
						},
					},
				}
				sources := map[string]string{"source": sourceDir}
				return spec, sources, workspaceDir
			},
			validateFunc:  nil,
			expectedError: "no files match pattern",
		},
		{
			name: "Extract overwrites existing files from previous extract",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")
				setupDirs(t, sourceDir, workspaceDir)

				// Create two tarballs with different content to prove overwrite works
				createTestTarballWithContent(filepath.Join(sourceDir, "first.tgz"), `
name: first-app
version: 1.0.0
env: first
`)
				createTestTarballWithContent(filepath.Join(sourceDir, "second.tgz"), `
name: second-app
version: 2.0.0
env: second
`)

				spec := &swapi.OutputArtifact{
					Name: "extract-overwrite",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/first.tgz",
							To:       "@artifact/",
							Strategy: swapi.ExtractStrategy,
						},
						{
							From:     "@source/second.tgz",
							To:       "@artifact/",
							Strategy: swapi.ExtractStrategy,
						},
					},
				}
				sources := map[string]string{"source": sourceDir}
				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string) {
				g := NewWithT(t)
				stagingDir := filepath.Join(workspaceDir, "extract-overwrite")

				// config.yaml should have content from second.tgz (overwrote first)
				configContent, err := os.ReadFile(filepath.Join(stagingDir, "config.yaml"))
				g.Expect(err).ToNot(HaveOccurred())
				// Should have second tarball's content, NOT first
				g.Expect(string(configContent)).To(ContainSubstring("name: second-app"))
				g.Expect(string(configContent)).To(ContainSubstring("version: 2.0.0"))
				g.Expect(string(configContent)).To(ContainSubstring("env: second"))
				// Should NOT have first tarball's content
				g.Expect(string(configContent)).ToNot(ContainSubstring("name: first-app"))
				g.Expect(string(configContent)).ToNot(ContainSubstring("env: first"))
			},
			expectedError: "",
		},
		{
			name: "Extract skips non-tarball files in glob pattern",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")
				setupDirs(t, sourceDir, workspaceDir)

				// Create a regular file and a tarball
				createFile(t, sourceDir, "readme.txt", "This is a readme")
				createTestTarball(filepath.Join(sourceDir, "archive.tgz"))

				spec := &swapi.OutputArtifact{
					Name: "extract-skip-non-tarball",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/*",
							To:       "@artifact/",
							Strategy: swapi.ExtractStrategy,
						},
					},
				}
				sources := map[string]string{"source": sourceDir}
				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string) {
				g := NewWithT(t)
				stagingDir := filepath.Join(workspaceDir, "extract-skip-non-tarball")

				// Tarball contents should be extracted
				g.Expect(filepath.Join(stagingDir, "config.yaml")).To(BeAnExistingFile())
				g.Expect(filepath.Join(stagingDir, "prod", "config.yaml")).To(BeAnExistingFile())

				// readme.txt should NOT exist (non-tarball files are skipped with Extract strategy)
				g.Expect(filepath.Join(stagingDir, "readme.txt")).ToNot(BeAnExistingFile())
			},
			expectedError: "",
		},
		{
			name: "Extract strategy fails for directory source",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")
				setupDirs(t, sourceDir, workspaceDir)

				// Create a directory (not a tarball)
				createDir(t, sourceDir, "manifests")
				createFile(t, sourceDir, "manifests/config.yaml", "key: value")

				spec := &swapi.OutputArtifact{
					Name: "extract-directory-error",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/manifests",
							To:       "@artifact/",
							Strategy: swapi.ExtractStrategy,
						},
					},
				}
				sources := map[string]string{"source": sourceDir}
				return spec, sources, workspaceDir
			},
			validateFunc:  nil,
			expectedError: "extract strategy is not supported for directories",
		},
		{
			name: "Extract single file requires tarball extension",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")
				setupDirs(t, sourceDir, workspaceDir)

				// Create a non-tarball file
				createFile(t, sourceDir, "config.yaml", "key: value")

				spec := &swapi.OutputArtifact{
					Name: "extract-non-tarball-error",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/config.yaml",
							To:       "@artifact/",
							Strategy: swapi.ExtractStrategy,
						},
					},
				}
				sources := map[string]string{"source": sourceDir}
				return spec, sources, workspaceDir
			},
			validateFunc:  nil,
			expectedError: "extract strategy requires tarball file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			spec, sources, workspaceDir := tt.setupFunc(t)
			artifact, err := testBuilder.Build(context.Background(), spec, sources, "test-extract", workspaceDir)
			if tt.expectedError != "" {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring(tt.expectedError))
			} else {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(artifact).ToNot(BeNil())

				// Validate the result
				tt.validateFunc(t, artifact, workspaceDir)
			}
		})
	}

}

// createTestTarball creates a test tarball with sample files
func createTestTarball(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	// Add config.yaml.txt
	content1 := []byte(`
name: app
ports: [8080]
labels:
  env: dev
  keep: me
version: 1.0.1
replicas: 2
env: development
`)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "config.yaml",
		Mode: 0o644,
		Size: int64(len(content1)),
	}); err != nil {
		return err
	}
	if _, err := tarWriter.Write(content1); err != nil {
		return err
	}

	// Add subdir/
	if err := tarWriter.WriteHeader(&tar.Header{
		Name:     "prod/",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
	}); err != nil {
		return err
	}

	// Add prod/config.yaml
	content2 := []byte(`
name: app
ports: [8081]
labels:
  env: prod
  keep: me
version: 1.0.0
replicas: 5
env: production
`)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "prod/config.yaml",
		Mode: 0o644,
		Size: int64(len(content2)),
	}); err != nil {
		return err
	}
	if _, err := tarWriter.Write(content2); err != nil {
		return err
	}

	return nil
}

// createTestTarball creates a test tarball with sample files
func createTestTarballForInt(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	// Add subdir/
	if err := tarWriter.WriteHeader(&tar.Header{
		Name:     "int/",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
	}); err != nil {
		return err
	}

	// Add config.yaml.txt
	content1 := []byte(`
name: app
ports: [8082]
labels:
  env: int
  keep: me
version: 1.0.0
replicas: 3
env: int
`)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "int/config.yaml",
		Mode: 0o644,
		Size: int64(len(content1)),
	}); err != nil {
		return err
	}
	if _, err := tarWriter.Write(content1); err != nil {
		return err
	}

	return nil
}

// createTestTarballWithContent creates a test tarball with custom config.yaml content
func createTestTarballWithContent(path string, configContent string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	// Add config.yaml with custom content
	content := []byte(configContent)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "config.yaml",
		Mode: 0o644,
		Size: int64(len(content)),
	}); err != nil {
		return err
	}
	if _, err := tarWriter.Write(content); err != nil {
		return err
	}

	return nil
}
