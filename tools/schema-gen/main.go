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

// Command schema-gen turns a controller-gen CRD schema into a standalone
// JSON Schema (draft 2020-12) for a source-watcher KRM-style configuration
// type.
//
// It generates a throwaway wrapper package that embeds the target API type in
// an apiVersion/kind/metadata envelope (so controller-gen treats it as a CRD
// kind), renders it with controller-gen, then strips the ObjectMeta metadata
// and stamps the apiVersion/kind consts into the resulting schema. The wrapper
// carries metav1.ObjectMeta so the API types themselves stay clean (TypeMeta
// only).
//
// Three wrapping modes are supported:
//
//   - Named-field mode (-field set): the API type is placed under a single JSON
//     field, e.g. {apiVersion, kind, report: <ReportSpec>}.
//   - Inline mode (-field and -metadata unset): the API type is embedded inline
//     so its fields sit at the root.
//   - Metadata mode (-metadata set): the API type is placed under the root
//     metadata field, for KRM-style configuration objects whose metadata
//     carries the configuration rather than ObjectMeta. controller-gen
//     special-cases a field literally named metadata, so the wrapper uses a
//     placeholder field that is renamed back to metadata when rewriting the
//     root schema.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"sigs.k8s.io/yaml"
)

// metadataPlaceholder is the temporary JSON field used to wrap the API type in
// metadata mode. It is renamed back to "metadata" when rewriting the root
// schema.
const metadataPlaceholder = "metadataConfig"

func main() {
	inputPath := flag.String("in", "", "controller-gen CRD YAML input path, or '-' for stdin")
	outputPath := flag.String("out", "", "standalone JSON Schema output path")
	controllerGen := flag.String("controller-gen", "controller-gen", "controller-gen binary path")
	group := flag.String("group", "", "Kubernetes API group")
	version := flag.String("version", "", "Kubernetes API version")
	kind := flag.String("kind", "", "Kubernetes kind")
	apiType := flag.String("type", "", "Go API type to wrap, in import/path.Type form")
	field := flag.String("field", "", "JSON field for the wrapped type; empty embeds it inline at the root")
	metadata := flag.Bool("metadata", false, "wrap the API type under the root metadata field (KRM-style config)")
	scope := flag.String("scope", "Cluster", "CRD resource scope")
	schemaField := flag.Bool("schema-field", false, "include optional root $schema field")
	schemaID := flag.String("id", "", "JSON Schema $id")
	title := flag.String("title", "", "root schema title")
	flag.Parse()

	if *outputPath == "" {
		exit(errors.New("-out is required"))
	}
	if *schemaID == "" {
		exit(errors.New("-id is required"))
	}
	opts := schemaOptions{
		controllerGen:    *controllerGen,
		group:            *group,
		version:          *version,
		kind:             *kind,
		apiType:          *apiType,
		field:            *field,
		metadata:         *metadata,
		scope:            *scope,
		schemaField:      *schemaField,
		id:               *schemaID,
		title:            *title,
		controllerGenYML: *inputPath,
	}
	if err := opts.validate(); err != nil {
		exit(err)
	}

	input, err := opts.controllerGenOutput()
	if err != nil {
		exit(err)
	}

	schema, err := buildSchema(input, opts)
	if err != nil {
		exit(err)
	}

	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		exit(fmt.Errorf("marshal schema: %w", err))
	}
	data = append(data, '\n')

	if err := os.WriteFile(*outputPath, data, 0o644); err != nil {
		exit(fmt.Errorf("write %s: %w", *outputPath, err))
	}
}

func exit(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "schema-gen: %v\n", err)
	os.Exit(1)
}

func readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

type schemaOptions struct {
	controllerGen    string
	group            string
	version          string
	kind             string
	apiType          string
	field            string
	metadata         bool
	scope            string
	schemaField      bool
	id               string
	title            string
	controllerGenYML string
}

// inline reports whether the API type is embedded inline (its fields at the
// root) rather than placed under a single named field.
func (o schemaOptions) inline() bool {
	return o.field == "" && !o.metadata
}

