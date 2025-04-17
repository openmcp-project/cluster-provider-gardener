package v1alpha1

const (
	// LandscapeFinalizer is the finalizer that is used by the Landscape controller on Landscape resources.
	LandscapeFinalizer = "landscape." + GroupName
	// ProviderConfigLandscapeFinalizerPrefix is the prefix for the finalizers that the ProviderConfig controller uses to mark Landscape resources as used by a ProviderConfig.
	ProviderConfigLandscapeFinalizerPrefix = "pc." + GroupName + "/"
)
