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

package v1beta1

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

// ObservedSource contains the observed state of an artifact source.
// This is used to track the state of the sources used to generate
// an artifact in the ArtifactGeneratorStatus.ObservedSourcesDigest field.
type ObservedSource struct {
	// Digest is the artifact digest of the upstream source.
	// +required
	Digest string `json:"digest"`

	// Revision is the artifact revision of the upstream source.
	// +required
	Revision string `json:"revision"`

	// OriginRevision holds the origin revision of the upstream source,
	// extracted from the 'org.opencontainers.image.revision' annotation,
	// if available in the source artifact metadata.
	// +optional
	OriginRevision string `json:"originRevision,omitempty"`

	// URL is the artifact URL of the upstream source.
	// +required
	URL string `json:"url"`
}

// String returns a formatted string representation of the ObservedSource.
func (os ObservedSource) String() string {
	return fmt.Sprintf("digest=%s,revision=%s,url=%s", os.Digest, os.Revision, os.URL)
}

// HashObservedSources computes a hash of the ObservedSource map.
// It sorts the formatted source strings to ensure consistent hashing.
// The resulting hash is a SHA-256 digest represented as a hexadecimal string.
func HashObservedSources(sources map[string]ObservedSource) string {
	parts := make([]string, 0, len(sources))
	for alias, os := range sources {
		parts = append(parts, fmt.Sprintf("%s=[%s]", alias, os.String()))
	}

	sort.Strings(parts)
	digest := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return fmt.Sprintf("sha256:%x", digest)
}
