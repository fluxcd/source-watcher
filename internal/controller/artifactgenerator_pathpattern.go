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
	"bytes"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	"k8s.io/apimachinery/pkg/api/validate/content"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
)

type artifactRequest struct {
	swapi.OutputArtifact
	Labels map[string]string
}

// buildArtifactRequests expands the PathPattern into multiple artifact requests if specified.
// Otherwise, it returns the OutputArtifacts exactly as defined in the spec.
func buildArtifactRequests(obj *swapi.ArtifactGenerator, localSources map[string]string) ([]artifactRequest, error) {
	if obj.Spec.PathPattern == "" {
		reqs := make([]artifactRequest, 0, len(obj.Spec.OutputArtifacts))
		for _, oa := range obj.Spec.OutputArtifacts {
			reqs = append(reqs, artifactRequest{
				OutputArtifact: oa,
				Labels:         nil,
			})
		}
		return reqs, nil
	}

	// Parse @<alias>/<pattern>
	parts := strings.SplitN(obj.Spec.PathPattern, "/", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "@") {
		return nil, fmt.Errorf("invalid pathPattern format: %s", obj.Spec.PathPattern)
	}

	alias := strings.TrimPrefix(parts[0], "@")
	patternStr := parts[1]

	srcDir, ok := localSources[alias]
	if !ok {
		return nil, fmt.Errorf("source alias %s not found in local sources", alias)
	}

	// Convert the path pattern (e.g. "apps/{app}/envs/{env}") to a regex
	// with named capture groups (e.g. "^apps/(?P<app>[^/]+)/envs/(?P<env>[^/]+)$").
	escaped := regexp.QuoteMeta(patternStr)
	captureRe := regexp.MustCompile(`\\\{([a-zA-Z0-9_]+)\\\}`)
	regexStr := "^" + captureRe.ReplaceAllString(escaped, `(?P<$1>[^/]+)`) + "$"
	matcher, err := regexp.Compile(regexStr)
	if err != nil {
		return nil, fmt.Errorf("failed to compile path pattern: %w", err)
	}

	subexpNames := matcher.SubexpNames()
	for _, name := range subexpNames[1:] {
		if errs := content.IsLabelKey(name); len(errs) > 0 {
			return nil, fmt.Errorf(
				"pathPattern %q: capture variable %q is not a valid Kubernetes label key: %s",
				obj.Spec.PathPattern, name, strings.Join(errs, "; "))
		}
	}

	var reqs []artifactRequest
	// Track rendered artifact names to detect collisions after lowercasing.
	seenNames := make(map[string]string)

	err = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return fmt.Errorf("failed to compute relative path for %s: %w", path, err)
		}
		if rel == "." {
			return nil
		}
		// Normalize separators for cross-platform compatibility.
		rel = filepath.ToSlash(rel)

		matches := matcher.FindStringSubmatch(rel)
		if matches == nil {
			return nil
		}

		// Extract named captures from the regex match.
		rawCaptures := make(map[string]string)
		for i, name := range subexpNames {
			if i != 0 && name != "" {
				rawCaptures[name] = matches[i]
			}
		}

		// Run ToLower on all extracted values used in Kubernetes metadata.
		normalizedCaptures := make(map[string]string, len(rawCaptures))
		for k, v := range rawCaptures {
			normalizedCaptures[k] = strings.ToLower(v)
		}

		// Validate values with content.IsLabelValue.
		for k, v := range normalizedCaptures {
			if errs := content.IsLabelValue(v); len(errs) > 0 {
				return fmt.Errorf(
					"pathPattern %q: captured value %q for variable %q is not a valid Kubernetes label value: %s",
					obj.Spec.PathPattern, v, k, strings.Join(errs, "; "))
			}
		}

		// Render the OutputArtifacts using the captured variables.
		for _, oa := range obj.Spec.OutputArtifacts {
			req, err := renderArtifactRequest(oa, normalizedCaptures, rawCaptures)
			if err != nil {
				return fmt.Errorf("failed to render artifact %s for match %s: %w", oa.Name, rel, err)
			}

			// Validate rendered EA name with NameIsDNSSubdomain.
			if errs := apivalidation.NameIsDNSSubdomain(req.Name, false); len(errs) > 0 {
				return fmt.Errorf(
					"pathPattern %q: rendered artifact name %q is not a valid Kubernetes object name: %s",
					obj.Spec.PathPattern, req.Name, strings.Join(errs, "; "))
			}

			// Validate for duplicate names.
			if prevDir, exists := seenNames[req.Name]; exists {
				return fmt.Errorf(
					"pathPattern %q: directories %q and %q both resolve to artifact name %q",
					obj.Spec.PathPattern, prevDir, rel, req.Name)
			}
			seenNames[req.Name] = rel

			reqs = append(reqs, req)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk source directory: %w", err)
	}

	return reqs, nil
}

// renderArtifactRequest creates an artifactRequest by evaluating Go template expressions.
// Artifact names and labels use normalized captures, while copy paths use raw captures.
func renderArtifactRequest(oa swapi.OutputArtifact, normalizedCaptures, rawCaptures map[string]string) (artifactRequest, error) {
	name, err := renderTemplate(oa.Name, normalizedCaptures)
	if err != nil {
		return artifactRequest{}, err
	}

	req := artifactRequest{
		OutputArtifact: oa,
		Labels:         normalizedCaptures,
	}
	req.Name = name

	// Deep copy the Copy slice to avoid mutating the original OutputArtifact spec,
	// and render template expressions in each copy operation.
	req.Copy = make([]swapi.CopyOperation, len(oa.Copy))
	for i, copyOp := range oa.Copy {
		from, err := renderTemplate(copyOp.From, rawCaptures)
		if err != nil {
			return artifactRequest{}, err
		}

		to, err := renderTemplate(copyOp.To, rawCaptures)
		if err != nil {
			return artifactRequest{}, err
		}

		req.Copy[i] = swapi.CopyOperation{
			From:     from,
			To:       to,
			Exclude:  copyOp.Exclude,
			Strategy: copyOp.Strategy,
		}
	}

	return req, nil
}

// renderTemplate evaluates a Go template string with the provided data map.
// If the string does not contain any template delimiters, it is returned as-is.
func renderTemplate(tmplStr string, data map[string]string) (string, error) {
	if !strings.Contains(tmplStr, "{{") {
		return tmplStr, nil
	}
	tmpl, err := template.New("").Parse(tmplStr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
