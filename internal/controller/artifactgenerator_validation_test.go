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
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	gotkconditions "github.com/fluxcd/pkg/runtime/conditions"
	gotktestenv "github.com/fluxcd/pkg/runtime/testenv"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

func TestArtifactGeneratorReconciler_specValidation(t *testing.T) {
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
			g.Expect(gotkconditions.IsStalled(obj)).To(BeTrue())
			g.Expect(gotkconditions.GetReason(obj, gotkmeta.ReadyCondition)).To(Equal(tt.expectedReason))

			// Verify event was recorded
			events, err := gotktestenv.GetEvents(ctx, testClient, obj.Name, obj.Namespace, nil)
			g.Expect(err).ToNot(HaveOccurred())
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
			name: "invalid static artifact name",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-invalid-static-artifact-name",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.OutputArtifacts[0].Name = "Invalid Name"
				return obj
			},
			expectError: true,
		},
		{
			name: "templated artifact name with pathPattern",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-path-pattern-template-name",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				alias := obj.Spec.Sources[0].Alias
				obj.Spec.PathPattern = "@" + alias + "/apps/{app}"
				obj.Spec.OutputArtifacts[0].Name = "{app}"
				obj.Spec.OutputArtifacts[0].Copy[0].From = "@" + alias + "/apps/{app}/**"
				obj.Spec.OutputArtifacts[0].Copy[0].To = "@artifact/file.yaml"
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
		{
			name: "valid artifact namespace",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-valid-artifact-namespace",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.OutputArtifacts[0].Namespace = "another-namespace"
				return obj
			},
			expectError: false,
		},
		{
			name: "invalid artifact namespace",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-invalid-artifact-namespace",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.OutputArtifacts[0].Namespace = "Invalid-Namespace"
				return obj
			},
			expectError: true,
		},
		{
			name: "templated artifact namespace without pathPattern",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-templated-artifact-namespace-no-pattern",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.OutputArtifacts[0].Namespace = "{env}"
				return obj
			},
			expectError: true,
		},
		{
			name: "artifact namespace exceeding max length",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-long-artifact-namespace",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.OutputArtifacts[0].Namespace = strings.Repeat("a", 64)
				return obj
			},
			expectError: true,
		},
		{
			name: "templated artifact namespace with pathPattern",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-templated-artifact-namespace",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				alias := obj.Spec.Sources[0].Alias
				obj.Spec.PathPattern = "@" + alias + "/apps/{app}/envs/{env}"
				obj.Spec.OutputArtifacts[0].Name = "{app}-{env}"
				obj.Spec.OutputArtifacts[0].Namespace = "{env}"
				obj.Spec.OutputArtifacts[0].Copy[0].From = "@" + alias + "/apps/{app}/envs/{env}/**"
				obj.Spec.OutputArtifacts[0].Copy[0].To = "@artifact/"
				return obj
			},
			expectError: false,
		},
		{
			name: "valid service account name",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-valid-service-account",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.ServiceAccountName = "artifact-generator"
				return obj
			},
			expectError: false,
		},
		{
			name: "invalid service account name",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-invalid-service-account",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.ServiceAccountName = "Invalid ServiceAccount"
				return obj
			},
			expectError: true,
		},
		{
			name: "valid managed namespaces",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-valid-managed-namespaces",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.ManagedNamespaces = &swapi.ManagedNamespaces{
					Prune:        true,
					AllowedNames: []string{"ns.startsWith('test-')"},
				}
				return obj
			},
			expectError: false,
		},
		{
			name: "managed namespaces without allowed expressions",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-managed-no-allowed-exprs",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.ManagedNamespaces = &swapi.ManagedNamespaces{
					Prune: true,
				}
				return obj
			},
			expectError: true,
		},
		{
			name: "managed namespaces with empty allowed expressions",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-managed-empty-allowed-exprs",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.ManagedNamespaces = &swapi.ManagedNamespaces{
					Prune:        true,
					AllowedNames: []string{},
				}
				return obj
			},
			expectError: true,
		},
		{
			name: "managed namespaces with empty metadata",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-managed-empty-metadata",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.ManagedNamespaces = &swapi.ManagedNamespaces{
					Prune:        true,
					AllowedNames: []string{"true"},
					Metadata:     &swapi.ManagedNamespacesMetadata{},
				}
				return obj
			},
			expectError: true,
		},
		{
			name: "managed namespaces with fromSource metadata",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-managed-fromsource-metadata",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.ManagedNamespaces = &swapi.ManagedNamespaces{
					Prune:        true,
					AllowedNames: []string{"true"},
					Metadata: &swapi.ManagedNamespacesMetadata{
						FromSource: &swapi.NamespaceMetadataFromSource{
							Path:          "@repo/tenants/{tenant}/metadata.yaml",
							AllowedLabels: map[string]string{"observability": "value.matches('.*')"},
						},
					},
				}
				return obj
			},
			expectError: false,
		},
		{
			name: "managed namespaces with from metadata",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-managed-from-metadata",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.ManagedNamespaces = &swapi.ManagedNamespaces{
					Prune:        true,
					AllowedNames: []string{"true"},
					Metadata: &swapi.ManagedNamespacesMetadata{
						From: []swapi.NamespaceMetadataFrom{
							{Strategy: swapi.OverrideStrategy, Namespace: "*", Labels: map[string]string{"team": "platform"}},
						},
					},
				}
				return obj
			},
			expectError: false,
		},
		{
			name: "managed namespaces with fromSource metadata and no allowed keys",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-managed-fromsource-no-allowed",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.ManagedNamespaces = &swapi.ManagedNamespaces{
					Prune:        true,
					AllowedNames: []string{"true"},
					Metadata: &swapi.ManagedNamespacesMetadata{
						FromSource: &swapi.NamespaceMetadataFromSource{
							Path: "@repo/tenants/{tenant}/metadata.yaml",
						},
					},
				}
				return obj
			},
			expectError: true,
		},
		{
			name: "managed namespaces with fromSource metadata and allowed annotations",
			setupObj: func() *swapi.ArtifactGenerator {
				objKey := client.ObjectKey{
					Name:      "test-managed-fromsource-allowed-annotations",
					Namespace: ns.Name,
				}
				obj := getArtifactGenerator(objKey)
				obj.Spec.ManagedNamespaces = &swapi.ManagedNamespaces{
					Prune:        true,
					AllowedNames: []string{"true"},
					Metadata: &swapi.ManagedNamespacesMetadata{
						FromSource: &swapi.NamespaceMetadataFromSource{
							Path:               "@repo/tenants/{tenant}/metadata.yaml",
							AllowedAnnotations: map[string]string{"team": "value.matches('.*')"},
						},
					},
				}
				return obj
			},
			expectError: false,
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

// TestArtifactGenerator_crdValidation_ManagedNamespacesRequired verifies that
// the CRD requires allowedNames and prune when managedNamespaces is set. The
// typed client always serializes the boolean prune field, so unstructured
// objects are used to omit it.
func TestArtifactGenerator_crdValidation_ManagedNamespacesRequired(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ns, err := testEnv.CreateNamespace(ctx, "test-crd-managed-required")
	g.Expect(err).ToNot(HaveOccurred())

	spec := func(managed map[string]any) map[string]any {
		return map[string]any{
			"managedNamespaces": managed,
			"sources": []any{
				map[string]any{"alias": "git", "kind": "GitRepository", "name": "git"},
			},
			"artifacts": []any{
				map[string]any{
					"name": "app",
					"copy": []any{map[string]any{"from": "@git/**", "to": "@artifact/"}},
				},
			},
		}
	}

	tests := []struct {
		name        string
		managed     map[string]any
		errContains string
	}{
		{
			name:        "missing prune",
			managed:     map[string]any{"allowedNames": []any{"true"}},
			errContains: "prune",
		},
		{
			name:        "missing allowed expressions",
			managed:     map[string]any{"prune": true},
			errContains: "allowedNames",
		},
		{
			name:        "empty allowed expressions",
			managed:     map[string]any{"prune": true, "allowedNames": []any{}},
			errContains: "allowedNames",
		},
		{
			name:    "all required fields",
			managed: map[string]any{"prune": true, "allowedNames": []any{"true"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			u := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": swapi.GroupVersion.String(),
				"kind":       swapi.ArtifactGeneratorKind,
				"metadata": map[string]any{
					"name":      "test-managed-required-" + strings.ReplaceAll(tt.name, " ", "-"),
					"namespace": ns.Name,
				},
				"spec": spec(tt.managed),
			}}

			err := testEnv.Create(ctx, u)
			if tt.errContains != "" {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring(tt.errContains))
			} else {
				g.Expect(err).ToNot(HaveOccurred())
				defer func() { _ = testEnv.Delete(ctx, u) }()
			}
		})
	}
}
