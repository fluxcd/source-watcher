# Changelog

All notable changes to this project are documented in this file.

## 2.3.1

**Release date:** 2026-10-26

This patch release hardens the controller's artifact server by setting a read
header timeout, which also bounds the TLS handshake, and an idle timeout. As a
result, connections that never send a request, or keep-alive connections from
clients without an idle timeout, no longer stay open until the server shuts
down. No timeout is set for reading or writing the whole request, so large
artifacts can still be downloaded by slow clients.

Improvements:
- Update fluxcd/pkg dependencies
  [#415](https://github.com/fluxcd/source-watcher/pull/415)
  [#416](https://github.com/fluxcd/source-watcher/pull/416)

## 2.3.0

**Release date:** 2026-10-24

This minor release introduces SPIFFE mTLS between the controllers, tenant
namespace provisioning with ArtifactGenerator, and cross-namespace artifact
generation.

### Inter-Controller mTLS

This release introduces SPIFFE mTLS for inter-controller communication. Flux
controllers authenticate each other with X.509-SVIDs, and each traffic type is
enabled independently by configuring the SPIFFE Workload API socket and the
SPIFFE IDs that the controller authorizes. See the
[inter-controller mTLS documentation](https://fluxcd.io/flux/installation/configuration/intercontroller-mtls/)
for the controller flags and an example configuration.

### ArtifactGenerator

Generated artifacts can target other namespaces through the new
`.spec.artifacts[].namespace` field. The new `.spec.serviceAccountName` field
selects the ServiceAccount used to authorize reconciling the output artifacts in
the target namespaces; when unset, `--default-service-account` supplies the
default. The controller verifies the ServiceAccount authorization with a
SubjectAccessReview for each target namespace.

Namespace management is opt-in with the new `.spec.managedNamespaces` field:
`allowedNames` is a list of CEL expressions bounding which namespaces may be
managed, `prune` controls deletion of namespaces that are no longer targeted,
and `metadata` defines their labels and annotations. The
`source.extensions.fluxcd.io/ssa`, `/prune` and `/externalFinalizer` annotations
tune the lifecycle of individual Namespace objects. At least one of
`--default-service-account` or `.spec.serviceAccountName` must be set.

Namespace management requires cluster-scoped RBAC that the controller does not
have by default. Because the controller performs the operations with its own
credentials, the `flux-system/source-watcher` ServiceAccount must be granted
`get`, `create`, `patch` and `delete` on `namespaces`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: source-watcher-namespace-management
rules:
  - apiGroups: [""]
    resources: ["namespaces"]
    verbs: ["get", "create", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: source-watcher-namespace-management
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: source-watcher-namespace-management
subjects:
  - kind: ServiceAccount
    name: source-watcher
    namespace: flux-system
```

Copy operations accept an `optional` flag, so a copy that matches no files can
be skipped instead of failing the reconciliation.

A new annotation in the ArtifactGenerator was introduced to set the reconcile
interval: `source.extensions.fluxcd.io/reconcileEvery`.

### General updates

The controller is built with Kubernetes 1.37 and Go 1.27.

Improvements:
- Add an optional flag to copy operations
  [#371](https://github.com/fluxcd/source-watcher/pull/371)
- Generate cross-namespace artifacts
  [#387](https://github.com/fluxcd/source-watcher/pull/387)
- Add tenant namespace provisioning for ResourceSets
  [#392](https://github.com/fluxcd/source-watcher/pull/392)
- Replace impersonation with SubjectAccessReview
  [#402](https://github.com/fluxcd/source-watcher/pull/402)
- Add SPIFFE mTLS between the controllers
  [#399](https://github.com/fluxcd/source-watcher/pull/399)
- Update to Kubernetes 1.37 and Go 1.27
  [#381](https://github.com/fluxcd/source-watcher/pull/381)
  [#397](https://github.com/fluxcd/source-watcher/pull/397)
- Various dependency updates
  [#362](https://github.com/fluxcd/source-watcher/pull/362)
  [#404](https://github.com/fluxcd/source-watcher/pull/404)

## 2.2.4

**Release date:** 2026-08-31

This patch release updates the fluxcd/pkg dependencies, which bring Kubernetes
to 1.36.4.

Improvements:
- Update fluxcd/pkg dependencies
  [#377](https://github.com/fluxcd/source-watcher/pull/377)
  [#378](https://github.com/fluxcd/source-watcher/pull/378)

## 2.2.3

**Release date:** 2026-08-07

This patch release aligns the tarball extraction performed by `Extract` copy
operations with the other copy operations, so that it writes through `os.Root`
and stays confined to the artifact root. It also bounds the expansion of the
`from` and `exclude` glob patterns, which may now contain at most 20 commas
inside `{}` alternation groups. On the `ArtifactGenerator` API, the `to` field
validation pattern no longer accepts `.` and `..` path segments, and each
`exclude` pattern is limited to 1024 characters.

Note that this release contains a CRD schema change, the `ArtifactGenerator`
CRD must be updated along with the controller.

Fixes:
- Confine tarball extraction and bound glob expansion
  [#374](https://github.com/fluxcd/source-watcher/pull/374)

## 2.2.2

**Release date:** 2026-07-07

This patch release disables Flux variable substitution on the ArtifactGenerator
CRD. The CRD is now annotated with
`kustomize.toolkit.fluxcd.io/substitute: disabled` so that Kustomizations with
post-build substitution enabled no longer corrupt the CRD schema when it
contains `${...}` sequences.

Fixes:
- Disable variable substitution in CRDs
  [#366](https://github.com/fluxcd/source-watcher/pull/366)

## 2.2.1

**Release date:** 2026-06-30

This patch release fixes the sharding flags not being honored, so that
source-watcher correctly restricts reconciliation to the objects matching its
configured shard. It also updates Kubernetes to 1.36.2 and the fluxcd/pkg
dependencies, adds kubectl categories to the ArtifactGenerator CRD and documents
the controller's command-line options.

Fixes:
- Fix sharding flags not being honored
  [#353](https://github.com/fluxcd/source-watcher/pull/353)

Improvements:
- Add categories to the ArtifactGenerator CRD
  [#352](https://github.com/fluxcd/source-watcher/pull/352)
- Document controller options
  [#357](https://github.com/fluxcd/source-watcher/pull/357)
- Update fluxcd/pkg dependencies
  [#356](https://github.com/fluxcd/source-watcher/pull/356)

## 2.2.0

**Release date:** 2026-06-24

This minor release comes with enhancements to the `ArtifactGenerator` API and
updated Kubernetes and source-controller dependencies.

### ArtifactGenerator

The `ArtifactGenerator` now supports directory discovery through the
`.spec.pathPattern` field, a `@<alias>/<pattern>` traversal expression whose
named captures (e.g. `{app}`) can be used as placeholders in the output
artifact fields.

The API also gains a `.spec.commonMetadata` field for setting common labels and
annotations on the generated artifacts.

### General updates

In addition, the Kubernetes dependencies have been updated to v1.36 along with
controller-runtime v0.24, and the source-controller API has been bumped to
v1.9.0.

Fixes:
- Fix copy exclude pattern matching
  [#334](https://github.com/fluxcd/source-watcher/pull/334)

Improvements:
- Add `.spec.commonMetadata` to ArtifactGenerator
  [#333](https://github.com/fluxcd/source-watcher/pull/333)
- Add `.spec.pathPattern` directory discovery to ArtifactGenerator
  [#341](https://github.com/fluxcd/source-watcher/pull/341)
- Update to Kubernetes 1.36 and Go 1.26
  [#335](https://github.com/fluxcd/source-watcher/pull/335)
- Upgrade source-controller API to v1.9.0
  [#342](https://github.com/fluxcd/source-watcher/pull/342)
- Various dependency updates
  [#336](https://github.com/fluxcd/source-watcher/pull/336)
  [#338](https://github.com/fluxcd/source-watcher/pull/338)
  [#339](https://github.com/fluxcd/source-watcher/pull/339)
  [#340](https://github.com/fluxcd/source-watcher/pull/340)

## 2.1.1

**Release date:** 2026-03-12

This patch release comes with dependency updates.

Improvements:
- Remove no longer needed workaround for Flux 2.8
  [#319](https://github.com/fluxcd/source-watcher/pull/319)
- Update fluxcd/pkg dependencies
  [#324](https://github.com/fluxcd/source-watcher/pull/324)

## 2.1.0

**Release date:** 2026-02-17

This minor release comes with new ArtifactGenerator sources and various
improvements.

### ArtifactGenerator

The `ArtifactGenerator` now supports `HelmChart` and `ExternalArtifact`
as source kinds, and copy operations have been extended with tarball
extraction capabilities.

A `DirectSourceFetch` feature gate has been added to bypass cache for
source objects.

The reconciler now emits GitOps Toolkit events for artifact changes.

### General updates

In addition, the Kubernetes dependencies have been updated to v1.35.0 and
the controller is now built with Go 1.26.

Improvements:
- Add HelmChart support
  [#297](https://github.com/fluxcd/source-watcher/pull/297)
- Add support for using ExternalArtifact as an ArtifactGenerator source
  [#300](https://github.com/fluxcd/source-watcher/pull/300)
- Extend copy operations with tarball extraction capabilities
  [#302](https://github.com/fluxcd/source-watcher/pull/302)
- Adds GitOps Toolkit EventRecorder to ArtifactGenerator reconciler
  [#310](https://github.com/fluxcd/source-watcher/pull/310)
- Add `DirectSourceFetch` feature gate to bypass cache for source objects
  [#314](https://github.com/fluxcd/source-watcher/pull/314)
- Various dependency updates
  [#308](https://github.com/fluxcd/source-watcher/pull/308)
  [#313](https://github.com/fluxcd/source-watcher/pull/313)
  [#315](https://github.com/fluxcd/source-watcher/pull/315)

## 2.0.3

**Release date:** 2025-11-19

This patch release comes with various dependency updates.

Improvements:
- Upgrade k8s to 1.34.2 and c-r to 0.22.4
  [#292](https://github.com/fluxcd/source-watcher/pull/292)

## 2.0.2

**Release date:** 2025-10-08

This patch release comes with various dependency updates.

The controller is now built with Go 1.25.2 which includes
fixes for vulnerabilities in the Go stdlib:
[CVE-2025-58183](https://github.com/golang/go/issues/75677),
[CVE-2025-58188](https://github.com/golang/go/issues/75675)
and many others. The full list of security fixes can be found
[here](https://groups.google.com/g/golang-announce/c/4Emdl2iQ_bI/m/qZN5nc-mBgAJ).

Improvements:
- Update dependencies to Kubernetes v1.34.1 and Go 1.25.2
  [#280](https://github.com/fluxcd/source-watcher/pull/280)

## 2.0.1

**Release date:** 2025-09-26

This patch release fixes the Deployment manifest for source-watcher
adding `securityContext.fsGroup` to the Pod spec.

Fixes:
- Add `securityContext.fsGroup` to deployment
  [#275](https://github.com/fluxcd/source-watcher/pull/275)

## 2.0.0

**Release date:** 2025-09-15

This major release marks the addition of source-watcher to the [Flux](https://fluxcd.io) controller suite.

### ArtifactGenerator

The [ArtifactGenerator](docs/spec/v1beta1/artifactgenerators.md) is an extension of Flux APIs
that allows source composition and decomposition.

The `ArtifactGenerator` API is currently in `v1beta1` and can be used in production
environments running Flux v2.7 and later with kustomize-controller and helm-controller
configured with `--feature-gates=ExternalArtifact`.

Improvements:
- Introduce ArtifactGenerator API
  [#255](https://github.com/fluxcd/source-watcher/pull/255)
- Conform with GitOps Toolkit standards
  [#257](https://github.com/fluxcd/source-watcher/pull/257)
- Prepare for v2 release
  [#258](https://github.com/fluxcd/source-watcher/pull/258)
- Update source-controller API to v1.7.0
  [#261](https://github.com/fluxcd/source-watcher/pull/261)