type crdIdentity struct {
	apiVersion string
	kind       string
}

func (o schemaOptions) validate() error {
	if o.kind == "" {
		return errors.New("-kind is required")
	}
	if o.field != "" && o.metadata {
		return errors.New("-field and -metadata are mutually exclusive")
	}
	if o.controllerGenYML != "" {
		return nil
	}
	switch {
	case o.controllerGen == "":
		return errors.New("-controller-gen is required")
	case o.group == "":
		return errors.New("-group is required")
	case o.version == "":
		return errors.New("-version is required")
	case o.apiType == "":
		return errors.New("-type is required")
	case !strings.Contains(o.apiType, "."):
		return fmt.Errorf("-type must be in import/path.Type form, got %q", o.apiType)
	}
	return nil
}

func (o schemaOptions) controllerGenOutput() ([]byte, error) {
	if o.controllerGenYML != "" {
		return readInput(o.controllerGenYML)
	}
	dir, err := os.MkdirTemp(".", ".schema-gen-")
	if err != nil {
		return nil, fmt.Errorf("create temp package: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := o.writeWrapperPackage(dir); err != nil {
		return nil, err
	}
	return runControllerGen(o.controllerGen, "crd", "paths="+dir, "output:stdout")
}

func runControllerGen(bin string, args ...string) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("run controller-gen: %s", msg)
	}
	return out, nil
}

func (o schemaOptions) writeWrapperPackage(dir string) error {
	importPath, typeName, ok := splitGoType(o.apiType)
	if !ok {
		return fmt.Errorf("-type must be in import/path.Type form, got %q", o.apiType)
	}
	plural := strings.ToLower(o.kind) + "s"
	schemaField := ""
	if o.schemaField {
		schemaField = `
	// +optional
	Schema string ` + "`json:\"$schema,omitempty\"`" + `
`
	}
	// Build the field that carries the API type. In metadata mode the type is
	// placed under metadataPlaceholder because controller-gen special-cases a
	// field literally named metadata; the placeholder is renamed when rewriting
	// the root schema.
	var body string
	switch {
	case o.metadata:
		body = fmt.Sprintf("\t%s api.%s `json:%q`", exportName(metadataPlaceholder), typeName, metadataPlaceholder)
	case o.inline():
		body = fmt.Sprintf("\tapi.%s `json:\",inline\"`", typeName)
	default:
		body = fmt.Sprintf("\t%s api.%s `json:%q`", exportName(o.field), typeName, o.field)
	}
	doc := fmt.Sprintf(`// Copyright 2026 The Flux Authors
// SPDX-License-Identifier: Apache-2.0

// +groupName=%s
// +versionName=%s
// +kubebuilder:object:generate=false
package schemagen
`, o.group, o.version)
	types := fmt.Sprintf(`// Copyright 2026 The Flux Authors
// SPDX-License-Identifier: Apache-2.0

package schemagen

import (
	api "%s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=%s,scope=%s
type %s struct {
	metav1.TypeMeta   `+"`json:\",inline\"`"+`
	metav1.ObjectMeta `+"`json:\"metadata,omitempty\"`"+`
%s
%s
}
`, importPath, plural, o.scope, o.kind, schemaField, body)
	if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte(doc), 0o644); err != nil {
		return fmt.Errorf("write wrapper doc.go: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "types.go"), []byte(types), 0o644); err != nil {
		return fmt.Errorf("write wrapper types.go: %w", err)
	}
	return nil
}

func splitGoType(s string) (importPath, typeName string, ok bool) {
	slash := strings.LastIndex(s, "/")
	dot := strings.LastIndex(s, ".")
	if dot <= slash || dot == len(s)-1 {
		return "", "", false
	}
	return s[:dot], s[dot+1:], true
}

