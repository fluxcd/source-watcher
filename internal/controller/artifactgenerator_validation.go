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
	"strings"

	swapi "github.com/fluxcd/source-watcher/api/v1beta1"
)

// validateSpec validates the ArtifactGenerator spec for uniqueness and multi-tenancy constraints.
func (r *ArtifactGeneratorReconciler) validateSpec(obj *swapi.ArtifactGenerator) error {
	// Validate source aliases.
	aliasMap := make(map[string]bool)
	for _, src := range obj.Spec.Sources {
		// Check for duplicate aliases
		if aliasMap[src.Alias] {
			return r.newTerminalErrorFor(obj,
				swapi.ValidationFailedReason,
				"duplicate source alias '%s' found", src.Alias)
		}
		aliasMap[src.Alias] = true

		// Enforce multi-tenancy lockdown if configured.
		if r.NoCrossNamespaceRefs && src.Namespace != "" && src.Namespace != obj.Namespace {
			return r.newTerminalErrorFor(obj,
				swapi.AccessDeniedReason,
				"cross-namespace reference to source %s/%s/%s is not allowed",
				src.Kind, src.Namespace, src.Name)
		}
	}

	// Validate output artifact.
	nameMap := make(map[string]bool)
	for _, artifact := range obj.Spec.OutputArtifacts {
		// Check for duplicate artifact names.
		if nameMap[artifact.Name] {
			return r.newTerminalErrorFor(obj,
				swapi.ValidationFailedReason,
				"duplicate artifact name '%s' found", artifact.Name)
		}

		// Check that the revision source alias exists.
		if artifact.Revision != "" && !aliasMap[strings.TrimPrefix(artifact.Revision, "@")] {
			return r.newTerminalErrorFor(obj,
				swapi.ValidationFailedReason,
				"artifact %s revision source alias '%s' not found",
				artifact.Name, strings.TrimPrefix(artifact.Revision, "@"))
		}
		nameMap[artifact.Name] = true
	}

	return nil
}
