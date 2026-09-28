# Running the Gardener ClusterProvider

## Environment Variables

There are a few environment variables that are evaluated on startup and can be used to control the behavior of the ClusterProvider:

- `ACCESS_REQUEST_SERVICE_ACCOUNT_NAMESPACE` specifies the namespace on shoot clusters that is used to create a `ServiceAccount` for granting access to that shoot cluster. It defaults to `accessrequests`.
- `ENABLE_SHOOT_PROMETHEUS_OBSERVABILITY` enables Shoot Prometheus metrics federation for all Clusters when set to `true`. It defaults to `false`.

## Shoot Prometheus Observability

Federates Shoot etcd WAL fsync duration and database size metrics into platform
Prometheus when the operator flag is enabled, using HTTPS with basic authentication
(`/federate`, every 60 seconds).

Prerequisites:
- Prometheus Operator `ScrapeConfig` CRD installed on the platform cluster.
- Gardener `<shoot-name>.monitoring` Secret in the Shoot's project namespace.
- Platform Prometheus selects generated ScrapeConfigs in the Cluster namespaces
  and can reach Shoot Prometheus endpoints.

Set `ENABLE_SHOOT_PROMETHEUS_OBSERVABILITY=true` on the operator to enable
federation for all Clusters; it defaults to `false`. Generated ScrapeConfigs carry
the platform Prometheus selector label `open-control-plane.io/observability=enabled`.

The provider creates a ScrapeConfig and authentication Secret in each Cluster's
namespace when enabled. When disabled, it removes generated resources; deleting
the Cluster also garbage-collects them.

The `ShootObservability` condition reports successful configuration (`True`) or
configuration errors (`False`, with details in the condition message). Check
target health in platform Prometheus to verify actual metric ingestion.