func exportName(s string) string {
	var b strings.Builder
	upperNext := true
	for _, r := range s {
		if r == '-' || r == '_' || r == '.' {
			upperNext = true
			continue
		}
		if upperNext {
			r = unicode.ToUpper(r)
			upperNext = false
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "Value"
	}
	return b.String()
}

func buildSchema(input []byte, opts schemaOptions) (map[string]any, error) {
	crd, err := selectCRD(input, opts.kind)
	if err != nil {
		return nil, err
	}

	identity, err := extractCRDIdentity(crd)
	if err != nil {
		return nil, err
	}

	openAPI, err := extractOpenAPISchema(crd)
	if err != nil {
		return nil, err
	}

	schema, ok := transformNode(cloneMap(openAPI)).(map[string]any)
	if !ok {
		return nil, errors.New("openAPIV3Schema is not an object")
	}

	if err := rewriteRootSchema(schema, identity, opts); err != nil {
		return nil, err
	}

	return schema, nil
}

// selectCRD parses one or more `---`-separated CRD documents and returns the
// one whose spec.names.kind matches kind. controller-gen emits one CRD per
// root type in the rendered package, so inline-root mode must pick the right one.
func selectCRD(input []byte, kind string) (map[string]any, error) {
	docs := splitYAMLDocs(input)
	var kinds []string
	for _, doc := range docs {
		var crd map[string]any
		if err := yaml.Unmarshal(doc, &crd); err != nil {
			return nil, fmt.Errorf("parse controller-gen output: %w", err)
		}
		if len(crd) == 0 {
			continue
		}
		k, err := crdKind(crd)
		if err != nil {
			return nil, err
		}
		if k == kind {
			return crd, nil
		}
		kinds = append(kinds, k)
	}
	return nil, fmt.Errorf("no CRD for kind %q in controller-gen output (found: %s)", kind, strings.Join(kinds, ", "))
}

func splitYAMLDocs(input []byte) [][]byte {
	parts := bytes.Split(input, []byte("\n---\n"))
	out := make([][]byte, 0, len(parts))
	for _, p := range parts {
		if len(bytes.TrimSpace(p)) > 0 {
			out = append(out, p)
		}
	}
	return out
}

func crdKind(crd map[string]any) (string, error) {
	spec, err := requiredMap(crd, "spec")
	if err != nil {
		return "", err
	}
	names, err := requiredMap(spec, "names")
	if err != nil {
		return "", fmt.Errorf("spec: %w", err)
	}
	return requiredString(names, "kind")
}

func extractCRDIdentity(crd map[string]any) (crdIdentity, error) {
	spec, err := requiredMap(crd, "spec")
	if err != nil {
		return crdIdentity{}, err
	}
	group, err := requiredString(spec, "group")
	if err != nil {
		return crdIdentity{}, fmt.Errorf("spec: %w", err)
	}
	names, err := requiredMap(spec, "names")
	if err != nil {
		return crdIdentity{}, fmt.Errorf("spec: %w", err)
	}
	kind, err := requiredString(names, "kind")
	if err != nil {
		return crdIdentity{}, fmt.Errorf("spec.names: %w", err)
	}
	versions, err := requiredSlice(spec, "versions")
	if err != nil {
		return crdIdentity{}, err
	}
	if len(versions) == 0 {
		return crdIdentity{}, errors.New("spec.versions is empty")
	}
	version, ok := versions[0].(map[string]any)
	if !ok {
		return crdIdentity{}, errors.New("spec.versions[0] is not an object")
	}
	versionName, err := requiredString(version, "name")
	if err != nil {
		return crdIdentity{}, fmt.Errorf("spec.versions[0]: %w", err)
	}
	return crdIdentity{
		apiVersion: group + "/" + versionName,
		kind:       kind,
	}, nil
}

func extractOpenAPISchema(crd map[string]any) (map[string]any, error) {
	spec, err := requiredMap(crd, "spec")
	if err != nil {
		return nil, err
	}
	versions, err := requiredSlice(spec, "versions")
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, errors.New("spec.versions is empty")
	}
	version, ok := versions[0].(map[string]any)
	if !ok {
		return nil, errors.New("spec.versions[0] is not an object")
	}
	schema, err := requiredMap(version, "schema")
	if err != nil {
		return nil, fmt.Errorf("spec.versions[0]: %w", err)
	}
	openAPI, err := requiredMap(schema, "openAPIV3Schema")
	if err != nil {
		return nil, fmt.Errorf("spec.versions[0].schema: %w", err)
	}
	return openAPI, nil
}

