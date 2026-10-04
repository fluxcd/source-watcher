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

package builder_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotktar "github.com/fluxcd/pkg/tar"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

// TestBuildSameNameDifferentNamespaces verifies that artifacts sharing a name
// in different namespaces are staged in isolated directories, so that the
// contents of one cannot leak into the other.
func TestBuildSameNameDifferentNamespaces(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	workspace := t.TempDir()
	srcA := filepath.Join(t.TempDir(), "a")
	srcB := filepath.Join(t.TempDir(), "b")
	g.Expect(os.MkdirAll(srcA, 0o755)).To(Succeed())
	g.Expect(os.MkdirAll(srcB, 0o755)).To(Succeed())
	createFile(t, srcA, "a.yaml", "a")
	createFile(t, srcB, "b.yaml", "b")

	sources := map[string]string{"a": srcA, "b": srcB}

	artifactA, err := testBuilder.Build(ctx, &swapi.OutputArtifact{
		Name: "app",
		Copy: []swapi.CopyOperation{{From: "@a/**", To: "@artifact/"}},
	}, sources, "ns-a", workspace)
	g.Expect(err).ToNot(HaveOccurred())

	artifactB, err := testBuilder.Build(ctx, &swapi.OutputArtifact{
		Name: "app",
		Copy: []swapi.CopyOperation{{From: "@b/**", To: "@artifact/"}},
	}, sources, "ns-b", workspace)
	g.Expect(err).ToNot(HaveOccurred())

	// Each artifact must contain only its own file.
	g.Expect(artifactFiles(t, artifactA)).To(ConsistOf("a.yaml"))
	g.Expect(artifactFiles(t, artifactB)).To(ConsistOf("b.yaml"))
}

// artifactFiles extracts the artifact and returns the names of the files it
// contains.
func artifactFiles(t *testing.T, artifact *gotkmeta.Artifact) []string {
	t.Helper()
	g := NewWithT(t)

	extractDir := t.TempDir()
	f, err := os.Open(filepath.Join(testStorage.BasePath, artifact.Path))
	g.Expect(err).ToNot(HaveOccurred())
	defer f.Close()

	g.Expect(gotktar.Untar(f, extractDir, gotktar.WithMaxUntarSize(-1))).To(Succeed())

	entries, err := os.ReadDir(extractDir)
	g.Expect(err).ToNot(HaveOccurred())

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		files = append(files, entry.Name())
	}
	return files
}
