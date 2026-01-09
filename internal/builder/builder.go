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

	"github.com/bmatcuk/doublestar/v4"
	"golang.org/x/mod/sumdb/dirhash"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkstorage "github.com/fluxcd/pkg/artifact/storage"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

// ArtifactBuilder is responsible for building and storing artifacts
// based on a given specification and source files.
type ArtifactBuilder struct {
	Storage *gotkstorage.Storage
}

// New creates a new ArtifactBuilder with the given storage backend.
func New(storage *gotkstorage.Storage) *ArtifactBuilder {
	return &ArtifactBuilder{
		Storage: storage,
	}
}

// Build creates an artifact from the given specification and sources.
// It stages the files in a temporary directory within the provided workspace,
// applies the copy operations, and then archives the staged files into the
// artifact storage. The resulting artifact metadata is returned.
// The artifact archive is stored under the following path:
// <storage-root>/<kind>/<namespace>/<name>/<contents-hash>.tar.gz
func (r *ArtifactBuilder) Build(ctx context.Context,
	spec *swapi.OutputArtifact,
	sources map[string]string,
	namespace string,
	workspace string) (*gotkmeta.Artifact, error) {
	// Create a dir to stage the artifact files.
	stagingDir := filepath.Join(workspace, spec.Name)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create staging dir: %w", err)
	}

	// Apply the copy operations to the staging dir.
	if err := applyCopyOperations(ctx, spec.Copy, sources, stagingDir); err != nil {
		return nil, fmt.Errorf("failed to apply copy operations: %w", err)
	}

	// Compute the hash of the staging dir contents.
	contentsHash, err := dirhash.HashDir(stagingDir, spec.Name, builderHash)
	if err != nil {
		return nil, fmt.Errorf("failed to hash staging dir: %w", err)
	}

	// Initialize the Artifact object in the storage backend.
	artifact := r.Storage.NewArtifactFor(
		sourcev1.ExternalArtifactKind,
		&metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: namespace,
		},
		spec.Revision,
		fmt.Sprintf("%s.tar.gz", contentsHash),
	)

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
	if err := r.Storage.Archive(&artifact, stagingDir, gotkstorage.SourceIgnoreFilter(nil, nil)); err != nil {
		return nil, fmt.Errorf("failed to create artifact: %w", err)
	}

	// Set the artifact revision to include the digest.
	artifact.Revision = fmt.Sprintf("latest@%s", artifact.Digest)

	return artifact.DeepCopy(), nil
}

