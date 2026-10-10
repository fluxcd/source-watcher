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
	"fmt"
	"strings"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
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

	// Validate output artifacts. Artifacts are uniquely identified by their
	// namespace and name, as the same name may be used in different namespaces.
	type artifactKey struct {
		namespace string
		name      string
	}
	artifactMap := make(map[artifactKey]bool)
	for _, artifact := range obj.Spec.OutputArtifacts {
		key := artifactKey{namespace: obj.GetArtifactNamespace(&artifact), name: artifact.Name}
		if artifactMap[key] {
			return r.newTerminalErrorFor(obj,
				swapi.ValidationFailedReason,
				"duplicate artifact name '%s' found in namespace '%s'", artifact.Name, key.namespace)
		}
		artifactMap[key] = true

		if obj.Spec.PathPattern == "" {
			if errs := apivalidation.NameIsDNSSubdomain(artifact.Name, false); len(errs) > 0 {
				return r.newTerminalErrorFor(obj,
					swapi.ValidationFailedReason,
					"artifact name %q is not a valid Kubernetes object name: %s",
					artifact.Name, strings.Join(errs, "; "))
			}

			if artifact.Namespace != "" {
				if errs := apivalidation.ValidateNamespaceName(artifact.Namespace, false); len(errs) > 0 {
					return r.newTerminalErrorFor(obj,
						swapi.ValidationFailedReason,
						"artifact namespace %q is not a valid Kubernetes namespace: %s",
						artifact.Namespace, strings.Join(errs, "; "))
				}
			}
		}

		// Check that the revision source alias exists.
		if artifact.Revision != "" && !aliasMap[strings.TrimPrefix(artifact.Revision, "@")] {
			return r.newTerminalErrorFor(obj,
				swapi.ValidationFailedReason,
				"artifact %s revision source alias '%s' not found",
				artifact.Name, strings.TrimPrefix(artifact.Revision, "@"))
		}

		// Check that the origin revision source alias exists.
		if artifact.OriginRevision != "" && !aliasMap[strings.TrimPrefix(artifact.OriginRevision, "@")] {
			return r.newTerminalErrorFor(obj,
				swapi.ValidationFailedReason,
				"artifact %s origin revision source alias '%s' not found",
				artifact.Name, strings.TrimPrefix(artifact.OriginRevision, "@"))
		}

		// Check that the inputs source alias exists and that capture
		// placeholders are only used together with pathPattern.
		if artifact.InputsFrom != "" {
			if err := validateSourcePathAlias(artifact.InputsFrom, aliasMap); err != nil {
				return r.newTerminalErrorFor(obj,
					swapi.ValidationFailedReason,
					"artifact %s inputsFrom %q: %s", artifact.Name, artifact.InputsFrom, err.Error())
			}
			if obj.Spec.PathPattern == "" && strings.ContainsAny(artifact.InputsFrom, "{}") {
				return r.newTerminalErrorFor(obj,
					swapi.ValidationFailedReason,
					"artifact %s inputsFrom %q uses capture placeholders but pathPattern is not set",
					artifact.Name, artifact.InputsFrom)
			}
		}
	}

	// Validate the namespace metadata source alias.
	if ns := obj.Spec.Namespaces; ns != nil && ns.Metadata != nil && ns.Metadata.FromSource != nil {
		path := ns.Metadata.FromSource.Path
		if err := validateSourcePathAlias(path, aliasMap); err != nil {
			return r.newTerminalErrorFor(obj,
				swapi.ValidationFailedReason,
				"namespaces metadata fromSource path %q: %s", path, err.Error())
		}
		if obj.Spec.PathPattern == "" && strings.ContainsAny(path, "{}") {
			return r.newTerminalErrorFor(obj,
				swapi.ValidationFailedReason,
				"namespaces metadata fromSource path %q uses capture placeholders but pathPattern is not set", path)
		}
	}

	return nil
}

// validateSourcePathAlias checks that the source path in the format
// "@<alias>/<path>" references an existing source alias, allowing the special
// "artifact" alias that refers to the generated artifact.
func validateSourcePathAlias(path string, aliases map[string]bool) error {
	if !strings.HasPrefix(path, "@") {
		return fmt.Errorf("path must start with '@'")
	}
	parts := strings.SplitN(strings.TrimPrefix(path, "@"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("path must be in the format '@<alias>/<path>'")
	}
	if parts[0] == swapi.ArtifactAlias {
		return nil
	}
	if !aliases[parts[0]] {
		return fmt.Errorf("source alias '%s' not found", parts[0])
	}
	return nil
}
