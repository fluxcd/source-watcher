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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/fluxcd/pkg/artifact/config"
	"github.com/fluxcd/pkg/artifact/digest"
	"github.com/fluxcd/pkg/artifact/storage"
	"github.com/fluxcd/pkg/runtime/testenv"
	"github.com/fluxcd/pkg/testserver"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
	// +kubebuilder:scaffold:imports
)

var (
	controllerName   = "source-watcher"
	timeout          = 10 * time.Second
	testStorage      *storage.Storage
	testServer       *testserver.ArtifactServer
	testEnv          *testenv.Environment
	testClient       client.Client
	testCtx          = ctrl.SetupSignalHandler()
	retentionTTL     = 2 * time.Second
	retentionRecords = 2
)

func NewTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(sourcev1.AddToScheme(s))
	utilruntime.Must(swapi.AddToScheme(s))

	return s
}

func TestMain(m *testing.M) {
	var err error
	testEnv = testenv.New(
		testenv.WithCRDPath(
			filepath.Join("..", "..", "config", "crd", "bases"),
		),
		testenv.WithScheme(NewTestScheme()),
	)

	testServer, err = testserver.NewTempArtifactServer()
	if err != nil {
		panic(fmt.Sprintf("Failed to create a temporary storage server: %v", err))
	}
	fmt.Println("Starting the test storage server")
	testServer.Start()

	testStorage, err = newTestStorage(testServer.HTTPServer)
	if err != nil {
		panic(fmt.Sprintf("Failed to create a test storage: %v", err))
	}

	testClient, err = client.New(testEnv.Config, client.Options{Scheme: NewTestScheme(), Cache: nil})
	if err != nil {
		panic(fmt.Sprintf("Failed to create test environment client: %v", err))
	}

	go func() {
		fmt.Println("Starting the test environment")
		if err := testEnv.Start(testCtx); err != nil {
			panic(fmt.Sprintf("Failed to start the test environment manager: %v", err))
		}
	}()
	<-testEnv.Manager.Elected()

	code := m.Run()

	fmt.Println("Stopping the test environment")
	if err := testEnv.Stop(); err != nil {
		panic(fmt.Sprintf("Failed to stop the test environment: %v", err))
	}

	os.Exit(code)
}

func newTestStorage(s *testserver.HTTPServer) (*storage.Storage, error) {
	opts := &config.Options{
		StoragePath:              s.Root(),
		StorageAddress:           s.URL(),
		StorageAdvAddress:        s.URL(),
		ArtifactRetentionTTL:     retentionTTL,
		ArtifactRetentionRecords: retentionRecords,
		ArtifactDigestAlgo:       digest.Canonical.String(),
	}
	st, err := storage.New(opts)
	if err != nil {
		return nil, err
	}
	return st, nil
}

func objToYaml(obj client.Object) string {
	obj.SetManagedFields(nil)
	yamlBytes, err := yaml.Marshal(obj)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return fmt.Sprintf("---\n%s", string(yamlBytes))
}

func getEvents(objName, objNamespace string) []corev1.Event {
	var result []corev1.Event
	events := &corev1.EventList{}
	_ = testClient.List(context.Background(), events)
	for _, event := range events.Items {
		if event.InvolvedObject.Name == objName && event.InvolvedObject.Namespace == objNamespace {
			result = append(result, event)
		}
	}
	return result
}
