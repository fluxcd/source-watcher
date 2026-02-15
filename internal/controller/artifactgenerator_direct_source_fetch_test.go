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
	"testing"
	"time"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkconditions "github.com/fluxcd/pkg/runtime/conditions"
	gotktestsrv "github.com/fluxcd/pkg/testserver"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

func TestArtifactGeneratorReconciler_DirectSourceFetch(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Create a namespace
	ns, err := testEnv.CreateNamespace(ctx, "direct-fetch-test")
	g.Expect(err).ToNot(HaveOccurred())

	t.Run("reconciles with DirectSourceFetch enabled (uses APIReader)", func(t *testing.T) {
		g := NewWithT(t)

		// Create reconciler with DirectSourceFetch enabled
		reconciler := &ArtifactGeneratorReconciler{
			ControllerName:            controllerName,
			Client:                    testClient,
			APIReader:                 testClient,
			Scheme:                    testEnv.Scheme(),
			EventRecorder:             testEnv.GetEventRecorderFor(controllerName),
			Storage:                   testStorage,
			ArtifactFetchRetries:      1,
			DependencyRequeueInterval: 5 * time.Second,
			NoCrossNamespaceRefs:      true,
			DirectSourceFetch:         true, // Enable DirectSourceFetch
		}

		// Create the ArtifactGenerator object
		objKey := client.ObjectKey{
			Name:      "direct-fetch-enabled",
			Namespace: ns.Name,
		}
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
						Alias: fmt.Sprintf("%s-git", objKey.Name),
						Kind:  sourcev1.GitRepositoryKind,
						Name:  objKey.Name,
					},
				},
				OutputArtifacts: []swapi.OutputArtifact{
					{
						Name: fmt.Sprintf("%s-git", objKey.Name),
						Copy: []swapi.CopyOperation{
							{
								From: fmt.Sprintf("@%s-git/**", objKey.Name),
								To:   "@artifact/",
							},
						},
					},
				},
			},
		}
		err := testClient.Create(ctx, obj)
		g.Expect(err).ToNot(HaveOccurred())

		// Create the GitRepository source
		gitFiles := []gotktestsrv.File{
			{Name: "app.yaml", Body: "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: direct-fetch-test"},
		}
		err = applyGitRepository(objKey, "main@sha256:directfetch123", gitFiles)
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
		g.Expect(gotkconditions.IsReady(obj)).To(BeTrue())
		g.Expect(gotkconditions.GetReason(obj, gotkmeta.ReadyCondition)).To(Equal(gotkmeta.SucceededReason))

		t.Log(objToYaml(obj))
	})
}
