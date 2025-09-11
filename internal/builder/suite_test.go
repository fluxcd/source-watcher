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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/artifact/config"
	"github.com/fluxcd/pkg/artifact/digest"
	"github.com/fluxcd/pkg/artifact/storage"
	"github.com/fluxcd/pkg/tar"
	"github.com/fluxcd/pkg/testserver"

	"github.com/fluxcd/source-watcher/internal/builder"
)

var (
	testBuilder *builder.ArtifactBuilder
	testStorage *storage.Storage
	testServer  *testserver.ArtifactServer
)

func TestMain(m *testing.M) {
	var err error
	testServer, err = testserver.NewTempArtifactServer()
	if err != nil {
		panic(fmt.Sprintf("Failed to create a temporary storage server: %v", err))
	}
	testServer.Start()

	testStorage, err = storage.New(&config.Options{
		ArtifactRetentionRecords: 2,

		StoragePath:          testServer.Root(),
		StorageAddress:       testServer.URL(),
		StorageAdvAddress:    testServer.URL(),
		ArtifactDigestAlgo:   digest.Canonical.String(),
		ArtifactRetentionTTL: time.Hour,
	})
	if err != nil {
		panic(fmt.Sprintf("Failed to create test storage: %v", err))
	}

	testBuilder = builder.New(testStorage)

	code := m.Run()

	testServer.Stop()
	if err := os.RemoveAll(testServer.Root()); err != nil {
		panic(fmt.Sprintf("Failed to remove storage server dir: %v", err))
	}

	os.Exit(code)
}

// verifyContents extracts and verifies the contents of a tar.gz artifact
// It takes the expected files from the staging directory and verifies they exist in the tar.gz
func verifyContents(t *testing.T,
	testStorage *storage.Storage,
	artifact *meta.Artifact,
	stagingDir string,
	expectedFiles map[string]string) {
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
