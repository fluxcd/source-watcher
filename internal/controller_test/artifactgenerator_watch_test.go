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

package controller_test

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

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkconditions "github.com/fluxcd/pkg/runtime/conditions"
	gotktestsrv "github.com/fluxcd/pkg/testserver"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

func TestArtifactGenerator_Watch(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ns, err := testEnv.CreateNamespace(ctx, "test")
	g.Expect(err).ToNot(HaveOccurred())

	revision := "v1.0.0"
	resultAG := &swapi.ArtifactGenerator{}
	objKey := client.ObjectKey{
		Namespace: ns.Name,
		Name:      "e2e",
	}

	ociFiles := []gotktestsrv.File{
		{Name: "cm.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test"},
		{Name: "sa.yaml", Body: "apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: test"},
	}
	err = applyOCIRepository(objKey, revision, ociFiles)
	g.Expect(err).ToNot(HaveOccurred())

	obj := &swapi.ArtifactGenerator{
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
					Alias: fmt.Sprintf("%s-oci", objKey.Name),
					Kind:  sourcev1.OCIRepositoryKind,
					Name:  objKey.Name,
				},
			},
			OutputArtifacts: []swapi.OutputArtifact{
				{
					Name:     fmt.Sprintf("%s-cm", objKey.Name),
					Revision: fmt.Sprintf("@%s-oci", objKey.Name),
					Copy: []swapi.CopyOperation{
						{
							From: fmt.Sprintf("@%s-oci/cm.yaml", objKey.Name),
							To:   "@artifact/",
						},
					},
				},
				{
					Name:     fmt.Sprintf("%s-sa", objKey.Name),
					Revision: fmt.Sprintf("@%s-oci", objKey.Name),
					Copy: []swapi.CopyOperation{
						{
							From: fmt.Sprintf("@%s-oci/sa.yaml", objKey.Name),
							To:   "@artifact/",
						},
					},
				},
			},
		},
	}
	err = testClient.Create(ctx, obj)
	g.Expect(err).ToNot(HaveOccurred())

	t.Run("reconciles and creates artifacts", func(t *testing.T) {
		gt := NewWithT(t)
		gt.Eventually(func() bool {
			_ = testClient.Get(ctx, client.ObjectKeyFromObject(obj), resultAG)
			return gotkconditions.IsTrue(resultAG, gotkmeta.ReadyCondition)
		}, timeout, time.Second).Should(BeTrue(), "controller did not reconcile new artifacts")

		gt.Expect(gotkconditions.GetReason(resultAG, gotkmeta.ReadyCondition)).Should(BeIdenticalTo(gotkmeta.SucceededReason))

		gt.Expect(resultAG.Status.Inventory).To(HaveLen(2))
		for _, inv := range resultAG.Status.Inventory {
			externalArtifact := &sourcev1.ExternalArtifact{}
			key := client.ObjectKey{Name: inv.Name, Namespace: inv.Namespace}
			err = testClient.Get(ctx, key, externalArtifact)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(externalArtifact.Status.Artifact).ToNot(BeNil())
			g.Expect(externalArtifact.Status.Artifact.Revision).To(Equal(revision))
		}
	})

	t.Run("reconciles on source revision change", func(t *testing.T) {
		gt := NewWithT(t)
		revision = "v2.0.0"
		err = applyOCIRepository(objKey, revision, ociFiles)
		gt.Expect(err).ToNot(HaveOccurred())

		gt.Eventually(func() bool {
			eaList := &sourcev1.ExternalArtifactList{}
			_ = testClient.List(ctx, eaList, client.InNamespace(objKey.Namespace))
			countOK := 0
			for _, ea := range eaList.Items {
				if ea.Status.Artifact != nil && ea.Status.Artifact.Revision == revision {
					countOK++
				}
			}
			return countOK == 2
		}, timeout, time.Second).Should(BeTrue(), "controller did not reconcile new revision")
	})

	t.Run("reconciles on annotation set", func(t *testing.T) {
		gt := NewWithT(t)
		ts := time.Now().Format(time.RFC3339)
		patch := client.MergeFrom(obj.DeepCopy())
		if obj.Annotations == nil {
			obj.Annotations = map[string]string{}
		}
		obj.Annotations[gotkmeta.ReconcileRequestAnnotation] = ts
		err = testClient.Patch(ctx, obj, patch)
		gt.Expect(err).ToNot(HaveOccurred())

		gt.Eventually(func() bool {
			_ = testClient.Get(ctx, client.ObjectKeyFromObject(obj), resultAG)
			return resultAG.Status.LastHandledReconcileAt == ts
		}, timeout, time.Second).Should(BeTrue(), "controller did not reconcile on annotation set")
	})

	t.Run("finalizes and deletes artifacts", func(t *testing.T) {
		gt := NewWithT(t)
		err = testClient.Delete(ctx, obj)
		gt.Expect(err).ToNot(HaveOccurred())

		gt.Eventually(func() bool {
			err = testClient.Get(ctx, client.ObjectKeyFromObject(obj), resultAG)
			return apierrors.IsNotFound(err)
		}, timeout, time.Second).Should(BeTrue(), "controller did not finalize")

		eaList := &sourcev1.ExternalArtifactList{}
		_ = testClient.List(ctx, eaList, client.InNamespace(objKey.Namespace))
		gt.Expect(eaList.Items).To(BeEmpty())
	})
}

func applyOCIRepository(objKey client.ObjectKey, revision string, files []gotktestsrv.File) error {
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
				Type:               gotkmeta.ReadyCondition,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             gotkmeta.SucceededReason,
			},
		},
		Artifact: &gotkmeta.Artifact{
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
