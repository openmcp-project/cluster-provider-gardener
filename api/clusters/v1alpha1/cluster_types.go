package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ClusterSpec defines the desired state of Cluster
type ClusterSpec struct {
	// ClusterProfileRef is a reference to the cluster provider.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clusterProfileRef is immutable"
	ClusterProfileRef ObjectReference `json:"clusterProfileRef"`

	// ClusterConfigRef is a reference to a cluster configuration.
	// +optional
	ClusterConfigRef *ClusterConfigRef `json:"clusterConfigRef,omitempty"`

	// Kubernetes configuration for the cluster.
	Kubernetes K8sConfiguration `json:"kubernetes"`

	// Purposes lists the purposes this cluster is intended for.
	Purposes []string `json:"purposes"`

	// Tenancy is the tenancy model of the cluster.
	// +kubebuilder:validation:Enum=exclusive;shared
	Tenancy Tenancy `json:"tenancy"`
}

// ClusterConfigRef is a reference to a cluster configuration.
type ClusterConfigRef struct {
	// APIGroup is the group for the resource being referenced.
	// +kubebuilder:validation:MinLength=1
	APIGroup string `json:"apiGroup"`
	// Kind is the kind of the resource being referenced.
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`
	// Name is the name of the resource being referenced.
	// Defaults to the name of the referencing resource, if not specified.
	// +optional
	Name string `json:"name,omitempty"`
}

type K8sConfiguration struct {
	// Version is the k8s version of the cluster.
	Version string `json:"version"`
}

// ClusterStatus defines the observed state of Cluster
type ClusterStatus struct {
	CommonStatus `json:",inline"`

	// Phase is the current phase of the cluster.
	Phase ClusterPhase `json:"phase"`

	// ProviderStatus is the provider-specific status of the cluster.
	// x-kubernetes-preserve-unknown-fields: true
	// +optional
	ProviderStatus *runtime.RawExtension `json:"providerStatus,omitempty"`
}

type ClusterPhase string

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=onboarding"

// Cluster is the Schema for the clusters API
type Cluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterSpec   `json:"spec,omitempty"`
	Status ClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterList contains a list of Cluster
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Cluster{}, &ClusterList{})
}
