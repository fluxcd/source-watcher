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
	"fmt"
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
func (r *ArtifactBuilder) Build(spec *swapi.OutputArtifact,
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
	if err := applyCopyOperations(spec.Copy, sources, stagingDir); err != nil {
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

func applyCopyOperations(operations []swapi.CopyOperation, sources map[string]string, stagingDir string) error {
	for _, op := range operations {
		if err := applyCopyOperation(op, sources, stagingDir); err != nil {
			return fmt.Errorf("failed to apply copy operation from '%s' to '%s': %w", op.From, op.To, err)
		}
	}
	return nil
}

func applyCopyOperation(op swapi.CopyOperation, sources map[string]string, stagingDir string) error {
	srcAlias, srcPattern, err := parseCopySource(op.From)
	if err != nil {
		return fmt.Errorf("invalid copy source '%s': %w", op.From, err)
	}

	destPath, err := parseCopyDestination(op.To, stagingDir)
	if err != nil {
		return fmt.Errorf("invalid copy destination '%s': %w", op.To, err)
	}

	srcDir, exists := sources[srcAlias]
	if !exists {
		return fmt.Errorf("source alias '%s' not found", srcAlias)
	}

	srcGlob := filepath.Join(srcDir, srcPattern)
	matches, err := filepath.Glob(srcGlob)
	if err != nil {
		return fmt.Errorf("invalid glob pattern '%s': %w", srcGlob, err)
	}

	if len(matches) == 0 {
		return fmt.Errorf("no files match pattern '%s' in source '%s'", srcPattern, srcAlias)
	}

	for _, match := range matches {
		relPath, err := filepath.Rel(srcDir, match)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		destFile := filepath.Join(destPath, relPath)
		if err := copyFile(match, destFile); err != nil {
			return fmt.Errorf("failed to copy file '%s' to '%s': %w", match, destFile, err)
		}
	}

	return nil
}

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

func parseCopyDestination(to, stagingDir string) (string, error) {
	if !strings.HasPrefix(to, "@artifact/") {
		return "", fmt.Errorf("destination must start with '@artifact/'")
	}

	relPath := strings.TrimPrefix(to, "@artifact/")
	return filepath.Join(stagingDir, relPath), nil
}

func copyFile(src, dest string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if srcInfo.IsDir() {
		return copyDir(src, dest)
	}

	return copyRegularFile(src, dest)
}

func copyRegularFile(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	destFile, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer destFile.Close()

	if _, err := destFile.ReadFrom(srcFile); err != nil {
		return err
	}

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	return os.Chmod(dest, srcInfo.Mode())
}

func copyDir(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		destPath := filepath.Join(dest, relPath)

		if info.IsDir() {
			return os.MkdirAll(destPath, info.Mode())
		}

		return copyRegularFile(path, destPath)
	})
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
