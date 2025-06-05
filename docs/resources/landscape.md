# Landscape

A [`Landscape`](../../api/core/v1alpha1/landscape_types.go) is a cluster-scoped custom resource that represents a Gardener landscape.

The Gardener ClusterProvider watches `Shoot` resources on all Gardener landscapes that have corresponding `Landscape` resources.

```yaml
apiVersion: gardener.clusters.openmcp.cloud/v1alpha1
  kind: Landscape
  metadata:
    name: canary
  spec:
    access:
      # either inline or secretRef must be specified
      inline: |
        apiVersion: v1
        kind: Config
      secretRef:
        name: foo
        namespace: bar
  status:
    apiServer: https://api.gardener.openmcp.cloud
    conditions:
    - lastTransitionTime: "2025-05-21T12:30:05Z"
      status: "True"
      type: Project_mcptest
    lastReconcileTime: "2025-05-21T12:30:05Z"
    observedGeneration: 1
    phase: Available
    projects:
    - name: mcptest
      namespace: garden-mcptest
```

## Spec

At the moment, its spec does not contain anything except a kubeconfig for the landscape's 'garden' cluster, either as an inline string or in form of a secret reference.
Note that the secret is not watched and the `Landscape` resource needs to be annotated with `openmcp.cloud/operation: reconcile` for the controller to take notice of it.

## Status

When a `Landscape` is reconciled, the ClusterProvider uses [SelfSubjectRulesReview](https://kubernetes.io/docs/reference/kubernetes-api/authorization-resources/self-subject-rules-review-v1/) to figure out which projects the specified kubeconfig has access to. This is shown in the conditions as well as in `status.projects`. Only projects with `Admin` privileges are listed, as this is needed to create and manage `Shoot` resources.

It is also possible to create multiple `Landscape` resources which point to the same Gardener landscape, but have permissions for different projects.

Since the ClusterProvider watches `Shoot` resources in all Gardener projects it has access to, it is recommended to not grant more permissions than necessary to the given kubeconfig.
