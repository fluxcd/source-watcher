/*
Copyright 2026 The Flux authors

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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

// maxInputsFileSize bounds the size of the inputs and namespace metadata files
// read from source artifacts.
const maxInputsFileSize = 1 << 20 // 1 MiB

// loadExportedInputs reads, parses and returns the structured inputs
// referenced by .spec.artifacts[].inputsFrom, for publication in the
// ExternalArtifact .status.exportedInputs field.
func (r *ArtifactGeneratorReconciler) loadExportedInputs(ctx context.Context,
	oa *swapi.OutputArtifact,
	sources map[string]string,
	artifactDir string) (map[string]*apiextensionsv1.JSON, error) {
	if oa.InputsFrom == "" {
		return nil, nil
	}

	path, err := resolveLocalSourcePath(oa.InputsFrom, sources, artifactDir)
	if err != nil {
		return nil, fmt.Errorf("invalid inputsFrom path: %w", err)
	}

	data, err := readFileLimited(path, maxInputsFileSize)
	if err != nil {
		return nil, fmt.Errorf("failed to read inputs file %q: %w", oa.InputsFrom, err)
	}

	var value map[string]any
	if err := yaml.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("failed to parse inputs file %q: %w", oa.InputsFrom, err)
	}

	inputs, err := toExportedInputs(value)
	if err != nil {
		return nil, fmt.Errorf("failed to process inputs file %q: %w", oa.InputsFrom, err)
	}

	return inputs, nil
}

// resolveLocalSourcePath resolves a path in the format "@<alias>/<path>"
// against the local sources. The alias "artifact" resolves to the staging
// directory of the generated artifact. The resolved path is guaranteed to stay
// within the source root.
func resolveLocalSourcePath(path string, sources map[string]string, artifactDir string) (string, error) {
	if !strings.HasPrefix(path, "@") {
		return "", fmt.Errorf("path must start with '@'")
	}

	parts := strings.SplitN(strings.TrimPrefix(path, "@"), "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("path format must be '@alias/path'")
	}

	alias, rel := parts[0], parts[1]

	var root string
	if alias == swapi.ArtifactAlias {
		root = artifactDir
	} else {
		var ok bool
		root, ok = sources[alias]
		if !ok {
			return "", fmt.Errorf("source alias %q not found", alias)
		}
	}
	if root == "" {
		return "", fmt.Errorf("no local path found for alias %q", alias)
	}

	rel = filepath.FromSlash(rel)
	if rel == "" || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path %q must stay within the source root", rel)
	}

	return filepath.Join(root, rel), nil
}

// readFileLimited reads a file up to max bytes, returning an error when the
// file exceeds the limit.
func readFileLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("file exceeds the maximum size of %d bytes", max)
	}
	return data, nil
}

// toExportedInputs converts a parsed inputs document into the map of JSON
// values published in the ExternalArtifact status.
func toExportedInputs(value map[string]any) (map[string]*apiextensionsv1.JSON, error) {
	if len(value) == 0 {
		return nil, nil
	}
	inputs := make(map[string]*apiextensionsv1.JSON, len(value))
	for key, val := range value {
		raw, err := json.Marshal(val)
		if err != nil {
			return nil, fmt.Errorf("failed to encode input %q: %w", key, err)
		}
		inputs[key] = &apiextensionsv1.JSON{Raw: raw}
	}
	return inputs, nil
}
