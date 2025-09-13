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

	. "github.com/onsi/gomega"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

func TestBuild_YAMLMergeStrategy(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string)
		validateFunc  func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string)
		expectedError string
	}{
		{
			name: "merge YAML files successfully",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				source1Dir := filepath.Join(tmpDir, "source1")
				source2Dir := filepath.Join(tmpDir, "source2")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, source1Dir, source2Dir, workspaceDir)

				// Create first source with base config
				createFile(t, source1Dir, "config.yaml", `
name: app
ports: [80, 443]
labels:
  env: dev
  keep: me
version: 1.0.0
replicas: 3
`)

				// Create second source with override config
				createFile(t, source2Dir, "config.yaml", `
replicas: 5 # This should overwrite the replicas
ports: [8080] # This should overwrite the ports array
labels:
  env: prod # This should overwrite the env label
env: production # This should add a new top-level field
`)

				spec := &swapi.OutputArtifact{
					Name: "yaml-to-yaml-merge",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source1/config.yaml",
							To:       "@artifact/config.yaml",
							Strategy: swapi.OverwriteStrategy,
						},
						{
							From:     "@source2/config.yaml",
							To:       "@artifact/config.yaml",
							Strategy: swapi.MergeStrategy,
						},
					},
				}

				sources := map[string]string{
					"source1": source1Dir,
					"source2": source2Dir,
				}
				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string) {
				g := NewWithT(t)
				g.Expect(artifact).ToNot(BeNil())

				// Read the merged config from staging directory
				stagingDir := filepath.Join(workspaceDir, "yaml-to-yaml-merge")
				configPath := filepath.Join(stagingDir, "config.yaml")

				configContent, err := os.ReadFile(configPath)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(configContent).ToNot(BeEmpty())

				// Verify the merged YAML contains expected content
				g.Expect(configContent).To(MatchYAML(`
name: app
ports: [8080]
labels:
  env: prod
  keep: me
version: 1.0.0
replicas: 5
env: production
`))
			},
		},
		{
			name: "merge JSON with YAML successfully",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				source1Dir := filepath.Join(tmpDir, "source1")
				source2Dir := filepath.Join(tmpDir, "source2")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, source1Dir, source2Dir, workspaceDir)

				// Create first source with yaml file
				createFile(t, source1Dir, "config.yaml", "a: b")

				// Create second source with json file
				createFile(t, source2Dir, "config.json", `{"c": {"d": "e"}, "a": "override"}`)

				spec := &swapi.OutputArtifact{
					Name: "json-to-yaml-merge",
					Copy: []swapi.CopyOperation{
						{
							From: "@source1/config.yaml",
							To:   "@artifact/config.yaml",
						},
						{
							From:     "@source2/config.json",
							To:       "@artifact/config.yaml",
							Strategy: swapi.MergeStrategy, // Should work as YAML is a superset of JSON
						},
					},
				}

				sources := map[string]string{
					"source1": source1Dir,
					"source2": source2Dir,
				}
				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string) {
				g := NewWithT(t)
				g.Expect(artifact).ToNot(BeNil())

				// Read the merged config from staging directory
				stagingDir := filepath.Join(workspaceDir, "json-to-yaml-merge")
				configPath := filepath.Join(stagingDir, "config.yaml")

				configContent, err := os.ReadFile(configPath)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(configContent).ToNot(BeEmpty())
				g.Expect(configContent).To(MatchYAML(`
a: override
c:
  d: e
`))
			},
		},
		{
			name: "merge with non existing destination file",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, sourceDir, workspaceDir)
				createFile(t, sourceDir, "config.yaml", "a: b")

				spec := &swapi.OutputArtifact{
					Name: "yaml-merge-no-dest",
					Copy: []swapi.CopyOperation{
						{
							From:     "@source/config.yaml",
							To:       "@artifact/config.yaml",
							Strategy: swapi.MergeStrategy,
						},
					},
				}

				sources := map[string]string{"source": sourceDir}
				return spec, sources, workspaceDir
			},
			validateFunc: func(t *testing.T, artifact *gotkmeta.Artifact, workspaceDir string) {
				g := NewWithT(t)
				g.Expect(artifact).ToNot(BeNil())

				// Read config from staging directory (no merge needed)
				stagingDir := filepath.Join(workspaceDir, "yaml-merge-no-dest")
				configPath := filepath.Join(stagingDir, "config.yaml")

				configContent, err := os.ReadFile(configPath)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(configContent).To(MatchYAML("a: b"))
			},
		},
		{
			name: "fails with destination not YAML",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				source1Dir := filepath.Join(tmpDir, "source1")
				source2Dir := filepath.Join(tmpDir, "source2")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, source1Dir, source2Dir, workspaceDir)

				// Create first source with env file
				createFile(t, source1Dir, "config.env", "A=B")

				// Create second source with yaml file
				createFile(t, source2Dir, "config.yaml", "a: b")

				spec := &swapi.OutputArtifact{
					Name: "yaml-to-env-error",
					Copy: []swapi.CopyOperation{
						{
							From: "@source1/config.env",
							To:   "@artifact/config.env",
						},
						{
							From:     "@source2/config.yaml",
							To:       "@artifact/config.env",
							Strategy: swapi.MergeStrategy,
						},
					},
				}

				sources := map[string]string{
					"source1": source1Dir,
					"source2": source2Dir,
				}
				return spec, sources, workspaceDir
			},
			expectedError: "cannot unmarshal YAML document",
			validateFunc:  nil,
		},
		{
			name: "fails with source not YAML",
			setupFunc: func(t *testing.T) (*swapi.OutputArtifact, map[string]string, string) {
				tmpDir := t.TempDir()
				source1Dir := filepath.Join(tmpDir, "source1")
				source2Dir := filepath.Join(tmpDir, "source2")
				workspaceDir := filepath.Join(tmpDir, "workspace")

				setupDirs(t, source1Dir, source2Dir, workspaceDir)

				// Create first source with yaml file
				createFile(t, source1Dir, "config.yaml", "a: b")

				// Create second source with env file
				createFile(t, source2Dir, "config.env", "A=B")

				spec := &swapi.OutputArtifact{
					Name: "env-to-yaml-error",
					Copy: []swapi.CopyOperation{
						{
							From: "@source1/config.yaml",
							To:   "@artifact/config.yaml",
						},
						{
							From:     "@source2/config.env",
							To:       "@artifact/config.yaml",
							Strategy: swapi.MergeStrategy,
						},
					},
				}

				sources := map[string]string{
					"source1": source1Dir,
					"source2": source2Dir,
				}
				return spec, sources, workspaceDir
			},
			expectedError: "cannot unmarshal YAML document",
			validateFunc:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			spec, sources, workspaceDir := tt.setupFunc(t)
			artifact, err := testBuilder.Build(context.Background(), spec, sources, "test-merge", workspaceDir)
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
