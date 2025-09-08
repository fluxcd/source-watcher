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
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/artifact/storage"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

// ArtifactBuilder is responsible for building and storing artifacts
// based on a given specification and source files.
type ArtifactBuilder struct {
	Storage *storage.Storage
}

// New creates a new ArtifactBuilder with the given storage backend.
func New(storage *storage.Storage) *ArtifactBuilder {
	return &ArtifactBuilder{
		Storage: storage,
	}
}

// Build creates an artifact from the given specification and sources.
// It stages the files in a temporary directory within the provided workspace,
// applies the copy operations, and then archives the staged files into the
// artifact storage. The resulting artifact metadata is returned.
// The artifact archive is stored under the following path:
// <storage-root>/<kind>/<namespace>/<name>/<artifact-uuid>.tar.gz
func (r *ArtifactBuilder) Build(ctx context.Context,
	spec *swapi.OutputArtifact,
	sources map[string]string,
	namespace string,
	workspace string) (*meta.Artifact, error) {
	// Initialize the Artifact object in the storage backend.
	artifact := r.Storage.NewArtifactFor(
		sourcev1.ExternalArtifactKind,
		&metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: namespace,
		},
		spec.Revision,
		fmt.Sprintf("%s.tar.gz", uuid.NewString()),
	)

	// Create a dir to stage the artifact files.
	stagingDir := filepath.Join(workspace, spec.Name)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create staging dir: %w", err)
	}

	// Apply the copy operations to the staging dir.
	if err := applyCopyOperations(ctx, spec.Copy, sources, stagingDir); err != nil {
		return nil, fmt.Errorf("failed to apply copy operations: %w", err)
	}

	// Create the artifact directory in storage.
	if err := r.Storage.MkdirAll(artifact); err != nil {
		return nil, fmt.Errorf("failed to create artifact directory: %w", err)
	}

	// Lock the artifact during creation to prevent concurrent writes.
	unlock, err := r.Storage.Lock(artifact)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire artifact lock: %w", err)
	}
	defer unlock()

	// Create the artifact tarball from the staging dir.
	if err := r.Storage.Archive(&artifact, stagingDir, storage.SourceIgnoreFilter(nil, nil)); err != nil {
		return nil, fmt.Errorf("failed to create artifact: %w", err)
	}

	// Set the artifact revision to include the digest.
	artifact.Revision = fmt.Sprintf("latest@%s", artifact.Digest)

	return artifact.DeepCopy(), nil
}

// applyCopyOperations applies a list of copy operations from the sources to the staging directory.
// The operations are applied in the order of the ops array, and any error will stop the process.
func applyCopyOperations(ctx context.Context, operations []swapi.CopyOperation, sources map[string]string, stagingDir string) error {
	for _, op := range operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := applyCopyOperation(ctx, op, sources, stagingDir); err != nil {
			return fmt.Errorf("failed to apply copy operation from '%s' to '%s': %w", op.From, op.To, err)
		}
	}
	return nil
}

// applyCopyOperation applies a single copy operation from the sources to the staging directory.
func applyCopyOperation(ctx context.Context, op swapi.CopyOperation, sources map[string]string, stagingDir string) error {
	srcAlias, srcPattern, err := parseCopySource(op.From)
	if err != nil {
		return fmt.Errorf("invalid copy source '%s': %w", op.From, err)
	}

	destRelPath, err := parseCopyDestinationRelative(op.To)
	if err != nil {
		return fmt.Errorf("invalid copy destination '%s': %w", op.To, err)
	}

	srcDir, exists := sources[srcAlias]
	if !exists {
		return fmt.Errorf("source alias '%s' not found", srcAlias)
	}

	// Create secure roots for file operations
	srcRoot, err := os.OpenRoot(srcDir)
	if err != nil {
		return fmt.Errorf("failed to open source root '%s': %w", srcDir, err)
	}
	defer srcRoot.Close()

	stagingRoot, err := os.OpenRoot(stagingDir)
	if err != nil {
		return fmt.Errorf("failed to open staging root '%s': %w", stagingDir, err)
	}
	defer stagingRoot.Close()

	// Use the source root filesystem to safely glob for files
	matches, err := fs.Glob(srcRoot.FS(), srcPattern)
	if err != nil {
		return fmt.Errorf("invalid glob pattern '%s': %w", srcPattern, err)
	}

	if len(matches) == 0 {
		return fmt.Errorf("no files match pattern '%s' in source '%s'", srcPattern, srcAlias)
	}

	for _, match := range matches {
		if err := ctx.Err(); err != nil {
			return err
		}
		destFile := filepath.Join(destRelPath, match)
		if err := copyFileWithRoots(ctx, srcRoot, match, stagingRoot, destFile); err != nil {
			return fmt.Errorf("failed to copy file '%s' to '%s': %w", match, destFile, err)
		}
	}

	return nil
}

