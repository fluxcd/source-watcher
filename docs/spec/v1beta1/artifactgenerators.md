# Artifact Generators

<!-- menuweight:110 -->

The ArtifactGenerator is an extension of Flux APIs that allows source composition and decomposition.
It enables the generation of [ExternalArtifacts][externalartifact] from multiple sources
([GitRepositories][gitrepository], [OCIRepositories][ocirepository], [Buckets][bucket], [HelmCharts][helmchart] and [ExternalArtifacts][externalartifact])
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
        - from: "@frontend/deploy/*.yaml"
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
  interval: 30m
  targetNamespace: apps
  sourceRef:
    kind: ExternalArtifact
    name: my-app-composite
  path: "./my-app"
  prune: true
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
      originRevision: "@chart"
      copy:
        - from: "@chart/"
          to: "@artifact/"
        - from: "@repo/charts/podinfo/values-prod.yaml"
          to: "@artifact/podinfo/values.yaml"
          strategy: Merge # Or `Overwrite` to replace the values.yaml
```

The above generator will create an ExternalArtifact named `podinfo-composite` in the `apps` namespace,
which contains the Helm chart from the `podinfo-chart` OCI repository with the `values.yaml` merged with
`values-prod.yaml` from the Git repository.

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
      originRevision: "@repo"
      copy:
        - from: "@repo/deploy/frontend/**"
          to: "@artifact/"
    - name: backend
      originRevision: "@repo"
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
  interval: 30m
  sourceRef:
    kind: ExternalArtifact
    name: backend
  path: "./"
  prune: true
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: frontend
  namespace: apps
spec:
  interval: 30m
  sourceRef:
    kind: ExternalArtifact
    name: frontend
  path: "./"
  prune: true
```

Every time the monorepo is updated, new revisions will be generated only for the affected artifacts.
If the manifests in `deploy/frontend/` directory are modified, only the `frontend` artifact will
receive a new revision, triggering the Flux Kustomization that applies it.
While the `backend` artifact remains unchanged and its Kustomization will not reconcile.

## Writing an ArtifactGenerator

As with all other Kubernetes config, an ArtifactGenerator needs `apiVersion`,`kind`,
`metadata.name` and `metadata.namespace` fields.

The `spec` field defines the desired state of the ArtifactGenerator, while the `status`
field reports the latest observed state.

### Sources

The `.spec.sources` field defines the Flux source-controller resources that will be used as inputs
for artifact generation. Each source must specify:

- `alias`: A unique identifier used to reference the source in copy operations.
   Alias names must be unique within the same ArtifactGenerator and can only contain
   alphanumeric characters, dashes and underscores.
- `kind`: The type of Flux source resource (`GitRepository`, `OCIRepository`, `Bucket`, `HelmChart`, or `ExternalArtifact`)
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

- `name` (required): The name of the generated ExternalArtifact resource. It must be unique in the context
  of the ArtifactGenerator and must conform to Kubernetes resource naming conventions.
- `copy` (required): A list of copy operations to perform from sources to the artifact.
- `revision` (optional): A specific source revision to use in the format `@alias`.
  If not specified, the revision is automatically computed as `latest@<digest>` based on the artifact content.
- `originRevision` (optional): A specific source origin revision to include in the artifact metadata
  in the format `@alias`. This is useful for the decomposition use case, where you want to track
  the original source revision of the artifact (e.g. the monorepo commit SHA) without affecting
  the artifact revision itself.

```yaml
spec:
  artifacts:
    - name: my-app
      revision: "@backend"
      originRevision: "@frontend"
      copy:
        - from: "@backend/deploy/**"
          to: "@artifact/backend/"
          exclude: ["**/charts/**"]
        - from: "@frontend/manifests/*.yaml"
          to: "@artifact/frontend/"
        - from: "@config/envs/prod/configmap.yaml"
          to: "@artifact/env.yaml"
```

#### Copy Operations

Each copy operation specifies how to copy files from sources into the generated artifact:

- `from`: Source path in the format `@alias/pattern` where `alias` references 
  a source and `pattern` is a glob pattern or a specific file/directory path within that source.
- `to`: Destination path in the format `@artifact/path` where `artifact` is
  the root of the generated artifact and `path` is the relative path to a file or directory.
- `exclude` (optional): A list of glob patterns to filter out from the source selection.
  Any file matched by `from` that also matches an exclude pattern will be ignored.
- `strategy` (optional): Defines how to handle files during copy operations:
  `Overwrite` (default), `Merge` (for YAML files), or `Extract` (for tarball archives).

Copy operations use `cp`-like semantics:

- Operations are executed in order; later operations can overwrite files from earlier ones
- Trailing slash in destination (`@artifact/dest/`) indicates copying into a directory
- `@source/dir/` copies as subdirectory, `@source/dir/**` strips directory prefix and copies contents recursively

Examples of copy operations:

