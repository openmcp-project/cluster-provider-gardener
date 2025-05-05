package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	gardenconstants "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1/constants"
)

type ProviderConfigSpec struct {
	// ProviderRef is a reference to the provider this configuration belongs to.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="providerRef is immutable"
	ProviderRef ObjectReference `json:"providerRef"`

	// LandscapeRef is a reference to the Landscape resource this configuration belongs to.
	LandscapeRef ObjectReference `json:"landscapeRef"`

	// Project is the Gardener project which should be used to create shoot clusters in it.
	// The provided kubeconfig must have priviliges for this project.
	// +kubebuilder:validation:MinLength=1
	Project string `json:"project"`

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
	PROVIDER_CONFIG_PHASE_AVAILABLE           ProviderConfigPhase = "Available"
	PROVIDER_CONFIG_PHASE_UNAVAILABLE         ProviderConfigPhase = "Unavailable"
	PROVIDER_CONFIG_PHASE_PARTIALLY_AVAILABLE ProviderConfigPhase = "Partially Available"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=gpcfg
// +kubebuilder:printcolumn:JSONPath=".status.phase",name="Phase",type=string
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

// CloudProfile returns the name of the Gardener CloudProfile that is referenced in the shoot template.
// Can only handle cluster-scoped CloudProfiles at the moment.
func (gpcfg *ProviderConfig) CloudProfile() string {
	if gpcfg == nil {
		return ""
	}
	if gpcfg.Spec.ShootTemplate.Spec.CloudProfile != nil {
		if gpcfg.Spec.ShootTemplate.Spec.CloudProfile.Kind != "" && gpcfg.Spec.ShootTemplate.Spec.CloudProfile.Kind != gardenconstants.CloudProfileReferenceKindCloudProfile {
			return "" // we can only handle standard CloudProfiles at the moment
		}
		return gpcfg.Spec.ShootTemplate.Spec.CloudProfile.Name
	} else if gpcfg.Spec.ShootTemplate.Spec.CloudProfileName != nil {
		return *gpcfg.Spec.ShootTemplate.Spec.CloudProfileName
	}
	return ""
}
