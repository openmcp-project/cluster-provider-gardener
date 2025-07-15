# ClusterConfig

A [`ClusterConfig`](../../api/core/v1alpha1/clusterconfiguration_types.go) is a namespaced resource that can be used to provide additional configuration for a `Cluster` resource.

For a `ClusterConfig` to take effect, it has to be referenced by a `Cluster` resource that uses a profile which belongs to the Gardener ClusterProvider. Only `ClusterConfig` resources in the same namespace can be referenced.
```yaml
apiVersion: clusters.openmcp.cloud/v1alpha1
kind: Cluster
metadata:
  name: my-cluster
  namespace: clusters
spec:
  profile: default.gardener.gcp-workerless
  kubernetes:
    version: "1.32"
  purposes:
  - test
  tenancy: Exclusive
  clusterConfigRef:
    name: my-cluster
```

```yaml
apiVersion: gardener.clusters.openmcp.cloud/v1alpha1
kind: ClusterConfig
metadata:
  name: my-cluster
  namespace: clusters
spec:
  patchOptions: # optional
    ignoreMissingOnRemove: true
    createMissingOnAdd: true
  patches: # optional
  - op: add
    path: .spec.extensions[-1]
    value:
      type: "test-extension"
      providerConfig:
        foo: bar
      disabled: true
  - op: remove
    path: /spec/seedName
  - op: add
    path: /spec/seedName
    value: "test-seed"
```

## Spec

Via the `spec.patches` field of a `ClusterConfig`, it is possible to manipulate the generated `Shoot` manifest before it is sent to Gardener. The specified changes are applied after all default logic for generating the manifest has been executed.

`spec.patches` describes a [JSON patch](https://datatracker.ietf.org/doc/html/rfc6902), consisting of a list of operations (see the JSON patch documentation for a detailed list of available operations). The operations are executed in the given order.

The shoot's `metadata` and `spec` fields are available for modification.

There are a few additions to the standard JSON patch behavior:
- Negative indices for arrays are supported. They are counted from the end of the array.
- If `spec.patchOptions.createMissingOnAdd` is `true`, `add` operations will create missing parent fields. Otherwise, missing parent fields cause an error.
  - Defaults to `false` if not set.
- If `spec.patchOptions.ignoreMissingOnRemove` is `true`, `remove` operations that target non-existing paths will not throw an error and do nothing instead.
  - Defaults to `false` if not set.

### Path in JSON Patch Operations

The `path` argument for a JSON patch operation usually uses the [JSON pointer](https://datatracker.ietf.org/doc/html/rfc6901) notation. In this notation, a slash (`/`) marks the beginning of the path as well as the beginning of a new segment. Slashes in field names must be escaped by using `~1`, a tilde (`~`) must be written as `~0` instead. The notation does not differentiate between array indices and field names.

Example: Referencing the third finalizer would use the path `/metadata/finalizers/2`.

In addition to the described JSON pointer syntax, paths can also be expressed using a simplified variant of the [JSON path](https://datatracker.ietf.org/doc/html/rfc9535) syntax, which separates fields by using dots (`.`) and is more common in the k8s universe for path references. Note that while JSON path is a somewhat powerful query expression language, only plain references are allowed in this context.
Instead of dots, square brackets (`[]`) can also be used for referencing field names or array indices.
Backslashes (`\`) are used for escaping. Escaping is not required when the bracket notation is used in combination with quotes (either single `'` or double `"`).

Slash and tilde characters do not need to be escaped in JSON path notation.

As this notation is translated into the JSON pointer notation, there is no difference between field names and array indices here either.

Example: Referencing the third finalizer could be done by using any of the following expressions:
- `.metadata.finalizers.2`
- `.metadata.finalizers[2]`
- `metadata.finalizers[2]`
- `metadata["finalizers"][2]`

If the path starts with a `/`, it is assumed to be in JSON pointer notation and not converted. Otherwise, the JSON path syntax is assumed and it will be converted into JSON pointer notation.

See the documentation [here](https://github.com/openmcp-project/controller-utils/blob/main/docs/libs/jsonpatch.md#path-notation) for further information regarding the path syntax.

## ⚠️ Warning

Note that any of the shoot's fields (except for its `status`) can be modified using the `ClusterConfig` resource. This should be done very carefully, since there is a lot of potential for creating invalid changes to the shoot manifest.

This applies even more if a `ClusterConfig` is added to an already existing `Cluster` resource or if a referenced `ClusterConfig` is modified. Apart from many fields in the shoot manifest that Gardener considers immutable and will cause the shoot update to fail if modified, messing with the shoot's name or namespace could lead to a completely new shoot being created, leaking the existing one. Changes of these kind can only be done by creating the `ClusterConfig` before the corresponding `Cluster` is created and having the `Cluster` reference it directly from creation.

In any way, it is recommended to handle `ClusterConfig` resources with extreme caution.
