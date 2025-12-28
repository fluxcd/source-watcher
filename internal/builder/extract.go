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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fluxcd/pkg/tar"
)

// tarballExtensions defines the recognized tarball file extensions.
// These are the formats produced by:
//   - flux build artifact
//   - helm package
//
// Currently supported: .tar.gz and .tgz (gzip-compressed tar archives)
var tarballExtensions = []string{".tar.gz", ".tgz"}

// isTarball checks if a file path has a recognized tarball extension.
// The check is case-insensitive to handle variations like .TGZ or .Tar.Gz.
func isTarball(path string) bool {
	lowerPath := strings.ToLower(path)
	for _, ext := range tarballExtensions {
		if strings.HasSuffix(lowerPath, ext) {
			return true
		}
	}
	return false
}

// extractTarball extracts a tarball archive to the destination directory.
// It uses fluxcd/pkg/tar.Untar for secure extraction which provides:
//   - Automatic gzip decompression
//   - Path traversal attack prevention
//   - Symlink security validation
//   - File permission preservation
//
// The tarball contents are extracted maintaining their internal directory structure.
// If the destination directory doesn't exist, it will be created with 0755 permissions.
func extractTarball(ctx context.Context,
	srcRoot *os.Root,
	srcPath string,
	stagingDir string,
	destPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Open the tarball through the source root for secure file access
	srcFile, err := srcRoot.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open tarball %q: %w", srcPath, err)
	}
	defer srcFile.Close()

	// Create the full destination path
	fullDestPath := filepath.Join(stagingDir, destPath)
	if err := os.MkdirAll(fullDestPath, 0o755); err != nil {
		return fmt.Errorf("failed to create destination directory %q: %w", fullDestPath, err)
	}

	// Use fluxcd/pkg/tar.Untar for secure extraction
	if err := tar.Untar(srcFile, fullDestPath); err != nil {
		return fmt.Errorf("failed to extract tarball %q to %q: %w", srcPath, fullDestPath, err)
	}

	return nil
}
