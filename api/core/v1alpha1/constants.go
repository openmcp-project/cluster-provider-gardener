package v1alpha1

const (
	// LandscapeFinalizer is the finalizer that is used by the Landscape controller on Landscape resources.
	LandscapeFinalizer = "landscape." + GroupName
	// ProviderConfigFinalizer is the finalizer that is used by the ProviderConfig controller on ProviderConfig resources.
	ProviderConfigFinalizer = "providerconfig." + GroupName
)
