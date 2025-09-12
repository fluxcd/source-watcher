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

package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"github.com/opencontainers/go-digest"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/conditions"
	"github.com/fluxcd/pkg/testserver"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

func TestArtifactGeneratorReconciler_Reconcile(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Create a namespace
	ns, err := testEnv.CreateNamespace(ctx, "test")
	g.Expect(err).ToNot(HaveOccurred())

	// Create the ArtifactGenerator object
	objKey := client.ObjectKey{
		Name:      "test",
		Namespace: ns.Name,
	}
	obj := getArtifactGenerator(objKey)
	err = testClient.Create(ctx, obj)
	g.Expect(err).ToNot(HaveOccurred())

	// Create the GitRepository source
	gitFiles := []testserver.File{
		{Name: "app.yaml", Body: "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: test-app"},
		{Name: "service.yaml", Body: "apiVersion: v1\nkind: Service\nmetadata:\n  name: test-service"},
	}
	err = applyGitRepository(objKey, "main@sha256:abc123", gitFiles)
	g.Expect(err).ToNot(HaveOccurred())

	// Create the OCIRepository source
	ociRevision := digest.FromString("test").String()
	ociFiles := []testserver.File{
		{Name: "config.json", Body: "{\"version\": \"1.0\", \"env\": \"production\"}"},
		{Name: "manifest.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-config"},
	}
	err = applyOCIRepository(objKey, ociRevision, ociFiles)
	g.Expect(err).ToNot(HaveOccurred())

	// Initialize the ArtifactGenerator with the finalizer
	r, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: objKey,
	})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(r.RequeueAfter).To(BeEquivalentTo(time.Millisecond))

	// Reconcile to process the sources and build artifacts
	r, err = reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: objKey,
	})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(r.RequeueAfter).To(Equal(obj.GetRequeueAfter()))

	// Verify the ArtifactGenerator status
	err = testClient.Get(ctx, objKey, obj)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(conditions.IsReady(obj)).To(BeTrue())
	g.Expect(conditions.GetReason(obj, meta.ReadyCondition)).To(Equal(meta.SucceededReason))

	// Verify that ObservedSourcesDigest is set
	g.Expect(obj.Status.ObservedSourcesDigest).ToNot(BeEmpty())
	observedSourcesDigest := obj.Status.ObservedSourcesDigest

	// Verify inventory contains both output artifacts
	g.Expect(obj.Status.Inventory).To(HaveLen(2))
	g.Expect(obj.Status.Inventory[0].Name).To(Equal(fmt.Sprintf("%s-git", obj.Name)))
	g.Expect(obj.Status.Inventory[1].Name).To(Equal(fmt.Sprintf("%s-oci", obj.Name)))

	t.Log(objToYaml(obj))

	// Verify ExternalArtifact objects were created
	inventory := obj.Status.DeepCopy().Inventory
	gitArtifactDigest := ""
	ociArtifactDigest := ""
	for _, inv := range inventory {
		externalArtifact := &sourcev1.ExternalArtifact{}
		key := client.ObjectKey{Name: inv.Name, Namespace: inv.Namespace}
		err = testClient.Get(ctx, key, externalArtifact)
		g.Expect(err).ToNot(HaveOccurred())

		t.Log(objToYaml(externalArtifact))

		// Verify labels
		g.Expect(externalArtifact.Labels).ToNot(BeNil())
		g.Expect(externalArtifact.Labels["app.kubernetes.io/managed-by"]).To(Equal(controllerName))
		g.Expect(externalArtifact.Labels[swapi.ArtifactGeneratorLabel]).To(BeEquivalentTo(obj.GetUID()))

		// Verify source reference
		g.Expect(externalArtifact.Spec.SourceRef.APIVersion).To(Equal(swapi.GroupVersion.String()))
		g.Expect(externalArtifact.Spec.SourceRef.Kind).To(Equal(swapi.ArtifactGeneratorKind))
		g.Expect(externalArtifact.Spec.SourceRef.Name).To(Equal(obj.Name))
		g.Expect(externalArtifact.Spec.SourceRef.Namespace).To(Equal(obj.Namespace))

		// Verify the status
		g.Expect(externalArtifact.Status.Artifact).ToNot(BeNil())
		g.Expect(externalArtifact.Status.Artifact.URL).To(ContainSubstring(testServer.URL()))
		g.Expect(conditions.IsReady(externalArtifact)).To(BeTrue())

		if inv.Name == fmt.Sprintf("%s-git", obj.Name) {
			gitArtifactDigest = externalArtifact.Status.Artifact.Digest
		}

		if inv.Name == fmt.Sprintf("%s-oci", obj.Name) {
			ociArtifactDigest = externalArtifact.Status.Artifact.Digest

			// Verify that the ExternalArtifact inherited the origin revision of the OCIRepository
			originRev, ok := externalArtifact.Status.Artifact.Metadata[swapi.ArtifactOriginRevisionAnnotation]
			g.Expect(ok).To(BeTrue(), "expected origin revision in metadata")
			g.Expect(originRev).To(Equal("main@sha1:xyz123"))
		}
	}

	// Update the OCIRepository revision
	ociRevision = digest.FromString("new-test").String()
	err = applyOCIRepository(objKey, ociRevision, gitFiles)
	g.Expect(err).ToNot(HaveOccurred())

	// Reconcile to process the updated source
	r, err = reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: objKey,
	})
	g.Expect(err).ToNot(HaveOccurred())

	// Read the object to get the latest inventory
	err = testClient.Get(ctx, objKey, obj)
	g.Expect(err).ToNot(HaveOccurred())

	// Verify that ObservedSourcesDigest was updated
	g.Expect(obj.Status.ObservedSourcesDigest).ToNot(BeIdenticalTo(observedSourcesDigest))
	observedSourcesDigest = obj.Status.ObservedSourcesDigest

	// Verify garbage collection keeps 2 versions per artifact
	archives, err := findArtifactsInStorage(obj.Namespace)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(archives).To(HaveLen(3)) // 2 for OCI, 1 for Git

	// Verify that only the OCI ExternalArtifact was updated
	for _, inv := range inventory {
		externalArtifact := &sourcev1.ExternalArtifact{}
		key := client.ObjectKey{Name: inv.Name, Namespace: inv.Namespace}
		err = testClient.Get(ctx, key, externalArtifact)
		g.Expect(err).ToNot(HaveOccurred())

		if inv.Name == fmt.Sprintf("%s-git", obj.Name) {
			g.Expect(externalArtifact.Status.Artifact.Digest).To(Equal(gitArtifactDigest))
		}

		if inv.Name == fmt.Sprintf("%s-oci", obj.Name) {
			g.Expect(externalArtifact.Status.Artifact.Digest).ToNot(Equal(ociArtifactDigest))
			g.Expect(externalArtifact.Status.Artifact.Digest).To(Equal(gitArtifactDigest))
		}

		// Verify that garbage collection did not remove the current artifacts
		g.Expect(archives).To(ContainElement(externalArtifact.Status.Artifact.Path))
	}

	// Remove the Git OutputArtifact from the spec
	gitArtifactName := fmt.Sprintf("%s-git", obj.Name)
	var outputArtifacts []swapi.OutputArtifact
	for _, art := range obj.Spec.OutputArtifacts {
		if art.Name != gitArtifactName {
			outputArtifacts = append(outputArtifacts, art)
		}
	}
	obj.Spec.OutputArtifacts = outputArtifacts
	err = testClient.Update(ctx, obj)
	g.Expect(err).ToNot(HaveOccurred())

	// Reconcile to process the spec change
	r, err = reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: objKey,
	})
	g.Expect(err).ToNot(HaveOccurred())

	// Verify inventory contains only one output artifact
	err = testClient.Get(ctx, objKey, obj)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(obj.Status.Inventory).To(HaveLen(1))

	// Verify that ObservedSourcesDigest did not change
	g.Expect(obj.Status.ObservedSourcesDigest).To(BeIdenticalTo(observedSourcesDigest))

	// Verify the ExternalArtifact object was deleted
	deletedArtifact := &sourcev1.ExternalArtifact{}
	key := client.ObjectKey{Name: gitArtifactName, Namespace: obj.Namespace}
	err = testClient.Get(ctx, key, deletedArtifact)
	g.Expect(err).To(HaveOccurred())
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

	// Change the revision to point to the OCIRepository revision
	obj.Spec.OutputArtifacts[0].Revision = fmt.Sprintf("@%s-oci", obj.Name)
	err = testClient.Update(ctx, obj)
	g.Expect(err).ToNot(HaveOccurred())

	// Reconcile to process the spec change
	r, err = reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: objKey,
	})
	g.Expect(err).ToNot(HaveOccurred())

	// Verify that the ExternalArtifact inherited the OCIRepository revision
	updatedArtifact := &sourcev1.ExternalArtifact{}
	key = client.ObjectKey{Name: obj.Spec.OutputArtifacts[0].Name, Namespace: obj.Namespace}
	err = testClient.Get(ctx, key, updatedArtifact)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(updatedArtifact.Status.Artifact).ToNot(BeNil())
	g.Expect(updatedArtifact.Status.Artifact.Revision).To(Equal(ociRevision))

	// Verify events were recorded
	events := getEvents(obj.Name, obj.Namespace)
	g.Expect(events).ToNot(BeEmpty())
	for _, e := range events {
		g.Expect(e.Type).To(Equal(corev1.EventTypeNormal))
		g.Expect(e.Reason).To(Equal(meta.ReadyCondition))
	}

	// Delete the object to trigger finalization
	err = testClient.Delete(ctx, obj)
	g.Expect(err).ToNot(HaveOccurred())

	// Reconcile to free resources
	r, err = reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: objKey,
	})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(r.RequeueAfter).To(BeZero())

	// Verify the ArtifactGenerator was deleted
	resultFinal := &swapi.ArtifactGenerator{}
	err = testClient.Get(ctx, objKey, resultFinal)
	g.Expect(err).To(HaveOccurred())
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

	// Verify the ExternalArtifact objects were deleted
	for _, inv := range inventory {
		externalArtifact := &sourcev1.ExternalArtifact{}
		key := client.ObjectKey{Name: inv.Name, Namespace: inv.Namespace}
		err = testClient.Get(ctx, key, externalArtifact)
		g.Expect(err).To(HaveOccurred())
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}

	// Verify that all artifacts were deleted from storage
	a, err := findArtifactsInStorage(obj.Namespace)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(a).To(HaveLen(0))
}

