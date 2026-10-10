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
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkconditions "github.com/fluxcd/pkg/runtime/conditions"
	gotktestenv "github.com/fluxcd/pkg/runtime/testenv"
	"github.com/fluxcd/pkg/ssa"
	gotktestsrv "github.com/fluxcd/pkg/testserver"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

func TestNamespaceApplyOptions(t *testing.T) {
	g := NewWithT(t)
	r := &ArtifactGeneratorReconciler{
		ControllerName:          "source-watcher",
		DisallowedFieldManagers: []string{"argocd", "rancher"},
	}

	opts := r.namespaceApplyOptions()

	g.Expect(opts.ExclusionSelector).To(Equal(map[string]string{
		swapi.ReconcileAnnotation: swapi.DisabledValue,
		swapi.SSAAnnotation:       swapi.IgnoreValue,
	}))
	g.Expect(opts.IfNotPresentSelector).To(Equal(map[string]string{
		swapi.SSAAnnotation: swapi.IfNotPresentValue,
	}))
	g.Expect(opts.Cleanup.Exclusions).To(Equal(map[string]string{
		swapi.SSAAnnotation: swapi.MergeValue,
	}))
	g.Expect(opts.Cleanup.FieldManagers).To(Equal([]ssa.FieldManager{
		{Name: "kubectl", OperationType: metav1.ManagedFieldsOperationApply},
		{Name: "kubectl", OperationType: metav1.ManagedFieldsOperationUpdate},
		{Name: "before-first-apply", OperationType: metav1.ManagedFieldsOperationUpdate},
		{Name: "argocd", OperationType: metav1.ManagedFieldsOperationApply, ExactMatch: true},
		{Name: "argocd", OperationType: metav1.ManagedFieldsOperationUpdate, ExactMatch: true},
		{Name: "rancher", OperationType: metav1.ManagedFieldsOperationApply, ExactMatch: true},
		{Name: "rancher", OperationType: metav1.ManagedFieldsOperationUpdate, ExactMatch: true},
	}))
}

func TestNamespaceMetadata(t *testing.T) {
	g := NewWithT(t)
	r := &ArtifactGeneratorReconciler{ControllerName: "source-watcher"}

	obj := &swapi.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{Namespace: "flux-system", UID: "uid"},
		Spec: swapi.ArtifactGeneratorSpec{
			CommonMetadata: &swapi.CommonMetadata{
				Labels:      map[string]string{"common": "base", "team": "platform"},
				Annotations: map[string]string{"owner": "platform", "preserved": "common"},
			},
			Namespaces: &swapi.Namespaces{
				Strategy: swapi.NamespaceStrategyManaged,
				Metadata: &swapi.NamespacesMetadata{
					From: []swapi.NamespaceMetadataFrom{
						{
							// Override merges onto the common metadata,
							// replacing existing keys.
							Strategy:    swapi.OverrideStrategy,
							Namespace:   "*",
							Labels:      map[string]string{"scope": "global", "team": "all"},
							Annotations: map[string]string{"scope": "global"},
						},
						{
							// Merge only adds absent keys.
							Strategy:    swapi.MergeStrategy,
							Namespace:   "tenant-a",
							Labels:      map[string]string{"scope": "ignored", "tier": "gold"},
							Annotations: map[string]string{"owner": "ignored"},
						},
						{
							// Override replaces only the values that are set.
							Strategy:  swapi.OverrideStrategy,
							Namespace: "tenant-a",
							Labels:    map[string]string{"tier": "silver"},
						},
						{
							// The controller labels cannot be overridden.
							Strategy:  swapi.OverrideStrategy,
							Namespace: "tenant-a",
							Labels:    map[string]string{swapi.ArtifactGeneratorLabel: "spoofed"},
						},
					},
				},
			},
		},
	}

	// Namespaces other than tenant-a only get the '*' operation.
	labels, annotations := r.namespaceMetadataFor(obj, "tenant-b", nil, nil)
	g.Expect(labels).To(Equal(map[string]string{
		"common":                       "base",
		"team":                         "all",
		"scope":                        "global",
		"app.kubernetes.io/managed-by": "source-watcher",
		swapi.ArtifactGeneratorLabel:   "uid",
	}))
	g.Expect(annotations).To(Equal(map[string]string{
		"owner":     "platform",
		"preserved": "common",
		"scope":     "global",
	}))

	// tenant-a applies all matching operations in order.
	labels, annotations = r.namespaceMetadataFor(obj, "tenant-a", nil, nil)
	g.Expect(labels).To(Equal(map[string]string{
		"common":                       "base",
		"team":                         "all",
		"scope":                        "global",
		"tier":                         "silver",
		"app.kubernetes.io/managed-by": "source-watcher",
		swapi.ArtifactGeneratorLabel:   "uid",
	}))
	g.Expect(annotations).To(Equal(map[string]string{
		"owner":     "platform",
		"preserved": "common",
		"scope":     "global",
	}))

	// Reset clears the metadata built so far, then applies its own.
	obj.Spec.Namespaces.Metadata.From = []swapi.NamespaceMetadataFrom{
		{
			Strategy:    swapi.NamespaceMetadataResetStrategy,
			Namespace:   "*",
			Labels:      map[string]string{"reset": "true"},
			Annotations: map[string]string{"reset": "true"},
		},
	}
	labels, annotations = r.namespaceMetadataFor(obj, "tenant-b", nil, nil)
	g.Expect(labels).To(Equal(map[string]string{
		"reset":                        "true",
		"app.kubernetes.io/managed-by": "source-watcher",
		swapi.ArtifactGeneratorLabel:   "uid",
	}))
	g.Expect(annotations).To(Equal(map[string]string{"reset": "true"}))
}

