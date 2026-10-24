# source-watcher

[![fossa](https://app.fossa.com/api/projects/custom%2B162%2Fgithub.com%2Ffluxcd%2Fsource-watcher.svg?type=small)](https://app.fossa.com/projects/custom%2B162%2Fgithub.com%2Ffluxcd%2Fsource-watcher?ref=badge_small)
[![test](https://github.com/fluxcd/source-watcher/workflows/e2e/badge.svg)](https://github.com/fluxcd/source-watcher/actions)
[![license](https://img.shields.io/github/license/fluxcd/source-watcher.svg)](https://github.com/fluxcd/source-watcher/blob/main/LICENSE)
[![release](https://img.shields.io/github/release/fluxcd/source-watcher/all.svg)](https://github.com/fluxcd/source-watcher/releases)

The source-watcher is a [GitOps toolkit](https://fluxcd.io/flux/components/) controller
that extends Flux with advanced source composition and decomposition patterns.

## Overview

The source-watcher controller implements the [ArtifactGenerator](docs/README.md) API,
which allows Flux users to:

- 🔗 **Compose** multiple Flux sources (GitRepository, OCIRepository, Bucket, HelmChart, ExternalArtifact) into a single deployable artifact
- 📦 **Decompose** monorepos into multiple independent artifacts with separate deployment lifecycles
- 🎯 **Optimize** reconciliation by only triggering updates when specific paths change
- 🏗️ **Structure** complex deployments from distributed sources maintained by different teams

## Quick Start

### Prerequisites

- Flux v2.7.2 or later
- A Kubernetes cluster bootstrapped with:
  - `flux bootstrap git --components-extra=source-watcher` (production mode), or
  - `flux install --components-extra=source-watcher` (dev mode)

### Example: Composing Multiple Sources

Deploy an application that combines Kubernetes manifests from different sources:

```yaml
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata:
  name: my-app
  namespace: apps
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
  namespace: apps
spec:
  interval: 15m
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
  namespace: platform
spec:
  sources:
    - alias: monorepo
      kind: GitRepository
      name: platform-monorepo
  artifacts:
    - name: policies
      copy:
        - from: "@monorepo/platform/policy/**"
          to: "@artifact/"
    - name: auth-service
      copy:
        - from: "@monorepo/services/auth/deploy/**"
          to: "@artifact/"
          exclude: ["*.md"]
    - name: api-gateway
      copy:
        - from: "@monorepo/services/gateway/deploy/**"
          to: "@artifact/"
          exclude: ["*.md", "**/charts/**"]
```

Each service gets its own ExternalArtifact with an independent revision.
Changes to `services/auth/` only trigger the reconciliation of the auth-service,
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
    - alias: repo
      kind: GitRepository
      name: podinfo-values
  artifacts:
    - name: podinfo-composite
      originRevision: "@chart"
      copy:
        - from: "@chart/"
          to: "@artifact/"
        - from: "@repo/charts/podinfo/values.yaml"
          to: "@artifact/podinfo/values.yaml"
          strategy: Overwrite
        - from: "@repo/charts/podinfo/values-prod.yaml"
          to: "@artifact/podinfo/values.yaml"
          strategy: Merge
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: podinfo
  namespace: apps
spec:
  interval: 15m
  releaseName: podinfo
  chartRef:
    kind: ExternalArtifact
    name: podinfo-composite
```

## Key Features

### Content-based Revision Tracking

The generated artifacts are versioned based on the hash computed from the artifact's content.
This means:

- ✅ Identical content = same revision (no unnecessary reconciliations)
- ✅ Any change to included content = new revision (guaranteed updates)
- ✅ Automatic change detection across all source types

### Flexible Copy Operations

Copy operations support glob patterns, exclusions and YAML merge:

- `@source/file.yaml` → `@artifact/dest/` - Copy file into directory
- `@source/dir/` → `@artifact/dest/` - Copy directory as subdirectory
- `@source/dir/**` → `@artifact/dest/` - Copy directory contents
- `@source/file.yaml` → `@artifact/existing.yaml` - Later copy overwrites earlier ones
- `exclude: ["*.md"]` - Exclude matching files from copy
- `strategy: Merge` - Deep merge YAML files instead of overwriting

## Use Cases

- **Multi-Team Collaboration** - Different teams maintain different Kubernetes
  configurations in separate repositories. ArtifactGenerator combines them into deployable
  units while maintaining clear ownership boundaries.
- **Environment-Specific Composition** Compose base manifests with environment-specific
  configurations from different sources without duplicating files across repositories.
- **Vendor Chart Customization** - Combine upstream Helm charts from OCI registries with
  your organization's custom values and configuration overrides stored in Git.
- **Selective Deployment** - Deploy only changed components from large repositories
  by decomposing them into dedicated artifacts.

## Contributing

This project is Apache 2.0 licensed and accepts contributions via GitHub pull requests.
To start contributing please see the [contrib guide](CONTRIBUTING.md).
