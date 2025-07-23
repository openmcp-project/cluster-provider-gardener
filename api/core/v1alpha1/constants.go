package v1alpha1

const (
	// LandscapeFinalizer is the finalizer that is used by the Landscape controller on Landscape resources.
	LandscapeFinalizer = GroupName + "/landscape"
	// ProviderConfigFinalizer is the finalizer that is used by the ProviderConfig controller on ProviderConfig resources.
	ProviderConfigFinalizer = GroupName + "/providerconfig"
	// ClusterFinalizer is the finalizer that is used by the Cluster controller on Cluster resources.
	ClusterFinalizer = GroupName + "/cluster"
	// AccessRequestFinalizer is the finalizer that is used by the AccessRequest controller on AccessRequest resources.
	AccessRequestFinalizer = GroupName + "/accessrequest"

	// ManagedByNameLabel is used to mark resources that are managed by the Gardener ClusterProvider.
	ManagedByNameLabel = GroupName + "/managed-by-name"
	// ManagedByNamespaceLabel is used to mark resources that are managed by the Gardener ClusterProvider.
	ManagedByNamespaceLabel = GroupName + "/managed-by-namespace"

	// ClusterReferenceLabelName is the label on the shoot that holds the name of the Cluster resource that created it.
	ClusterReferenceLabelName = GroupName + "/cluster-name"
	// ClusterReferenceLabelNamespace is the label on the shoot that holds the namespace of the Cluster resource that created it.
	ClusterReferenceLabelNamespace = GroupName + "/cluster-namespace"
	// ClusterReferenceLabelProvider is the label on the shoot that holds the name of the provider that is responsible for the Cluster resource that created it.
	ClusterReferenceLabelProvider = GroupName + "/provider-name"
	// ClusterReferenceLabelEnvironment is the label on the shoot that holds the name of the environment that the responsible provider is in.
	ClusterReferenceLabelEnvironment = GroupName + "/environment"
)

const (
	ConditionMeta                = "Meta"
	ConditionLandscapeManagement = "LandscapeManagement"

	AccessRequestConditionFoundClusterAndProfile = "FoundClusterAndProfile"
	AccessRequestConditionSecretExistsAndIsValid = "SecretExistsAndIsValid"
	AccessRequestConditionShootAccess            = "ShootAccess"
	AccessRequestConditionCleanup                = "Cleanup"

	ClusterConditionShootManagement = "ShootManagement"

	ProviderConfigConditionCloudProfile             = "CloudProfile"
	ProviderConfigConditionClusterProfileManagement = "ClusterProfileManagement"

	LandscapeConditionGardenClusterAccess = "GardenClusterAccess"
)
