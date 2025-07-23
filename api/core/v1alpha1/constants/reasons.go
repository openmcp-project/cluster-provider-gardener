package constants

const (
	// ReasonGardenClusterInteractionProblem is used when the garden cluster cannot be reached.
	ReasonGardenClusterInteractionProblem = "GardenClusterInteractionProblem"
	// ReasonShootClusterInteractionProblem is used when the shoot cluster cannot be reached.
	ReasonShootClusterInteractionProblem = "GardenClusterInteractionProblem"
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
	// ReasonUnknownProfile indicates that a referenced profile has not been found.
	ReasonUnknownProfile = "UnknownProfile"
	// ReasonInternalError indicates that something went wrong internally.
	ReasonInternalError = "InternalError"
)

const (
	ReasonProjectAvailable = "ProjectAvailable"
)