func TestArtifactGeneratorReconciler_CrossNamespace(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-agns-src")
	g.Expect(err).ToNot(HaveOccurred())
	tgtNS, err := testEnv.CreateNamespace(ctx, "test-agns-tgt")
	g.Expect(err).ToNot(HaveOccurred())

	// The ServiceAccount lives in the ArtifactGenerator namespace and is
	// granted access to the target namespace through a RoleBinding.
	saName := "artifact-generator"
	g.Expect(createImpersonationRBAC(ctx, saName, srcNS.Name, tgtNS.Name)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-agns", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.ServiceAccountName = saName
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: tgtNS.Name,
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

	// Add the finalizer.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	// Build the artifact and reconcile the ExternalArtifact.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(gotkconditions.IsReady(obj)).To(BeTrue())
	g.Expect(obj.Status.Inventory).To(HaveLen(1))
	g.Expect(obj.Status.Inventory[0].Namespace).To(Equal(tgtNS.Name))

	// The ExternalArtifact must exist in the target namespace only.
	eaKey := client.ObjectKey{Name: fmt.Sprintf("%s-git", objKey.Name), Namespace: tgtNS.Name}
	ea := &sourcev1.ExternalArtifact{}
	g.Expect(testClient.Get(ctx, eaKey, ea)).To(Succeed())
	g.Expect(ea.Status.Artifact).ToNot(BeNil())
	g.Expect(ea.Spec.SourceRef.Namespace).To(Equal(srcNS.Name))

	err = testClient.Get(ctx, client.ObjectKey{Name: eaKey.Name, Namespace: srcNS.Name}, &sourcev1.ExternalArtifact{})
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

	// Deleting the ArtifactGenerator must remove the ExternalArtifact
	// from the target namespace.
	g.Expect(testClient.Delete(ctx, obj)).To(Succeed())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	err = testClient.Get(ctx, eaKey, &sourcev1.ExternalArtifact{})
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
}

func TestArtifactGeneratorReconciler_CrossNamespacePathPattern(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-agns-pp-src")
	g.Expect(err).ToNot(HaveOccurred())
	tgtNS1, err := testEnv.CreateNamespace(ctx, "test-agns-pp-a")
	g.Expect(err).ToNot(HaveOccurred())
	tgtNS2, err := testEnv.CreateNamespace(ctx, "test-agns-pp-b")
	g.Expect(err).ToNot(HaveOccurred())

	// The ServiceAccount lives in the ArtifactGenerator namespace and is
	// granted access to both target namespaces through RoleBindings.
	saName := "artifact-generator-pp"
	g.Expect(createImpersonationRBAC(ctx, saName, srcNS.Name, tgtNS1.Name, tgtNS2.Name)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-agns-pp", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.ServiceAccountName = saName
	obj.Spec.Sources = obj.Spec.Sources[:1]
	alias := obj.Spec.Sources[0].Alias
	obj.Spec.PathPattern = fmt.Sprintf("@%s/apps/{tenant}", alias)
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      "{tenant}",
			Namespace: "{tenant}",
			Copy: []swapi.CopyOperation{
				{From: fmt.Sprintf("@%s/apps/{tenant}/**", alias), To: "@artifact/"},
			},
		},
	}
	g.Expect(testClient.Create(ctx, obj)).To(Succeed())

	// The captured directory names are the target namespaces.
	gitFiles := []gotktestsrv.File{
		{Name: fmt.Sprintf("apps/%s/app.yaml", tgtNS1.Name), Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-config"},
		{Name: fmt.Sprintf("apps/%s/app.yaml", tgtNS2.Name), Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-config"},
	}
	g.Expect(applyGitRepository(objKey, "main@sha256:abc123", gitFiles)).To(Succeed())

	// Add the finalizer.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	// Build the artifacts and reconcile the ExternalArtifacts.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(gotkconditions.IsReady(obj)).To(BeTrue())
	g.Expect(obj.Status.Inventory).To(HaveLen(2))

	for _, targetNS := range []*corev1.Namespace{tgtNS1, tgtNS2} {
		eaKey := client.ObjectKey{Name: targetNS.Name, Namespace: targetNS.Name}
		ea := &sourcev1.ExternalArtifact{}
		g.Expect(testClient.Get(ctx, eaKey, ea)).To(Succeed())
		g.Expect(ea.Status.Artifact).ToNot(BeNil())
		g.Expect(ea.Spec.SourceRef.Namespace).To(Equal(srcNS.Name))
	}

	// Deleting the ArtifactGenerator must remove the ExternalArtifacts
	// from the target namespaces.
	g.Expect(testClient.Delete(ctx, obj)).To(Succeed())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	for _, targetNS := range []*corev1.Namespace{tgtNS1, tgtNS2} {
		eaKey := client.ObjectKey{Name: targetNS.Name, Namespace: targetNS.Name}
		err = testClient.Get(ctx, eaKey, &sourcev1.ExternalArtifact{})
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}
}

func TestArtifactGeneratorReconciler_DefaultServiceAccount(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-agns-def-src")
	g.Expect(err).ToNot(HaveOccurred())
	tgtNS, err := testEnv.CreateNamespace(ctx, "test-agns-def-tgt")
	g.Expect(err).ToNot(HaveOccurred())

	// Multi-tenancy lockdown: the controller default ServiceAccount is used
	// when the ArtifactGenerator does not specify one.
	saName := "artifact-generator-default"
	reconciler.DefaultServiceAccount = saName
	g.Expect(createImpersonationRBAC(ctx, saName, srcNS.Name, tgtNS.Name)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-agns-def", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: tgtNS.Name,
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

	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(gotkconditions.IsReady(obj)).To(BeTrue())

	eaKey := client.ObjectKey{Name: fmt.Sprintf("%s-git", objKey.Name), Namespace: tgtNS.Name}
	g.Expect(testClient.Get(ctx, eaKey, &sourcev1.ExternalArtifact{})).To(Succeed())
}

// TestArtifactGeneratorReconciler_ServiceAccountSameNamespace verifies that an
// explicit .spec.serviceAccountName is used for ExternalArtifacts in the
// ArtifactGenerator namespace too, so the ServiceAccount needs permission to
// manage them there.
func TestArtifactGeneratorReconciler_ServiceAccountSameNamespace(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-agns-same")
	g.Expect(err).ToNot(HaveOccurred())

	// The ServiceAccount lives in the ArtifactGenerator namespace and is
	// granted access to it.
	saName := "test-agns-same-sa"
	g.Expect(createImpersonationRBAC(ctx, saName, srcNS.Name, srcNS.Name)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-agns-same", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.ServiceAccountName = saName
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name: fmt.Sprintf("%s-git", objKey.Name),
			Copy: []swapi.CopyOperation{
				{From: fmt.Sprintf("@%s-git/**", objKey.Name), To: "@artifact/"},
			},
		},
		{
			Name:      fmt.Sprintf("%s-git-same-ns", objKey.Name),
			Namespace: srcNS.Name,
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

	// Add the finalizer.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	// Build the artifacts and reconcile the ExternalArtifacts.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(gotkconditions.IsReady(obj)).To(BeTrue())
	g.Expect(obj.Status.Inventory).To(HaveLen(2))

	for _, name := range []string{
		fmt.Sprintf("%s-git", objKey.Name),
		fmt.Sprintf("%s-git-same-ns", objKey.Name),
	} {
		ea := &sourcev1.ExternalArtifact{}
		g.Expect(testClient.Get(ctx, client.ObjectKey{Name: name, Namespace: srcNS.Name}, ea)).To(Succeed())
		g.Expect(ea.Status.Artifact).ToNot(BeNil())
	}
}

// TestArtifactGeneratorReconciler_ServiceAccountSameNamespaceDenied verifies
// that an explicit .spec.serviceAccountName is used for artifacts in the
// ArtifactGenerator namespace: when the ServiceAccount has no RBAC, the
// reconciliation fails instead of falling back to the controller credentials.
func TestArtifactGeneratorReconciler_ServiceAccountSameNamespaceDenied(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-agns-same-deny")
	g.Expect(err).ToNot(HaveOccurred())

	// The ServiceAccount exists but has no RBAC bindings.
	saName := "test-agns-same-deny-sa"
	g.Expect(testClient.Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: srcNS.Name},
	})).To(Succeed())

	objKey := client.ObjectKey{Name: "test-agns-same-deny", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.ServiceAccountName = saName
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name: fmt.Sprintf("%s-git", objKey.Name),
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

	// Add the finalizer.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	// The apply is rejected by RBAC, so the reconciliation must fail and no
	// ExternalArtifact may be created.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).To(HaveOccurred())
	g.Expect(apierrors.IsForbidden(err)).To(BeTrue())

	eaKey := client.ObjectKey{Name: fmt.Sprintf("%s-git", objKey.Name), Namespace: srcNS.Name}
	err = testClient.Get(ctx, eaKey, &sourcev1.ExternalArtifact{})
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
}

// TestArtifactGeneratorReconciler_DefaultServiceAccountSameNamespace verifies
// that the controller default ServiceAccount is not used for artifacts in the
// ArtifactGenerator namespace: those keep using the controller credentials
// unless .spec.serviceAccountName is set.
func TestArtifactGeneratorReconciler_DefaultServiceAccountSameNamespace(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	// The default ServiceAccount does not exist, so any attempt to impersonate
	// it would make the reconciliation fail. In-namespace artifacts must not
	// use it.
	reconciler.DefaultServiceAccount = "missing-default-sa"
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-agns-def-same")
	g.Expect(err).ToNot(HaveOccurred())

	objKey := client.ObjectKey{Name: "test-agns-def-same", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name: fmt.Sprintf("%s-git", objKey.Name),
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

	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(gotkconditions.IsReady(obj)).To(BeTrue())

	eaKey := client.ObjectKey{Name: fmt.Sprintf("%s-git", objKey.Name), Namespace: srcNS.Name}
	ea := &sourcev1.ExternalArtifact{}
	g.Expect(testClient.Get(ctx, eaKey, ea)).To(Succeed())
	g.Expect(ea.Status.Artifact).ToNot(BeNil())
}

func TestArtifactGeneratorReconciler_OwnershipConflict(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-agns-owner-src")
	g.Expect(err).ToNot(HaveOccurred())
	tgtNS, err := testEnv.CreateNamespace(ctx, "test-agns-owner-tgt")
	g.Expect(err).ToNot(HaveOccurred())

	// The ServiceAccount lives in the ArtifactGenerator namespace and is
	// granted access to the target namespace through a RoleBinding.
	saName := "artifact-generator-owner"
	g.Expect(createImpersonationRBAC(ctx, saName, srcNS.Name, tgtNS.Name)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-agns-owner", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.ServiceAccountName = saName
	obj.Spec.Sources = obj.Spec.Sources[:1]
	eaNames := []string{
		fmt.Sprintf("%s-git", objKey.Name),
		fmt.Sprintf("%s-git-second", objKey.Name),
	}
	obj.Spec.OutputArtifacts = nil
	for _, name := range eaNames {
		obj.Spec.OutputArtifacts = append(obj.Spec.OutputArtifacts, swapi.OutputArtifact{
			Name:      name,
			Namespace: tgtNS.Name,
			Copy: []swapi.CopyOperation{
				{From: fmt.Sprintf("@%s-git/**", objKey.Name), To: "@artifact/"},
			},
		})
	}
	g.Expect(testClient.Create(ctx, obj)).To(Succeed())

	gitFiles := []gotktestsrv.File{
		{Name: "app.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-config"},
	}
	g.Expect(applyGitRepository(objKey, "main@sha256:abc123", gitFiles)).To(Succeed())

	// Pre-create the ExternalArtifacts in the target namespace, owned by a
	// different ArtifactGenerator.
	for _, name := range eaNames {
		otherEA := &sourcev1.ExternalArtifact{
			TypeMeta: metav1.TypeMeta{
				APIVersion: sourcev1.GroupVersion.String(),
				Kind:       sourcev1.ExternalArtifactKind,
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: tgtNS.Name,
				Labels: map[string]string{
					swapi.ArtifactGeneratorLabel: "11111111-1111-1111-1111-111111111111",
				},
			},
			Spec: sourcev1.ExternalArtifactSpec{
				SourceRef: &gotkmeta.NamespacedObjectKindReference{
					APIVersion: swapi.GroupVersion.String(),
					Kind:       swapi.ArtifactGeneratorKind,
					Name:       "other-generator",
					Namespace:  "other-namespace",
				},
			},
		}
		g.Expect(testClient.Create(ctx, otherEA)).To(Succeed())
	}

	// Add the finalizer.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	// Build the artifacts and take over the ExternalArtifacts.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	// The ExternalArtifacts are now owned by the current ArtifactGenerator.
	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	for _, name := range eaNames {
		ea := &sourcev1.ExternalArtifact{}
		g.Expect(testClient.Get(ctx, client.ObjectKey{Name: name, Namespace: tgtNS.Name}, ea)).To(Succeed())
		g.Expect(ea.Labels).To(HaveKeyWithValue(swapi.ArtifactGeneratorLabel, string(obj.GetUID())))

		// One warning event per taken over ExternalArtifact.
		g.Expect(eventsWithReason(getEvents(g, name, tgtNS.Name), swapi.OwnershipConflictReason)).
			To(HaveLen(1))
	}

	// A single summary warning event is emitted for the ArtifactGenerator,
	// counting the ExternalArtifacts that had a conflict.
	agConflicts := eventsWithReason(getEvents(g, obj.Name, obj.Namespace), swapi.OwnershipConflictReason)
	g.Expect(agConflicts).To(HaveLen(1))
	g.Expect(agConflicts[0].Type).To(Equal(corev1.EventTypeWarning))
	g.Expect(agConflicts[0].Message).To(Equal("ownership conflict detected for 2 ExternalArtifact(s)"))
}

// getEvents returns the Kubernetes events recorded for the given object,
// failing the test if they cannot be fetched.
func getEvents(g *WithT, objName, namespace string) []corev1.Event {
	events, err := gotktestenv.GetEvents(testCtx, testClient, objName, namespace, nil)
	g.Expect(err).ToNot(HaveOccurred())
	return events
}

// eventsWithReason returns the events with the given reason.
func eventsWithReason(events []corev1.Event, reason string) []corev1.Event {
	var result []corev1.Event
	for _, e := range events {
		if e.Reason == reason {
			result = append(result, e)
		}
	}
	return result
}

func TestArtifactGeneratorReconciler_CrossNamespaceAccessDenied(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-agns-deny-src")
	g.Expect(err).ToNot(HaveOccurred())
	tgtNS, err := testEnv.CreateNamespace(ctx, "test-agns-deny-tgt")
	g.Expect(err).ToNot(HaveOccurred())

	// The ServiceAccount exists but has no RBAC bindings in the target namespace.
	saName := "artifact-generator-denied"
	g.Expect(testClient.Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: srcNS.Name},
	})).To(Succeed())

	objKey := client.ObjectKey{Name: "test-agns-deny", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.ServiceAccountName = saName
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: tgtNS.Name,
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

	// The apply is rejected by RBAC, so the reconciliation must fail and no
	// ExternalArtifact may be created.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).To(HaveOccurred())
	g.Expect(apierrors.IsForbidden(err)).To(BeTrue())

	eaKey := client.ObjectKey{Name: fmt.Sprintf("%s-git", objKey.Name), Namespace: tgtNS.Name}
	err = testClient.Get(ctx, eaKey, &sourcev1.ExternalArtifact{})
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
}

func TestArtifactGeneratorReconciler_ManagedNamespaces(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-src")
	g.Expect(err).ToNot(HaveOccurred())

	tgtName := fmt.Sprintf("test-mns-tgt-%s", rand.String(5))

	objKey := client.ObjectKey{Name: "test-mns", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	prune := true
	obj.Spec.Namespaces = &swapi.Namespaces{
		Strategy: swapi.NamespaceStrategyManaged,
		Prune:    &prune,
	}
	obj.Spec.CommonMetadata = &swapi.CommonMetadata{
		Labels:      map[string]string{"team": "platform"},
		Annotations: map[string]string{"owner": "platform"},
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

	// Add the finalizer.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	// Create the namespace and reconcile the ExternalArtifact.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(gotkconditions.IsReady(obj)).To(BeTrue())
	g.Expect(gotkconditions.GetMessage(obj, gotkmeta.ReadyCondition)).To(ContainSubstring("manages 1 namespace(s)"))

	// The namespace is created with the common metadata and controller labels.
	ns := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, ns)).To(Succeed())
	g.Expect(ns.Labels).To(HaveKeyWithValue("team", "platform"))
	g.Expect(ns.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", controllerName))
	g.Expect(ns.Labels).To(HaveKeyWithValue(swapi.ArtifactGeneratorLabel, string(obj.GetUID())))
	g.Expect(ns.Annotations).To(HaveKeyWithValue("owner", "platform"))

	// The ExternalArtifact exists in the managed namespace.
	eaKey := client.ObjectKey{Name: fmt.Sprintf("%s-git", objKey.Name), Namespace: tgtName}
	g.Expect(testClient.Get(ctx, eaKey, &sourcev1.ExternalArtifact{})).To(Succeed())

	// The inventory tracks both the artifact and the namespace.
	g.Expect(obj.Status.Inventory).To(HaveLen(2))
	kinds := make([]string, 0, len(obj.Status.Inventory))
	for _, ref := range obj.Status.Inventory {
		kinds = append(kinds, ref.Kind)
	}
	g.Expect(kinds).To(ConsistOf(sourcev1.ExternalArtifactKind, swapi.NamespaceKind))

	// Metadata drift on a managed namespace is corrected.
	ns.Labels["team"] = "changed"
	g.Expect(testClient.Update(ctx, ns)).To(Succeed())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, ns)).To(Succeed())
	g.Expect(ns.Labels).To(HaveKeyWithValue("team", "platform"))

	// Changing the desired metadata is reported as a namespace update.
	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	obj.Spec.CommonMetadata.Labels["tier"] = "gold"
	g.Expect(testClient.Update(ctx, obj)).To(Succeed())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, ns)).To(Succeed())
	g.Expect(ns.Labels).To(HaveKeyWithValue("tier", "gold"))
	var reported bool
	for _, e := range getEvents(g, objKey.Name, objKey.Namespace) {
		if e.Type == corev1.EventTypeNormal && strings.Contains(e.Message, "namespaces reconciled") {
			reported = true
		}
	}
	g.Expect(reported).To(BeTrue())

	// Deleting the ArtifactGenerator prunes the managed namespace.
	g.Expect(testClient.Delete(ctx, obj)).To(Succeed())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	expectNamespaceDeleted(g, ctx, tgtName)
}

