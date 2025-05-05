package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterProfileSpec defines the desired state of Provider.
type ClusterProfileSpec struct {
	// Environment is the environment in which the ClusterProvider resides.
	Environment string `json:"environment"`

	// ProviderRef is a reference to the ClusterProvider
	ProviderRef ObjectReference `json:"providerRef"`

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
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=onboarding"
// +kubebuilder:selectablefield:JSONPath=".spec.environment"
// +kubebuilder:selectablefield:JSONPath=".spec.providerRef.name"
// +kubebuilder:selectablefield:JSONPath=".spec.providerConfigRef.name"
// +kubebuilder:printcolumn:JSONPath=".spec.environment",name="Env",type=string
// +kubebuilder:printcolumn:JSONPath=".spec.providerRef.name",name="Provider",type=string
// +kubebuilder:printcolumn:JSONPath=".spec.providerConfigRef.name",name="Config",type=string

type ClusterProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ClusterProfileSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

type ClusterProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterProfile{}, &ClusterProfileList{})
}
