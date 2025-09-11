# source-watcher

[![test](https://github.com/fluxcd/source-watcher/workflows/test/badge.svg)](https://github.com/fluxcd/source-watcher/actions)

A Flux extension controller that enables advanced source composition and decomposition patterns for GitOps workflows.

## Overview

source-watcher introduces the **ArtifactGenerator** API, which allows you to:

- 🔗 **Compose** multiple Flux sources (GitRepository, OCIRepository, Bucket) into a single deployable artifact
- 📦 **Decompose** monorepos into multiple independent artifacts with separate deployment lifecycles
- 🎯 **Optimize** reconciliation by only triggering updates when specific paths change
- 🏗️ **Structure** complex deployments from distributed sources maintained by different teams

## Quick Start

### Prerequisites

- Flux v2.7.0 or later
- source-watcher v2.0.0 or later
- kustomize-controller & helm-controller with `--feature-gates=ExternalArtifacts`

### Example: Composing Multiple Sources

Deploy an application that combines Kubernetes manifests from different sources:

```yaml
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata:
  name: my-app
  namespace: flux-system
spec:
  sources:
    - alias: backend
      kind: GitRepository
      name: backend-repo
    - alias: frontend  
      kind: OCIRepository
      name: frontend-oci
    - alias: config
      kind: Bucket
      name: config-bucket
  artifacts:
    - name: my-app-composite
      copy:
        - from: "@backend/deploy/k8s/**"
          to: "@artifact/backend/"
        - from: "@frontend/k8s/manifests/*.yaml"
          to: "@artifact/frontend/"
        - from: "@config/prod/settings.yaml"
          to: "@artifact/config.yaml"
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-app
  namespace: flux-system
spec:
  interval: 10m
  sourceRef:
    kind: ExternalArtifact
    name: my-app-composite
  path: "./"
  prune: true
```

### Example: Monorepo Decomposition

Split a monorepo into independently deployable services:

```yaml
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata:
  name: platform-services
  namespace: flux-system
spec:
  sources:
    - alias: monorepo
      kind: GitRepository
      name: platform-monorepo
  artifacts:
    - name: policy-config
      copy:
        - from: "@monorepo/platform/policy/**"
          to: "@artifact/"
    - name: auth-service
      copy:
        - from: "@monorepo/services/auth/deploy/**"
          to: "@artifact/"
    - name: api-gateway
      copy:
        - from: "@monorepo/services/gateway/deploy/**"
          to: "@artifact/"
          exclude: ["**/charts/**"]
```

Each service gets its own ExternalArtifact with an independent revision.
Changes to `services/auth/` only trigger reconciliation of the auth-service,
leaving other services untouched.

### Example: Helm Chart Composition

Compose Helm charts with custom values from different sources:

```yaml
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata:
  name: podinfo
  namespace: apps
spec:
  sources:
    - alias: chart
      kind: OCIRepository
      name: podinfo-chart
      namespace: apps
    - alias: repo
      kind: GitRepository
      name: podinfo-values
      namespace: apps
  artifacts:
    - name: podinfo-composite
      originRevision: "@chart" # Track chart version
      copy:
        - from: "@chart/"
          to: "@artifact/"
        - from: "@repo/charts/podinfo/values-prod.yaml"
          to: "@artifact/podinfo/values.yaml" # Override default values
```

## Key Features

### Content-based Revision Tracking

The generated artifacts are versioned as `latest@sha256:<hash>` where the hash
is computed from the artifact's content. This means:

- ✅ Identical content = same revision (no unnecessary reconciliations)
- ✅ Any change = new revision (guaranteed updates)
- ✅ Automatic change detection across all source types

The controller attaches the origin source revision (e.g. Git commit SHA)
to the generated artifact metadata for traceability and Flux commit status updates.

### Flexible Copy Operations

Copy operations support glob patterns and exclusions:

- `@source/file.yaml` → `@artifact/dest/` - Copy file into directory
- `@source/dir/` → `@artifact/dest/` - Copy directory as subdirectory
- `@source/dir/**` → `@artifact/dest/` - Copy directory contents
- `@source/file.yaml` → `@artifact/existing.yaml` - Later copy overwrites earlier ones

## Use Cases

- **Multi-Team Collaboration** - Different teams maintain different Kubernetes
  configurations in separate repositories. ArtifactGenerator combines them into deployable
  units while maintaining clear ownership boundaries.
- **Environment-Specific Composition** Compose base manifests with environment-specific
  configurations from different sources without duplicating files across repositories.
- **Vendor Chart Customization** - Combine upstream Helm charts from OCI registries with
  your organization's custom values and configuration overrides stored in Git.
- **Selective Deployment** - Deploy only changed components from large repositories
  by decomposing them into focused artifacts.

## API Reference

- [ArtifactGenerator CRD](docs/spec/v1beta1/artifactgenerators.md)

## Contributing

This project is Apache 2.0 licensed and accepts contributions via GitHub pull requests.
To start contributing please see the [development guide](CONTRIBUTING.md).
