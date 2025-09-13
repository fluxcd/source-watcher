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
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gotkmeta "github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
	"github.com/fluxcd/source-watcher/internal/builder"
)

func TestArtifactGeneratorReconciler_DetectDrift(t *testing.T) {
	g := NewWithT(t)
	reconciler := getArtifactGeneratorReconciler()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Create a namespace
	ns, err := testEnv.CreateNamespace(ctx, "test")
	g.Expect(err).ToNot(HaveOccurred())

	// Create a temporary directory with test files
	tmpDir := t.TempDir()

	// Create source directory with test files
	sourceDir := filepath.Join(tmpDir, "test")
	err = os.MkdirAll(sourceDir, 0755)
	g.Expect(err).ToNot(HaveOccurred())

	err = os.WriteFile(filepath.Join(sourceDir, "test.yaml"), []byte("---"), 0644)
	g.Expect(err).ToNot(HaveOccurred())

	// Create the output artifact
	outputArtifact := &swapi.OutputArtifact{
		Name: "test-artifact",
		Copy: []swapi.CopyOperation{
			{From: "@test/test.yaml", To: "@artifact/"},
		},
	}

	// Map the source alias to the source directory
	sources := map[string]string{
		"test": sourceDir,
	}

	// Build the artifact in storage
	b := builder.New(testStorage)
	artifact, err := b.Build(ctx, outputArtifact, sources, ns.Name, tmpDir)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(artifact).ToNot(BeNil())

	// Generate the ExternalArtifact in cluster
	_, err = reconciler.reconcileExternalArtifact(ctx, &swapi.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-generator",
			Namespace: ns.Name,
		}}, outputArtifact, artifact)
	g.Expect(err).ToNot(HaveOccurred())

	tests := []struct {
		name           string
		obj            *swapi.ArtifactGenerator
		setupFunc      func()
		currentDigest  string
		expectedDrift  bool
		expectedReason string
	}{
		{
			name: "no drift when everything matches",
			obj: &swapi.ArtifactGenerator{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-generator",
					Namespace:  ns.Name,
					Generation: 1,
				},
				Spec: swapi.ArtifactGeneratorSpec{
					Sources: []swapi.SourceReference{
						{Alias: "test", Kind: sourcev1.GitRepositoryKind, Name: "test-repo"},
					},
					OutputArtifacts: []swapi.OutputArtifact{*outputArtifact},
				},
				Status: swapi.ArtifactGeneratorStatus{
					Conditions: []metav1.Condition{
						{
							Type:               gotkmeta.ReadyCondition,
							Status:             metav1.ConditionTrue,
							Reason:             gotkmeta.SucceededReason,
							ObservedGeneration: 1,
						},
					},
					ObservedSourcesDigest: "test123",
					Inventory: []swapi.ExternalArtifactReference{
						{
							Namespace: ns.Name,
							Name:      "test-artifact",
							Digest:    artifact.Digest,
							Filename:  filepath.Base(artifact.Path),
						},
					},
				},
			},
			currentDigest:  "test123",
			expectedDrift:  false,
			expectedReason: "NoDriftDetected",
		},
		{
			name: "drift detected when object is not ready",
			obj: &swapi.ArtifactGenerator{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-generator",
					Namespace:  ns.Name,
					Generation: 1,
				},
				Status: swapi.ArtifactGeneratorStatus{
					Conditions: []metav1.Condition{
						{
							Type:               gotkmeta.ReadyCondition,
							Status:             metav1.ConditionFalse, // Not ready
							Reason:             gotkmeta.BuildFailedReason,
							ObservedGeneration: 1,
						},
					},
					ObservedSourcesDigest: "test123",
				},
			},
			currentDigest:  "test123",
			expectedDrift:  true,
			expectedReason: "NotReady",
		},
		{
			name: "drift detected when generation changed",
			obj: &swapi.ArtifactGenerator{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-generator",
					Namespace:  ns.Name,
					Generation: 2, // Different from ObservedGeneration
				},
				Status: swapi.ArtifactGeneratorStatus{
					Conditions: []metav1.Condition{
						{
							Type:               gotkmeta.ReadyCondition,
							Status:             metav1.ConditionTrue,
							Reason:             gotkmeta.SucceededReason,
							ObservedGeneration: 1, // Different from Generation
						},
					},
					ObservedSourcesDigest: "test123",
				},
			},
			currentDigest:  "test123",
			expectedDrift:  true,
			expectedReason: "GenerationChanged",
		},
		{
			name: "drift detected when sources digest changed",
			obj: &swapi.ArtifactGenerator{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-generator",
					Namespace:  ns.Name,
					Generation: 1,
				},
				Status: swapi.ArtifactGeneratorStatus{
					Conditions: []metav1.Condition{
						{
							Type:               gotkmeta.ReadyCondition,
							Status:             metav1.ConditionTrue,
							Reason:             gotkmeta.SucceededReason,
							ObservedGeneration: 1,
						},
					},
					ObservedSourcesDigest: "old123", // Different from currentDigest
				},
			},
			currentDigest:  "new456",
			expectedDrift:  true,
			expectedReason: "SourcesChanged",
		},
		{
			name: "drift detected when number of output artifacts changed",
			obj: &swapi.ArtifactGenerator{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-generator",
					Namespace:  ns.Name,
					Generation: 1,
				},
				Spec: swapi.ArtifactGeneratorSpec{
					OutputArtifacts: []swapi.OutputArtifact{
						{Name: "artifact-1"},
						{Name: "artifact-2"}, // Two artifacts in spec
					},
				},
				Status: swapi.ArtifactGeneratorStatus{
					Conditions: []metav1.Condition{
						{
							Type:               gotkmeta.ReadyCondition,
							Status:             metav1.ConditionTrue,
							Reason:             gotkmeta.SucceededReason,
							ObservedGeneration: 1,
						},
					},
					ObservedSourcesDigest: "test123",
					Inventory: []swapi.ExternalArtifactReference{
						{
							Namespace: ns.Name,
							Name:      "artifact-1",
							Digest:    "sha256:test",
							Filename:  "test.tar.gz",
						},
						// Only one artifact in inventory
					},
				},
			},
			currentDigest:  "test123",
			expectedDrift:  true,
			expectedReason: "ArtifactsChanged",
		},
		{
			name: "drift detected when artifact missing from storage",
			obj: &swapi.ArtifactGenerator{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-generator",
					Namespace:  ns.Name,
					Generation: 1,
				},
				Spec: swapi.ArtifactGeneratorSpec{
					OutputArtifacts: []swapi.OutputArtifact{*outputArtifact},
				},
				Status: swapi.ArtifactGeneratorStatus{
					Conditions: []metav1.Condition{
						{
							Type:               gotkmeta.ReadyCondition,
							Status:             metav1.ConditionTrue,
							Reason:             gotkmeta.SucceededReason,
							ObservedGeneration: 1,
						},
					},
					ObservedSourcesDigest: "test123",
					Inventory: []swapi.ExternalArtifactReference{
						{
							Namespace: ns.Name,
							Name:      "missing-artifact",
							Digest:    "sha256:missing",
							Filename:  "missing.tar.gz",
						},
					},
				},
			},
			currentDigest:  "test123",
			expectedDrift:  true,
			expectedReason: "ArtifactMissing",
		},
		{
			name: "drift detected when ExternalArtifact is missing",
			setupFunc: func() {
				// Delete the in cluster artifact
				err = testEnv.Cleanup(ctx, &sourcev1.ExternalArtifact{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-artifact",
						Namespace: ns.Name,
					},
				})
				g.Expect(err).ToNot(HaveOccurred())
			},
			obj: &swapi.ArtifactGenerator{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-generator",
					Namespace:  ns.Name,
					Generation: 1,
				},
				Spec: swapi.ArtifactGeneratorSpec{
					Sources: []swapi.SourceReference{
						{Alias: "test", Kind: sourcev1.GitRepositoryKind, Name: "test-repo"},
					},
					OutputArtifacts: []swapi.OutputArtifact{*outputArtifact},
				},
				Status: swapi.ArtifactGeneratorStatus{
					Conditions: []metav1.Condition{
						{
							Type:               gotkmeta.ReadyCondition,
							Status:             metav1.ConditionTrue,
							Reason:             gotkmeta.SucceededReason,
							ObservedGeneration: 1,
						},
					},
					ObservedSourcesDigest: "test123",
					Inventory: []swapi.ExternalArtifactReference{
						{
							Namespace: ns.Name,
							Name:      "test-artifact",
							Digest:    artifact.Digest,
							Filename:  filepath.Base(artifact.Path),
						},
					},
				},
			},
			currentDigest:  "test123",
			expectedDrift:  true,
			expectedReason: "ExternalArtifactsChanged",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gt := NewWithT(t)
			if tt.setupFunc != nil {
				tt.setupFunc()
			}

			hasDrift, reason := reconciler.detectDrift(ctx, tt.obj, tt.currentDigest)
			gt.Expect(hasDrift).To(Equal(tt.expectedDrift))
			gt.Expect(reason).To(Equal(tt.expectedReason))
		})
	}
}
