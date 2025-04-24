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

	// Profiles lists the available profiles.
	// The profile itself is a separate resource.
	// +optional
	Profiles []string `json:"profiles"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=platform"

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
