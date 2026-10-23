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
	"fmt"

	authzv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"

	swapi "github.com/fluxcd/source-watcher/api/v2/v1beta1"
)

// accessDeniedError indicates that a ServiceAccount is not authorized to
// perform an operation required by the reconciliation.
type accessDeniedError struct {
	msg string
}

// Error implements the error interface.
func (e *accessDeniedError) Error() string {
	return e.msg
}

type verb string
type namespace string

type externalArtifactOperation struct {
	verb        verb
	subresource string
}

// accessChecker performs SubjectAccessReview checks for the ServiceAccount
// used to authorize a reconciliation. Each verb/kind/namespace is checked
// at most once (the result is cached).
type accessChecker struct {
	client                client.Client
	obj                   *swapi.ArtifactGenerator
	defaultServiceAccount string

	namespaceVerbs        map[verb]struct{}
	externalArtifactVerbs map[namespace]map[externalArtifactOperation]struct{}
}

// newAccessChecker returns a SubjectAccessReview checker for the given
// ArtifactGenerator.
func newAccessChecker(c client.Client, obj *swapi.ArtifactGenerator,
	defaultServiceAccount string) *accessChecker {
	return &accessChecker{
		client:                c,
		obj:                   obj,
		defaultServiceAccount: defaultServiceAccount,
		namespaceVerbs:        make(map[verb]struct{}),
		externalArtifactVerbs: make(map[namespace]map[externalArtifactOperation]struct{}),
	}
}

// ensureNamespaceVerb checks a Namespace verb cluster-wide. Each verb is
// checked at most once per reconciliation.
func (c *accessChecker) ensureNamespaceVerb(ctx context.Context, v string) error {
	verb := verb(v)
	if _, ok := c.namespaceVerbs[verb]; ok {
		return nil
	}
	serviceAccount := c.serviceAccountForNamespaces()
	if serviceAccount == "" {
		return nil
	}
	if err := c.check(ctx, serviceAccount, v, "", "v1", "namespaces", "", ""); err != nil {
		return err
	}
	c.namespaceVerbs[verb] = struct{}{}
	return nil
}

// ensureExternalArtifactVerb checks an ExternalArtifact verb in the given
// namespace. Each verb is checked at most once per namespace.
func (c *accessChecker) ensureExternalArtifactVerb(ctx context.Context,
	ns, v, subresource string) error {
	serviceAccount := c.serviceAccountForExternalArtifacts(ns)
	if serviceAccount == "" {
		return nil
	}
	namespace := namespace(ns)
	if c.externalArtifactVerbs[namespace] == nil {
		c.externalArtifactVerbs[namespace] = make(map[externalArtifactOperation]struct{})
	}
	op := externalArtifactOperation{verb: verb(v), subresource: subresource}
	if _, ok := c.externalArtifactVerbs[namespace][op]; ok {
		return nil
	}
	if err := c.check(ctx, serviceAccount, v,
		sourcev1.GroupVersion.Group, sourcev1.GroupVersion.Version,
		"externalartifacts", subresource, ns); err != nil {
		return err
	}
	c.externalArtifactVerbs[namespace][op] = struct{}{}
	return nil
}

// serviceAccountForNamespaces returns the ServiceAccount that authorizes
// namespace management.
func (c *accessChecker) serviceAccountForNamespaces() string {
	if c.obj.Spec.ServiceAccountName != "" {
		return c.obj.Spec.ServiceAccountName
	}
	return c.defaultServiceAccount
}

// serviceAccountForExternalArtifacts returns the ServiceAccount that authorizes
// ExternalArtifacts in the given namespace. An empty value means that no
// ServiceAccount authorization check is performed for that namespace.
func (c *accessChecker) serviceAccountForExternalArtifacts(namespace string) string {
	if c.obj.Spec.ServiceAccountName != "" {
		return c.obj.Spec.ServiceAccountName
	}

	// For backward compatibility with Flux <2.10, we only force multi-tenancy
	// lockdown, i.e. a SAR using --default-service-account, when the artifact
	// is in another namespace.
	if namespace != c.obj.Namespace {
		return c.defaultServiceAccount
	}
	return ""
}

// check performs a single SubjectAccessReview.
func (c *accessChecker) check(ctx context.Context, serviceAccount, verb,
	group, version, resource, subresource, namespace string) error {
	if c.client == nil {
		return fmt.Errorf("cannot check access without a client")
	}

	sar := &authzv1.SubjectAccessReview{
		Spec: authzv1.SubjectAccessReviewSpec{
			User: fmt.Sprintf("system:serviceaccount:%s:%s", c.obj.Namespace, serviceAccount),
			// Mirror the implicit groups the API server derives for a
			// ServiceAccount, otherwise group-bound RBAC would be missed and
			// the review would falsely deny.
			Groups: []string{
				"system:serviceaccounts",
				"system:serviceaccounts:" + c.obj.Namespace,
				"system:authenticated",
			},
			ResourceAttributes: &authzv1.ResourceAttributes{
				Verb:        verb,
				Group:       group,
				Version:     version,
				Resource:    resource,
				Subresource: subresource,
				Namespace:   namespace,
			},
		},
	}
	if err := c.client.Create(ctx, sar); err != nil {
		return fmt.Errorf("failed to create SubjectAccessReview for ServiceAccount %s: %w", serviceAccount, err)
	}
	if sar.Status.EvaluationError != "" {
		return fmt.Errorf("SubjectAccessReview for ServiceAccount %s failed: %s",
			serviceAccount, sar.Status.EvaluationError)
	}
	if !sar.Status.Allowed {
		scope := "the cluster"
		if namespace != "" {
			scope = fmt.Sprintf("namespace %q", namespace)
		}
		name := resource
		if subresource != "" {
			name = resource + "/" + subresource
		}
		return &accessDeniedError{msg: fmt.Sprintf(
			"ServiceAccount %s is not allowed to %s %s in %s",
			serviceAccount, verb, name, scope)}
	}
	return nil
}