func TestArtifactGeneratorReconciler_fetchSources(t *testing.T) {
	reconciler := getArtifactGeneratorReconciler()

	tests := []struct {
		name        string
		setupFunc   func() (*swapi.ArtifactGenerator, func())
		expectError bool
		expectCount int
	}{
		{
			name: "successfully gets git and oci sources",
			setupFunc: func() (*swapi.ArtifactGenerator, func()) {
				gitKey := client.ObjectKey{Name: "test-git", Namespace: "default"}
				ociKey := client.ObjectKey{Name: "test-oci", Namespace: "default"}
				objKey := client.ObjectKey{Name: "test-generator", Namespace: "default"}

				gitFiles := []testserver.File{
					{Name: "file1.yaml", Body: "content1"},
					{Name: "file2.yaml", Body: "content2"},
				}
				ociFiles := []testserver.File{
					{Name: "file3.yaml", Body: "content3"},
					{Name: "file4.yaml", Body: "content4"},
				}

				if err := applyGitRepository(gitKey, "main@sha1:abcd", gitFiles); err != nil {
					t.Fatalf("Failed to apply git repository: %v", err)
				}
				if err := applyOCIRepository(ociKey, "latest@sha256:1234", ociFiles); err != nil {
					t.Fatalf("Failed to apply OCI repository: %v", err)
				}

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      objKey.Name,
						Namespace: objKey.Namespace,
					},
					Spec: swapi.ArtifactGeneratorSpec{
						Sources: []swapi.SourceReference{
							{
								Alias: gitKey.Name,
								Kind:  sourcev1.GitRepositoryKind,
								Name:  gitKey.Name,
							},
							{
								Alias: ociKey.Name,
								Kind:  sourcev1.OCIRepositoryKind,
								Name:  ociKey.Name,
							},
						},
					},
				}

				cleanup := func() {
					testClient.Delete(context.Background(), &sourcev1.GitRepository{
						ObjectMeta: metav1.ObjectMeta{Name: gitKey.Name, Namespace: gitKey.Namespace},
					})
					testClient.Delete(context.Background(), &sourcev1.OCIRepository{
						ObjectMeta: metav1.ObjectMeta{Name: ociKey.Name, Namespace: ociKey.Namespace},
					})
				}

				return generator, cleanup
			},
			expectError: false,
			expectCount: 2,
		},
		{
			name: "fails when git source not found",
			setupFunc: func() (*swapi.ArtifactGenerator, func()) {
				gitKey := client.ObjectKey{Name: "nonexistent-git", Namespace: "default"}
				ociKey := client.ObjectKey{Name: "nonexistent-oci", Namespace: "default"}
				objKey := client.ObjectKey{Name: "test-generator", Namespace: "default"}

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      objKey.Name,
						Namespace: objKey.Namespace,
					},
					Spec: swapi.ArtifactGeneratorSpec{
						Sources: []swapi.SourceReference{
							{
								Alias: gitKey.Name,
								Kind:  sourcev1.GitRepositoryKind,
								Name:  gitKey.Name,
							},
							{
								Alias: ociKey.Name,
								Kind:  sourcev1.OCIRepositoryKind,
								Name:  ociKey.Name,
							},
						},
					},
				}
				return generator, func() {}
			},
			expectError: true,
			expectCount: 0,
		},
		{
			name: "successfully gets single git source",
			setupFunc: func() (*swapi.ArtifactGenerator, func()) {
				gitKey := client.ObjectKey{Name: "single-git", Namespace: "default"}
				objKey := client.ObjectKey{Name: "single-generator", Namespace: "default"}

				gitFiles := []testserver.File{
					{Name: "config.yaml", Body: "apiVersion: v1\nkind: ConfigMap"},
				}

				if err := applyGitRepository(gitKey, "main@sha1:xyz123", gitFiles); err != nil {
					t.Fatalf("Failed to apply git repository: %v", err)
				}

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      objKey.Name,
						Namespace: objKey.Namespace,
					},
					Spec: swapi.ArtifactGeneratorSpec{
						Sources: []swapi.SourceReference{
							{
								Alias: gitKey.Name,
								Kind:  sourcev1.GitRepositoryKind,
								Name:  gitKey.Name,
							},
						},
					},
				}

				cleanup := func() {
					testClient.Delete(context.Background(), &sourcev1.GitRepository{
						ObjectMeta: metav1.ObjectMeta{Name: gitKey.Name, Namespace: gitKey.Namespace},
					})
				}

				return generator, cleanup
			},
			expectError: false,
			expectCount: 1,
		},
		{
			name: "handles explicit namespace in source reference",
			setupFunc: func() (*swapi.ArtifactGenerator, func()) {
				gitKey := client.ObjectKey{Name: "explicit-ns-git", Namespace: "default"}
				objKey := client.ObjectKey{Name: "explicit-ns-generator", Namespace: "default"}

				gitFiles := []testserver.File{
					{Name: "explicit-ns.yaml", Body: "explicit namespace content"},
				}

				if err := applyGitRepository(gitKey, "main@sha1:explicit", gitFiles); err != nil {
					t.Fatalf("Failed to apply git repository: %v", err)
				}

				generator := &swapi.ArtifactGenerator{
					TypeMeta: metav1.TypeMeta{
						Kind:       swapi.ArtifactGeneratorKind,
						APIVersion: swapi.GroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      objKey.Name,
						Namespace: objKey.Namespace,
					},
					Spec: swapi.ArtifactGeneratorSpec{
						Sources: []swapi.SourceReference{
							{
								Alias:     gitKey.Name,
								Kind:      sourcev1.GitRepositoryKind,
								Name:      gitKey.Name,
								Namespace: gitKey.Namespace,
							},
						},
					},
				}

				cleanup := func() {
					testClient.Delete(context.Background(), &sourcev1.GitRepository{
						ObjectMeta: metav1.ObjectMeta{Name: gitKey.Name, Namespace: gitKey.Namespace},
					})
				}

				return generator, cleanup
			},
			expectError: false,
			expectCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generator, cleanup := tt.setupFunc()
			defer cleanup()

			tmpDir := t.TempDir()

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			remoteSources, getErr := reconciler.observeSources(ctx, generator)
			result, fetchErr := reconciler.fetchSources(ctx, remoteSources, tmpDir)
			err := errors.Join(getErr, fetchErr)
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if len(result) != tt.expectCount {
					t.Errorf("Expected %d sources, got %d", tt.expectCount, len(result))
				}

				// Verify that the returned paths exist and correspond to the source aliases
				for alias, path := range result {
					if _, err := os.Stat(path); os.IsNotExist(err) {
						t.Errorf("Expected source path %s to exist for alias %s", path, alias)
					}

					// Verify the path structure matches expectations
					expectedPath := tmpDir + "/" + alias
					if path != expectedPath {
						t.Errorf("Expected path %s for alias %s, got %s", expectedPath, alias, path)
					}
				}
			}
		})
	}
}

