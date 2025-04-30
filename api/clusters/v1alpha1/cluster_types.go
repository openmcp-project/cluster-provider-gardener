package v1alpha1

import (
	"encoding/json"

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

const (
	// CLUSTER_PHASE_UNKNOWN represents an unknown status for the cluster.
	CLUSTER_PHASE_UNKNOWN ClusterPhase = "Unknown"
	// CLUSTER_PHASE_READY represents a cluster that is ready.
	CLUSTER_PHASE_READY ClusterPhase = "Ready"
	// CLUSTER_PHASE_NOT_READY represents a cluster that is not ready.
	CLUSTER_PHASE_NOT_READY ClusterPhase = "Not Ready"
	// CLUSTER_PHASE_ERROR represents a cluster that could not be reconciled successfully.
	CLUSTER_PHASE_ERROR ClusterPhase = "Error"
	// CLUSTER_PHASE_DELETING represents a cluster that is being deleted.
	CLUSTER_PHASE_DELETING ClusterPhase = "In Deletion"
	// CLUSTER_PHASE_DELETING_ERROR represents a cluster that could not be reconciled successfully while being in deletion.
	CLUSTER_PHASE_DELETING_ERROR ClusterPhase = "Error In Deletion"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=onboarding"
// +kubebuilder:selectablefield:JSONPath=".spec.clusterProfileRef.name"
// +kubebuilder:printcolumn:JSONPath=".spec.purposes",name="Purposes",type=string
// +kubebuilder:printcolumn:JSONPath=`.status.phase`,name="Phase",type=string
// +kubebuilder:printcolumn:JSONPath=`.metadata.annotations["clusters.openmcp.cloud/k8sversion"]`,name="Version",type=string
// +kubebuilder:printcolumn:JSONPath=`.metadata.annotations["clusters.openmcp.cloud/profile"]`,name="Profile",type=string
// +kubebuilder:printcolumn:JSONPath=`.metadata.labels["environment.clusters.openmcp.cloud"]`,name="Env",type=string,priority=10
// +kubebuilder:printcolumn:JSONPath=`.metadata.labels["provider.clusters.openmcp.cloud"]`,name="Provider",type=string, priority=10
// +kubebuilder:printcolumn:JSONPath=".spec.clusterProfileRef.name",name="ProfileRef",type=string,priority=10
// +kubebuilder:printcolumn:JSONPath=`.metadata.annotations["clusters.openmcp.cloud/providerinfo"]`,name="Info",type=string,priority=10
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

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

// GetProviderStatus tries to unmarshal the provider status into the given variable.
func (cs *ClusterStatus) GetProviderStatus(into any) error {
	return json.Unmarshal(cs.ProviderStatus.Raw, into)
}

// SetProviderStatus marshals the given variable into the provider status.
func (cs *ClusterStatus) SetProviderStatus(from any) error {
	b, err := json.Marshal(from)
	if err != nil {
		return err
	}
	cs.ProviderStatus = &runtime.RawExtension{Raw: b}
	return nil
}
