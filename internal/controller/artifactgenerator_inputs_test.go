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

package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gotktestsrv "github.com/fluxcd/pkg/testserver"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

func TestResolveLocalSourcePath(t *testing.T) {
	g := NewWithT(t)
	dir := t.TempDir()
	artifactDir := t.TempDir()
	sources := map[string]string{"repo": dir}

	path, err := resolveLocalSourcePath("@repo/tenants/a/inputs.yaml", sources, artifactDir)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(path).To(Equal(filepath.Join(dir, "tenants/a/inputs.yaml")))

	path, err = resolveLocalSourcePath("@artifact/inputs.yaml", sources, artifactDir)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(path).To(Equal(filepath.Join(artifactDir, "inputs.yaml")))

	_, err = resolveLocalSourcePath("repo/inputs.yaml", sources, artifactDir)
	g.Expect(err).To(HaveOccurred())

	_, err = resolveLocalSourcePath("@missing/inputs.yaml", sources, artifactDir)
	g.Expect(err).To(HaveOccurred())

	_, err = resolveLocalSourcePath("@repo/../escape.yaml", sources, artifactDir)
	g.Expect(err).To(HaveOccurred())

	_, err = resolveLocalSourcePath("@repo/", sources, artifactDir)
	g.Expect(err).To(HaveOccurred())
}

func TestLoadExportedInputs(t *testing.T) {
	g := NewWithT(t)
	dir := t.TempDir()
	g.Expect(os.WriteFile(filepath.Join(dir, "inputs.yaml"),
		[]byte("replicas: 3\nname: test\nnested:\n  foo: bar\n"), 0o644)).To(Succeed())

	sources := map[string]string{"repo": dir}

	r := &ArtifactGeneratorReconciler{ControllerName: controllerName}

	t.Run("exports inputs", func(t *testing.T) {
		g := NewWithT(t)
		inputs, err := r.loadExportedInputs(context.Background(), &swapi.OutputArtifact{
			Name:       "test",
			InputsFrom: "@repo/inputs.yaml",
		}, sources, "")
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(inputs).To(HaveKey("replicas"))
		g.Expect(inputs).To(HaveKey("name"))
		g.Expect(inputs).To(HaveKey("nested"))
		g.Expect(string(inputs["name"].Raw)).To(Equal(`"test"`))
	})

	t.Run("missing file", func(t *testing.T) {
		g := NewWithT(t)
		_, err := r.loadExportedInputs(context.Background(), &swapi.OutputArtifact{
			Name:       "test",
			InputsFrom: "@repo/missing.yaml",
		}, sources, "")
		g.Expect(err).To(HaveOccurred())
	})
}

func TestFilterNamespaceMetadata(t *testing.T) {
	g := NewWithT(t)

	filtered, err := filterNamespaceMetadata(
		map[string]string{"allowed": "ok", "other": "ignored"},
		map[string]string{"allowed": ".*"},
		"label",
	)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(filtered).To(Equal(map[string]string{"allowed": "ok"}))

	_, err = filterNamespaceMetadata(
		map[string]string{"allowed": "nope"},
		map[string]string{"allowed": "^ok$"},
		"label",
	)
	g.Expect(err).To(HaveOccurred())

	_, err = filterNamespaceMetadata(
		map[string]string{"other": "ignored"},
		map[string]string{"allowed": ".*"},
		"label",
	)
	g.Expect(err).ToNot(HaveOccurred())
}