// parseCopySource parses the source string and returns the alias and pattern.
func parseCopySource(from string) (alias, pattern string, err error) {
	if !strings.HasPrefix(from, "@") {
		return "", "", fmt.Errorf("source must start with '@'")
	}

	parts := strings.SplitN(from[1:], "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("source format must be '@alias/pattern'")
	}

	return parts[0], parts[1], nil
}

// parseCopyDestinationRelative parses the destination path and returns the relative path
// without joining it to the staging directory (for use with os.Root).
func parseCopyDestinationRelative(to string) (string, error) {
	if !strings.HasPrefix(to, "@artifact/") {
		return "", fmt.Errorf("destination must start with '@artifact/'")
	}

	return strings.TrimPrefix(to, "@artifact/"), nil
}

// copyFileWithRoots copies a file from srcRoot to stagingRoot os.Root.
func copyFileWithRoots(ctx context.Context, srcRoot *os.Root, srcPath string, stagingRoot *os.Root, destPath string) error {
	srcInfo, err := srcRoot.Stat(srcPath)
	if err != nil {
		return err
	}

	if srcInfo.IsDir() {
		return copyDirWithRoots(ctx, srcRoot, srcPath, stagingRoot, destPath)
	}

	return copyRegularFileWithRoots(ctx, srcRoot, srcPath, stagingRoot, destPath)
}

// copyRegularFileWithRoots copies a regular file using os.Root.
func copyRegularFileWithRoots(ctx context.Context, srcRoot *os.Root, srcPath string, stagingRoot *os.Root, destPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Create destination directory
	destDir := filepath.Dir(destPath)
	if destDir != "." && destDir != "" {
		// Create parent directories recursively
		if err := createDirRecursive(stagingRoot, destDir); err != nil {
			return fmt.Errorf("failed to create destination directory: %w", err)
		}
	}

	// Open source file through root
	srcFile, err := srcRoot.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Create destination file through root
	destFile, err := stagingRoot.Create(destPath)
	if err != nil {
		return err
	}
	defer destFile.Close()

	// Copy file contents
	if _, err := destFile.ReadFrom(srcFile); err != nil {
		return err
	}

	// Copy file permissions
	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	return destFile.Chmod(srcInfo.Mode())
}

// copyDirWithRoots copies a directory recursively using os.Root.
func copyDirWithRoots(ctx context.Context, srcRoot *os.Root, srcPath string, stagingRoot *os.Root, destPath string) error {
	return fs.WalkDir(srcRoot.FS(), srcPath, func(path string, d fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err != nil {
			return err
		}

		// Calculate relative path from srcPath to current path
		relPath, err := filepath.Rel(srcPath, path)
		if err != nil {
			return err
		}

		// Skip the root directory itself
		if relPath == "." {
			// Create the destination directory
			return createDirRecursive(stagingRoot, destPath)
		}

		destFilePath := filepath.Join(destPath, relPath)

		if d.IsDir() {
			return createDirRecursive(stagingRoot, destFilePath)
		}

		return copyRegularFileWithRoots(ctx, srcRoot, path, stagingRoot, destFilePath)
	})
}

// createDirRecursive creates a directory and all its parents using os.Root.
func createDirRecursive(root *os.Root, path string) error {
	if path == "." || path == "" {
		return nil
	}

	// Try to create the directory
	err := root.Mkdir(path, 0o755)
	if err == nil {
		return nil
	}

	// If it already exists, that's fine
	if os.IsExist(err) {
		return nil
	}

	// If the parent doesn't exist, create it recursively
	parent := filepath.Dir(path)
	if parent != path { // Avoid infinite recursion
		if err := createDirRecursive(root, parent); err != nil {
			return err
		}
		// Try again after creating parent
		return root.Mkdir(path, 0o755)
	}

	return err
}

// MkdirTempAbs creates a tmp dir and returns the absolute path to the dir.
// This is required since certain OSes like MacOS create temporary files in
// e.g. `/private/var`, to which `/var` is a symlink.
func MkdirTempAbs(dir, pattern string) (string, error) {
	tmpDir, err := os.MkdirTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	if err != nil {
		return "", fmt.Errorf("error evaluating symlink: %w", err)
	}
	return tmpDir, nil
}
