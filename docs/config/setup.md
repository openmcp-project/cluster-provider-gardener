# Running the Gardener ClusterProvider

## Environment Variables

There are a few environment variables that are evaluated on startup and can be used to control the behavior of the ClusterProvider:

- `PROVIDER_NAME` can be overwritten to specify the provider name. It defaults to `gardener` if not set.
  - Must be a k8s name compatible string.
  - The provider name is used as an identifier to avoid conflicts between multiple Gardener ClusterProviders working on the same k8s cluster. Its value doesn't really matter, as long as only one instance of the ClusterProvider is working on the cluster. If multipel instances are working on the same cluster, they must all have unique provider names to avoid conflicts on reconciled resources.
- `ACCESS_REQUEST_SERVICE_ACCOUNT_NAMESPACE` specifies the namespace on shoot clusters that is used to create a `ServiceAccount` for granting access to that shoot cluster. It defaults to `accessrequests`.
