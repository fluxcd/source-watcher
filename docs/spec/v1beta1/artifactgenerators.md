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
        - from: "@repo/charts/podinfo/values.yaml"
          to: "@artifact/podinfo/values.yaml"
          strategy: Overwrite
        - from: "@repo/charts/podinfo/values-prod.yaml"
          to: "@artifact/podinfo/values.yaml"
          strategy: Merge
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

### Path Pattern (Directory Discovery)

The `.spec.pathPattern` field allows for dynamic, path-based discovery of artifacts. When specified, the controller will scan the referenced source for directories matching the pattern and dynamically generate one ExternalArtifact for each matched directory.

- `pathPattern` (optional): Specifies a directory traversal pattern within a source in the format `@<alias>/<pattern>`.
  Named captures in the pattern (e.g., `{app}`, `{env}`) can be used as placeholders in the `artifacts` fields.

When `pathPattern` is used, the generated ExternalArtifacts will automatically have their labels populated with the extracted capture variables.

```yaml
spec:
  sources:
    - alias: monorepo
      kind: GitRepository
      name: my-monorepo
  pathPattern: "@monorepo/apps/{app}/envs/{env}"
  artifacts:
    - name: "{app}-{env}"
      copy:
        - from: "@monorepo/apps/{app}/envs/{env}/**"
          to: "@artifact/"
```

#### Directory Name Constraints

Directory names matched by path pattern captures must comply with
[Kubernetes label value restrictions](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-semantics):

- Must be 63 characters or fewer
- Must begin and end with an alphanumeric character (`[a-zA-Z0-9]`)
- May contain dashes (`-`), underscores (`_`), dots (`.`), and alphanumeric characters

The controller automatically lowercases captured values before using them
in artifact names and labels. This means a directory named `Auth` will produce
an artifact with `app=auth`. Copy expressions receive the original captured
path values so source paths preserve their case.

**Note:** If two directories differ only in case (e.g., `apps/Auth/` and `apps/auth/`),
they will resolve to the same artifact name after lowercasing. In this case, the
controller will stall the reconciliation with an error indicating the collision.

If any captured directory name does not conform to the label value restrictions
(e.g., names starting with a dot like `.hidden`, containing spaces, or exceeding
63 characters), the reconciliation will fail with a terminal error that includes
the `pathPattern` and the invalid value.

### Artifacts

The `.spec.artifacts` field defines the list of ExternalArtifacts to be generated from the sources.
When `pathPattern` is defined, the artifacts act as templates for each matched directory.
Each artifact must specify:

- `name` (required): The name of the generated ExternalArtifact resource. It must be unique in the context
  of the ArtifactGenerator and must conform to Kubernetes resource naming conventions. Supports capture placeholders if `pathPattern` is used.
