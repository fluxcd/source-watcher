# AGENTS.md

Guidance for AI coding assistants working in `fluxcd/source-watcher`. Read this file before making changes.

## Contribution workflow for AI agents

These rules come from [`fluxcd/flux2/CONTRIBUTING.md`](https://github.com/fluxcd/flux2/blob/main/CONTRIBUTING.md) and apply to every Flux repository.

- **Do not add `Signed-off-by` or `Co-authored-by` trailers with your agent name.** Only a human can legally certify the DCO.
- **Disclose AI assistance** with an `Assisted-by` trailer naming your agent and model:
  ```sh
  git commit -s -m "Add support for X" --trailer "Assisted-by: <agent-name>/<model-id>"
  ```
  The `-s` flag adds the human's `Signed-off-by` from their git config — do not remove it.
- **Commit message format:** Subject in imperative mood ("Add feature X" instead of "Adding feature X"), capitalized, no trailing period, ≤50 characters. Body wrapped at 72 columns, explaining what and why. No `@mentions` or `#123` issue references in the commit — put those in the PR description.
- **Trim verbiage:** in PR descriptions, commit messages, and code comments. No marketing prose, no restating the diff, no emojis.
- **Rebase, don't merge:** Never merge `main` into the feature branch; rebase onto the latest `main` and push with `--force-with-lease`. Squash before merge when asked.
- **Pre-PR gate:** `make tidy fmt vet && make test` must pass and the working tree must be clean after codegen. Commit regenerated files in the same PR.
- **Flux is GA:** Backward compatibility is mandatory. Breaking changes to CRD fields, status, CLI flags, metrics, or observable behavior will be rejected. Design additive changes and keep older API versions round-tripping.
- **Copyright:** All new `.go` files must begin with the boilerplate from `hack/boilerplate.go.txt` (Apache 2.0). Update the year to the current year when copying.
- **Spec docs:** New features and API changes must be documented in `docs/spec/v1beta1/artifactgenerators.md`. Update it in the same PR that introduces the change.
- **Tests:** New features, improvements and fixes must have test coverage. Add unit tests in `internal/controller/*_test.go` and other `internal/*` packages as appropriate. Follow the existing patterns for test organization, fixtures, and assertions. Run tests locally before pushing.

## Code quality

Before submitting code, review your changes for the following:

- **No secrets in logs or events.** Never surface auth tokens, passwords, or credential URLs in error messages, conditions, events, or log lines. Use `fluxcd/pkg/masktoken` when scrubbing strings that may contain secret material.
- **No unchecked I/O.** Close HTTP response bodies, file handles, and archive readers in `defer` statements. Check and propagate errors from `io.Copy`, `os.Remove`, `os.Rename`.
- **No path traversal.** File paths from source tarballs and glob patterns must stay within the expected working directory. Validate extracted paths before writing. Never `filepath.Join` with untrusted components without validation.
- **No unbounded reads.** Use `io.LimitReader` when reading from network or archive sources. Respect existing size limits; do not introduce new reads without bounds.
- **No command injection.** Do not shell out via `os/exec`. Use Go libraries for all operations.
- **Error handling.** Wrap errors with `%w` for chain inspection. Do not swallow errors silently. Return actionable error messages that help users diagnose the issue without leaking internal state.
- **Resource cleanup.** Ensure temporary files and directories are cleaned up on all code paths (success and error). Use `defer` and `t.TempDir()` in tests.
- **Deterministic output.** The builder must produce identical tarballs for identical inputs — sorted file walks, stable timestamps, no randomness in `internal/builder`.
- **Concurrency safety.** Do not introduce shared mutable state without synchronization. Reconcilers run concurrently; per-object work must be isolated.
- **No panics.** Never use `panic` in runtime code paths. Return errors and let the reconciler handle them gracefully.
- **Minimal surface.** Keep new exported APIs, flags, and environment variables to the minimum needed. Every export is a backward-compatibility commitment.

## Project overview

source-watcher is a production Kubernetes controller in the [Flux GitOps Toolkit](https://fluxcd.io/flux/components/) (shipped via `flux install --components-extra=source-watcher`). It reconciles the `ArtifactGenerator` CRD under `source.extensions.fluxcd.io`, which composes and decomposes artifacts produced by source-controller. The controller watches `GitRepository`, `OCIRepository`, `Bucket`, `HelmChart`, and `ExternalArtifact` resources, fetches their tarballs, runs glob-based copy/merge/extract operations across them, and publishes the resulting tar.gz archives as `ExternalArtifact` objects that kustomize-controller and helm-controller can consume.

## Repository layout

- `cmd/main.go` — manager entrypoint. Wires the scheme, gotk runtime options, the artifact storage server from `github.com/fluxcd/pkg/artifact`, and registers `ArtifactGeneratorReconciler`.
- `api/` — separate Go module (`github.com/fluxcd/source-watcher/api/v2`) containing the CRD types. `replace`d locally from the root `go.mod`.
  - `api/v1beta1/` — `ArtifactGenerator` types, constants (finalizer, annotations, reasons, strategies), `groupversion_info.go`, and `zz_generated.deepcopy.go`.
- `internal/controller/` — `ArtifactGeneratorReconciler` split across `artifactgenerator_controller.go` (reconcile loop), `_manager.go` (SetupWithManager + predicates), `_status.go`, `_finalize.go`, `_drift.go`, `_validation.go`, plus unit tests.
- `internal/controller_test/` — black-box integration tests against envtest (separate package to avoid import cycles with controller internals).
- `internal/builder/` — artifact builder logic: file/dir copy with glob patterns (`doublestar/v4`), YAML merge (Helm-style), tarball extract, directory hashing (`dirhash.go`), and the tar.gz writer.
- `config/` — Kustomize overlays. `crd/bases/` holds the generated source-watcher CRD plus source-controller CRDs downloaded by `make download-crd-deps` (tests only). `default/`, `manager/`, `rbac/`, `release/`, `samples/`, `testdata/`.
- `docs/spec/v1beta1/artifactgenerators.md` — user-facing API spec. Keep in sync when changing `api/v1beta1`.
- `hack/boilerplate.go.txt` — license header injected by `controller-gen object`.

## APIs and CRDs

- Group/version: `source.extensions.fluxcd.io/v1beta1`. Kind: `ArtifactGenerator` (short name `ag`). Namespaced; status subresource enabled.
- Types live in `api/v1beta1/artifactgenerator_types.go`. `zz_generated.deepcopy.go` is generated — do not hand-edit.
- Spec shape: `Sources []SourceReference` (`alias`, `name`, `namespace`, `kind` one of `Bucket|GitRepository|OCIRepository|HelmChart|ExternalArtifact`) plus `OutputArtifacts []OutputArtifact` (with `name`, optional `revision`/`originRevision` referencing `@alias`, and `CopyOperation` entries `from: "@alias/<glob>"` → `to: "@artifact/<path>"` with optional `exclude` and `strategy` one of `Overwrite|Merge|Extract`).
- Status tracks `Conditions`, an `Inventory` of generated `ExternalArtifact` refs (for orphan GC), and `ObservedSourcesDigest`.
- This controller defines no other CRDs. It consumes source-controller CRDs and produces `ExternalArtifact` resources (owned by source-controller's API). RBAC markers live on the reconciler in `internal/controller/artifactgenerator_controller.go`.

## Build, test, lint

All targets in the root `Makefile`. `CONTROLLER_GEN_VERSION` is pinned there; tools install into `./bin`.

- `make tidy` — tidy both the root and `api/` modules.
- `make fmt` / `make vet` — run in both modules.
- `make generate` — `controller-gen object` inside `api/` to refresh `zz_generated.deepcopy.go` using `hack/boilerplate.go.txt`.
- `make manifests` — `controller-gen` for CRDs and RBAC into `config/crd/bases` and `config/rbac`.
- `make manager` — builds the binary to `./bin/manager` (depends on `generate fmt vet`).
- `make download-crd-deps` — pulls the pinned source-controller CRDs (`GitRepository`, `Bucket`, `OCIRepository`, `HelmChart`, `ExternalArtifact`) into `config/crd/bases`. Version is derived from `go list -m` on `github.com/fluxcd/source-controller/api`.
- `make install-envtest` / `make setup-envtest` — installs `setup-envtest` into `./bin` and fetches kube-apiserver/etcd assets into `./testbin`.
- `make test` — full run: `tidy generate fmt vet manifests install-envtest download-crd-deps` then `go test ./... -coverprofile cover.out` with `KUBEBUILDER_ASSETS` exported. Honors `GO_TEST_ARGS` for extra flags (e.g. `-run`).
- `make run` — runs the manager locally with storage under `bin/data`.
- `make docker-build` / `make docker-push` — image build (default `fluxcd/source-watcher:latest`).
- `make manifests-release` — renders release YAML to `build/source-watcher.deployment.yaml`.

## Codegen and generated files

Check `go.mod` and the `Makefile` for current dependency and tool versions. After changing API types or kubebuilder markers, regenerate and commit the results:

```sh
make generate manifests
```

Generated files (never hand-edit):

- `api/v1beta1/zz_generated.deepcopy.go`
- `config/crd/bases/source.extensions.fluxcd.io_*.yaml` (source-watcher CRD only)
- Downloaded source-controller CRDs under `config/crd/bases/` (`gitrepositories.yaml`, `buckets.yaml`, `ocirepositories.yaml`, `helmcharts.yaml`, `externalartifacts.yaml`) — overwritten by `make download-crd-deps`.

Load-bearing `replace` in `go.mod` — do not remove:

- `opencontainers/go-digest` pinned to a fork for BLAKE3 support.

Bump `fluxcd/pkg/*` modules as a set. Run `make tidy` after any bump.

## Conventions

- Standard `gofmt`. Exported names and top-level declarations must have doc comments. Keep comments terse.
- Patterns in use: finalizer-based cleanup, `gotkpatch.Helper` for status, `gotkconditions` helpers, `gotkmeta` reason constants, `gotkjitter` for requeue jitter. Reuse these rather than inventing new mechanics.
- The controller never caches `Secret` or `ConfigMap` (`mgrConfig.Client.Cache.DisableFor` in `cmd/main.go`). Preserve that.
- Respect the `NoCrossNamespaceRefs` ACL option in `internal/controller/artifactgenerator_validation.go`: cross-namespace source refs are rejected when the flag is set.
- Source artifacts are fetched over HTTP from source-controller's advertised URL via `gotkfetch.ArchiveFetcher`, extracted with `gotkpkg/tar`, combined by `internal/builder`, and stored through `gotkstorage.Storage`. Storage layout: `<storage-root>/<kind>/<namespace>/<name>/<uuid>.tar.gz`. Generated artifacts are served by `gotkserver` once this pod is the elected leader.
- Revision semantics: `OutputArtifact.Revision` may be `@<alias>` to mirror a source revision; otherwise the controller computes `latest@<content-digest>` from the tarball. `OriginRevision` maps to the OCI `org.opencontainers.image.revision` annotation.
- Orphan GC: before writing status, the reconciler diffs `status.Inventory` against current `OutputArtifacts` and deletes any `ExternalArtifact` no longer referenced. Do not break this invariant.
- Generated `ExternalArtifact` objects carry the label `source.extensions.fluxcd.io/generator` (`swapi.ArtifactGeneratorLabel`) pointing at the owning `ArtifactGenerator`.
- Reconciliation pause: annotation `source.extensions.fluxcd.io/reconcile: disabled` on the `ArtifactGenerator`.

## Testing

- Framework: `github.com/onsi/gomega` (`NewWithT(t)`) + Go `testing`. No Ginkgo.
- Integration tests use `github.com/fluxcd/pkg/runtime/testenv` to start envtest. Binaries land in `./testbin` via `make install-envtest`. Tests rely on CRDs under `config/crd/bases` — you must have run `make download-crd-deps` at least once; `make test` chains this for you.
- HTTP artifact serving in tests comes from `github.com/fluxcd/pkg/testserver.ArtifactServer`, wired to a `gotkstorage.Storage` instance in `internal/controller/suite_test.go` `TestMain`.
- Test suites:
  - `internal/builder/` — pure unit tests using `t.TempDir()`. Covers copy, exclude, glob, merge, extract, dirhash.
  - `internal/controller/` — white-box tests for reconciler helpers (validation, drift, finalize) using the shared envtest setup.
  - `internal/controller_test/` — black-box end-to-end tests for the full reconcile loop (watch events, status, inventory GC).
- Run a single test: `make test GO_TEST_ARGS='-run TestBuild/copy_operations_overwrite_existing_files'`.

## Gotchas and non-obvious rules

- `api/` is a **separate Go module**. Running `go mod tidy` from the root does not tidy it; use `make tidy`, which handles both. Adding an import to `api/v1beta1/*.go` not already in `api/go.mod` silently breaks downstream consumers.
- `internal/controller_test/` is a sibling of `internal/controller/`, not a subpackage. It exists to keep integration tests in a black-box package; do not collapse them.
- `cmd/main.go` imports the API module as `github.com/fluxcd/source-watcher/api/v2/v1beta1` (note `/v2/`) and the controller as `github.com/fluxcd/source-watcher/v2/internal/controller`. Respect the major-version path segments when adding imports.
- A package-level typo in `internal/controller/artifactgenerator_controller.go` imports `gotkstroage "github.com/fluxcd/pkg/artifact/storage"` (misspelled). Don't "fix" it without running the full test suite — search for all usages first.
- `make test` depends on `make download-crd-deps`, which pulls source-controller CRDs over the network using the version resolved from `go.mod`. Bumping `github.com/fluxcd/source-controller/api` requires deleting `build/.src-crd-*` (or running `make cleanup-crd-deps`) so the new CRDs are refetched.
- The controller hosts the HTTP server for its own generated artifacts (`gotkserver.Start` in `cmd/main.go`), started only after leader election. Do not add logic that assumes the storage server is available before `mgr.Elected()`.
- `api/v1beta1` constants (finalizer name, label, annotations, strategy strings) are part of the wire format. Renaming them is a breaking change even though they are Go identifiers.
- `config/crd/bases/` mixes two kinds of files: the source-watcher CRD (generated by `make manifests` from our types) and the five source-controller CRDs (downloaded). Only touch the first category by editing types and regenerating; never hand-edit the downloaded ones.
