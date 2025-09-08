# Artifact Generators

<!-- menuweight:110 -->

The ArtifactGenerator is an extension of Flux APIs that allows source composition and decomposition.
It enables the generation of [ExternalArtifacts][externalartifact] from multiple sources
([GitRepositories][gitrepository], [OCIRepositories][ocirepository] and [Buckets][bucket])
or the splitting of a single source into multiple artifacts.

## Source Composition Example

The following example shows how to compose an artifact from multiple sources:

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
      name: my-backend
    - alias: frontend
      kind: OCIRepository
      name: my-frontend
    - alias: config
      kind: Bucket
      name: my-configs
  artifacts:
    - name: my-app-composite
      copy:
        - from: "@backend/deploy/**"
          to: "@artifact/my-app/backend/"
        - from: "@frontend/deploy/**/*.yaml"
          to: "@artifact/my-app/frontend/"
        - from: "@config/envs/prod/configmap.yaml"
          to: "@artifact/my-app/env.yaml"
```

The above generator will create an ExternalArtifact named `my-app-composite`
in the `apps` namespace, which contains the deployment manifests from both
the `my-backend` Git repository and the `my-frontend` OCI repository,
as well as a ConfigMap from the `my-configs` Bucket.

The ExternalArtifact revision is computed based on the final content of the artifact,
in the format `latest@sha256:<hash>`, where `<hash>` is a SHA256 checksum of the combined files.

The generated ExternalArtifact can be deployed using a Flux Kustomization, for example:

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-app
  namespace: apps
spec:
  targetNamespace: apps
  sourceRef:
    kind: ExternalArtifact
    name: my-app-composite
  path: "./my-app"
  interval: 30m
  timeout: 5m
  prune: true
  wait: true
```

Every time one of the sources is updated, a new artifact revision will be generated
with the latest content and the Flux Kustomization will automatically reconcile it.

## Helm Chart Composition Example

The following example shows how to compose a Helm chart from multiple sources:

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
      revision: "@chart"
      copy:
        - from: "@chart/**"
          to: "@artifact/"
        - from: "@repo/charts/podinfo/values/values-prod.yaml"
          to: "@artifact/values.yaml"
```

The above generator will create an ExternalArtifact named `podinfo-composite` in the `apps` namespace,
which contains the Helm chart from the `podinfo-chart` OCI repository with the `values.yaml` overwritten
by the `values-prod.yaml` file from the Git repository.

The generated ExternalArtifact can be deployed using a Flux HelmRelease, for example:

```yaml
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: podinfo
  namespace: apps
spec:
  interval: 10m
  releaseName: podinfo
  chartRef:
    kind: ExternalArtifact
    name: podinfo-composite
```

## Source Decomposition Example

The following example shows how to decompose a source into multiple artifacts:

```yaml
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata:
  name: my-app
  namespace: apps
spec:
  sources:
    - alias: repo
      kind: GitRepository
      name: my-monorepo
  artifacts:
    - name: frontend
      copy:
        - from: "@repo/deploy/frontend/**"
          to: "@artifact/"
    - name: backend
      copy:
        - from: "@repo/deploy/backend/**"
          to: "@artifact/"
```

The above generator will create two ExternalArtifacts named `frontend` and `backend`
in the `apps` namespace, each containing the respective deployment manifests
from the `my-monorepo` Git repository.

The generated ExternalArtifacts can be deployed using Flux Kustomizations, for example:

```yaml
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: backend
  namespace: apps
spec:
  sourceRef:
    kind: ExternalArtifact
    name: backend
  path: "./"
  interval: 30m
  prune: true
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: frontend
  namespace: apps
spec:
  sourceRef:
    kind: ExternalArtifact
    name: frontend
  path: "./"
  interval: 30m
  prune: true
```

Every time the monorepo is updated, new revisions will be generated only for the affected artifacts.
If the manifests in `deploy/frontend/` directory are modified, only the `frontend` artifact will
receive a new revision, triggering the Flux Kustomization that applies it.
While the `backend` artifact remains unchanged and its Kustomization will not reconcile.

## Writing an ArtifactGenerator spec

As with all other Kubernetes config, an ArtifactGenerator needs `apiVersion`,
`kind`, `metadata.name` and `metadata.namespace` fields.

### Sources

The `.spec.sources` field defines the Flux source-controller resources that will be used as inputs
for artifact generation. Each source must specify:

- `alias`: A unique identifier used to reference the source in copy operations.
   Alias names must be unique within the same ArtifactGenerator and can only contain
   alphanumeric characters, dashes and underscores.
- `kind`: The type of Flux source resource (`GitRepository`, `OCIRepository`, or `Bucket`)
- `name`: The name of the source resource
- `namespace` (optional): The namespace of the source resource if different from the ArtifactGenerator namespace

**Note** that on multi-tenant clusters, platform admins can disable cross-namespace references
by starting the controller with the `--no-cross-namespace-refs=true` flag.

```yaml
spec:
  sources:
    - alias: backend
      kind: GitRepository
      name: my-backend
    - alias: frontend
      kind: OCIRepository
      name: my-frontend
    - alias: config
      kind: Bucket
      name: my-configs
