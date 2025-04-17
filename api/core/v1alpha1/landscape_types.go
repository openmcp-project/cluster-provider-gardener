package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type LandscapeSpec struct {
	// Access holds the access information for this Gardener Landscape.
	Access GardenClusterAccess `json:"access"`
}

type GardenClusterAccess struct {
	// Inline holds an inline kubeconfig.
	// Only one of the fields in this struct may be set.
	// +optional
	Inline string `json:"inline,omitempty"`

	// SecretRef is a reference to a secret containing the kubeconfig.
	// Only one of the fields in this struct may be set.
	// +optional
	SecretRef *NamespacedObjectReference `json:"secretRef,omitempty"`
}

type LandscapeStatus struct {
	CommonStatus `json:",inline"`

	// Phase is the current phase of the cluster.
	Phase LandscapePhase `json:"phase"`

	// Projects lists the available projects.
	Projects []string `json:"projects,omitempty"`
}

type LandscapePhase string

const (
	LANDSCAPE_PHASE_AVAILABLE   LandscapePhase = "Available"
	LANDSCAPE_PHASE_UNAVAILABLE LandscapePhase = "Unavailable"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=gpls
// +kubebuilder:selectablefield:JSONPath=".status.phase"
// +kubebuilder:printcolumn:JSONPath=".status.phase",name="Phase",type=string
// +kubebuilder:printcolumn:JSONPath=".status.reason",name="Reason",type=string
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=platform"

type Landscape struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LandscapeSpec   `json:"spec,omitempty"`
	Status LandscapeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type LandscapeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Landscape `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Landscape{}, &LandscapeList{})
}
