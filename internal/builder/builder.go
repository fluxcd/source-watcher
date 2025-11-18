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

	// Resolve symlinks before archiving to ensure their content is included
	if err := ResolveSymlinks(stagingDir); err != nil {
		return nil, fmt.Errorf("failed to resolve symlinks in staging directory: %w", err)
	}

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
		return applySingleSourceCopy(ctx, op, srcRoot, srcPattern, stagingRoot, destRelPath, destEndsWithSlash)
	}

	// Glob pattern - find all matches and copy each
	matches, err := fs.Glob(srcRoot.FS(), srcPattern)
	if err != nil {
		return fmt.Errorf("invalid glob pattern '%s': %w", srcPattern, err)
	}

	if len(matches) == 0 {
		return fmt.Errorf("no files match pattern '%s' in source '%s'", srcPattern, srcAlias)
	}

	// Filter out excluded files
	filteredMatches := make([]string, 0, len(matches))
	for _, match := range matches {
		if !shouldExclude(match, op.Exclude) {
			filteredMatches = append(filteredMatches, match)
		}
	}

	if len(filteredMatches) == 0 {
		return fmt.Errorf("all files matching pattern '%s' in source '%s' were excluded", srcPattern, srcAlias)
	}

	// For glob patterns, destination should be a directory (like cp *.txt dest/)
	for _, match := range filteredMatches {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Calculate destination path based on glob pattern type
		destFile := calculateGlobDestination(srcPattern, match, destRelPath)
		if err := copyFileWithRoots(ctx, op, srcRoot, match, stagingRoot, destFile); err != nil {
			return fmt.Errorf("failed to copy file '%s' to '%s': %w", match, destFile, err)
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
		return applySingleDirectoryCopy(ctx, op, srcRoot, srcPath, stagingRoot, destPath)
	} else {
		return applySingleFileCopy(ctx, op, srcRoot, srcPath, stagingRoot, destPath, destEndsWithSlash)
	}
}