func TestNamespaceMetadataFromSource(t *testing.T) {
	g := NewWithT(t)
	dir := t.TempDir()
	g.Expect(os.MkdirAll(filepath.Join(dir, "tenants", "tenant-a"), 0o755)).To(Succeed())
	g.Expect(os.WriteFile(filepath.Join(dir, "tenants", "tenant-a", "namespace-metadata.yaml"),
		[]byte(`apiVersion: source.extensions.fluxcd.io/v1beta1
kind: NamespaceMetadata
metadata:
  labels:
    observability: enabled
    forbidden: nope
  annotations:
    team: platform
`), 0o644)).To(Succeed())

	r := &ArtifactGeneratorReconciler{ControllerName: controllerName}
	obj := &swapi.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{Namespace: "flux-system", UID: "uid"},
		Spec: swapi.ArtifactGeneratorSpec{
			CommonMetadata: &swapi.CommonMetadata{
				Labels: map[string]string{"common": "base"},
			},
			Namespaces: &swapi.Namespaces{
				Strategy: swapi.NamespaceStrategyManaged,
				Metadata: &swapi.NamespacesMetadata{
					FromSource: &swapi.NamespaceMetadataFromSource{
						Path:               "@repo/tenants/{namespace}/namespace-metadata.yaml",
						AllowedLabels:      map[string]string{"observability": "^(enabled|disabled)$"},
						AllowedAnnotations: map[string]string{"team": ".*"},
					},
					From: []swapi.NamespaceMetadataFrom{
						{
							Strategy:    swapi.OverrideStrategy,
							Namespace:   "*",
							Labels:      map[string]string{"common": "overridden"},
							Annotations: map[string]string{"owner": "platform"},
						},
					},
				},
			},
		},
	}

	labels, annotations, err := r.namespaceMetadata(obj, "tenant-a",
		map[string]string{"namespace": "tenant-a"}, map[string]string{"repo": dir}, "")
	g.Expect(err).ToNot(HaveOccurred())
	// Sourced metadata overrides the common metadata, the disallowed key is
	// ignored, and the .from operation overrides existing values.
	g.Expect(labels).To(HaveKeyWithValue("common", "overridden"))
	g.Expect(labels).To(HaveKeyWithValue("observability", "enabled"))
	g.Expect(labels).ToNot(HaveKey("forbidden"))
	g.Expect(labels).To(HaveKeyWithValue(swapi.ArtifactGeneratorLabel, "uid"))
	g.Expect(annotations).To(HaveKeyWithValue("team", "platform"))
	g.Expect(annotations).To(HaveKeyWithValue("owner", "platform"))

	// A value that does not match the allowed schema is rejected.
	obj.Spec.Namespaces.Metadata.FromSource.AllowedLabels["observability"] = "^disabled$"
	_, _, err = r.namespaceMetadata(obj, "tenant-a",
		map[string]string{"namespace": "tenant-a"}, map[string]string{"repo": dir}, "")
	g.Expect(err).To(HaveOccurred())

	// An unsupported kind is rejected.
	obj.Spec.Namespaces.Metadata.FromSource.AllowedLabels["observability"] = ".*"
	g.Expect(os.WriteFile(filepath.Join(dir, "tenants", "tenant-a", "namespace-metadata.yaml"),
		[]byte("apiVersion: source.extensions.fluxcd.io/v1beta1\nkind: Wrong\n"), 0o644)).To(Succeed())
	_, _, err = r.namespaceMetadata(obj, "tenant-a",
		map[string]string{"namespace": "tenant-a"}, map[string]string{"repo": dir}, "")
	g.Expect(err).To(HaveOccurred())

	// The reserved externalFinalizer annotation is rejected even when it comes
	// from the NamespaceMetadata file, which the CRD cannot validate. Only the
	// final desired namespace metadata is checked, so every source is covered.
	g.Expect(os.WriteFile(filepath.Join(dir, "tenants", "tenant-a", "namespace-metadata.yaml"),
		[]byte(`apiVersion: source.extensions.fluxcd.io/v1beta1
kind: NamespaceMetadata
metadata:
  annotations:
    source.extensions.fluxcd.io/externalFinalizer: fluxcd.controlplane.io/v1/ResourceSet//tenants/uid
`), 0o644)).To(Succeed())
	obj.Spec.Namespaces.Metadata.FromSource.AllowedAnnotations = map[string]string{
		swapi.ExternalFinalizerAnnotation: ".*",
	}
	_, _, err = r.namespaceMetadata(obj, "tenant-a",
		map[string]string{"namespace": "tenant-a"}, map[string]string{"repo": dir}, "")
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(swapi.ExternalFinalizerAnnotation))
}

func TestArtifactGeneratorReconciler_ExportedInputs(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ns, err := testEnv.CreateNamespace(ctx, "test-inputs")
	g.Expect(err).ToNot(HaveOccurred())

	objKey := client.ObjectKey{Name: "test-inputs", Namespace: ns.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:       fmt.Sprintf("%s-out", objKey.Name),
			InputsFrom: fmt.Sprintf("@%s-git/inputs.yaml", objKey.Name),
			Copy: []swapi.CopyOperation{
				{From: fmt.Sprintf("@%s-git/**", objKey.Name), To: "@artifact/"},
			},
		},
	}
	g.Expect(testClient.Create(ctx, obj)).To(Succeed())

	gitFiles := []gotktestsrv.File{
		{Name: "app.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-config"},
		{Name: "inputs.yaml", Body: "replicas: 3\nname: test\nnested:\n  foo: bar\n"},
	}
	g.Expect(applyGitRepository(objKey, "main@sha256:abc123", gitFiles)).To(Succeed())

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	ea := &sourcev1.ExternalArtifact{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{
		Name:      fmt.Sprintf("%s-out", objKey.Name),
		Namespace: ns.Name,
	}, ea)).To(Succeed())
	g.Expect(ea.Status.ExportedInputs).To(HaveKey("replicas"))
	g.Expect(ea.Status.ExportedInputs).To(HaveKey("name"))
	g.Expect(ea.Status.ExportedInputs).To(HaveKey("nested"))
	g.Expect(string(ea.Status.ExportedInputs["replicas"].Raw)).To(Equal("3"))
	g.Expect(string(ea.Status.ExportedInputs["name"].Raw)).To(Equal(`"test"`))
}
