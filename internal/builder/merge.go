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

package builder

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"os"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// loadYAML reads the multi-doc YAML from the provided path
// and merges all documents into a single map.
func loadYAML(srcRoot *os.Root, srcPath string) (map[string]any, error) {
	srcFile, err := srcRoot.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer srcFile.Close()

	out := map[string]any{}
	reader := utilyaml.NewYAMLReader(bufio.NewReader(srcFile))
	for {
		currentMap := map[string]any{}
		raw, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("error reading YAML document: %w", err)
		}
		if err := yaml.Unmarshal(raw, &currentMap); err != nil {
			return nil, fmt.Errorf("cannot unmarshal YAML document: %w", err)
		}
		out = mergeMap(out, currentMap)
	}
	return out, nil
}

// mergeYAML merges two maps and returns the result as YAML bytes.
func mergeYAML(base, overlay map[string]any) ([]byte, error) {
	merged := mergeMap(base, overlay)
	return yaml.Marshal(merged)
}

// mergeMap performs a deep merge of two maps.
// Nested maps are merged recursively.
// If a key exists in both maps, the value from the overlay will be used.
// Arrays are overwritten entirely.
// This function is akin to Helm's merge logic for values files.
// ref: https://github.com/helm/helm/blob/5534c01cdb9e1634f35a22621e207d39c21e01f9/pkg/chart/v2/loader/load.go
func mergeMap(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base))
	maps.Copy(out, base)
	for k, v := range overlay {
		if v, ok := v.(map[string]any); ok {
			if bv, ok := out[k]; ok {
				if bv, ok := bv.(map[string]any); ok {
					out[k] = mergeMap(bv, v)
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}