```

Sources are watched for changes, and when any source is updated, the controller will
regenerate the affected artifacts automatically.

### Artifacts

The `.spec.artifacts` field defines the list of ExternalArtifacts to be generated from the sources.
Each artifact must specify:

- `name`: The name of the generated ExternalArtifact resource. It must be unique in the context
  of the ArtifactGenerator and must conform to Kubernetes resource naming conventions.
- `revision` (optional): A specific source revision to use in the format `@alias`.
  If not specified, the revision is automatically computed as `latest@<digest>` based on the artifact content.
- `copy`: A list of copy operations to perform from sources to the artifact.

#### Copy Operations

Each copy operation specifies how to copy files from sources into the generated artifact:

- `from`: Source path in the format `@alias/pattern` where `alias` references 
  a source and `pattern` is a glob pattern.
- `to`: Destination path in the format `@artifact/path` where `artifact` is
  the root of the generated artifact.

```yaml
spec:
  artifacts:
    - name: my-app
      revision: "@backend"  # Use backend source revision
      copy:
        - from: "@backend/deploy/**/*.yaml"
          to: "@artifact/backend/"
        - from: "@frontend/manifests/service.yaml"
          to: "@artifact/frontend/service.yaml"
        - from: "@config/**"
          to: "@artifact/config/"
```

Copy operation behavior:

- Operations are executed in the order they are listed.
- Later operations can overwrite files created by earlier operations.
- Glob patterns support `*` (single directory) and `**` (recursive directories).
- Source paths are relative to the root of the source artifact.
- Destination paths are relative to the root of the generated artifact, and must start with `@artifact/`.
- Destination paths ending with `/` indicate a directory, otherwise it's treated as a file.

## ArtifactGenerator status

The ArtifactGenerator reports the latest synchronized state in the `.status` field.

### Conditions

ArtifactGenerator has various states during its lifecycle, reflected as
Kubernetes Conditions. It can be [reconciling](#reconciling-artifactgenerator)
while fetching the remote state, it can be [ready](#ready-artifactgenerator),
or it can [fail during reconciliation](#failed-artifactgenerator).

All conditions have a `message` field that provides additional context about
the current state.

#### Reconciling ArtifactGenerator

The controller marks an ArtifactGenerator as _reconciling_ when 
it is actively working to produce artifacts from source changes.

When the ArtifactGenerator is reconciling, the controller sets a Condition
with the following attributes in the ArtifactGenerator's `.status.conditions`:

- `type: Reconciling`
- `status: "True"`
- `reason: Progressing`

In addition, the controller sets the `Ready` Condition to `Unknown`.

#### Ready ArtifactGenerator

The controller marks an OCIRepository as _ready_ when it has successfully
produced and stored artifacts in the controller's storage.

When the ArtifactGenerator is "ready", the controller sets a Condition with the
following attributes in the OCIRepository's `.status.conditions`:

- `type: Ready`
- `status: "True"`
- `reason: Succeeded`

This `Ready` Condition will retain a status value of `"True"` until the
ArtifactGenerator is marked as [reconciling](#reconciling-artifactgenerator), or e.g. a
[transient error](#failed-artifactgenerator) occurs.

#### Failed ArtifactGenerator

The controller may encounter errors while attempting to produce and store
artifacts. These errors can be transient or terminal, such as:

- The Flux source-controller is unreachable (e.g. network issues).
- One of the referenced sources is not found or access is denied.
- The copy operation fails due to duplicate aliases, invalid glob patterns or missing files.
- A storage related failure when storing the artifacts.

When an error occurs, the controller sets the `Ready` Condition status to `False`,
with one of the following reasons:

- `type: Ready`
- `status: "False"`
- `reason: AccessDenied | ArtifactFailed | BuildFaild | ReconciliationFailed`

Transient errors (e.g. network issues) will cause the controller to
retry the reconciliation after a backoff period, while terminal errors
(e.g. access denied, invalid spec) will cause the controller to
mark the ArtifactGenerator as [stalled](#stalled-artifactgenerator).

#### Stalled ArtifactGenerator

The controller marks an ArtifactGenerator as _stalled_ when it encounters
a terminal failure that prevents it from making progress.

When the ArtifactGenerator is stalled, the controller sets the following condition:

- `type: Stalled`
- `status: "True"`
- `reason: AccessDenied | ValidationFailed`

### Inventory

The ArtifactGenerator reports the list of generated ExternalArtifacts in the
`.status.inventory` field. The inventory is used by the controller to
keep track of the artifacts in storage and to perform garbage collection
of orphaned artifacts.

[externalartifact]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/externalartifacts.md
[gitrepository]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/gitrepositories.md
[ocirepository]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/ocirepositories.md
[bucket]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/buckets.md