func TestArtifactGeneratorReconciler_ManagedNamespacesMetadata(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-meta-src")
	g.Expect(err).ToNot(HaveOccurred())

	tgtName := fmt.Sprintf("test-mns-meta-tgt-%s", rand.String(5))

	objKey := client.ObjectKey{Name: "test-mns-meta", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.CommonMetadata = &swapi.CommonMetadata{
		Labels:      map[string]string{"common": "base", "team": "platform"},
		Annotations: map[string]string{"owner": "platform"},
	}
	obj.Spec.Namespaces = &swapi.Namespaces{
		Strategy: swapi.NamespaceStrategyManaged,
		Metadata: &swapi.NamespacesMetadata{
			From: []swapi.NamespaceMetadataFrom{
				{
					// Override merges the metadata onto the common one.
					Strategy:    swapi.OverrideStrategy,
					Namespace:   "*",
					Labels:      map[string]string{"managed": "true"},
					Annotations: map[string]string{"scope": "all"},
				},
				{
					// Override replaces existing values.
					Strategy:    swapi.OverrideStrategy,
					Namespace:   tgtName,
					Labels:      map[string]string{"team": "tenant"},
					Annotations: map[string]string{"owner": "tenant"},
				},
			},
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
	g.Expect(testClient.Create(ctx, obj)).To(Succeed())

	gitFiles := []gotktestsrv.File{
		{Name: "app.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-config"},
	}
	g.Expect(applyGitRepository(objKey, "main@sha256:abc123", gitFiles)).To(Succeed())

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	ns := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, ns)).To(Succeed())
	// The '*' Override merges onto the common metadata.
	g.Expect(ns.Labels).To(HaveKeyWithValue("managed", "true"))
	g.Expect(ns.Labels).To(HaveKeyWithValue("common", "base"))
	// The target-specific Override replaces the common values.
	g.Expect(ns.Labels).To(HaveKeyWithValue("team", "tenant"))
	g.Expect(ns.Labels).To(HaveKey(swapi.ArtifactGeneratorLabel))
	g.Expect(ns.Annotations).To(HaveKeyWithValue("scope", "all"))
	g.Expect(ns.Annotations).To(HaveKeyWithValue("owner", "tenant"))
}

func TestArtifactGeneratorReconciler_ManagedNamespacesAdopt(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-adopt-src")
	g.Expect(err).ToNot(HaveOccurred())
	tgtNS, err := testEnv.CreateNamespace(ctx, "test-mns-adopt-tgt")
	g.Expect(err).ToNot(HaveOccurred())

	objKey := client.ObjectKey{Name: "test-mns-adopt", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.Namespaces = &swapi.Namespaces{Strategy: swapi.NamespaceStrategyManaged}
	obj.Spec.CommonMetadata = &swapi.CommonMetadata{
		Labels: map[string]string{"team": "platform"},
	}
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: tgtNS.Name,
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

	// The pre-existing namespace is adopted and labeled.
	ns := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtNS.Name}, ns)).To(Succeed())
	g.Expect(ns.Labels).To(HaveKeyWithValue(swapi.ArtifactGeneratorLabel, string(obj.GetUID())))
	g.Expect(ns.Labels).To(HaveKeyWithValue("team", "platform"))

	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	var nsDigest string
	for _, ref := range obj.Status.Inventory {
		if ref.Kind == swapi.NamespaceKind && ref.Name == tgtNS.Name {
			nsDigest = ref.Digest
		}
	}
	g.Expect(nsDigest).ToNot(BeEmpty())
	g.Expect(obj.HasNamespaceInInventory(tgtNS.Name, nsDigest)).To(BeTrue())

	// The adoption is summarized as a warning event on the ArtifactGenerator.
	g.Expect(eventsWithReason(getEvents(g, objKey.Name, objKey.Namespace), swapi.NamespaceAdoptedReason)).To(HaveLen(1))
}

