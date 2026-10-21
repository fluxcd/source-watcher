# Source Watcher

## Controller Specification

The source-watcher implements the `source.extensions.fluxcd.io` API that
extends the capabilities of the Flux [source-controller](https://github.com/fluxcd/source-controller)
with advanced source composition and decomposition patterns.

## Flags

| Name                                  | Type          | Description                                                                                                                                                                              |
|---------------------------------------|---------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `--artifact-digest-algo`              | string        | The hashing algorithm used to calculate the digest of artifacts. (default "sha256")                                                                                                      |
| `--artifact-retention-records`        | int           | The maximum number of artifacts to be kept in storage after a garbage collection. (default 2)                                                                                            |
| `--artifact-retention-ttl`            | duration      | The duration of time that artifacts from previous reconciliations will be kept in storage before being garbage collected. (default 1m0s)                                                 |
| `--authorized-artifact-client-spiffe-id` | strings    | SPIFFE ID authorized to fetch artifacts from the artifact server. Can be repeated. When set, the artifact server uses SPIFFE mTLS.                                                        |
| `--authorized-artifact-server-spiffe-id` | strings    | SPIFFE ID of an artifact server authorized to serve artifacts to source-watcher. Can be repeated. When set, the artifact fetcher uses SPIFFE mTLS.                                       |
| `--authorized-event-server-spiffe-id` | strings      | SPIFFE ID of an event server authorized to receive events from source-watcher. Can be repeated. When set, the event recorder uses SPIFFE mTLS.                                           |
| `--concurrent`                        | int           | The number of concurrent reconciles per controller. (default 10)                                                                                                                         |
| `--default-service-account`           | string        | The default service account used to authorize cross-namespace artifacts and managed namespaces when `.spec.serviceAccountName` is not specified.                                         |
| `--enable-leader-election`            | boolean       | Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.                                                                    |
| `--events-addr`                       | string        | The address of the events receiver.                                                                                                                                                      |
| `--health-addr`                       | string        | The address the health endpoint binds to. (default ":9440")                                                                                                                              |
| `--http-retry`                        | int           | The maximum number of retries when failing to fetch artifacts over HTTP. (default 9)                                                                                                     |
| `--interval-jitter-percentage`        | uint8         | Percentage of jitter to apply to interval durations. A value of 10 will apply a jitter of +/-10% to the interval duration. It cannot be negative, and must be less than 100. (default 5) |
| `--leader-election-lease-duration`    | duration      | Interval at which non-leader candidates will wait to force acquire leadership (duration string). (default 35s)                                                                           |
| `--leader-election-release-on-cancel` | boolean       | Defines if the leader should step down voluntarily on controller manager shutdown. (default true)                                                                                        |
| `--leader-election-renew-deadline`    | duration      | Duration that the leading controller manager will retry refreshing leadership before giving up (duration string). (default 30s)                                                          |
| `--leader-election-retry-period`      | duration      | Duration the LeaderElector clients should wait between tries of actions (duration string). (default 5s)                                                                                  |
| `--log-encoding`                      | string        | Log encoding format. Can be 'json' or 'console'. (default "json")                                                                                                                        |
| `--log-level`                         | string        | Log verbosity level. Can be one of 'trace', 'debug', 'info', 'error'. (default "info")                                                                                                   |
| `--max-retry-delay`                   | duration      | The maximum amount of time for which an object being reconciled will have to wait before a retry. (default 15m0s)                                                                        |
| `--metrics-addr`                      | string        | The address the metric endpoint binds to. (default ":8080")                                                                                                                              |
| `--min-retry-delay`                   | duration      | The minimum amount of time for which an object being reconciled will have to wait before a retry. (default 750ms)                                                                        |
| `--no-cross-namespace-refs`           | boolean       | When set to true, references between custom resources are allowed only if the reference and the referee are in the same namespace. (default false)                                       |
| `--override-manager`                  | stringArray   | Field manager disallowed to perform changes on managed resources. Can be repeated.                                                                                                        |
| `--reconciliation-timeout`            | duration      | The maximum duration of a reconciliation. (default 10m0s)                                                                                                                                |
| `--requeue-dependency`                | duration      | The interval at which failing dependencies are reevaluated. (default 5s)                                                                                                                 |
| `--storage-addr`                      | string        | The address the static file server binds to. (default ":9090")                                                                                                                           |
| `--storage-adv-addr`                  | string        | The advertised address of the static file server.                                                                                                                                        |
| `--storage-path`                      | string        | The local storage path. (default "/data")                                                                                                                                                |
| `--watch-all-namespaces`              | boolean       | Watch for resources in all namespaces, if set to false it will only watch the runtime namespace. (default true)                                                                          |
| `--watch-label-selector`              | string        | Watch for resources with matching labels e.g. 'sharding.fluxcd.io/shard=shard1'.                                                                                                        |
| `--feature-gates`                     | mapStringBool | A comma separated list of key=value pairs defining the state of experimental features.                                                                                                   |

### SPIFFE mTLS

When at least one of the repeatable flags `--authorized-artifact-client-spiffe-id`,
`--authorized-artifact-server-spiffe-id` or `--authorized-event-server-spiffe-id`
is set, source-watcher acquires an X.509-SVID from the SPIFFE Workload API. The
`SPIFFE_ENDPOINT_SOCKET` environment variable must point to the Workload API
socket.

- `--authorized-artifact-client-spiffe-id` authorizes the given SPIFFE IDs to
  fetch artifacts from the artifact server. When set, the artifact server uses
  mTLS.
- `--authorized-artifact-server-spiffe-id` authorizes the given SPIFFE IDs of
  artifact servers from which source-watcher fetches artifacts. When set, the
  artifact fetcher uses mTLS.
- `--authorized-event-server-spiffe-id` authorizes the given SPIFFE IDs of
  event servers to which source-watcher sends events. When set, the event
  recorder uses mTLS.

Each traffic type is protected independently. SPIFFE mTLS is transparent to the
Flux API: the scheme of `.status.artifact.url` does not change.

Each flag value is interpreted as follows:

- `spiffe://<trust domain>/<path>` authorizes exactly that SPIFFE ID.
- `spiffe://<trust domain>/<path>/` (with a trailing slash) authorizes any
  SPIFFE ID with that path prefix.
- `spiffe://<trust domain>` (without a path) authorizes any SPIFFE ID in that
  trust domain.

Whenever `<trust domain>` equals the special value `self`, source-watcher
understands it as the trust domain of the controller's own X509-SVID.

Invalid values cause source-watcher to fail at startup.

### Feature Gates

| Name                         | Default Value | Description                                                                                                                                                                                                               |
|------------------------------|---------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `DirectSourceFetch`          | `false`       | Enables fetching source objects (GitRepository, OCIRepository, Bucket, HelmChart, ExternalArtifact) directly from the API server using APIReader, bypassing the controller's cache. This can be useful when immediate consistency is required for source object reads. |
