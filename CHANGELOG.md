# Changelog

All notable changes to this project are documented in this file.

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