// applyCopyOperations applies a list of copy operations from the sources to the staging directory.
// The operations are applied in the order of the ops array, and any error will stop the process.
func applyCopyOperations(ctx context.Context,
	operations []swapi.CopyOperation,
	sources map[string]string,
	stagingDir string) error {
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

// If the copy operation uses the Extract strategy, it uses doublestar.Glob as we do not need to walk the whole tree
// otherwise we us std fs.Glob
func getGlobMatchingEntries(op swapi.CopyOperation, srcRoot *os.Root, srcPattern string) ([]string, error) {
	if op.Strategy == swapi.ExtractStrategy {
		// Use doublestar.Glob for recursive and advanced glob patterns (e.g., **/*.tar.gz)
		return doublestar.Glob(srcRoot.FS(), srcPattern)
	} else {
		// Use fs.Glob for simple, non-recursive glob patterns
		return fs.Glob(srcRoot.FS(), srcPattern)
	}
}

// applyCopyOperation applies a single copy operation from the sources to the staging directory.
// This function implements cp-like semantics by first analyzing the source pattern to determine
// if it's a glob, direct file/directory reference, or wildcard pattern, then making copy decisions
// based on the actual source types found. Files matching exclude patterns are filtered out.
func applyCopyOperation(ctx context.Context,
	op swapi.CopyOperation,
	sources map[string]string,
	stagingDir string) error {
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

	for _, pattern := range op.Exclude {
		if _, err := doublestar.Match(pattern, "."); err != nil {
			return fmt.Errorf("invalid exclude pattern '%s'", pattern)
		}
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

	// First, analyze the source pattern to understand the copy intent
	isGlobPattern := containsGlobChars(srcPattern)
	destEndsWithSlash := strings.HasSuffix(op.To, "/")

	if !isGlobPattern {
		// Direct path reference - check what it actually is first (cp-like behavior)
		return applySingleSourceCopy(ctx, op, srcRoot, srcPattern, stagingRoot, stagingDir, destRelPath, destEndsWithSlash)
	}

	matches, err := getGlobMatchingEntries(op, srcRoot, srcPattern)

	if err != nil {
		return fmt.Errorf("invalid glob pattern '%s': %w", srcPattern, err)
	}

	if len(matches) == 0 {
		return fmt.Errorf("no files match pattern '%s' in source '%s'", srcPattern, srcAlias)
	}

	// Filter out excluded files and special directory entries
	filteredMatches := make([]string, 0, len(matches))
	for _, match := range matches {
		// Skip current directory and parent directory references
		// doublestar.Glob returns "." for patterns like "**" which would
		// cause the entire source to be copied, bypassing per-file strategies
		if match == "." || match == ".." {
			continue
		}
		if shouldExclude(match, op.Exclude) {
			continue
		}
		filteredMatches = append(filteredMatches, match)
	}

	if len(filteredMatches) == 0 {
		return fmt.Errorf("all files matching pattern '%s' in source '%s' were excluded", srcPattern, srcAlias)
	}

	// For glob patterns, destination should be a directory (like cp *.txt dest/)
	for _, match := range filteredMatches {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Handle Extract strategy for tarballs
		if op.Strategy == swapi.ExtractStrategy {
			if !isTarball(match) {
				// Ignore files that are not tarball archives and directories
				continue
			}
			if err := extractTarball(ctx, srcRoot, match, stagingDir, destRelPath); err != nil {
				return fmt.Errorf("failed to extract tarball '%s' to '%s': %w", match, destRelPath, err)
			}
		} else {
			// Calculate destination path based on glob pattern type
			destFile := calculateGlobDestination(srcPattern, match, destRelPath)

			if err := copyFileWithRoots(ctx, op, srcRoot, match, stagingRoot, destFile); err != nil {
				return fmt.Errorf("failed to copy file '%s' to '%s': %w", match, destFile, err)
			}
		}
	}

	return nil
}

// applySingleSourceCopy handles copying a single, non-glob source (file or directory)
// using cp-like semantics based on the source type and destination format.
func applySingleSourceCopy(ctx context.Context,
	op swapi.CopyOperation,
	srcRoot *os.Root,
	srcPath string,
	stagingRoot *os.Root,
	stagingDir string,
	destPath string,
	destEndsWithSlash bool) error {
	// Clean the source path to handle trailing slashes
	srcPath = filepath.Clean(srcPath)

	// Stat the source to determine if it's a file or directory
	srcInfo, err := srcRoot.Stat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("source '%s' does not exist", srcPath)
		}
		return fmt.Errorf("failed to stat source '%s': %w", srcPath, err)
	}

	if srcInfo.IsDir() {
		// Extract strategy is not supported for directories
		if op.Strategy == swapi.ExtractStrategy {
			return fmt.Errorf("extract strategy is not supported for directories, got '%s'", srcPath)
		}
		return applySingleDirectoryCopy(ctx, op, srcRoot, srcPath, stagingRoot, destPath)
	}

	return applySingleFileCopy(ctx, op, srcRoot, srcPath, stagingRoot, stagingDir, destPath, destEndsWithSlash)
}

// applySingleFileCopy handles copying a single file using cp-like semantics:
// - file -> dest (no slash) = copy to dest as filename or dest/filename if dest is an existing directory
// - file -> dest/ (with slash) = copy to dest/filename
func applySingleFileCopy(ctx context.Context,
	op swapi.CopyOperation,
	srcRoot *os.Root,
	srcPath string,
	stagingRoot *os.Root,
	stagingDir string,
	destPath string,
	destEndsWithSlash bool) error {
	// Check if the file should be excluded
	if shouldExclude(srcPath, op.Exclude) {
		return nil // Skip excluded file
	}

	// Handle Extract strategy for tarballs
	if op.Strategy == swapi.ExtractStrategy {
		if !isTarball(srcPath) {
			return fmt.Errorf("extract strategy requires tarball file (.tar.gz or .tgz), got '%s'", srcPath)
		}
		return extractTarball(ctx, srcRoot, srcPath, stagingDir, destPath)
	}

	var finalDestPath string

	if destEndsWithSlash {
		// Destination is explicitly a directory - use source filename
		srcFileName := filepath.Base(srcPath)
		finalDestPath = filepath.Join(destPath, srcFileName)
	} else {
		// Check if destination path already exists as a directory
		if destInfo, err := stagingRoot.Stat(destPath); err == nil && destInfo.IsDir() {
			srcFileName := filepath.Base(srcPath)
			finalDestPath = filepath.Join(destPath, srcFileName)
		} else {
			// Destination is the target filename
			finalDestPath = destPath
		}
	}

	return copyFileWithRoots(ctx, op, srcRoot, srcPath, stagingRoot, finalDestPath)
}

// applySingleDirectoryCopy handles copying a single directory using cp-like semantics.
// Copies the source directory as a subdirectory within the destination path.
// Example: cp -r configs dest -> creates dest/configs/
func applySingleDirectoryCopy(ctx context.Context,
	op swapi.CopyOperation,
	srcRoot *os.Root,
	srcPath string,
	stagingRoot *os.Root,
	destPath string) error {
	srcDirName := filepath.Base(srcPath)
	finalDestPath := filepath.Join(destPath, srcDirName)

	return copyFileWithRoots(ctx, op, srcRoot, srcPath, stagingRoot, finalDestPath)
}

// containsGlobChars returns true if the path contains glob metacharacters
func containsGlobChars(path string) bool {
	return strings.ContainsAny(path, "*?[]")
}