func getArtifactGeneratorReconciler() *ArtifactGeneratorReconciler {
	return &ArtifactGeneratorReconciler{
		ControllerName:            controllerName,
		Client:                    testClient,
		APIReader:                 testClient,
		Scheme:                    testEnv.Scheme(),
		EventRecorder:             testEnv.GetEventRecorderFor(controllerName),
		Storage:                   testStorage,
		ArtifactFetchRetries:      1,
		DependencyRequeueInterval: 5 * time.Second,
		NoCrossNamespaceRefs:      true,
	}
}

func getArtifactGenerator(objectKey client.ObjectKey) *swapi.ArtifactGenerator {
	return &swapi.ArtifactGenerator{
		TypeMeta: metav1.TypeMeta{
			Kind:       swapi.ArtifactGeneratorKind,
			APIVersion: swapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      objectKey.Name,
			Namespace: objectKey.Namespace,
		},
		Spec: swapi.ArtifactGeneratorSpec{
			Sources: []swapi.SourceReference{
				{
					Alias: fmt.Sprintf("%s-git", objectKey.Name),
					Kind:  sourcev1.GitRepositoryKind,
					Name:  objectKey.Name,
				},
				{
					Alias: fmt.Sprintf("%s-oci", objectKey.Name),
					Kind:  sourcev1.OCIRepositoryKind,
					Name:  objectKey.Name,
				},
			},
			OutputArtifacts: []swapi.OutputArtifact{
				{
					Name: fmt.Sprintf("%s-git", objectKey.Name),
					Copy: []swapi.CopyOperation{
						{
							From: fmt.Sprintf("@%s-git/**", objectKey.Name),
							To:   "@artifact/",
						},
					},
				},
				{
					Name:           fmt.Sprintf("%s-oci", objectKey.Name),
					OriginRevision: fmt.Sprintf("@%s-oci", objectKey.Name),
					Copy: []swapi.CopyOperation{
						{
							From: fmt.Sprintf("@%s-oci/**", objectKey.Name),
							To:   "@artifact/",
						},
					},
				},
			},
		},
	}
}