// applySingleFileCopy handles copying a single file using cp-like semantics:
// - file -> dest (no slash) = copy to dest as filename or dest/filename if dest is an existing directory
// - file -> dest/ (with slash) = copy to dest/filename
func applySingleFileCopy(ctx context.Context,
	op swapi.CopyOperation,
	srcRoot *os.Root,
	srcPath string,
	stagingRoot *os.Root,
	destPath string,
	destEndsWithSlash bool) error {
	// Check if the file should be excluded
	if shouldExclude(srcPath, op.Exclude) {
		return nil // Skip excluded file
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

	for _, pattern := range excludePatterns {
		// We validate the patterns when parsing the copy operation,
		// so it's safe to use MatchUnvalidated here.
		if doublestar.MatchUnvalidated(pattern, filePath) {
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

// ResolveSymlinks recursively resolves symlinks in the given directory by replacing
// them with copies of their target files/directories. This ensures that symlink
// content is included in the archive, as the Archive function skips symlinks.
// Symlinks pointing outside the root directory are skipped for security reasons.
func ResolveSymlinks(rootDir string) error {
	rootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	// First pass: collect all symlinks
	type symlinkInfo struct {
		path   string
		target string
	}
	var symlinks []symlinkInfo

	err = filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Use Lstat to check for symlinks
		lstatInfo, err := os.Lstat(path)
		if err != nil {
			return err
		}

		// Check if this is a symlink
		if lstatInfo.Mode()&os.ModeSymlink != 0 {
			// Resolve the symlink target
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("failed to read symlink %s: %w", path, err)
			}

			// Make target path absolute if it's relative
			if !filepath.IsAbs(target) {
				// Get the absolute path of the symlink's parent directory
				parentDir, err := filepath.Abs(filepath.Dir(path))
				if err != nil {
					return fmt.Errorf("failed to get absolute path of parent directory: %w", err)
				}
				// For relative paths with .., we need to properly resolve them
				// Process path components manually to handle .. correctly
				parts := strings.Split(target, string(filepath.Separator))
				resolved := parentDir
				for _, part := range parts {
					if part == "" || part == "." {
						continue
					}
					if part == ".." {
						resolved = filepath.Dir(resolved)
					} else {
						resolved = filepath.Join(resolved, part)
					}
				}
				target = resolved
			} else {
				// Clean the absolute path to normalize ../
				target = filepath.Clean(target)
			}

			// Security check: ensure target is within root directory
			// Check: target must be an absolute path that starts with rootDir
			if !strings.HasPrefix(target, rootDir+string(filepath.Separator)) && target != rootDir {
				// Symlink points outside root directory - skip it
				return nil
			}

			symlinks = append(symlinks, symlinkInfo{
				path:   path,
				target: target,
			})
		}

		return nil
	})
	if err != nil {
		return err
	}

	// Second pass: resolve symlinks (process in reverse order to handle nested symlinks)
	for i := len(symlinks) - 1; i >= 0; i-- {
		sym := symlinks[i]

		// Check if target still exists
		targetInfo, err := os.Lstat(sym.target)
		if err != nil {
			// Target doesn't exist - skip broken symlink
			continue
		}

		// Skip self-referencing symlinks to avoid infinite loops
		// Compare normalized paths to handle different path representations
		symPathAbs, err := filepath.Abs(sym.path)
		if err != nil {
			continue
		}
		targetAbs, err := filepath.Abs(sym.target)
		if err != nil {
			continue
		}
		if symPathAbs == targetAbs {
			// Self-referencing symlink - skip it
			continue
		}

		// If target is itself a symlink, check if it points outside
		// This handles chain symlinks that eventually point outside
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			// Read the target of the target symlink
			chainTarget, err := os.Readlink(sym.target)
			if err == nil {
				// Resolve chain target path
				if !filepath.IsAbs(chainTarget) {
					chainTarget = filepath.Clean(filepath.Join(filepath.Dir(sym.target), chainTarget))
				}
				chainTarget, err = filepath.Abs(chainTarget)
				if err == nil {
					// Check if chain target is outside root directory
					if !strings.HasPrefix(chainTarget, rootDir+string(filepath.Separator)) && chainTarget != rootDir {
						// Chain symlink points outside - skip the original symlink
						continue
					}
					relPath, err := filepath.Rel(rootDir, chainTarget)
					if err != nil || strings.HasPrefix(relPath, "..") {
						// Chain symlink points outside - skip the original symlink
						continue
					}
				}
			}
		}

		// Remove the symlink
		if err := os.Remove(sym.path); err != nil {
			return fmt.Errorf("failed to remove symlink %s: %w", sym.path, err)
		}

		// Copy target to symlink location
		if targetInfo.IsDir() {
			// Copy directory recursively
			if err := copyDir(sym.target, sym.path); err != nil {
				return fmt.Errorf("failed to copy directory from %s to %s: %w", sym.target, sym.path, err)
			}
		} else {
			// Copy file
			if err := copyFile(sym.target, sym.path); err != nil {
				return fmt.Errorf("failed to copy file from %s to %s: %w", sym.target, sym.path, err)
			}
		}
	}

	return nil
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = destFile.ReadFrom(sourceFile)
	if err != nil {
		return err
	}

	// Preserve file mode
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, srcInfo.Mode())
}

// copyDir recursively copies a directory from src to dst.
func copyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			// Check if it's a symlink
			info, err := os.Lstat(srcPath)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				// Resolve symlink recursively
				target, err := os.Readlink(srcPath)
				if err != nil {
					return err
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(srcPath), target)
				}
				targetInfo, err := os.Lstat(target)
				if err != nil {
					continue // Skip broken symlink
				}
				if targetInfo.IsDir() {
					if err := copyDir(target, dstPath); err != nil {
						return err
					}
				} else {
					if err := copyFile(target, dstPath); err != nil {
						return err
					}
				}
			} else {
				if err := copyFile(srcPath, dstPath); err != nil {
					return err
				}
			}
		}
	}

	return nil
}
