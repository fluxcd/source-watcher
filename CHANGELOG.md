# Changelog

All notable changes to this project are documented in this file.

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
