/*
Copyright 2026 The Flux authors

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

// This is the only white-box test file in the package. It is reserved for
// defensive guards that no black-box test can reach, because an earlier layer
// rejects the input before the guard runs. Anything reachable through Build
// belongs in builder_test.go instead.

package builder

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
)

// TestRelativeToBase covers the guard rejecting paths that escape basePath.
// Build never produces such a pair: excludeBasePath is always either the copy
// source itself or the literal prefix of its glob, so every walked path is
// underneath it. The guard only matters if a future caller passes an
// unrelated base.
func TestRelativeToBase(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
		filePath string
		want     string
		wantOK   bool
	}{
		{
			name:     "empty base returns the path unchanged",
			basePath: "",
			filePath: filepath.Join("a", "b"),
			want:     filepath.Join("a", "b"),
			wantOK:   true,
		},
		{
			name:     "current directory base returns the path unchanged",
			basePath: ".",
			filePath: filepath.Join("a", "b"),
			want:     filepath.Join("a", "b"),
			wantOK:   true,
		},
		{
			name:     "path under base is made relative",
			basePath: "a",
			filePath: filepath.Join("a", "b", "c"),
			want:     filepath.Join("b", "c"),
			wantOK:   true,
		},
		{
			name:     "trailing slash on base is cleaned",
			basePath: "a" + string(filepath.Separator),
			filePath: filepath.Join("a", "b"),
			want:     "b",
			wantOK:   true,
		},
		{
			name:     "path equal to base",
			basePath: filepath.Join("a", "b"),
			filePath: filepath.Join("a", "b"),
			want:     ".",
			wantOK:   true,
		},
		{
			name:     "leading dots in a name are not treated as traversal",
			basePath: "a",
			filePath: filepath.Join("a", "..b"),
			want:     "..b",
			wantOK:   true,
		},
		{
			name:     "sibling of base escapes",
			basePath: filepath.Join("a", "b"),
			filePath: filepath.Join("a", "c"),
			wantOK:   false,
		},
		{
			name:     "parent of base escapes",
			basePath: filepath.Join("a", "b"),
			filePath: "a",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			got, ok := relativeToBase(tt.basePath, tt.filePath)
			g.Expect(ok).To(Equal(tt.wantOK))
			if !tt.wantOK {
				g.Expect(got).To(BeEmpty())
				return
			}
			g.Expect(got).To(Equal(tt.want))
		})
	}
}

func TestExtractTarballConfinesDestination(t *testing.T) {
	g := NewWithT(t)
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	stagingDir := filepath.Join(tmpDir, "staging")
	g.Expect(os.MkdirAll(sourceDir, 0o755)).To(Succeed())
	g.Expect(os.MkdirAll(stagingDir, 0o755)).To(Succeed())
	g.Expect(os.WriteFile(filepath.Join(sourceDir, "manifests.tgz"), []byte("invalid"), 0o644)).To(Succeed())

	srcRoot, err := os.OpenRoot(sourceDir)
	g.Expect(err).ToNot(HaveOccurred())
	defer srcRoot.Close()
	stagingRoot, err := os.OpenRoot(stagingDir)
	g.Expect(err).ToNot(HaveOccurred())
	defer stagingRoot.Close()

	err = extractTarball(context.Background(), srcRoot, "manifests.tgz", stagingRoot, "../escape")
	g.Expect(err).To(MatchError(ContainSubstring("failed to create destination directory")))
	g.Expect(filepath.Join(tmpDir, "escape")).ToNot(BeADirectory())
	g.Expect(filepath.Join(tmpDir, "escape", "config.yaml")).ToNot(BeAnExistingFile())
}