func applyGitRepository(objKey client.ObjectKey, revision string, files []testserver.File) error {
	artifactName, err := testServer.ArtifactFromFiles(files)
	if err != nil {
		return err
	}

	repo := &sourcev1.GitRepository{
		TypeMeta: metav1.TypeMeta{
			Kind:       sourcev1.GitRepositoryKind,
			APIVersion: sourcev1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
		Spec: sourcev1.GitRepositorySpec{
			URL:      "https://github.com/test/repository",
			Interval: metav1.Duration{Duration: time.Minute},
		},
	}

	b, _ := os.ReadFile(filepath.Join(testServer.Root(), artifactName))
	dig := digest.SHA256.FromBytes(b)

	url := fmt.Sprintf("%s/%s", testServer.URL(), artifactName)

	status := sourcev1.GitRepositoryStatus{
		Conditions: []metav1.Condition{
			{
				Type:               meta.ReadyCondition,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             sourcev1.GitOperationSucceedReason,
			},
		},
		Artifact: &meta.Artifact{
			Path:           url,
			URL:            url,
			Revision:       revision,
			Digest:         dig.String(),
			LastUpdateTime: metav1.Now(),
		},
	}

	patchOpts := []client.PatchOption{
		client.ForceOwnership,
		client.FieldOwner("kustomize-controller"),
	}

	if err := testClient.Patch(context.Background(), repo, client.Apply, patchOpts...); err != nil {
		return err
	}

	repo.ManagedFields = nil
	repo.Status = status

	statusOpts := &client.SubResourcePatchOptions{
		PatchOptions: client.PatchOptions{
			FieldManager: "source-controller",
		},
	}

	return testClient.Status().Patch(context.Background(), repo, client.Apply, statusOpts)
}

func applyOCIRepository(objKey client.ObjectKey, revision string, files []testserver.File) error {
	artifactName, err := testServer.ArtifactFromFiles(files)
	if err != nil {
		return err
	}

	repo := &sourcev1.OCIRepository{
		TypeMeta: metav1.TypeMeta{
			Kind:       sourcev1.OCIRepositoryKind,
			APIVersion: sourcev1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
		Spec: sourcev1.OCIRepositorySpec{
			URL:      "oci://ghcr.io/test/repository",
			Interval: metav1.Duration{Duration: time.Minute},
		},
	}
	b, _ := os.ReadFile(filepath.Join(testServer.Root(), artifactName))
	dig := digest.SHA256.FromBytes(b)

	url := fmt.Sprintf("%s/%s", testServer.URL(), artifactName)

	status := sourcev1.OCIRepositoryStatus{
		Conditions: []metav1.Condition{
			{
				Type:               meta.ReadyCondition,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             meta.SucceededReason,
			},
		},
		Artifact: &meta.Artifact{
			Path:           url,
			URL:            url,
			Revision:       revision,
			Digest:         dig.String(),
			LastUpdateTime: metav1.Now(),
			Metadata: map[string]string{
				swapi.ArtifactOriginRevisionAnnotation: "main@sha1:xyz123",
			},
		},
	}

	patchOpts := []client.PatchOption{
		client.ForceOwnership,
		client.FieldOwner("kustomize-controller"),
	}

	if err := testClient.Patch(context.Background(), repo, client.Apply, patchOpts...); err != nil {
		return err
	}

	repo.ManagedFields = nil
	repo.Status = status

	statusOpts := &client.SubResourcePatchOptions{
		PatchOptions: client.PatchOptions{
			FieldManager: "source-controller",
		},
	}

	return testClient.Status().Patch(context.Background(), repo, client.Apply, statusOpts)
}

func findArtifactsInStorage(namespace string) ([]string, error) {
	var artifacts []string
	basePath := filepath.Join(testStorage.BasePath, strings.ToLower(sourcev1.ExternalArtifactKind), namespace)
	err := filepath.WalkDir(basePath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(d.Name()) == ".gz" {
			relPath, _ := filepath.Rel(testStorage.BasePath, path)
			artifacts = append(artifacts, relPath)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return artifacts, nil
	}
	return artifacts, err
}