func TestArtifactGeneratorReconciler_ManagedNamespacesTakeover(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-take-src")
	g.Expect(err).ToNot(HaveOccurred())

	tgtName := fmt.Sprintf("test-mns-take-tgt-%s", rand.String(5))

	// Pre-create the namespace owned by another ArtifactGenerator.
	other := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: tgtName,
			Labels: map[string]string{
				swapi.ArtifactGeneratorLabel: "11111111-1111-1111-1111-111111111111",
			},
		},
	}
	g.Expect(testClient.Create(ctx, other)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-mns-take", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.Namespaces = &swapi.Namespaces{Strategy: swapi.NamespaceStrategyManaged}
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

	// The namespace is taken over and labeled with the new owner.
	ns := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, ns)).To(Succeed())
	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(ns.Labels).To(HaveKeyWithValue(swapi.ArtifactGeneratorLabel, string(obj.GetUID())))

	// The takeover is summarized as an ownership conflict event on the AG.
	g.Expect(eventsWithReason(getEvents(g, objKey.Name, objKey.Namespace), swapi.OwnershipConflictReason)).To(HaveLen(1))
}

func TestArtifactGeneratorReconciler_ManagedNamespacesNoPrune(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-noprune-src")
	g.Expect(err).ToNot(HaveOccurred())

	tgtName := fmt.Sprintf("test-mns-noprune-tgt-%s", rand.String(5))

	prune := false
	objKey := client.ObjectKey{Name: "test-mns-noprune", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.Namespaces = &swapi.Namespaces{
		Strategy: swapi.NamespaceStrategyManaged,
		Prune:    &prune,
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

	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, &corev1.Namespace{})).To(Succeed())

	// Deleting the ArtifactGenerator leaves the managed namespace in place.
	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(testClient.Delete(ctx, obj)).To(Succeed())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	ns := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, ns)).To(Succeed())
	g.Expect(ns.DeletionTimestamp).To(BeNil())
}