- `namespace` (optional): The namespace where the generated ExternalArtifact is created.
  If not specified, it defaults to the ArtifactGenerator namespace. Supports capture placeholders
  if `pathPattern` is used. See [Cross-namespace Artifacts](#cross-namespace-artifacts).
- `copy` (required): A list of copy operations to perform from sources to the artifact.
- `revision` (optional): A specific source revision to use in the format `@alias`.
  If not specified, the revision is automatically computed as `latest@<digest>` based on the artifact content.
- `originRevision` (optional): A specific source origin revision to include in the artifact metadata
  in the format `@alias`. This is useful for the decomposition use case, where you want to track
  the original source revision of the artifact (e.g. the monorepo commit SHA) without affecting
  the artifact revision itself.
- `inputsFrom` (optional): A source path in the format `@alias/path` or
  `@artifact/path` pointing to a YAML file whose contents are exported in the
  generated ExternalArtifact `.status.exportedInputs` field. Supports capture
  placeholders when `pathPattern` is used. See [Artifact Inputs](#artifact-inputs).

When `pathPattern` is not set, `name` and `namespace` are validated as a Kubernetes object name
and namespace respectively. When `pathPattern` is set, these fields are treated as templates and
the rendered values are validated instead.

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
  When `pathPattern` is set, this field may use capture placeholders and must render to this format.
- `to`: Destination path in the format `@artifact/path` where `artifact` is
  the root of the generated artifact and `path` is the relative path to a file or directory.
  When `pathPattern` is set, this field may use capture placeholders and must render to this format.
- `exclude` (optional): A list of glob patterns to filter out from the source selection.
  Any file matched by `from` that also matches an exclude pattern will be ignored.
  Patterns are matched against paths relative to the source alias root or to the
  non-glob prefix of `from`. Patterns without a separator (e.g. `*.md`) match the
  file name at any depth. Each pattern is limited to 1024 characters.
- `strategy` (optional): Defines how to handle files during copy operations:
  `Overwrite` (default), `Merge` (for YAML files), or `Extract` (for tarball archives).
- `optional` (optional): When set to `true`, the copy operation silently
  skips if the source path or glob pattern matches no files. Defaults to `false`,
  which causes the reconciliation to fail with an error on missing sources.

The `from` and `exclude` glob patterns may each contain at most 20 commas
inside `{}` alternation groups; patterns over the limit are rejected.

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
    - "subdir/**"                  # Excludes configs/subdir/ relative to the from prefix
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

### Artifact Inputs

The `.spec.artifacts[].inputsFrom` field allows exporting structured data from a
source or from the generated artifact itself, so that downstream consumers such
as ResourceSet can consume it for templating. The field is a path in the format
`@<alias>/<path>` (or `@artifact/<path>` for the generated artifact) pointing to
a YAML file. The file must contain a YAML mapping; each top-level key becomes an
entry in the `.status.exportedInputs` field of the generated ExternalArtifact.

```yaml
spec:
  sources:
    - alias: repo
      kind: GitRepository
      name: my-monorepo
  artifacts:
    - name: my-app
      inputsFrom: "@repo/apps/my-app/inputs.yaml"
      copy:
        - from: "@repo/apps/my-app/manifests/**"
          to: "@artifact/"
```

Given an `inputs.yaml` file like:

```yaml
replicas: 3
environment: production
```

the generated ExternalArtifact reports:

```yaml
status:
  exportedInputs:
    replicas: 3
    environment: production
```

### Common Metadata

The `.spec.commonMetadata` field defines labels and annotations that are uniformly applied to all 
[ExternalArtifacts][externalartifact] generated by the ArtifactGenerator. This provides a mechanism 
to propagate metadata to the output artifacts, which is particularly useful for enabling label 
selectors in downstream components.

```yaml
spec:
  commonMetadata:
    labels:
      app.kubernetes.io/name: my-app
      env: prod
    annotations:
      description: "Generated composite artifact"
```

Any existing label or annotation on the generated resources will be overridden if its key matches 
a common one. Note that the `app.kubernetes.io/managed-by` and `source.extensions.fluxcd.io/generator` 
labels are reserved by the controller and cannot be overridden by common metadata.

### Cross-namespace Artifacts

By default, the generated ExternalArtifacts are created in the same namespace as the
ArtifactGenerator. The `.spec.artifacts[].namespace` field can be used to create an
ExternalArtifact in a different namespace. This is useful for multi-tenant clusters
where the sources and the ArtifactGenerator run in a shared namespace, while the
generated artifacts are consumed by tenants in their own namespaces.

When `.spec.serviceAccountName` is set, the controller impersonates that
ServiceAccount for every generated ExternalArtifact, including the ones created
in the ArtifactGenerator namespace. The ServiceAccount must exist in the
ArtifactGenerator namespace, and its RBAC bindings determine which namespaces it
can access.

When `.spec.serviceAccountName` is not specified, the controller uses its own
credentials for artifacts in the ArtifactGenerator namespace (the default when
`.namespace` is not set). For artifacts targeting another namespace, it uses the
default ServiceAccount configured by the cluster administrator (see below), or
its own credentials when no default is configured.

For example, the following generator creates an ExternalArtifact in the `tenant-app`
namespace, using the `tenant-artifacts` ServiceAccount:

```yaml
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata:
  name: tenant-app
  namespace: flux-system
spec:
  serviceAccountName: tenant-artifacts
  sources:
    - alias: repo
      kind: GitRepository
      name: my-monorepo
  artifacts:
    - name: tenant-app
      namespace: tenant-app
      copy:
        - from: "@repo/tenants/tenant-app/**"
          to: "@artifact/"
```

**Note** that on multi-tenant clusters, platform admins should configure a default
ServiceAccount for impersonation by starting the controller with the
`--default-service-account=<name>` flag. It is used whenever
`.spec.serviceAccountName` is not specified, and only applies to artifacts
targeting a namespace different from the ArtifactGenerator namespace and to the
managed namespaces those artifacts target. Artifacts in the ArtifactGenerator
namespace keep using the controller credentials unless `.spec.serviceAccountName`
is set, so enabling the default does not change how in-namespace artifacts are
reconciled. Set `.spec.serviceAccountName` when an ArtifactGenerator must be
reconciled entirely with a specific ServiceAccount.

When `pathPattern` is set, `.spec.artifacts[].namespace` may use capture placeholders,
so each matched directory can be published to a namespace derived from the captured
values. For example, the following generator decomposes a monorepo into one
ExternalArtifact per tenant namespace:

```yaml
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata:
  name: tenants
  namespace: flux-system
spec:
  serviceAccountName: tenant-artifacts
  sources:
    - alias: repo
      kind: GitRepository
      name: my-monorepo
  pathPattern: "@repo/tenants/{tenant}/apps/{app}"
  artifacts:
    - name: "{app}"
      namespace: "{tenant}"
      copy:
        - from: "@repo/tenants/{tenant}/apps/{app}/**"
          to: "@artifact/"
```

The captured directory names are lowercased before being used as the artifact
name and namespace, so a directory named `Tenant-A` is published to the
`tenant-a` namespace. The rendered namespace must be a valid Kubernetes
namespace; otherwise the reconciliation fails with a terminal error.

### Namespace Management

By default, the controller does not manage the namespaces targeted by the
generated artifacts: they must already exist and are left untouched. The
`.spec.namespaces` field can be used to make the controller the manager
of those namespaces:

- `.spec.namespaces.strategy` (required when `.spec.namespaces` is set):
  `Unmanaged` or `Managed`. When `.spec.namespaces` is omitted, the target
  namespaces are unmanaged.
- `.spec.namespaces.prune` (optional): whether the controller deletes
  managed namespaces that are no longer targeted by any generated artifact, or
  when the ArtifactGenerator is deleted. Defaults to `false`. Individual
  namespaces can be protected from deletion with the
  `source.extensions.fluxcd.io/prune: Disabled` annotation.
- `.spec.namespaces.metadata` (optional): defines how the metadata of the
  managed namespaces is built, on top of `.spec.commonMetadata`. The metadata
  is constructed by applying `.spec.commonMetadata` first, then the metadata
  sourced from a `NamespaceMetadata` file, then the operations in `.from`, in
  order. It has:
  - `fromSource` (optional): sources metadata from a `NamespaceMetadata` file
    inside a source artifact. It has:
    - `path` (required): the path to the file, in the format `@<alias>/<path>`.
      Supports capture placeholders when `pathPattern` is used.
    - `allowedAnnotations` and `allowedLabels` (optional): schemas for the
      annotations and labels that the file is allowed to set. Each key is an
      annotation or label key and each value is a regular expression that the
      corresponding value must match. Keys that are not present in the schema
      are ignored, while values that do not match the respective regular
      expression are rejected.
  - `from` (optional): a list of metadata operations applied in order, after
    `fromSource`. Each operation has:
    - `strategy` (required): `Reset` clears all the existing labels and
      annotations before applying the ones defined in the operation, `Override`
      merges the ones defined in the operation into the existing values,
      overriding existing keys, while `Merge` merges only the absent keys,
      preserving existing values.
    - `namespace` (required): the name of a desired namespace, or `*` to apply
      the operation to all desired namespaces.
    - `labels` and `annotations` (optional): the metadata to apply. The
      `app.kubernetes.io/managed-by` and `source.extensions.fluxcd.io/generator`
      labels are reserved by the controller and cannot be overridden.

The `NamespaceMetadata` file allows tenants to provide additional metadata for
their namespaces, for example to opt into platform features that are enabled via
namespace metadata. It is a KRM-style configuration object read from a source
artifact:

```yaml
# tenants/tenant-a/namespace-metadata.yaml
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: NamespaceMetadata
metadata:
  annotations:
    foo: bar
  labels:
    baz: qux
```

The following example sources the namespace metadata from the monorepo, allows
tenants to set the `observability` label to `enabled` or `disabled`, and lets
platform admins override the metadata for a specific namespace:

```yaml
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata:
  name: tenants
  namespace: flux-system
spec:
  commonMetadata:
    labels:
      app.kubernetes.io/part-of: tenants
  sources:
    - alias: repo
      kind: OCIRepository
      name: monorepo
  namespaces:
    strategy: Managed
    metadata:
      fromSource:
        path: "@repo/tenants/{tenant}/namespace-metadata.yaml"
        allowedLabels:
          observability: "^(enabled|disabled)$"
      from:
        - strategy: Override
          namespace: tenant-a
          labels:
            observability: disabled
  pathPattern: "@repo/tenants/{tenant}"
  artifacts:
    - name: "{tenant}"
      namespace: "{tenant}"
      copy:
        - from: "@repo/tenants/{tenant}/**"
          to: "@artifact/"
```

When `.spec.namespaces.prune` is unset and the `DefaultToPruneNamespaces`
feature gate is enabled with `--feature-gates=DefaultToPruneNamespaces=true`,
the controller interprets the field as `true`. When the field is set, the
feature gate is ignored. The feature gate is disabled by default.

**Note:** both `.spec.namespaces.prune` and the `DefaultToPruneNamespaces`
feature gate only apply when `.spec.namespaces.strategy` is explicitly set to
`Managed`. With the `Unmanaged` strategy, or when `.spec.namespaces` is
omitted, the controller never deletes namespaces.

When set to `Managed`, the controller creates the namespaces that do not exist
and applies `.spec.commonMetadata` to all of them, along with the
`app.kubernetes.io/managed-by` and `source.extensions.fluxcd.io/generator`
labels. Namespaces that already exist are adopted, and namespaces managed by
another ArtifactGenerator are taken over, in both cases a warning event is
emitted. Combined with `pathPattern`, this allows namespaces to be created and
removed dynamically as directories are added or removed from the source.

```yaml
apiVersion: source.extensions.fluxcd.io/v1beta1
kind: ArtifactGenerator
metadata:
  name: tenants
  namespace: flux-system
spec:
  namespaces:
    strategy: Managed
    prune: true
  commonMetadata:
    labels:
      app.kubernetes.io/part-of: tenants
  sources:
    - alias: repo
      kind: GitRepository
      name: my-monorepo
  pathPattern: "@repo/tenants/{tenant}"
  artifacts:
    - name: "{tenant}"
      namespace: "{tenant}"
      copy:
        - from: "@repo/tenants/{tenant}/**"
          to: "@artifact/"
```

**Warning:** pruning deletes the entire namespace and everything in it, and
cannot be undone. If another Flux resource, such as a Kustomization or
HelmRelease, also manages workloads in the namespace, pruning wipes those
workloads. Only enable pruning when the namespaces are fully owned by the
ArtifactGenerator. If a delete request is rejected by the API server, the
namespace is kept in the inventory and the deletion is retried on the next
reconciliation, keeping the ArtifactGenerator in the terminating state when
it is being deleted.

#### Controlling the apply behavior of managed namespaces

To change the lifecycle behavior for specific managed namespaces, you can
annotate them with:

| Annotation | Default | Values | Role |
| --- | --- | --- | --- |
| `source.extensions.fluxcd.io/ssa` | `Override` | `Override`, `Merge`, `IfNotPresent`, `Ignore` | Apply policy |
| `source.extensions.fluxcd.io/prune` | `Enabled` | `Enabled`, `Disabled` | Delete policy |

**Note:** these annotations are meant for granular, per-object control. Set
them on individual in-cluster Namespace objects, for example by another
controller or with `kubectl annotate --field-manager=<name>`. They are not
allowed in `.spec.commonMetadata.annotations`. Use `.spec.namespaces.prune`
to control pruning for the whole ArtifactGenerator. The values are
case-insensitive.

##### `source.extensions.fluxcd.io/ssa`

###### Override

The `Override` policy instructs the controller to reconcile the namespace with
the desired metadata defined in the ArtifactGenerator, taking ownership of
fields managed by other field managers.

Fields added with `kubectl` are treated as drift and reverted on the next
reconciliation. To preserve fields added with `kubectl`, specify a field
manager that does not start with `kubectl`, for example:

```sh
kubectl apply --field-manager=manual-admin -f namespace.yaml
```

###### Merge

The `Merge` policy instructs the controller to preserve fields and metadata
recorded by other field managers, for example the
`kubectl.kubernetes.io/last-applied-configuration` annotation. The controller
skips the metadata cleanup, while the fields defined in the ArtifactGenerator
still override overlapping ones.

###### IfNotPresent

The `IfNotPresent` policy instructs the controller to only create the namespace
when it is missing, leaving existing namespaces untouched.

###### Ignore

The `Ignore` policy instructs the controller to skip applying and deleting the
namespace.

Platform admins can extend the set of field managers whose ownership the
controller takes over with one or more `--override-manager=<name>` flags. The
names are matched exactly, and the fields managed by them are treated as drift
in the same way as `kubectl` fields.

##### `source.extensions.fluxcd.io/prune`

When set to `Disabled`, the controller does not delete the namespace when it is
no longer targeted by any generated artifact or when the ArtifactGenerator is
deleted. The annotation is read from the in-cluster Namespace at deletion time
and only prevents deletion: it does not override `.spec.namespaces.prune`.
Namespaces are deleted only when pruning is enabled at the ArtifactGenerator
level, and this annotation protects individual namespaces from that deletion.
Setting the annotation to `Enabled` has no effect.

Namespace management uses the same credentials as artifact generation. When
`.spec.serviceAccountName` is set, the controller impersonates it. Otherwise,
when `--default-service-account` is set, the controller impersonates the default
for the namespaces targeted by cross-namespace artifacts; otherwise it uses its
own credentials. Creating and deleting namespaces requires cluster-scoped RBAC,
so the ServiceAccount must be bound to a `ClusterRole` granting the `namespaces`
verbs.

## Working with ArtifactGenerators

### Suspend and Resume Reconciliation

You can temporarily suspend the reconciliation of an ArtifactGenerator by setting
the following annotation on the resource:

```yaml
metadata:
  annotations:
    source.extensions.fluxcd.io/reconcile: Disabled
```

To resume reconciliation, remove the annotation or set its value to `Enabled`.

To pause the management of a specific managed namespace, annotate the
in-cluster Namespace with:

```yaml
source.extensions.fluxcd.io/reconcile: Disabled
```

**Note:** when the `source.extensions.fluxcd.io/reconcile` annotation is set to
`Disabled` on a managed namespace, the controller no longer applies changes,
nor does it delete the namespace. To resume management, remove the annotation
from the in-cluster Namespace, or set it to `Enabled`.

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

### Reconciliation Interval

The controller reconciles the ArtifactGenerator periodically to repair drift
caused by other tools and controllers modifying the generated resources. The
default interval is one hour and can be overridden by setting the following
annotation to a [Go duration](https://pkg.go.dev/time#ParseDuration) string:

```yaml
metadata:
  annotations:
    source.extensions.fluxcd.io/reconcileEvery: 10m
```

The value must be a positive Go duration, otherwise it is ignored and the
default interval is used. The interval is approximate and may be subject to
jitter.

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

The controller reports the list of objects managed by the ArtifactGenerator in
the `.status.inventory` field. The inventory is used by the controller to keep
track of the generated ExternalArtifacts and the managed namespaces, and to
perform garbage collection of the ones that are no longer targeted.

Each entry has a `kind`:

- Generated artifacts have `kind: ExternalArtifact`, along with the `name`,
  `namespace`, `digest` and `filename` of the referent. The `digest` is the
  content digest of the artifact.
- Managed namespaces have `kind: Namespace`, with the namespace `name` and a
  `digest` computed from the metadata applied by the controller. The
  `namespace` and `filename` fields are empty as namespaces are cluster-scoped.

## ArtifactGenerator Events

The controller emits Kubernetes events to provide insights into the lifecycle
of an ArtifactGenerator. These events can be viewed using `kubectl describe`
or with `kubectl events`.

Events are emitted for the following scenarios:

- ArtifactGenerator reconciliation completion (success or failure).
- ExternalArtifacts creation, update, or deletion.
- Namespaces creation, update, adoption, takeover, or deletion.
- Source fetch failures or access issues.
- Build failures (e.g. invalid glob patterns, missing files).
- Storage operations (e.g. garbage collection, integrity validation failures).
- Drift detection (e.g. manual changes to generated ExternalArtifacts or managed namespaces).
- Ownership conflicts (e.g. an ExternalArtifact or namespace managed by another ArtifactGenerator is taken over).

All events are also logged to the controller's standard output and contain 
the ArtifactGenerator name and namespace.

[externalartifact]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/externalartifacts.md
[gitrepository]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/gitrepositories.md
[ocirepository]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/ocirepositories.md
[bucket]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/buckets.md
[helmchart]: https://github.com/fluxcd/source-controller/blob/main/docs/spec/v1/helmcharts.md
