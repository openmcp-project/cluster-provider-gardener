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
)
