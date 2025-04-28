package v1alpha1

const (
	// LandscapeFinalizer is the finalizer that is used by the Landscape controller on Landscape resources.
	LandscapeFinalizer = GroupName + "/landscape"
	// ProviderConfigFinalizer is the finalizer that is used by the ProviderConfig controller on ProviderConfig resources.
	ProviderConfigFinalizer = GroupName + "/providerconfig"
	// ClusterFinalizer is the finalizer that is used by the Cluster controller on Cluster resources.
	ClusterFinalizer = GroupName + "/cluster"

	// ClusterReferenceLabelName is the label on the shoot that holds the name of the Cluster resource that created it.
	ClusterReferenceLabelName = GroupName + "/cluster-name"
	// ClusterReferenceLabelNamespace is the label on the shoot that holds the namespace of the Cluster resource that created it.
	ClusterReferenceLabelNamespace = GroupName + "/cluster-namespace"
	// ClusterReferenceLabelProvider is the label on the shoot that holds the name of the provider that is responsible for the Cluster resource that created it.
	ClusterReferenceLabelProvider = GroupName + "/provider-name"
	// ClusterReferenceLabelEnvironment is the label on the shoot that holds the name of the environment that the responsible provider is in.
	ClusterReferenceLabelEnvironment = GroupName + "/environment"
)
