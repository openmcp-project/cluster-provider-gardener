# Running the Gardener ClusterProvider

## Environment Variables

There are a few environment variables that are evaluated on startup and can be used to control the behavior of the ClusterProvider:

- `ACCESS_REQUEST_SERVICE_ACCOUNT_NAMESPACE` specifies the namespace on shoot clusters that is used to create a `ServiceAccount` for granting access to that shoot cluster. It defaults to `accessrequests`.

## Shoot Prometheus Observability

The Gardener ClusterProvider supports federating selected etcd metrics from Shoot
Prometheus instances into the platform's Prometheus. This includes WAL fsync
duration and total database size.

### Prerequisites

- The platform cluster has the Prometheus Operator `ScrapeConfig` CRD installed.
- Gardener provides a `<shoot-name>.monitoring` Secret in the Shoot's project
  namespace with monitoring credentials and a Prometheus endpoint.
- Platform Prometheus is configured to select ScrapeConfigs labeled
  `open-control-plane.io/observability=enabled` in the relevant Cluster namespaces
  and can reach the Shoot Prometheus endpoints.

### Enabling Observability

Add `open-control-plane.io/observability=enabled` to the `Cluster` resource on the
platform cluster:

```sh
kubectl label clusters.clusters.openmcp.cloud <cluster-name> \
  -n <cluster-namespace> open-control-plane.io/observability=enabled --overwrite
```

The provider copies the Gardener monitoring credentials into an authentication
Secret and creates a ScrapeConfig in the Cluster's namespace. The ScrapeConfig
uses HTTPS with basic authentication to federate the selected metrics through
the Shoot Prometheus `/federate` endpoint every 60 seconds.

To disable observability, remove the label. The provider removes the generated
ScrapeConfig and authentication Secret. Both resources are also owned by the
Cluster and are garbage-collected when it is deleted.

### Status

The Cluster's `ShootObservability` condition reports whether the provider has
successfully applied the desired configuration:

| Status | Reason | Meaning |
| --- | --- | --- |
| `True` | `ObservabilityEnabled` | The ScrapeConfig and authentication Secret are synchronized. |
| `True` | `ObservabilityDisabled` | Observability is disabled and generated resources are removed. |
| `False` | Error-specific reason | Configuration failed; see the condition message for details. |

This condition describes resource configuration, not metric ingestion. Check
the target's health in platform Prometheus to verify that scraping succeeds.
