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
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkconditions "github.com/fluxcd/pkg/runtime/conditions"
	gotktestsrv "github.com/fluxcd/pkg/testserver"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

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

func TestArtifactGeneratorReconciler_ServiceAccountSameNamespace(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	reconciler.DefaultServiceAccount = "missing-default-sa"
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	srcNS, err := testEnv.CreateNamespace(ctx, "test-agns-same")
	g.Expect(err).ToNot(HaveOccurred())

	// The ServiceAccount does not exist, so any attempt to impersonate it
	// would make the reconciliation fail. Output artifacts in the
	// ArtifactGenerator namespace must be reconciled with the controller
	// client instead.
	objKey := client.ObjectKey{Name: "test-agns-same", Namespace: srcNS.Name}
	obj := getArtifactGenerator(objKey)
	obj.Spec.ServiceAccountName = "missing-sa"
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
		g.Expect(eventsWithReason(getEvents(name, tgtNS.Name), swapi.OwnershipConflictReason)).
			To(HaveLen(1))
	}

	// A single summary warning event is emitted for the ArtifactGenerator,
	// counting the ExternalArtifacts that had a conflict.
	agConflicts := eventsWithReason(getEvents(obj.Name, obj.Namespace), swapi.OwnershipConflictReason)
	g.Expect(agConflicts).To(HaveLen(1))
	g.Expect(agConflicts[0].Type).To(Equal(corev1.EventTypeWarning))
	g.Expect(agConflicts[0].Message).To(Equal("ownership conflict detected for 2 ExternalArtifact(s)"))
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

// createImpersonationRBAC creates a ServiceAccount in saNamespace and grants
// it permission to manage ExternalArtifacts in the target namespace.
func createImpersonationRBAC(ctx context.Context, saName, saNamespace, targetNamespace string) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: saNamespace,
		},
	}
	if err := testClient.Create(ctx, sa); err != nil {
		return err
	}

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