func TestArtifactGeneratorReconciler_ManagedNamespacesReconcileDisabled(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-disabled-src")
	g.Expect(err).ToNot(HaveOccurred())
	tgtNS, err := testEnv.CreateNamespace(ctx, "test-mns-disabled-tgt")
	g.Expect(err).ToNot(HaveOccurred())

	objKey := client.ObjectKey{Name: "test-mns-disabled", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.Namespaces = &swapi.Namespaces{Strategy: swapi.NamespaceStrategyManaged}
	obj.Spec.CommonMetadata = &swapi.CommonMetadata{
		Labels: map[string]string{"team": "platform"},
	}
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: tgtNS.Name,
			Copy: []swapi.CopyOperation{
				{From: fmt.Sprintf("@%s-git/**", objKey.Name), To: "@artifact/"},
			},
		},
	}

	// The target namespace opts out of reconciliation in-cluster.
	ns := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtNS.Name}, ns)).To(Succeed())
	ns.SetAnnotations(map[string]string{swapi.ReconcileAnnotation: swapi.DisabledValue})
	g.Expect(testClient.Update(ctx, ns)).To(Succeed())

	g.Expect(testClient.Create(ctx, obj)).To(Succeed())

	gitFiles := []gotktestsrv.File{
		{Name: "app.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-config"},
	}
	g.Expect(applyGitRepository(objKey, "main@sha256:abc123", gitFiles)).To(Succeed())

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	// The namespace is tracked in the inventory but not modified.
	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(obj.Status.Inventory).To(HaveLen(2))
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtNS.Name}, ns)).To(Succeed())
	g.Expect(ns.Labels).ToNot(HaveKey("team"))
	g.Expect(ns.Labels).ToNot(HaveKey(swapi.ArtifactGeneratorLabel))

	// Skipped namespaces are not reported as adopted.
	g.Expect(eventsWithReason(getEvents(g, objKey.Name, objKey.Namespace), swapi.NamespaceAdoptedReason)).To(BeEmpty())

	// Deleting the ArtifactGenerator leaves the namespace in place.
	g.Expect(testClient.Delete(ctx, obj)).To(Succeed())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtNS.Name}, ns)).To(Succeed())
	g.Expect(ns.DeletionTimestamp).To(BeNil())
}

