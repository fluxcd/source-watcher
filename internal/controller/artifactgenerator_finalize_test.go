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
	"testing"
	"time"

	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/conditions"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

func TestResourceSetReconciler_Finalize(t *testing.T) {
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
	g.Expect(conditions.IsReconciling(obj)).To(BeTrue())
	g.Expect(conditions.GetReason(obj, meta.ReadyCondition)).To(Equal(meta.ProgressingReason))

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

func TestResourceSetReconciler_Finalize_Disabled(t *testing.T) {
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
	g.Expect(conditions.IsTrue(obj, meta.ReadyCondition)).To(BeTrue())
	g.Expect(conditions.GetReason(obj, meta.ReadyCondition)).To(Equal(swapi.ReconciliationDisabledReason))

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
