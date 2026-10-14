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

package v1beta1

// Action describes an observable stage of the ArtifactGenerator reconcile
// loop, from validating the spec and resolving sources through building,
// publishing, pruning and finalizing the generated artifacts.
type Action string

// String returns the string representation of the Action.
func (a Action) String() string {
	return string(a)
}

const (
	// ActionReconcile denotes the overall outcome of the reconcile loop,
	// emitted once per run to report that reconciliation finished or failed.
	ActionReconcile Action = "Reconcile"

	// ActionResolveSource resolves the referenced GitRepository, OCIRepository,
	// Bucket, HelmChart and ExternalArtifact sources and records their
	// advertised artifact URL, digest and revision.
	ActionResolveSource Action = "ResolveSource"

	// ActionFetchSource downloads and extracts each resolved source tarball
	// into a per-alias working directory before the artifacts are built.
	ActionFetchSource Action = "FetchSource"

	// ActionBuild composes and decomposes the fetched sources into output
	// artifacts by running the glob-based copy, merge and extract operations.
	ActionBuild Action = "Build"

	// ActionPublish writes each built tarball to storage and applies the
	// corresponding ExternalArtifact object with its revision metadata.
	ActionPublish Action = "Publish"
)
