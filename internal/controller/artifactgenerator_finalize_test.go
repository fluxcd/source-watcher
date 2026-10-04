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
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkconditions "github.com/fluxcd/pkg/runtime/conditions"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

func TestArtifactGeneratorReconciler_Finalize(t *testing.T) {
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

	// Initialize the object with the finalizer
	r, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: objKey,
	})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(r.RequeueAfter).To(BeEquivalentTo(time.Millisecond))

	// Verify the finalizer was added
	err = testClient.Get(ctx, objKey, obj)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(obj.Finalizers).To(ContainElement(swapi.Finalizer))

	// Verify the object is in reconciling state
	g.Expect(gotkconditions.IsReconciling(obj)).To(BeTrue())
	g.Expect(gotkconditions.GetReason(obj, gotkmeta.ReadyCondition)).To(Equal(gotkmeta.ProgressingReason))

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
}

func TestArtifactGeneratorReconciler_FinalizeReferencesSurvivors(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	// A client whose Delete always fails, simulating an apiserver rejection
	// such as RBAC or an admission webhook. The namespace must exist in the
	// fake cluster, otherwise the server-side apply delete treats it as
	// already deleted.
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "tenant", errors.New("forbidden"))
	kubeClient := fake.NewClientBuilder().
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant"}}).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				return forbidden
			},
		}).Build()

	reconciler := &ArtifactGeneratorReconciler{
		Client:         kubeClient,
		ControllerName: controllerName,
		Storage:        testStorage,
	}

	pruneTrue := true
	obj := &swapi.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "src", UID: "uid"},
		Spec: swapi.ArtifactGeneratorSpec{
			Namespaces: &swapi.Namespaces{
				Strategy: swapi.NamespaceStrategyManaged,
				Prune:    &pruneTrue,
			},
		},
	}

	artifact := swapi.InventoryEntry{
		Kind: sourcev1.ExternalArtifactKind,
		Name: "ea", Namespace: "tenant", Filename: "ea.tar.gz",
	}
	namespace := swapi.InventoryEntry{
		Kind: swapi.NamespaceKind, Name: "tenant",
	}

	// Both entries are survivors when their deletion is not confirmed.
	survivors := reconciler.finalizeReferences(ctx, obj, []swapi.InventoryEntry{artifact, namespace}, nil)
	g.Expect(survivors).To(ConsistOf(artifact, namespace))

	// Namespaces are not survivors when management is disabled.
	unmanaged := obj.DeepCopy()
	unmanaged.Spec.Namespaces = &swapi.Namespaces{Strategy: swapi.NamespaceStrategyUnmanaged}
	survivors = reconciler.finalizeReferences(ctx, unmanaged, []swapi.InventoryEntry{namespace}, nil)
	g.Expect(survivors).To(BeEmpty())

	// Namespaces are not deleted when pruning is disabled, even with the
	// DefaultToPruneNamespaces feature gate enabled.
	gateReconciler := *reconciler
	gateReconciler.DefaultToPruneNamespaces = true

	noPrune := obj.DeepCopy()
	prune := false
	noPrune.Spec.Namespaces.Prune = &prune
	survivors = gateReconciler.finalizeReferences(ctx, noPrune, []swapi.InventoryEntry{namespace}, nil)
	g.Expect(survivors).To(BeEmpty())

	// With the feature gate enabled, an unset prune is interpreted as true.
	unsetPrune := obj.DeepCopy()
	unsetPrune.Spec.Namespaces.Prune = nil
	survivors = gateReconciler.finalizeReferences(ctx, unsetPrune, []swapi.InventoryEntry{namespace}, nil)
	g.Expect(survivors).To(ConsistOf(namespace))

	// Without the feature gate, an unset prune is interpreted as false.
	survivors = reconciler.finalizeReferences(ctx, unsetPrune, []swapi.InventoryEntry{namespace}, nil)
	g.Expect(survivors).To(BeEmpty())
}

func TestArtifactGeneratorReconciler_Finalize_Disabled(t *testing.T) {
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
	obj.SetAnnotations(map[string]string{
		swapi.ReconcileAnnotation: swapi.DisabledValue,
	})
	err = testClient.Create(ctx, obj)
	g.Expect(err).ToNot(HaveOccurred())

	// Initialize the object with the finalizer
	r, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: objKey,
	})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(r.RequeueAfter).To(BeEquivalentTo(time.Millisecond))

	// Verify the finalizer was added
	err = testClient.Get(ctx, objKey, obj)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(obj.Finalizers).To(ContainElement(swapi.Finalizer))

	// Reconcile disabled object
	r, err = reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: objKey,
	})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(r.RequeueAfter).To(BeZero())

	// Verify the object is marked as disabled
	err = testClient.Get(ctx, objKey, obj)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(gotkconditions.IsTrue(obj, gotkmeta.ReadyCondition)).To(BeTrue())
	g.Expect(gotkconditions.GetReason(obj, gotkmeta.ReadyCondition)).To(Equal(swapi.ReconciliationDisabledReason))

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
}
