# Running the Gardener ClusterProvider

## Environment Variables

There are a few environment variables that are evaluated on startup and can be used to control the behavior of the ClusterProvider:

- `ACCESS_REQUEST_SERVICE_ACCOUNT_NAMESPACE` specifies the namespace on shoot clusters that is used to create a `ServiceAccount` for granting access to that shoot cluster. It defaults to `accessrequests`.

## Shoot Prometheus Observability

Set `open-control-plane.io/observability=enabled` on a `Cluster` to create a
Shoot Prometheus `ScrapeConfig` and a forwarded authentication `Secret` in the
Cluster's namespace on the platform cluster. Adding the label or changing its
value to `enabled` triggers reconciliation. Removing the label or changing its
value away from `enabled` triggers cleanup. Resources annotated with
`openmcp.cloud/operation=ignore` remain excluded from reconciliation.

The `ShootObservability` condition distinguishes successful states by reason:

- `ObservabilityEnabled`: the ScrapeConfig and authentication Secret are synchronized.
- `ObservabilityDisabled`: the ScrapeConfig and authentication Secret are removed.

Both successful states have status `True`; an error has status `False` with its
failure reason and message. `ObservabilityEnabled` confirms resource synchronization,
not successful metric ingestion. Platform Prometheus must separately be configured
to select these ScrapeConfigs and their namespaces.