func TestArtifactGeneratorReconciler_ManagedNamespacesIgnore(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-ignore-src")
	g.Expect(err).ToNot(HaveOccurred())
	tgtNS, err := testEnv.CreateNamespace(ctx, "test-mns-ignore-tgt")
	g.Expect(err).ToNot(HaveOccurred())

	// The target namespace opts out of reconciliation in-cluster.
	ns := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtNS.Name}, ns)).To(Succeed())
	ns.SetAnnotations(map[string]string{swapi.SSAAnnotation: swapi.IgnoreValue})
	g.Expect(testClient.Update(ctx, ns)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-mns-ignore", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.Namespaces = &swapi.Namespaces{Strategy: swapi.NamespaceStrategyManaged}
	obj.Spec.CommonMetadata = &swapi.CommonMetadata{
		Labels: map[string]string{"team": "platform"},
	}
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: tgtNS.Name,
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

	// The namespace is neither labeled nor deleted.
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtNS.Name}, ns)).To(Succeed())
	g.Expect(ns.Labels).ToNot(HaveKey("team"))
	g.Expect(ns.Labels).ToNot(HaveKey(swapi.ArtifactGeneratorLabel))

	g.Expect(testClient.Delete(ctx, obj)).To(Succeed())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtNS.Name}, ns)).To(Succeed())
	g.Expect(ns.DeletionTimestamp).To(BeNil())
}

func TestArtifactGeneratorReconciler_ManagedNamespacesPruneDisabled(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-nodel-src")
	g.Expect(err).ToNot(HaveOccurred())

	tgtName := fmt.Sprintf("test-mns-nodel-tgt-%s", rand.String(5))

	prune := true
	objKey := client.ObjectKey{Name: "test-mns-nodel", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.Namespaces = &swapi.Namespaces{
		Strategy: swapi.NamespaceStrategyManaged,
		Prune:    &prune,
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

	// The namespace is created and managed.
	ns := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, ns)).To(Succeed())
	g.Expect(ns.Labels).To(HaveKey(swapi.ArtifactGeneratorLabel))

	// The namespace is protected from deletion in-cluster.
	ns.SetAnnotations(map[string]string{swapi.PruneAnnotation: swapi.DisabledValue})
	g.Expect(testClient.Update(ctx, ns)).To(Succeed())

	// Deleting the ArtifactGenerator leaves the namespace in place.
	g.Expect(testClient.Delete(ctx, obj)).To(Succeed())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtName}, ns)).To(Succeed())
	g.Expect(ns.DeletionTimestamp).To(BeNil())
}

