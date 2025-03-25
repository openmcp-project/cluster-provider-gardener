package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterProviderSpec defines the desired state of Provider.
type ClusterProviderSpec struct {
	// Image is the name of the image that contains the provider.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// ImagePullSecrets are named secrets in the same namespace that can be used
	// to fetch provider images from private registries.
	ImagePullSecrets []ObjectReference `json:"imagePullSecrets,omitempty"`
}

// ClusterProviderStatus defines the observed state of Provider.
type ClusterProviderStatus struct {
	CommonStatus `json:",inline"`

	// Phase is the current phase of the provider.
	Phase string `json:"phase"` // TODO: use phase type?

	// Profiles contains information about the available profiles.
	// +optional
	Profiles []ClusterProfile `json:"profiles"`
}

type ClusterProfile struct {
	// Name is the name of the profile.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// ProviderConfigRef is a reference to the provider-specific configuration.
	ProviderConfigRef ObjectReference `json:"providerConfigRef"`

	// SupportedVersions are the supported Kubernetes versions.
	SupportedVersions []SupportedK8sVersion `json:"supportedVersions"`
}

type SupportedK8sVersion struct {
	// Version is the Kubernetes version.
	// +kubebuilder:validation:MinLength=5
	Version string `json:"version"`

	// Deprecated indicates whether this version is deprecated.
	Deprecated bool `json:"deprecated,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// ClusterProvider is the Schema for the providers API.
type ClusterProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterProviderSpec   `json:"spec,omitempty"`
	Status ClusterProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterProviderList contains a list of Provider.
type ClusterProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterProvider{}, &ClusterProviderList{})
}

type ClusterProviderRef struct {
	// Name of the referenced ClusterProvider.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Profile is the profile of the referenced ClusterProvider that is used.
	// +kubebuilder:validation:MinLength=1
	Profile string `json:"profile"`
}
