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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"github.com/opencontainers/go-digest"
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

func TestArtifactGeneratorReconciler_LifeCycle(t *testing.T) {
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

	// Create GitRepository
	gitFiles := []testserver.File{
		{Name: "app.yaml", Body: "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: test-app"},
		{Name: "service.yaml", Body: "apiVersion: v1\nkind: Service\nmetadata:\n  name: test-service"},
	}
	err = applyGitRepository(objKey, "main@sha256:abc123", gitFiles)
	g.Expect(err).ToNot(HaveOccurred())

	// Create OCIRepository
	ociFiles := []testserver.File{
		{Name: "config.json", Body: "{\"version\": \"1.0\", \"env\": \"production\"}"},
		{Name: "manifest.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-config"},
	}
	err = applyOCIRepository(objKey, "latest@sha256:def456", ociFiles)
	g.Expect(err).ToNot(HaveOccurred())

	// Initialize the object with the finalizer
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

	// Verify the object status
	err = testClient.Get(ctx, objKey, obj)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(conditions.IsReady(obj)).To(BeTrue())
	g.Expect(conditions.GetReason(obj, meta.ReadyCondition)).To(Equal(meta.SucceededReason))

	// Verify inventory contains both output artifacts
	g.Expect(obj.Status.Inventory).To(HaveLen(2))
	g.Expect(obj.Status.Inventory[0].Kind).To(Equal(sourcev1.ExternalArtifactKind))
	g.Expect(obj.Status.Inventory[1].Kind).To(Equal(sourcev1.ExternalArtifactKind))

	// Verify ExternalArtifact objects were created
	inventory := obj.Status.DeepCopy().Inventory
	for _, inv := range inventory {
		externalArtifact := &sourcev1.ExternalArtifact{}
		key := client.ObjectKey{Name: inv.Name, Namespace: inv.Namespace}
		err = testClient.Get(ctx, key, externalArtifact)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(externalArtifact.OwnerReferences).To(HaveLen(1))
		g.Expect(externalArtifact.OwnerReferences[0].UID).To(Equal(obj.GetUID()))
		g.Expect(externalArtifact.OwnerReferences[0].Kind).To(Equal(swapi.ArtifactGeneratorKind))
		g.Expect(externalArtifact.Spec.SourceRef.Kind).To(Equal(swapi.ArtifactGeneratorKind))
		g.Expect(externalArtifact.Spec.SourceRef.Name).To(Equal(obj.Name))
		g.Expect(externalArtifact.Status.Artifact).ToNot(BeNil())
		g.Expect(externalArtifact.Status.Artifact.URL).To(ContainSubstring(testServer.URL()))
		g.Expect(conditions.IsReady(externalArtifact)).To(BeTrue())
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

	// Verify the object has been deleted
	resultFinal := &swapi.ArtifactGenerator{}
	err = testClient.Get(ctx, objKey, resultFinal)
	g.Expect(err).To(HaveOccurred())
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

	// Verify ExternalArtifact objects were deleted
	for _, inv := range inventory {
		externalArtifact := &sourcev1.ExternalArtifact{}
		key := client.ObjectKey{Name: inv.Name, Namespace: inv.Namespace}
		err = testClient.Get(ctx, key, externalArtifact)
		g.Expect(err).To(HaveOccurred())
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
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
					Name: fmt.Sprintf("%s-oci", objectKey.Name),
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
