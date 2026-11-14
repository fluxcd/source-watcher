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
	"errors"
	"testing"

	. "github.com/onsi/gomega"
	authzv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

func newAllowAllAccessChecker(obj *swapi.ArtifactGenerator) *accessChecker {
	kubeClient := fake.NewClientBuilder().
		WithScheme(NewTestScheme()).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				obj.(*authzv1.SubjectAccessReview).Status.Allowed = true
				return nil
			},
		}).Build()
	return newAccessChecker(kubeClient, obj, "")
}

func TestAccessCheckerDeduplicates(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	var reviews []authzv1.SubjectAccessReview
	kubeClient := fake.NewClientBuilder().
		WithScheme(NewTestScheme()).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				review := obj.(*authzv1.SubjectAccessReview)
				reviews = append(reviews, *review.DeepCopy())
				review.Status.Allowed = true
				return nil
			},
		}).Build()

	obj := &swapi.ArtifactGenerator{
		Name: "test", Namespace: "flux-system",
	}
	checker := newAccessChecker(kubeClient, obj, "default-sa")

	g.Expect(checker.ensureNamespaceVerb(ctx, "get")).To(Succeed())
	g.Expect(checker.ensureNamespaceVerb(ctx, "get")).To(Succeed())
	g.Expect(checker.ensureExternalArtifactVerb(ctx, "tenant", "get", "")).To(Succeed())
	g.Expect(checker.ensureExternalArtifactVerb(ctx, "tenant", "get", "")).To(Succeed())
	g.Expect(checker.ensureExternalArtifactVerb(ctx, "tenant", "patch", "status")).To(Succeed())

	g.Expect(reviews).To(HaveLen(3))
	g.Expect(reviews[0].Spec.User).To(Equal("system:serviceaccount:flux-system:default-sa"))
	g.Expect(reviews[0].Spec.ResourceAttributes.Resource).To(Equal("namespaces"))
	g.Expect(reviews[1].Spec.ResourceAttributes.Namespace).To(Equal("tenant"))
	g.Expect(reviews[1].Spec.ResourceAttributes.Resource).To(Equal("externalartifacts"))
	g.Expect(reviews[2].Spec.ResourceAttributes.Subresource).To(Equal("status"))
}

func TestAccessCheckerSameNamespaceUsesControllerCredentials(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	var reviews []authzv1.SubjectAccessReview
	kubeClient := fake.NewClientBuilder().
		WithScheme(NewTestScheme()).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				reviews = append(reviews, *obj.(*authzv1.SubjectAccessReview).DeepCopy())
				obj.(*authzv1.SubjectAccessReview).Status.Allowed = true
				return nil
			},
		}).Build()

	obj := &swapi.ArtifactGenerator{
		Name: "test", Namespace: "flux-system",
	}
	checker := newAccessChecker(kubeClient, obj, "default-sa")

	g.Expect(checker.ensureExternalArtifactVerb(ctx, "flux-system", "get", "")).To(Succeed())
	g.Expect(reviews).To(BeEmpty())

	g.Expect(checker.ensureExternalArtifactVerb(ctx, "tenant", "get", "")).To(Succeed())
	g.Expect(reviews).To(HaveLen(1))
}

func TestAccessCheckerDenied(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	kubeClient := fake.NewClientBuilder().
		WithScheme(NewTestScheme()).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				return nil
			},
		}).Build()

	obj := &swapi.ArtifactGenerator{
		Name: "test", Namespace: "flux-system",
	}
	checker := newAccessChecker(kubeClient, obj, "default-sa")

	err := checker.ensureNamespaceVerb(ctx, "create")
	g.Expect(err).To(HaveOccurred())
	var denied *accessDeniedError
	g.Expect(errors.As(err, &denied)).To(BeTrue())

	// A failed check must not be cached as allowed.
	g.Expect(checker.ensureNamespaceVerb(ctx, "create")).To(HaveOccurred())
}
