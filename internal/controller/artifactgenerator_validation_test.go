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

	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/conditions"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

func TestResourceSetReconciler_specValidation(t *testing.T) {
	gt := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ns, err := testEnv.CreateNamespace(ctx, "test")
	gt.Expect(err).ToNot(HaveOccurred())

	tests := []struct {
		name           string
		objectName     string
		setupObj       func(obj *swapi.ArtifactGenerator, ns string)
		expectedReason string
	}{
		{
			name:           "denied cross-namespace source",
			objectName:     "test-cross-ns-source",
			expectedReason: swapi.AccessDeniedReason,
			setupObj: func(obj *swapi.ArtifactGenerator, ns string) {
				// Add a second source with the same alias
				obj.Spec.Sources = append(obj.Spec.Sources, swapi.SourceReference{
					Alias:     "other-repo",
					Kind:      "GitRepository",
					Name:      "different-repo",
					Namespace: "other-namespace", // Cross-namespace reference
				})
			},
		},
		{
			name:           "duplicate source aliases",
			objectName:     "test-source-aliases",
			expectedReason: swapi.ValidationFailedReason,
			setupObj: func(obj *swapi.ArtifactGenerator, ns string) {
				// Add a second source with the same alias
				obj.Spec.Sources = append(obj.Spec.Sources, swapi.SourceReference{
					Alias:     obj.Spec.Sources[0].Alias, // Duplicate alias
					Kind:      "GitRepository",
					Name:      "different-repo",
					Namespace: ns,
				})
			},
		},
		{
			name:           "duplicate artifact names",
			objectName:     "test-artifact-names",
			expectedReason: swapi.ValidationFailedReason,
			setupObj: func(obj *swapi.ArtifactGenerator, ns string) {
				// Add a second artifact with the same name
				obj.Spec.OutputArtifacts = append(obj.Spec.OutputArtifacts, swapi.OutputArtifact{
					Name: obj.Spec.OutputArtifacts[0].Name, // Duplicate name
					Copy: []swapi.CopyOperation{
						{
							From: "@git/file2.yaml",
							To:   "@artifact/file2.yaml",
						},
					},
				})
			},
		},
		{
			name:           "unknown artifact revision",
			objectName:     "test-artifact-revision",
			expectedReason: swapi.ValidationFailedReason,
			setupObj: func(obj *swapi.ArtifactGenerator, ns string) {
				// Add an unknown artifact revision
				obj.Spec.OutputArtifacts[0].Revision = "@unknown"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			objKey := client.ObjectKey{
				Name:      tt.objectName,
				Namespace: ns.Name,
			}
			obj := getArtifactGenerator(objKey)
			tt.setupObj(obj, ns.Name)

			err = testClient.Create(ctx, obj)
			g.Expect(err).ToNot(HaveOccurred())

			// Initialize the object with the finalizer
			r, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: objKey,
			})
			g.Expect(err).ToNot(HaveOccurred())

			// Verify the reconciler fails with terminal error
			r, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: objKey,
			})
			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, reconcile.TerminalError(nil))).To(BeTrue())
			g.Expect(r.RequeueAfter).To(BeZero())

			// Verify the object is stalled with validation failed reason
			err = testClient.Get(ctx, objKey, obj)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(conditions.IsStalled(obj)).To(BeTrue())
			g.Expect(conditions.GetReason(obj, meta.ReadyCondition)).To(Equal(tt.expectedReason))

			// Verify event was recorded
			events := getEvents(obj.Name, obj.Namespace)
			g.Expect(events).ToNot(BeEmpty())
			g.Expect(events[0].Reason).To(Equal(tt.expectedReason))
		})
	}
}

func TestArtifactGenerator_crdValidation(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ns, err := testEnv.CreateNamespace(ctx, "test")
	g.Expect(err).ToNot(HaveOccurred())

	tests := []struct {
		name        string
		setupObj    func() *swapi.ArtifactGenerator
		expectError bool
	}{
		{
			name: "valid source alias",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-valid-alias",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.Sources[0].Alias = "git_repo-v2"
				return obj
			},
			expectError: false,
		},
		{
			name: "invalid source alias uppercase",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-invalid-alias",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.Sources[0].Alias = "Invalid-Alias"
				return obj
			},
			expectError: true,
		},
		{
			name: "invalid source alias with hyphen at start",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-hyphen-start",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.Sources[0].Alias = "-invalid"
				return obj
			},
			expectError: true,
		},
		{
			name: "invalid source alias with hyphen at end",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-hyphen-end",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.Sources[0].Alias = "invalid-"
				return obj
			},
			expectError: true,
		},
		{
			name: "invalid source alias with slash",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-hyphen-end",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.Sources[0].Alias = "invalid/alias"
				return obj
			},
			expectError: true,
		},
		{
			name: "valid artifact copy from and to",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-invalid-alias",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.OutputArtifacts[0].Copy[0].From = "@git_repo-v2/test.yaml"
				obj.Spec.OutputArtifacts[0].Copy[0].To = "@artifact/v2/replace.yaml"
				return obj
			},
			expectError: false,
		},
		{
			name: "invalid artifact copy from",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-invalid-alias",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.OutputArtifacts[0].Copy[0].From = "source/file.yaml"
				return obj
			},
			expectError: true,
		},
		{
			name: "invalid artifact copy to",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-invalid-alias",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.OutputArtifacts[0].Copy[0].To = "@artefact/file.yaml"
				return obj
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			obj := tt.setupObj()

			err := testEnv.Create(ctx, obj)
			if tt.expectError {
				g.Expect(err).To(HaveOccurred())
			} else {
				g.Expect(err).ToNot(HaveOccurred())
				// Clean up valid objects
				defer func() {
					_ = testEnv.Delete(ctx, obj)
				}()
			}
		})
	}
}
