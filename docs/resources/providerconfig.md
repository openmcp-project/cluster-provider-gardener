# ProviderConfig

A [`ProviderConfig`](../../api/core/v1alpha1/providerconfiguration_types.go) contains all necessary information for the Gardener ClusterProvider to manage `Shoot` resources. The idea is really simple - it mostly contains a shoot template, which is used for `Shoot` resources whenever a `Cluster` references the profile belonging to the `ProviderConfig`.

Each `ProviderConfig` results in a single `ClusterProfile`, whose name will be `<environment>.<provider-name>.<providerconfig-name>`, e.g. something like `default.gardener.gcp-workerless`.

```yaml
apiVersion: gardener.clusters.openmcp.cloud/v1alpha1
kind: ProviderConfig
metadata:
  name: gcp-workerless
spec:
  landscapeRef:
    name: canary
  project: mcptest
  providerRef:
    name: gardener
  enforcedShootExtensions: # optional
  - type: shoot-oidc-service
  shootTemplate:
    # beginning of shoot template
    # see Gardener documentation for further information
    spec:
      cloudProfile:
        kind: CloudProfile
        name: gcp
      kubernetes:
        version: 1.32.2
      maintenance:
        autoUpdate:
          kubernetesVersion: true
        timeWindow:
          begin: 220000+0200
          end: 230000+0200
      provider:
        type: gcp
      purpose: evaluation
      region: europe-west1
    # end of shoot template
status:
  lastReconcileTime: "2025-05-21T12:30:05Z"
  observedGeneration: 1
  phase: Available
```

## Spec

- `landscapeRef` references the [`Landscape`](./landscape.md) that should be used for `Cluster`s that use the profile from this `ProviderConfig`. At the moment, only a `name` may be specified.
- `project` is the Gardener project that should be used for `Shoot` clusters. It must be among the projects listed in the `Landscape`'s status - meaning the credentials from the `Landscape` resource need to have sufficient permissions for the project to manage `Shoot`s in it - otherwise the controller will show a corresponding error in the status.
- `providerRef` references the provider instance that is responsible for this resource. Only `name` may be specified for now.
  - This is only relevant if multiple instances of the Gardener ClusterProvider are running on the same cluster. The value is still required, though.
- `enforcedShootExtensions` allows to specify a list of Gardener shoot extensions that should always be present in the generated `Shoot` manifests.
  - To ensure backward compatibility, not specifying the field results in the `shoot-oidc-service` extension being enforced.
  - Setting the field to `[]` enforces no extensions at all, also disabling the `shoot-oidc-service` extension.
  - If the list has items, exactly those will be enforced as shoot extensions (including the `shoot-oidc-service` only if specified in the list).
    - For extensions with configuration, the ClusterProvider will restore the configuration from this field if the extension exists on the shoot with a different one.
- `shootTemplate` holds a template for a `Shoot` resource. It will be used to create new `Shoot` resource and reconcile existing ones.
  - Please take a look at the [Gardener documentation](https://gardener.cloud/docs/) for further information about the available fields.
  - Note that many fields in a `Shoot` spec are either immutable or otherwise restricted in how they can be updated. The kubernetes version cannot be downgraded, for example. Modifying the template in an existing `ProviderConfig` in an incompatible way will either result in the changes not taking effect, or in the corresponding `Cluster` resources being stuck in an error state.
