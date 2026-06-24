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
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	gotkcel "github.com/fluxcd/pkg/runtime/cel"
	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	"k8s.io/apimachinery/pkg/api/validate/content"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
)

type artifactRequest struct {
	swapi.OutputArtifact
	Labels map[string]string
}

var pathPatternCaptureNameRe = regexp.MustCompile("^[A-Za-z_][A-Za-z0-9_]*$")

type terminalPathPatternError struct {
	err error
}

func (e *terminalPathPatternError) Error() string {
	return e.err.Error()
}

func (e *terminalPathPatternError) Unwrap() error {
	return e.err
}

func newTerminalPathPatternError(format string, args ...any) error {
	return &terminalPathPatternError{err: fmt.Errorf(format, args...)}
}

func isTerminalPathPatternError(err error) bool {
	var terminalErr *terminalPathPatternError
	return errors.As(err, &terminalErr)
}

// buildArtifactRequests expands the PathPattern into multiple artifact requests if specified.
// Otherwise, it returns the OutputArtifacts exactly as defined in the spec.
func buildArtifactRequests(ctx context.Context, obj *swapi.ArtifactGenerator, localSources map[string]string) ([]artifactRequest, error) {
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
		return nil, newTerminalPathPatternError("invalid pathPattern format: %s", obj.Spec.PathPattern)
	}

	alias := strings.TrimPrefix(parts[0], "@")
	patternStr := parts[1]

	srcDir, ok := localSources[alias]
	if !ok {
		return nil, newTerminalPathPatternError("pathPattern %q: source alias %s not found in local sources", obj.Spec.PathPattern, alias)
	}

	// Convert the path pattern (e.g. "apps/{app}/envs/{env}") to a regex
	// with named capture groups (e.g. "^apps/(?P<app>[^/]+)/envs/(?P<env>[^/]+)$").
	escaped := regexp.QuoteMeta(patternStr)
	if err := validatePathPatternCaptures(obj.Spec.PathPattern, patternStr); err != nil {
		return nil, newTerminalPathPatternError("%w", err)
	}

	captureRe := regexp.MustCompile(`\\\{([a-zA-Z0-9_]+)\\\}`)
	regexStr := "^" + captureRe.ReplaceAllString(escaped, `(?P<$1>[^/]+)`) + "$"
	matcher, err := regexp.Compile(regexStr)
	if err != nil {
		return nil, newTerminalPathPatternError("pathPattern %q: failed to compile path pattern: %w", obj.Spec.PathPattern, err)
	}

	subexpNames := matcher.SubexpNames()
	for _, name := range subexpNames[1:] {
		if errs := content.IsLabelKey(name); len(errs) > 0 {
			return nil, newTerminalPathPatternError(
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
				return newTerminalPathPatternError(
					"pathPattern %q: captured value %q for variable %q is not a valid Kubernetes label value: %s",
					obj.Spec.PathPattern, v, k, strings.Join(errs, "; "))
			}
		}

		// Render the OutputArtifacts using the captured variables.
		for _, oa := range obj.Spec.OutputArtifacts {
			req, err := renderArtifactRequest(ctx, oa, normalizedCaptures, rawCaptures)
			if err != nil {
				return newTerminalPathPatternError("pathPattern %q: failed to render artifact %s for match %s: %w", obj.Spec.PathPattern, oa.Name, rel, err)
			}

			// Validate rendered EA name with NameIsDNSSubdomain.
			if errs := apivalidation.NameIsDNSSubdomain(req.Name, false); len(errs) > 0 {
				return newTerminalPathPatternError(
					"pathPattern %q: rendered artifact name %q is not a valid Kubernetes object name: %s",
					obj.Spec.PathPattern, req.Name, strings.Join(errs, "; "))
			}

			// Validate for duplicate names.
			if prevDir, exists := seenNames[req.Name]; exists {
				return newTerminalPathPatternError(
					"pathPattern %q: directories %q and %q both resolve to artifact name %q",
					obj.Spec.PathPattern, prevDir, rel, req.Name)
			}
			seenNames[req.Name] = rel

			reqs = append(reqs, req)
		}

		return nil
	})

	if err != nil {
		if isTerminalPathPatternError(err) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to walk source directory: %w", err)
	}

	return reqs, nil
}

func validatePathPatternCaptures(pathPattern, patternStr string) error {
	seen := make(map[string]struct{})
	for i := 0; i < len(patternStr); i++ {
		switch patternStr[i] {
		case '{':
			end := strings.IndexByte(patternStr[i+1:], '}')
			if end < 0 {
				return fmt.Errorf("pathPattern %q: invalid capture starting at offset %d: missing closing brace", pathPattern, i)
			}

			name := patternStr[i+1 : i+1+end]
			if name == "" {
				return fmt.Errorf("pathPattern %q: empty capture variable", pathPattern)
			}
			if !pathPatternCaptureNameRe.MatchString(name) {
				return fmt.Errorf("pathPattern %q: capture variable %q must be a valid CEL variable name matching [A-Za-z_][A-Za-z0-9_]*", pathPattern, name)
			}
			if !isCELStringVariableName(name) {
				return fmt.Errorf("pathPattern %q: capture variable %q is not a valid CEL string variable name", pathPattern, name)
			}
			if _, ok := seen[name]; ok {
				return fmt.Errorf("pathPattern %q: duplicate capture variable %q", pathPattern, name)
			}
			seen[name] = struct{}{}
			i += end + 1
		case '}':
			return fmt.Errorf("pathPattern %q: invalid capture ending at offset %d: missing opening brace", pathPattern, i)
		}
	}
	return nil
}

// renderArtifactRequest creates an artifactRequest by evaluating CEL expressions.
// Artifact names and labels use normalized captures, while copy paths use raw captures.
func renderArtifactRequest(ctx context.Context, oa swapi.OutputArtifact, normalizedCaptures, rawCaptures map[string]string) (artifactRequest, error) {
	name, err := renderCELString(ctx, oa.Name, normalizedCaptures)
	if err != nil {
		return artifactRequest{}, err
	}

	req := artifactRequest{
		OutputArtifact: oa,
		Labels:         normalizedCaptures,
	}
	req.Name = name

	// Deep copy the Copy slice to avoid mutating the original OutputArtifact spec,
	// and evaluate CEL expressions in each copy operation.
	req.Copy = make([]swapi.CopyOperation, len(oa.Copy))
	for i, copyOp := range oa.Copy {
		from, err := renderCELString(ctx, copyOp.From, rawCaptures)
		if err != nil {
			return artifactRequest{}, err
		}

		to, err := renderCELString(ctx, copyOp.To, rawCaptures)
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

func renderCELString(ctx context.Context, expr string, data map[string]string) (string, error) {
	celExpr, err := gotkcel.NewExpression(expr)
	if err != nil {
		return "", err
	}
	return celExpr.EvaluateString(ctx, celActivation(data))
}

func celActivation(data map[string]string) map[string]any {
	activation := make(map[string]any, len(data))
	for k, v := range data {
		activation[k] = v
	}
	return activation
}

func isCELStringVariableName(name string) bool {
	expr, err := gotkcel.NewExpression(name)
	if err != nil {
		return false
	}
	got, err := expr.EvaluateString(context.Background(), map[string]any{name: "test"})
	return err == nil && got == "test"
}
