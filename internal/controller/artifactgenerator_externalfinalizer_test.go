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

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkconditions "github.com/fluxcd/pkg/runtime/conditions"
	gotktestsrv "github.com/fluxcd/pkg/testserver"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

func TestParseExternalFinalizerRef(t *testing.T) {
	g := NewWithT(t)

	ref, err := parseExternalFinalizerRef("fluxcd.controlplane.io/v1/ResourceSet/flux-system/tenants/1234-uid")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(ref.Group).To(Equal("fluxcd.controlplane.io"))
	g.Expect(ref.Version).To(Equal("v1"))
	g.Expect(ref.Kind).To(Equal("ResourceSet"))
	g.Expect(ref.Namespace).To(Equal("flux-system"))
	g.Expect(ref.Name).To(Equal("tenants"))
	g.Expect(ref.UID).To(Equal(types.UID("1234-uid")))
	g.Expect(ref.GroupVersionKind().String()).To(Equal("fluxcd.controlplane.io/v1, Kind=ResourceSet"))
	g.Expect(ref.String()).To(Equal("fluxcd.controlplane.io/v1/ResourceSet/flux-system/tenants/1234-uid"))

	// Core group and namespaced object.
	ref, err = parseExternalFinalizerRef("core/v1/ConfigMap/flux-system/rset/abc")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(ref.Group).To(BeEmpty())
	g.Expect(ref.Namespace).To(Equal("flux-system"))
	g.Expect(ref.String()).To(Equal("core/v1/ConfigMap/flux-system/rset/abc"))

	// Core group and cluster-scoped object.
	ref, err = parseExternalFinalizerRef("core/v1/Namespace/tenant/abc")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(ref.Group).To(BeEmpty())
	g.Expect(ref.Namespace).To(BeEmpty())
	g.Expect(ref.Name).To(Equal("tenant"))
	g.Expect(ref.String()).To(Equal("core/v1/Namespace/tenant/abc"))

	for _, value := range []string{
		"self",
		"fluxcd.controlplane.io/v1/ResourceSet/tenants",
		"fluxcd.controlplane.io/v1/ResourceSet//tenants/uid",
		"fluxcd.controlplane.io/v1///tenants/uid",
		"fluxcd.controlplane.io/v1/ResourceSet//tenants/",
		"fluxcd.controlplane.io//ResourceSet/tenants/uid",
		"/v1/ConfigMap/flux-system/rset/abc",
		"/v1/Namespace/tenant/abc",
	} {
		_, err := parseExternalFinalizerRef(value)
		g.Expect(err).To(HaveOccurred(), "value %q should be rejected", value)
	}
}

func TestValidateNamespaceMetadataExternalFinalizer(t *testing.T) {
	g := NewWithT(t)

	g.Expect(validateNamespaceMetadataExternalFinalizer("tenant", nil, nil)).To(Succeed())
	g.Expect(validateNamespaceMetadataExternalFinalizer("tenant", nil, map[string]string{"owner": "platform"})).To(Succeed())

	err := validateNamespaceMetadataExternalFinalizer("tenant", nil, map[string]string{
		swapi.ExternalFinalizerAnnotation: "fluxcd.controlplane.io/v1/ResourceSet/flux-system/tenants/uid",
	})
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(swapi.ExternalFinalizerAnnotation))

	// An empty value does not hand over the finalization and is ignored.
	g.Expect(validateNamespaceMetadataExternalFinalizer("tenant", nil, map[string]string{
		swapi.ExternalFinalizerAnnotation: "",
	})).To(Succeed())
}