func TestArtifactGeneratorReconciler_ManagedNamespacesIfNotPresent(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-inp-src")
	g.Expect(err).ToNot(HaveOccurred())
	existingNS, err := testEnv.CreateNamespace(ctx, "test-mns-inp-existing")
	g.Expect(err).ToNot(HaveOccurred())
	newName := fmt.Sprintf("test-mns-inp-new-%s", rand.String(5))

	// The existing namespace opts out of updates in-cluster.
	existing := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: existingNS.Name}, existing)).To(Succeed())
	existing.SetAnnotations(map[string]string{swapi.SSAAnnotation: swapi.IfNotPresentValue})
	g.Expect(testClient.Update(ctx, existing)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-mns-inp", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.Namespaces = &swapi.Namespaces{Strategy: swapi.NamespaceStrategyManaged}
	obj.Spec.CommonMetadata = &swapi.CommonMetadata{
		Labels: map[string]string{"team": "platform"},
	}
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: existingNS.Name,
			Copy: []swapi.CopyOperation{
				{From: fmt.Sprintf("@%s-git/**", objKey.Name), To: "@artifact/"},
			},
		},
		{
			Name:      fmt.Sprintf("%s-git-new", objKey.Name),
			Namespace: newName,
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

	// The existing namespace is not modified.
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: existingNS.Name}, existing)).To(Succeed())
	g.Expect(existing.Labels).ToNot(HaveKey("team"))
	g.Expect(existing.Labels).ToNot(HaveKey(swapi.ArtifactGeneratorLabel))

	// The missing namespace is created with the common metadata.
	created := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: newName}, created)).To(Succeed())
	g.Expect(created.Labels).To(HaveKeyWithValue("team", "platform"))
	g.Expect(created.Labels).To(HaveKey(swapi.ArtifactGeneratorLabel))
}

func TestArtifactGeneratorReconciler_ManagedNamespacesMerge(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-merge-src")
	g.Expect(err).ToNot(HaveOccurred())
	tgtNS, err := testEnv.CreateNamespace(ctx, "test-mns-merge-tgt")
	g.Expect(err).ToNot(HaveOccurred())

	// Simulate metadata owned by another field manager and opt into merge.
	ns := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtNS.Name}, ns)).To(Succeed())
	ns.SetAnnotations(map[string]string{
		corev1.LastAppliedConfigAnnotation: "{}",
		swapi.SSAAnnotation:                swapi.MergeValue,
	})
	g.Expect(testClient.Update(ctx, ns)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-mns-merge", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.Namespaces = &swapi.Namespaces{Strategy: swapi.NamespaceStrategyManaged}
	obj.Spec.CommonMetadata = &swapi.CommonMetadata{
		Labels: map[string]string{"team": "platform"},
	}
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: tgtNS.Name,
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

	// The metadata cleanup is skipped and the common metadata is applied.
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtNS.Name}, ns)).To(Succeed())
	g.Expect(ns.Annotations).To(HaveKey(corev1.LastAppliedConfigAnnotation))
	g.Expect(ns.Annotations).To(HaveKeyWithValue(swapi.SSAAnnotation, swapi.MergeValue))
	g.Expect(ns.Labels).To(HaveKeyWithValue("team", "platform"))
	g.Expect(ns.Labels).To(HaveKey(swapi.ArtifactGeneratorLabel))
}

func TestArtifactGeneratorReconciler_ManagedNamespacesPathPattern(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-pp-src")
	g.Expect(err).ToNot(HaveOccurred())

	tgt1 := fmt.Sprintf("test-mns-pp-a-%s", rand.String(5))
	tgt2 := fmt.Sprintf("test-mns-pp-b-%s", rand.String(5))

	objKey := client.ObjectKey{Name: "test-mns-pp", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.Sources = obj.Spec.Sources[:1]
	prune := true
	obj.Spec.Namespaces = &swapi.Namespaces{
		Strategy: swapi.NamespaceStrategyManaged,
		Prune:    &prune,
	}
	alias := obj.Spec.Sources[0].Alias
	obj.Spec.PathPattern = fmt.Sprintf("@%s/apps/{tenant}", alias)
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      "{tenant}",
			Namespace: "{tenant}",
			Copy: []swapi.CopyOperation{
				{From: fmt.Sprintf("@%s/apps/{tenant}/**", alias), To: "@artifact/"},
			},
		},
	}
	g.Expect(testClient.Create(ctx, obj)).To(Succeed())

	body := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test-config"
	g.Expect(applyGitRepository(objKey, "main@sha256:abc123", []gotktestsrv.File{
		{Name: fmt.Sprintf("apps/%s/app.yaml", tgt1), Body: body},
		{Name: fmt.Sprintf("apps/%s/app.yaml", tgt2), Body: body},
	})).To(Succeed())

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	for _, name := range []string{tgt1, tgt2} {
		g.Expect(testClient.Get(ctx, client.ObjectKey{Name: name}, &corev1.Namespace{})).To(Succeed())
	}

	// Removing a directory prunes its managed namespace.
	g.Expect(applyGitRepository(objKey, "main@sha256:def456", []gotktestsrv.File{
		{Name: fmt.Sprintf("apps/%s/app.yaml", tgt1), Body: body},
	})).To(Succeed())

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())

	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgt1}, &corev1.Namespace{})).To(Succeed())
	expectNamespaceDeleted(g, ctx, tgt2)
}

// expectNamespaceDeleted asserts that a namespace is either gone or in the
// process of being deleted. envtest does not run the namespace controller, so
// namespaces may remain in Terminating state after deletion.
func expectNamespaceDeleted(g Gomega, ctx context.Context, name string) {
	ns := &corev1.Namespace{}
	err := testClient.Get(ctx, client.ObjectKey{Name: name}, ns)
	if err == nil {
		g.Expect(ns.DeletionTimestamp).ToNot(BeNil())
	} else {
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}
}