func rewriteRootSchema(schema map[string]any, identity crdIdentity, opts schemaOptions) error {
	props, err := requiredMap(schema, "properties")
	if err != nil {
		return err
	}
	delete(props, "metadata")

	apiVersion, err := requiredMap(props, "apiVersion")
	if err != nil {
		return fmt.Errorf("properties: %w", err)
	}
	apiVersion["const"] = identity.apiVersion
	delete(apiVersion, "description")

	kind, err := requiredMap(props, "kind")
	if err != nil {
		return fmt.Errorf("properties: %w", err)
	}
	kind["const"] = identity.kind
	delete(kind, "description")

	if opts.schemaField {
		schemaProp, err := requiredMap(props, "$schema")
		if err != nil {
			return fmt.Errorf("properties: %w", err)
		}
		schemaProp["format"] = "uri"
	}

	schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	schema["$id"] = opts.id

	required := []any{"apiVersion", "kind"}
	switch {
	case opts.metadata:
		// The wrapper placed the API type under metadataPlaceholder; rename it
		// back to metadata. The metadata is optional, as in the API type.
		field, err := requiredMap(props, metadataPlaceholder)
		if err != nil {
			return fmt.Errorf("properties: %w", err)
		}
		delete(props, metadataPlaceholder)
		props["metadata"] = field
		if opts.title != "" {
			schema["title"] = opts.title
		} else if title, ok := field["description"].(string); ok && title != "" {
			schema["title"] = firstLine(title)
		}
	case opts.inline():
		// The embedded type's doc is not carried onto the wrapper root, so the
		// title comes from the -title flag.
		if opts.title != "" {
			schema["title"] = opts.title
			if _, ok := schema["description"]; !ok {
				schema["description"] = opts.title
			}
		} else if title, ok := schema["description"].(string); ok && title != "" {
			schema["title"] = firstLine(title)
		}
	default:
		wrappedField, err := requiredMap(props, opts.field)
		if err != nil {
			return fmt.Errorf("properties: %w", err)
		}
		if title, ok := wrappedField["description"].(string); ok && title != "" {
			schema["title"] = firstLine(title)
		}
		required = append(required, opts.field)
	}

	schema["additionalProperties"] = false
	schema["required"] = required
	return nil
}

func transformNode(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			x[k] = transformNode(child)
		}
		if x["type"] == "object" {
			if _, ok := x["properties"]; ok {
				x["additionalProperties"] = false
			}
		}
		if nullable, _ := x["nullable"].(bool); nullable {
			delete(x, "nullable")
			inner := cloneMap(x)
			out := map[string]any{
				"oneOf": []any{
					map[string]any{"type": "null"},
					inner,
				},
			}
			if desc, ok := x["description"]; ok {
				out["description"] = desc
				delete(inner, "description")
			}
			return out
		}
		return x
	case []any:
		for i, child := range x {
			x[i] = transformNode(child)
		}
		return x
	default:
		return v
	}
}

// firstLine returns the first line of a (possibly multi-paragraph) description,
// used as a concise schema title.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

func requiredMap(parent map[string]any, key string) (map[string]any, error) {
	v, ok := parent[key]
	if !ok {
		return nil, fmt.Errorf("missing %q", key)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q is not an object", key)
	}
	return m, nil
}

func requiredSlice(parent map[string]any, key string) ([]any, error) {
	v, ok := parent[key]
	if !ok {
		return nil, fmt.Errorf("missing %q", key)
	}
	s, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%q is not an array", key)
	}
	return s, nil
}

func requiredString(parent map[string]any, key string) (string, error) {
	v, ok := parent[key]
	if !ok {
		return "", fmt.Errorf("missing %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%q is not a string", key)
	}
	return s, nil
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneMap(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return x
	}
}