func TestNamespaceExternallyFinalized(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	ns := &corev1.Namespace{Name: "tenant"}
	kubeClient := fake.NewClientBuilder().WithScheme(NewTestScheme()).WithObjects(ns).Build()
	r := &ArtifactGeneratorReconciler{
		Client:   kubeClient,
		Recorder: testEnv.GetEventRecorder(controllerName),
	}

	// Without the annotation the controller remains accountable.
	_, finalized, err := r.namespaceExternallyFinalized(ctx, "tenant")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(finalized).To(BeFalse())

	// A valid reference hands over the finalization without looking up the
	// referenced object.
	ns.Annotations = map[string]string{
		swapi.ExternalFinalizerAnnotation: "core/v1/ConfigMap/flux-system/missing/uid-1",
	}
	g.Expect(kubeClient.Update(ctx, ns)).To(Succeed())
	_, finalized, err = r.namespaceExternallyFinalized(ctx, "tenant")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(finalized).To(BeTrue())

	// A cluster-scoped reference is also accepted.
	ns.Annotations[swapi.ExternalFinalizerAnnotation] = "fluxcd.controlplane.io/v1/ResourceSet/tenants/uid-2"
	g.Expect(kubeClient.Update(ctx, ns)).To(Succeed())
	_, finalized, err = r.namespaceExternallyFinalized(ctx, "tenant")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(finalized).To(BeTrue())

	// A malformed value is propagated to the caller.
	ns.Annotations[swapi.ExternalFinalizerAnnotation] = "self"
	g.Expect(kubeClient.Update(ctx, ns)).To(Succeed())
	_, finalized, err = r.namespaceExternallyFinalized(ctx, "tenant")
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(swapi.ExternalFinalizerAnnotation))
	g.Expect(finalized).To(BeFalse())

	// A missing namespace is treated as already gone.
	_, finalized, err = r.namespaceExternallyFinalized(ctx, "does-not-exist")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(finalized).To(BeFalse())
}

func TestArtifactGeneratorReconciler_ManagedNamespacesExternalFinalizer(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-extfin-src")
	g.Expect(err).ToNot(HaveOccurred())

	tgtName := fmt.Sprintf("test-mns-extfin-tgt-%s", rand.String(5))

	saName := "test-mns-extfin-sa"
	g.Expect(createServiceAccountRBAC(ctx, saName, srcNS.Name)).To(Succeed())
	g.Expect(grantNamespaceManagement(ctx, saName, srcNS.Name)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-mns-extfin", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.ServiceAccountName = saName
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.ManagedNamespaces = &swapi.ManagedNamespaces{
		Prune:        true,
		AllowedNames: []string{"true"},
	}
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: tgtName,
			Copy: []swapi.CopyOperation{
				{From: fmt.Sprintf("@%s-git/**", objKey.Name), To: "@artifact/"},
			},
		},
	}
	g.Expect(testClient.Create(ctx, obj)).To(Succeed())

	gitFiles := []gotktestsrv.File{
		{Name: "app.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-config"},
	}
	g.Expect(applyGitRepository(objKey, "main@sha256:abc123", gitFiles)).To(Succeed())

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	// The external finalizer object, standing in for the ResourceSet.
	fin := &corev1.ConfigMap{Name: "test-finalizer", Namespace: srcNS.Name}
	g.Expect(testClient.Create(ctx, fin)).To(Succeed())
	g.Expect(testClient.Get(ctx, client.ObjectKeyFromObject(fin), fin)).To(Succeed())

	// Delegate the finalization of the managed namespace to the external object.
	ns := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, ns)).To(Succeed())
	if ns.Annotations == nil {
		ns.Annotations = map[string]string{}
	}
	ns.Annotations[swapi.ExternalFinalizerAnnotation] = fmt.Sprintf("core/v1/ConfigMap/%s/%s/%s", fin.Namespace, fin.Name, fin.UID)
	g.Expect(testClient.Update(ctx, ns)).To(Succeed())

	// Deleting the ArtifactGenerator hands the namespace over: the finalizer is
	// removed and the namespace is not deleted.
	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(testClient.Delete(ctx, obj)).To(Succeed())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(apierrors.IsNotFound(testClient.Get(ctx, objKey, &swapi.ArtifactGenerator{}))).To(BeTrue())
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, ns)).To(Succeed())
}

