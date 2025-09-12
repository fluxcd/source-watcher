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

package v1beta1_test

import (
	"testing"

	"github.com/fluxcd/source-watcher/api/v1beta1"
)

func TestHashObservedSources(t *testing.T) {
	sources := map[string]v1beta1.ObservedSource{
		"source1": {
			Digest:   "sha256:1b1452058f747245f79b4d45d589ad5693c516987e678d13231ddfdf26979208",
			Revision: "main@sha1:28deef923f4da39062d2902cb640011a36d52e19",
			URL:      "https://example.com/repo1.git",
		},
		"source2": {
			Digest:   "sha256:966a0b26898be9448d927312d81f9221edac224e383f131a6451f22b918bd66d",
			Revision: "main@sha1:4b94f17f5ceb13341bdfe15d4d9249bb0a3a011e",
			URL:      "https://example.com/repo2.git",
		},
	}

	hash := v1beta1.HashObservedSources(sources)
	expectedHash := "sha256:0dd16ff06ef2efe7bbc5b6a356eb9f3b59467d76617969eb7bb34e094abbd58e"

	if hash != expectedHash {
		t.Errorf("Hash mismatch: got %s, want %s", hash, expectedHash)
	}
}