// calculateGlobDestination determines the correct destination path for a glob match
// to match cp-like behavior for different glob patterns:
// - dir/** patterns strip the directory prefix (like cp -r dir/** dest/)
// - other patterns preserve the full match path
func calculateGlobDestination(pattern, match, destPath string) string {

	// Check if pattern ends with /** (recursive contents pattern)
	if strings.HasSuffix(pattern, "/**") {
		// Extract the directory prefix from pattern (everything before /**)
		dirPrefix := strings.TrimSuffix(pattern, "/**")

		// If match starts with this prefix, strip it (cp-like behavior)
		if strings.HasPrefix(match, dirPrefix+"/") {
			// Strip the directory prefix but keep the rest of the path
			relativeMatch := strings.TrimPrefix(match, dirPrefix+"/")
			return filepath.Join(destPath, relativeMatch)
		}
	}

	// For other glob patterns, preserve the full match path
	return filepath.Join(destPath, match)
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

// parseCopyDestinationRelative parses the destination path
// and returns the relative path within the artifact.
func parseCopyDestinationRelative(to string) (string, error) {
	if !strings.HasPrefix(to, "@artifact/") {
		return "", fmt.Errorf("destination must start with '@artifact/'")
	}

	return strings.TrimPrefix(to, "@artifact/"), nil
}

// copyFileWithRoots copies a file from srcRoot to stagingRoot os.Root,
// excluding files matching exclude patterns.
func copyFileWithRoots(ctx context.Context,
	op swapi.CopyOperation,
	srcRoot *os.Root,
	srcPath string,
	stagingRoot *os.Root,
	destPath string) error {
	srcInfo, err := srcRoot.Stat(srcPath)
	if err != nil {
		return err
	}

	if srcInfo.IsDir() {
		return copyDirWithRoots(ctx, srcRoot, srcPath, stagingRoot, destPath, op.Exclude)
	}

	if shouldMergeFile(op, stagingRoot, destPath) {
		return mergeFileWithRoots(ctx, srcRoot, srcPath, stagingRoot, destPath)
	}

	return copyRegularFileWithRoots(ctx, srcRoot, srcPath, stagingRoot, destPath)
}

// copyRegularFileWithRoots copies a regular file using os.Root.
func copyRegularFileWithRoots(ctx context.Context,
	srcRoot *os.Root,
	srcPath string,
	stagingRoot *os.Root,
	destPath string) error {
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

// shouldMergeFile determines if a file should be merged based
// on the copy operation strategy and if the destination file exists.
func shouldMergeFile(op swapi.CopyOperation, stagingRoot *os.Root, destPath string) bool {
	if op.Strategy != swapi.MergeStrategy {
		return false
	}
	if _, err := stagingRoot.Stat(destPath); err != nil {
		return false
	}
	return true
}

// mergeFileWithRoots merges the YAML content of srcPath into destPath using os.Root.
// It returns an error if the files cannot be read, parsed as YAML, merged, or written.
func mergeFileWithRoots(ctx context.Context,
	srcRoot *os.Root,
	srcPath string,
	stagingRoot *os.Root,
	destPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Read source file and parse as YAML
	srcData, err := loadYAML(srcRoot, srcPath)
	if err != nil {
		return err
	}

	// Read destination file and parse as YAML
	destData, err := loadYAML(stagingRoot, destPath)
	if err != nil {
		return err
	}

	// Merge and marshal the data into YAML
	mergedYAML, err := mergeYAML(destData, srcData)
	if err != nil {
		return fmt.Errorf("failed to merged YAML: %w", err)
	}

	// Overwriting the destination file
	return stagingRoot.WriteFile(destPath, mergedYAML, 0644)
}

// copyDirWithRoots copies a directory recursively using os.Root,
// skipping files and sub-dirs matching exclude patterns.
func copyDirWithRoots(ctx context.Context,
	srcRoot *os.Root,
	srcPath string,
	stagingRoot *os.Root,
	destPath string,
	excludePatterns []string) error {
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

		// Check if this path should be excluded
		if shouldExclude(relPath, excludePatterns) {
			if d.IsDir() {
				// Skip entire directory
				return fs.SkipDir
			}
			// Skip file
			return nil
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

// shouldExclude checks if a path matches any of the exclude patterns.
func shouldExclude(filePath string, excludePatterns []string) bool {
	if len(excludePatterns) == 0 {
		return false
	}

	fileName := filepath.Base(filePath)

	for _, pattern := range excludePatterns {
		// We validate the patterns when parsing the copy operation,
		// so it's safe to use MatchUnvalidated here.
		if doublestar.MatchUnvalidated(pattern, filePath) {
			return true
		}
		// For simple patterns without path separators (e.g., "*.md"),
		// also match against just the filename. This provides a more
		// intuitive user experience where "*.md" excludes all markdown
		// files regardless of their directory depth.
		if !strings.Contains(pattern, "/") && doublestar.MatchUnvalidated(pattern, fileName) {
			return true
		}
	}

	return false
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
