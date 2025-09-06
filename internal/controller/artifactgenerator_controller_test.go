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
	"time"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

func getArtifactGenerator(k client.ObjectKey) *swapi.ArtifactGenerator {
	return &swapi.ArtifactGenerator{
		TypeMeta: metav1.TypeMeta{
			Kind:       swapi.ArtifactGeneratorKind,
			APIVersion: swapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      k.Name,
			Namespace: k.Namespace,
		},
		Spec: swapi.ArtifactGeneratorSpec{
			Sources: []swapi.SourceReference{
				{
					Alias: "gitrepo",
					Kind:  sourcev1.GitRepositoryKind,
					Name:  "gitrepo",
				},
				{
					Alias: "ocirepo",
					Kind:  sourcev1.OCIRepositoryKind,
					Name:  "ocirepo",
				},
			},
			OutputArtifacts: []swapi.OutputArtifact{
				{
					Name: "gitrepo-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@gitrepo/**",
							To:   "@artifact/",
						},
					},
				},
				{
					Name: "ocirepo-artifact",
					Copy: []swapi.CopyOperation{
						{
							From: "@ocirepo/**",
							To:   "@artifact/",
						},
					},
				},
			},
		},
	}
}

func getArtifactGeneratorReconciler() *ArtifactGeneratorReconciler {
	return &ArtifactGeneratorReconciler{
		ControllerName:            controllerName,
		Client:                    testClient,
		APIReader:                 testClient,
		Scheme:                    testEnv.Scheme(),
		EventRecorder:             testEnv.GetEventRecorderFor(controllerName),
		Storage:                   testStorage,
		ArtifactFetchRetries:      1,
		DependencyRequeueInterval: 5 * time.Second,
	}
}
