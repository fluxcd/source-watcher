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

package controller_test

import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

func TestArtifactGenerator_CommonMetadataAnnotationsValidation(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ns, err := testEnv.CreateNamespace(ctx, "test-commonmeta")
	g.Expect(err).ToNot(HaveOccurred())

	newObj := func(name string) *swapi.ArtifactGenerator {
		return &swapi.ArtifactGenerator{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns.Name,
			},
			Spec: swapi.ArtifactGeneratorSpec{
				Sources: []swapi.SourceReference{
					{
						Alias: "repo",
						Kind:  "GitRepository",
						Name:  "repo",
					},
				},
				OutputArtifacts: []swapi.OutputArtifact{
					{
						Name: "app",
						Copy: []swapi.CopyOperation{
							{From: "@repo/**", To: "@artifact/"},
						},
					},
				},
			},
		}
	}

	// Object-level control annotations are rejected in commonMetadata.
	controlAnnotations := []string{
		swapi.SSAAnnotation,
		swapi.PruneAnnotation,
		swapi.ReconcileAnnotation,
	}
	for i, key := range controlAnnotations {
		obj := newObj(fmt.Sprintf("rejected-%d", i))
		obj.Spec.CommonMetadata = &swapi.CommonMetadata{
			Annotations: map[string]string{key: "disabled"},
		}
		err := testClient.Create(ctx, obj)
		g.Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected %s to be rejected", key)
		g.Expect(err.Error()).To(ContainSubstring("commonMetadata must not set"))
	}

	// Unrelated annotations are accepted.
	obj := newObj("accepted")
	obj.Spec.CommonMetadata = &swapi.CommonMetadata{
		Annotations: map[string]string{"example.com/team": "platform"},
	}
	g.Expect(testClient.Create(ctx, obj)).To(Succeed())
}