func TestArtifactGeneratorReconciler_ManagedNamespacesExternalFinalizerMalformed(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-extfin-malformed-src")
	g.Expect(err).ToNot(HaveOccurred())

	tgtName := fmt.Sprintf("test-mns-extfin-malformed-tgt-%s", rand.String(5))

	saName := "test-mns-extfin-malformed-sa"
	g.Expect(createServiceAccountRBAC(ctx, saName, srcNS.Name)).To(Succeed())
	g.Expect(grantNamespaceManagement(ctx, saName, srcNS.Name)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-mns-extfin-malformed", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.ServiceAccountName = saName
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.ManagedNamespaces = &swapi.ManagedNamespaces{
		Prune:        true,
		AllowedNames: []string{"true"},
	}
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: tgtName,
			Copy: []swapi.CopyOperation{
				{From: fmt.Sprintf("@%s-git/**", objKey.Name), To: "@artifact/"},
			},
		},
	}
	g.Expect(testClient.Create(ctx, obj)).To(Succeed())

	gitFiles := []gotktestsrv.File{
		{Name: "app.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-config"},
	}
	g.Expect(applyGitRepository(objKey, "main@sha256:abc123", gitFiles)).To(Succeed())

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	// A malformed reference is surfaced as an error, so the finalization is
	// retried and the namespace is not deleted.
	ns := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, ns)).To(Succeed())
	if ns.Annotations == nil {
		ns.Annotations = map[string]string{}
	}
	ns.Annotations[swapi.ExternalFinalizerAnnotation] = "self"
	g.Expect(testClient.Update(ctx, ns)).To(Succeed())

	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(testClient.Delete(ctx, obj)).To(Succeed())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).To(HaveOccurred())
	g.Expect(testClient.Get(ctx, objKey, &swapi.ArtifactGenerator{})).To(Succeed())
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, ns)).To(Succeed())
}

func TestArtifactGeneratorReconciler_ManagedNamespacesExternalFinalizerDesiredState(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-extfin-desired-src")
	g.Expect(err).ToNot(HaveOccurred())

	tgtName := fmt.Sprintf("test-mns-extfin-desired-tgt-%s", rand.String(5))

	saName := "test-mns-extfin-desired-sa"
	g.Expect(createServiceAccountRBAC(ctx, saName, srcNS.Name)).To(Succeed())
	g.Expect(grantNamespaceManagement(ctx, saName, srcNS.Name)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-mns-extfin-desired", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.ServiceAccountName = saName
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.ManagedNamespaces = &swapi.ManagedNamespaces{
		Prune:        true,
		AllowedNames: []string{"true"},
	}
	obj.Spec.CommonMetadata = &swapi.CommonMetadata{
		Annotations: map[string]string{
			swapi.ExternalFinalizerAnnotation: "core/v1/ConfigMap/flux-system/rset/uid",
		},
	}
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: tgtName,
			Copy: []swapi.CopyOperation{
				{From: fmt.Sprintf("@%s-git/**", objKey.Name), To: "@artifact/"},
			},
		},
	}
	// The annotation is accepted by the API but rejected when the controller
	// computes the final desired namespace metadata, so that every source
	// (commonMetadata, fromSource and from) is covered by a single check.
	g.Expect(testClient.Create(ctx, obj)).To(Succeed())

	gitFiles := []gotktestsrv.File{
		{Name: "app.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-config"},
	}
	g.Expect(applyGitRepository(objKey, "main@sha256:abc123", gitFiles)).To(Succeed())

	// Add the finalizer.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	// The reserved annotation in the desired metadata is rejected.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(swapi.ExternalFinalizerAnnotation))

	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(gotkconditions.IsReady(obj)).To(BeFalse())
	g.Expect(gotkconditions.GetReason(obj, gotkmeta.ReadyCondition)).To(Equal(gotkmeta.ReconciliationFailedReason))
	expectNamespaceDeleted(g, ctx, tgtName)
}