func TestArtifactGeneratorReconciler_ManagedNamespacesImpersonation(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-imp-src")
	g.Expect(err).ToNot(HaveOccurred())
	tgtNS, err := testEnv.CreateNamespace(ctx, "test-mns-imp-tgt")
	g.Expect(err).ToNot(HaveOccurred())

	// The ServiceAccount manages ExternalArtifacts in the target namespace and
	// namespaces at cluster scope.
	saName := "test-mns-imp-sa"
	g.Expect(createImpersonationRBAC(ctx, saName, srcNS.Name, tgtNS.Name)).To(Succeed())
	g.Expect(grantNamespaceManagement(ctx, saName, srcNS.Name)).To(Succeed())

	objKey := client.ObjectKey{Name: "test-mns-imp", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.ServiceAccountName = saName
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.Namespaces = &swapi.Namespaces{Strategy: swapi.NamespaceStrategyManaged}
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: tgtNS.Name,
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

	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(gotkconditions.IsReady(obj)).To(BeTrue())

	ns := &corev1.Namespace{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: tgtNS.Name}, ns)).To(Succeed())
	g.Expect(ns.Labels).To(HaveKeyWithValue(swapi.ArtifactGeneratorLabel, string(obj.GetUID())))

	eaKey := client.ObjectKey{Name: fmt.Sprintf("%s-git", objKey.Name), Namespace: tgtNS.Name}
	g.Expect(testClient.Get(ctx, eaKey, &sourcev1.ExternalArtifact{})).To(Succeed())
}

func TestArtifactGeneratorReconciler_ManagedNamespacesPruneRetry(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	// Pruning is enabled by the feature gate so that an unset prune
	// interprets as true.
	reconciler.DefaultToPruneNamespaces = true
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-mns-retry-src")
	g.Expect(err).ToNot(HaveOccurred())
	tgtNS, err := testEnv.CreateNamespace(ctx, "test-mns-retry-tgt")
	g.Expect(err).ToNot(HaveOccurred())

	// The ServiceAccount can manage ExternalArtifacts and namespaces, but
	// cannot delete namespaces.
	saName := "test-mns-retry-sa"
	g.Expect(createImpersonationRBAC(ctx, saName, srcNS.Name, tgtNS.Name)).To(Succeed())
	g.Expect(grantNamespaceManagement(ctx, saName, srcNS.Name, "get", "list", "watch", "create", "update", "patch")).To(Succeed())

	objKey := client.ObjectKey{Name: "test-mns-retry", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.ServiceAccountName = saName
	obj.Spec.Sources = obj.Spec.Sources[:1]
	obj.Spec.Namespaces = &swapi.Namespaces{Strategy: swapi.NamespaceStrategyManaged}
	obj.Spec.OutputArtifacts = []swapi.OutputArtifact{
		{
			Name:      fmt.Sprintf("%s-git", objKey.Name),
			Namespace: tgtNS.Name,
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

	// Deleting the ArtifactGenerator deletes the ExternalArtifact but the
	// namespace delete is rejected, so the finalizer is retained and the
	// namespace stays tracked in the inventory.
	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(testClient.Delete(ctx, obj)).To(Succeed())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).To(HaveOccurred())

	g.Expect(testClient.Get(ctx, objKey, obj)).To(Succeed())
	g.Expect(obj.Finalizers).To(ContainElement(swapi.Finalizer))
	g.Expect(obj.Status.Inventory).To(HaveLen(1))
	g.Expect(obj.Status.Inventory[0].Kind).To(Equal(swapi.NamespaceKind))
	g.Expect(gotkconditions.GetReason(obj, gotkmeta.ReadyCondition)).To(Equal(gotkmeta.PruneFailedReason))

	// Grant delete and retry: the finalizer is removed and the namespace deleted.
	role := &rbacv1.ClusterRole{}
	g.Expect(testClient.Get(ctx, client.ObjectKey{Name: saName}, role)).To(Succeed())
	role.Rules[0].Verbs = append(role.Rules[0].Verbs, "delete")
	g.Expect(testClient.Update(ctx, role)).To(Succeed())

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objKey})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(apierrors.IsNotFound(testClient.Get(ctx, objKey, &swapi.ArtifactGenerator{}))).To(BeTrue())
	expectNamespaceDeleted(g, ctx, tgtNS.Name)
}

// grantNamespaceManagement grants an existing ServiceAccount permission to
// manage Namespace objects at cluster scope. When no verbs are provided, all
// the namespace verbs are granted.
func grantNamespaceManagement(ctx context.Context, saName, saNamespace string, verbs ...string) error {
	if len(verbs) == 0 {
		verbs = []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	}
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: saName},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"namespaces"},
				Verbs:     verbs,
			},
		},
	}
	if err := testClient.Create(ctx, role); err != nil {
		return err
	}

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: saName},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      saName,
				Namespace: saNamespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     saName,
		},
	}
	return testClient.Create(ctx, binding)
}

// createImpersonationRBAC creates a ServiceAccount in saNamespace and grants
// it permission to manage ExternalArtifacts in the given target namespaces.
func createImpersonationRBAC(ctx context.Context, saName, saNamespace string, targetNamespaces ...string) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: saNamespace,
		},
	}
	if err := testClient.Create(ctx, sa); err != nil {
		return err
	}

	for _, targetNamespace := range targetNamespaces {
		if err := grantImpersonationInNamespace(ctx, saName, saNamespace, targetNamespace); err != nil {
			return err
		}
	}
	return nil
}

// grantImpersonationInNamespace grants an existing ServiceAccount permission
// to manage ExternalArtifacts in the target namespace.
func grantImpersonationInNamespace(ctx context.Context, saName, saNamespace, targetNamespace string) error {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: targetNamespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{sourcev1.GroupVersion.Group},
				Resources: []string{"externalartifacts"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{sourcev1.GroupVersion.Group},
				Resources: []string{"externalartifacts/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
		},
	}
	if err := testClient.Create(ctx, role); err != nil {
		return err
	}

	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: targetNamespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      saName,
				Namespace: saNamespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     saName,
		},
	}
	return testClient.Create(ctx, binding)
}