```yaml
# Copy file to specific path - (like `cp source/config.yaml artifact/apps/app.yaml`)
- from: "@source/config.yaml"
  to: "@artifact/apps/app.yaml"    # Creates apps/app.yaml file

# Copy file to directory - (like `cp source/config.yaml artifact/apps/`)
- from: "@source/config.yaml"
  to: "@artifact/apps/"            # Creates apps/config.yaml

# Copy files to directory - (like `cp source/configs/*.yaml artifact/apps/`)
- from: "@source/configs/*.yaml"   # All .yaml files in configs/
  to: "@artifact/apps/"            # Creates apps/file1.yaml, apps/file2.yaml

# Copy dir and files recursively - (like `cp -r source/configs/ artifact/apps/`)
- from: "@source/configs/"         # All files and sub-dirs under configs/  
  to: "@artifact/apps/"            # Creates apps/configs/ with contents

# Copy files and dirs recursively - (like `cp -r source/configs/** artifact/apps/`)
- from: "@source/configs/**"       # All files and sub-dirs under configs/  
  to: "@artifact/apps/"            # Creates apps/file1.yaml, apps/subdir/file2.yaml
  exclude:
    - "*.md"                       # Excludes all .md files
    - "**/testdata/**"             # Excludes all files under any testdata/ dir
```

#### Copy Strategies

By default, copy operations use the `Overwrite` strategy, where later copies
overwrite files from earlier ones.

When copying YAML files, the `Merge` strategy can be used to merge the contents
from the source file into the destination file.

Example of copy with `Merge` strategy:

```yaml
# Copy the chart contents (this includes chart-name/values.yaml)
- from: "@chart/"
  to: "@artifact/"
# Merge values.yaml files - (like `helm --values values-prod.yaml`)
- from: "@git/values-prod.yaml"
  to: "@artifact/chart-name/values.yaml"
  strategy: Merge
```

**Note** that the merge strategy will replace _arrays_ entirely, the behavior is
identical to how Helm merges `values.yaml` files when using multiple `--values` flags.

##### Extract Strategy

The `Extract` strategy is used for extracting the contents of tarball archives (`.tar.gz`, `.tgz`)
built with `flux build artifact` or `helm package`. The tarball contents are extracted
to the destination while preserving their internal directory structure.

Example of copy with `Extract` strategy:

```yaml
# Extract a Helm chart tarball built with `helm package`
- from: "@oci/podinfo-6.7.0.tgz"
  to: "@artifact/"
  strategy: Extract

# Extract multiple tarballs using glob patterns
- from: "@source/charts/*.tgz"
  to: "@artifact/charts/"
  strategy: Extract

# Extract tarballs recursively from nested directories
- from: "@source/releases/**/*.tgz"
  to: "@artifact/"
  strategy: Extract
```

**Note** that when using glob patterns (including recursive `**` patterns) with the `Extract`
strategy, non-tarball files are silently skipped. For single file sources, the file must have
a `.tar.gz` or `.tgz` extension. Directories are not supported with this strategy.

## Working with ArtifactGenerators

### Suspend and Resume Reconciliation

You can temporarily suspend the reconciliation of an ArtifactGenerator by setting
the following annotation on the resource:

```yaml
metadata:
  annotations:
    source.extensions.fluxcd.io/reconcile: "Disabled"
```

To resume reconciliation, remove the annotation or set its value to `Enabled`.

### Trigger Reconciliation

You can manually trigger a reconciliation of an ArtifactGenerator by adding
the following annotation to the resource:

```yaml
metadata:
  annotations:
    reconcile.fluxcd.io/requestedAt: "<timestamp>"
```

The controller will pick up the annotation and start a reconciliation as soon as possible.
After the reconciliation is complete, the controller sets the timestamp from the annotation
in the `.status.lastHandledReconcileAt` field.

## ArtifactGenerator Status

The controller reports the latest synchronized state of an ArtifactGenerator in the `.status` field.

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

When the ArtifactGenerator is reconciling, the controller sets
the `Reconciling` Condition with the following attributes:

- `type: Reconciling`
- `status: "True"`
- `reason: Progressing`

In addition, the controller sets the `Ready` Condition to `Unknown`.

#### Ready ArtifactGenerator

The controller marks an ArtifactGenerator as _ready_ when it has successfully
produced and stored artifacts in the controller's storage.

When the ArtifactGenerator is "ready", the controller sets
the `Ready` Condition with the following attributes:

- `type: Ready`
- `status: "True"`
- `reason: Succeeded`

This `Ready` Condition will retain a status value of `"True"` until the
ArtifactGenerator is marked as [reconciling](#reconciling-artifactgenerator), or an
[error](#failed-artifactgenerator) occurs.

#### Failed ArtifactGenerator

The controller may encounter errors while attempting to produce and store
artifacts. These errors can be transient or terminal, such as:

- The Flux source-controller is unreachable (e.g. network issues).
- One of the referenced sources is not found or access is denied.
- The copy operation fails due to duplicate aliases, invalid glob patterns or missing files.
- Encounters a storage related failure when storing the artifacts.

When an error occurs, the controller sets the `Ready` Condition status to `False`,
with one of the following reasons:

- `type: Ready`
- `status: "False"`
- `reason: BuildFaild | SourceFetchFailed | ReconciliationFailed`

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

The controller reports the list of generated ExternalArtifacts in the`.status.inventory`
field of the ArtifactGenerator. The inventory is used by the controller to keep track
of the artifacts in storage and to perform garbage collection of orphaned artifacts.

## ArtifactGenerator Events

The controller emits Kubernetes events to provide insights into the lifecycle
of an ArtifactGenerator. These events can be viewed using `kubectl describe`
or with `kubectl events`.

Events are emitted for the following scenarios:

- ArtifactGenerator reconciliation completion (success or failure).
- ExternalArtifacts creation, update, or deletion.
- Source fetch failures or access issues.
- Build failures (e.g. invalid glob patterns, missing files).
- Storage operations (e.g. garbage collection, integrity validation failures).
- Drift detection (e.g. manual changes to generated ExternalArtifacts).

All events are also logged to the controller's standard output and contain 
the ArtifactGenerator name and namespace.

[externalartifact]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/externalartifacts.md
[gitrepository]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/gitrepositories.md
[ocirepository]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/ocirepositories.md
[bucket]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/buckets.md
[helmchart]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/helmcharts.md
