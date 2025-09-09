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

package builder_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/fluxcd/pkg/artifact/config"
	"github.com/fluxcd/pkg/artifact/digest"
	"github.com/fluxcd/pkg/artifact/storage"
	"github.com/fluxcd/pkg/testserver"

	"github.com/fluxcd/source-watcher/internal/builder"
)

var (
	testBuilder *builder.ArtifactBuilder
	testStorage *storage.Storage
	testServer  *testserver.ArtifactServer
)

func TestMain(m *testing.M) {
	var err error
	testServer, err = testserver.NewTempArtifactServer()
	if err != nil {
		panic(fmt.Sprintf("Failed to create a temporary storage server: %v", err))
	}
	testServer.Start()

	testStorage, err = storage.New(&config.Options{
		ArtifactRetentionRecords: 2,

		StoragePath:          testServer.Root(),
		StorageAddress:       testServer.URL(),
		StorageAdvAddress:    testServer.URL(),
		ArtifactDigestAlgo:   digest.Canonical.String(),
		ArtifactRetentionTTL: time.Hour,
	})
	if err != nil {
		panic(fmt.Sprintf("Failed to create test storage: %v", err))
	}

	testBuilder = builder.New(testStorage)

	code := m.Run()

	testServer.Stop()
	if err := os.RemoveAll(testServer.Root()); err != nil {
		panic(fmt.Sprintf("Failed to remove storage server dir: %v", err))
	}

	os.Exit(code)
}
