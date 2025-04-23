package constants

const (
	// ReasonOnboardingClusterInteractionProblem is used when the onboarding cluster cannot be reached.
	ReasonOnboardingClusterInteractionProblem = "OnboardingClusterInteractionProblem"
	// ReasonPlatformClusterInteractionProblem is used when the platform cluster cannot be reached.
	ReasonPlatformClusterInteractionProblem = "PlatformClusterInteractionProblem"
	// ReasonGardenClusterInteractionProblem is used when the garden cluster cannot be reached.
	ReasonGardenClusterInteractionProblem = "GardenClusterInteractionProblem"
	// ReasonKubeconfigError indicates that the rest config could not be created.
	ReasonKubeconfigError = "KubeconfigError"
	// ReasonInvalidReference indicates that a reference points to a non-existing or otherwise wrong resource.
	ReasonInvalidReference = "InvalidReference"
	// ReasonOperatingSystemProblem indicates a problem with the OS, e.g. an error while reading or writing a file.
	ReasonOperatingSystemProblem = "OperatingSystemProblem"
	// ReasonConfigurationProblem indicates that something is configured incorrectly.
	ReasonConfigurationProblem = "ConfigurationProblem"
	// ReasonWaitingForDeletion indicates that the resource is waiting for deletion.
	ReasonWaitingForDeletion = "WaitingForDeletion"
	// ReasonUnknownLandscape indicates that a referenced landscape has not been found.
	ReasonUnknownLandscape = "UnknownLandscape"
	// ReasonInvalidProject means that the specified project is not manageable by the referenced landscape.
	ReasonInvalidProject = "InvalidProject"
	// ReasonUnknownCloudProfile indicates that a referenced cloud profile has not been found.
	ReasonUnknownCloudProfile = "UnknownCloudProfile"
)
