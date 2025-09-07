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

package builder

import (
	"os"
	"path/filepath"
	"testing"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

func TestApplyCopyOperations(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(t *testing.T) ([]swapi.CopyOperation, map[string]string, string)
		expectError  bool
		validateFunc func(t *testing.T, stagingDir string)
	}{
		{
			name: "copy single file from source to artifact",
			setupFunc: func(t *testing.T) ([]swapi.CopyOperation, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				stagingDir := filepath.Join(tmpDir, "staging")

				if err := os.MkdirAll(srcDir, 0o755); err != nil {
					t.Fatalf("Failed to create source dir: %v", err)
				}
				if err := os.MkdirAll(stagingDir, 0o755); err != nil {
					t.Fatalf("Failed to create staging dir: %v", err)
				}

				testFile := filepath.Join(srcDir, "test.yaml")
				if err := os.WriteFile(testFile, []byte("content"), 0o644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}

				operations := []swapi.CopyOperation{
					{
						From: "@source/test.yaml",
						To:   "@artifact/",
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return operations, sources, stagingDir
			},
			expectError: false,
			validateFunc: func(t *testing.T, stagingDir string) {
				expectedFile := filepath.Join(stagingDir, "test.yaml")
				if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
					t.Errorf("Expected file %s to exist", expectedFile)
				}

				content, err := os.ReadFile(expectedFile)
				if err != nil {
					t.Errorf("Failed to read copied file: %v", err)
				}

				if string(content) != "content" {
					t.Errorf("Expected content 'content', got '%s'", string(content))
				}
			},
		},
		{
			name: "copy multiple files with glob pattern",
			setupFunc: func(t *testing.T) ([]swapi.CopyOperation, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				stagingDir := filepath.Join(tmpDir, "staging")

				if err := os.MkdirAll(srcDir, 0o755); err != nil {
					t.Fatalf("Failed to create source dir: %v", err)
				}
				if err := os.MkdirAll(stagingDir, 0o755); err != nil {
					t.Fatalf("Failed to create staging dir: %v", err)
				}

				files := []string{"config.yaml", "deployment.yaml", "service.yaml"}
				for _, file := range files {
					if err := os.WriteFile(filepath.Join(srcDir, file), []byte("yaml content"), 0o644); err != nil {
						t.Fatalf("Failed to create file %s: %v", file, err)
					}
				}

				operations := []swapi.CopyOperation{
					{
						From: "@source/*.yaml",
						To:   "@artifact/manifests/",
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return operations, sources, stagingDir
			},
			expectError: false,
			validateFunc: func(t *testing.T, stagingDir string) {
				expectedFiles := []string{"config.yaml", "deployment.yaml", "service.yaml"}
				for _, file := range expectedFiles {
					expectedPath := filepath.Join(stagingDir, "manifests", file)
					if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
						t.Errorf("Expected file %s to exist", expectedPath)
					}
				}
			},
		},
		{
			name: "copy directory recursively",
			setupFunc: func(t *testing.T) ([]swapi.CopyOperation, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				stagingDir := filepath.Join(tmpDir, "staging")

				if err := os.MkdirAll(filepath.Join(srcDir, "subdir"), 0o755); err != nil {
					t.Fatalf("Failed to create source subdirectory: %v", err)
				}
				if err := os.MkdirAll(stagingDir, 0o755); err != nil {
					t.Fatalf("Failed to create staging dir: %v", err)
				}

				if err := os.WriteFile(filepath.Join(srcDir, "root.txt"), []byte("root"), 0o644); err != nil {
					t.Fatalf("Failed to create root file: %v", err)
				}
				if err := os.WriteFile(filepath.Join(srcDir, "subdir", "nested.txt"), []byte("nested"), 0o644); err != nil {
					t.Fatalf("Failed to create nested file: %v", err)
				}

				operations := []swapi.CopyOperation{
					{
						From: "@source/**",
						To:   "@artifact/",
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return operations, sources, stagingDir
			},
			expectError: false,
			validateFunc: func(t *testing.T, stagingDir string) {
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "root.txt"):             "root",
					filepath.Join(stagingDir, "subdir", "nested.txt"): "nested",
				}

				for expectedPath, expectedContent := range expectedFiles {
					if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
						t.Errorf("Expected file %s to exist", expectedPath)
						continue
					}

					content, err := os.ReadFile(expectedPath)
					if err != nil {
						t.Errorf("Failed to read file %s: %v", expectedPath, err)
						continue
					}

					if string(content) != expectedContent {
						t.Errorf("File %s: expected content '%s', got '%s'", expectedPath, expectedContent, string(content))
					}
				}
			},
		},
		{
			name: "multiple copy operations",
			setupFunc: func(t *testing.T) ([]swapi.CopyOperation, map[string]string, string) {
				tmpDir := t.TempDir()
				gitDir := filepath.Join(tmpDir, "git")
				ociDir := filepath.Join(tmpDir, "oci")
				stagingDir := filepath.Join(tmpDir, "staging")

				for _, dir := range []string{gitDir, ociDir, stagingDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				if err := os.WriteFile(filepath.Join(gitDir, "app.yaml"), []byte("git content"), 0o644); err != nil {
					t.Fatalf("Failed to create git file: %v", err)
				}
				if err := os.WriteFile(filepath.Join(ociDir, "config.json"), []byte("oci content"), 0o644); err != nil {
					t.Fatalf("Failed to create oci file: %v", err)
				}

				operations := []swapi.CopyOperation{
					{
						From: "@git/*.yaml",
						To:   "@artifact/manifests/",
					},
					{
						From: "@oci/*.json",
						To:   "@artifact/config/",
					},
				}

				sources := map[string]string{
					"git": gitDir,
					"oci": ociDir,
				}

				return operations, sources, stagingDir
			},
			expectError: false,
			validateFunc: func(t *testing.T, stagingDir string) {
				expectedFiles := map[string]string{
					filepath.Join(stagingDir, "manifests", "app.yaml"): "git content",
					filepath.Join(stagingDir, "config", "config.json"): "oci content",
				}

				for expectedPath, expectedContent := range expectedFiles {
					if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
						t.Errorf("Expected file %s to exist", expectedPath)
						continue
					}

					content, err := os.ReadFile(expectedPath)
					if err != nil {
						t.Errorf("Failed to read file %s: %v", expectedPath, err)
						continue
					}

					if string(content) != expectedContent {
						t.Errorf("File %s: expected content '%s', got '%s'", expectedPath, expectedContent, string(content))
					}
				}
			},
		},
		{
			name: "error when source alias not found",
			setupFunc: func(t *testing.T) ([]swapi.CopyOperation, map[string]string, string) {
				tmpDir := t.TempDir()
				stagingDir := filepath.Join(tmpDir, "staging")

				if err := os.MkdirAll(stagingDir, 0o755); err != nil {
					t.Fatalf("Failed to create staging dir: %v", err)
				}

				operations := []swapi.CopyOperation{
					{
						From: "@nonexistent/file.txt",
						To:   "@artifact/file.txt",
					},
				}

				sources := map[string]string{
					"source": "/some/path",
				}

				return operations, sources, stagingDir
			},
			expectError: true,
			validateFunc: func(t *testing.T, stagingDir string) {
				// No validation needed for error case
			},
		},
		{
			name: "error when no files match pattern",
			setupFunc: func(t *testing.T) ([]swapi.CopyOperation, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				stagingDir := filepath.Join(tmpDir, "staging")

				for _, dir := range []string{srcDir, stagingDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				operations := []swapi.CopyOperation{
					{
						From: "@source/*.nonexistent",
						To:   "@artifact/",
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return operations, sources, stagingDir
			},
			expectError: true,
			validateFunc: func(t *testing.T, stagingDir string) {
				// No validation needed for error case
			},
		},
		{
			name: "error with invalid source format",
			setupFunc: func(t *testing.T) ([]swapi.CopyOperation, map[string]string, string) {
				tmpDir := t.TempDir()
				stagingDir := filepath.Join(tmpDir, "staging")

				if err := os.MkdirAll(stagingDir, 0o755); err != nil {
					t.Fatalf("Failed to create staging dir: %v", err)
				}

				operations := []swapi.CopyOperation{
					{
						From: "invalid-source-format",
						To:   "@artifact/file.txt",
					},
				}

				sources := map[string]string{
					"source": "/some/path",
				}

				return operations, sources, stagingDir
			},
			expectError: true,
			validateFunc: func(t *testing.T, stagingDir string) {
				// No validation needed for error case
			},
		},
		{
			name: "error with invalid destination format",
			setupFunc: func(t *testing.T) ([]swapi.CopyOperation, map[string]string, string) {
				tmpDir := t.TempDir()
				srcDir := filepath.Join(tmpDir, "source")
				stagingDir := filepath.Join(tmpDir, "staging")

				for _, dir := range []string{srcDir, stagingDir} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("Failed to create dir %s: %v", dir, err)
					}
				}

				if err := os.WriteFile(filepath.Join(srcDir, "test.txt"), []byte("content"), 0o644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}

				operations := []swapi.CopyOperation{
					{
						From: "@source/test.txt",
						To:   "invalid-destination-format",
					},
				}

				sources := map[string]string{
					"source": srcDir,
				}

				return operations, sources, stagingDir
			},
			expectError: true,
			validateFunc: func(t *testing.T, stagingDir string) {
				// No validation needed for error case
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			operations, sources, stagingDir := tt.setupFunc(t)

			err := ApplyCopyOperations(operations, sources, stagingDir)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				tt.validateFunc(t, stagingDir)
			}
		})
	}
}
