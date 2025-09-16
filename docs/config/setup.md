# Running the Gardener ClusterProvider

## Environment Variables

There are a few environment variables that are evaluated on startup and can be used to control the behavior of the ClusterProvider:

- `ACCESS_REQUEST_SERVICE_ACCOUNT_NAMESPACE` specifies the namespace on shoot clusters that is used to create a `ServiceAccount` for granting access to that shoot cluster. It defaults to `accessrequests`.
