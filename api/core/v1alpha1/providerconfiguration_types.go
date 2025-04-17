package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
)

type ProviderConfigSpec struct {
	// ProviderRef is a reference to the provider this configuration belongs to.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="providerRef is immutable"
	ProviderRef ObjectReference `json:"providerRef"`

	// Configurations is a list of Gardener configurations.
	Configurations []GardenerConfiguration `json:"configs,omitempty"`
}

// GardenerConfiguration contains configuration for a Gardener.
type GardenerConfiguration struct {
	// Name is the name of this Gardener configuration.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// LandscapeRef is a reference to the Landscape resource this configuration belongs to.
	LandscapeRef ObjectReference `json:"landscapeRef"`

	// Project is the Gardener project which should be used to create shoot clusters in it.
	// The provided kubeconfig must have priviliges for this project.
	// +kubebuilder:validation:MinLength=1
	Project string `json:"project"`

	// CloudProfile is the name of the Gardener CloudProfile that should be used for this shoot.
	// +kubebuilder:validation:MinLength=1
	CloudProfile string `json:"cloudProfile"`

	// ShootTemplate contains the shoot template for this configuration.
	ShootTemplate gardenv1beta1.ShootTemplate `json:"shootTemplate"`
}

type ProviderConfigStatus struct {
	CommonStatus `json:",inline"`

	// Phase is the current phase of the cluster.
	Phase ProviderConfigPhase `json:"phase"`
}

type ProviderConfigPhase string

const (
	PROVIDER_CONFIG_PHASE_SUCCEEDED ProviderConfigPhase = "Succeeded"
	PROVIDER_CONFIG_PHASE_FAILED    ProviderConfigPhase = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=gpcfg
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=platform"

type ProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProviderConfigSpec   `json:"spec,omitempty"`
	Status ProviderConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ProviderConfig{}, &ProviderConfigList{})
}
