[![REUSE status](https://api.reuse.software/badge/github.com/openmcp-project/cluster-provider-gardener)](https://api.reuse.software/info/github.com/openmcp-project/cluster-provider-gardener)

# ClusterProvider: Gardener

## About this project

ClusterProvider Gardener is a `ClusterProvider` as described in [the OpenMCP Architecture Docs](https://github.com/openmcp-project/docs/blob/main/architecture/general/open-mcp-landscape-overview.md). It implements our [Cluster API](https://github.com/openmcp-project/docs/blob/main/adrs/cluster-api.md).

It is a k8s contorller which manages [Gardener](https://gardener.cloud/) shoot clusters based on `Cluster` resources and can grant access to these shoot clusters based on `ClusterRequest` resources.

## Requirements and Setup

In combination with the [openMCP Operator](https://github.com/openmcp-project/openmcp-operator), this controller can be deployed via a simple k8s resource:
```yaml
apiVersion: openmcp.cloud/v1alpha1
kind: ClusterProvider
metadata:
  name: gardener
spec:
  image: "ghcr.io/openmcp-project/images/cluster-provider-gardener:v0.2.0"
```

To run it locally, run
```shell
go run ./cmd/cluster-provider-gardener/main.go init --environment default --kubeconfig path/to/kubeconfig
```
to deploy the CRDs that are required for the controller and then
```shell
go run ./cmd/cluster-provider-gardener/main.go run --environment default --kubeconfig path/to/kubeconfig
```

See the [documentation](docs/README.md) for further details regarding resources and configuration.

## Support, Feedback, Contributing

This project is open to feature requests/suggestions, bug reports etc. via [GitHub issues](https://github.com/openmcp-project/cluster-provider-gardener/issues). Contribution and feedback are encouraged and always welcome. For more information about how to contribute, the project structure, as well as additional contribution information, see our [Contribution Guidelines](CONTRIBUTING.md).

## Security / Disclosure
If you find any bug that may be a security problem, please follow our instructions at [in our security policy](https://github.com/openmcp-project/cluster-provider-gardener/security/policy) on how to report it. Please do not create GitHub issues for security-related doubts or problems.

## Code of Conduct

We as members, contributors, and leaders pledge to make participation in our community a harassment-free experience for everyone. By participating in this project, you agree to abide by its [Code of Conduct](https://github.com/SAP/.github/blob/main/CODE_OF_CONDUCT.md) at all times.

## Licensing

Copyright 2025 SAP SE or an SAP affiliate company and cluster-provider-gardener contributors. Please see our [LICENSE](LICENSE) for copyright and license information. Detailed information including third-party components and their licensing/copyright information is available [via the REUSE tool](https://api.reuse.software/info/github.com/openmcp-project/cluster-provider-gardener).
