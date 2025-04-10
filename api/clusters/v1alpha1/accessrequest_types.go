package v1alpha1

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type AccessRequestSpec struct {
	// ClusterRef is the reference to the Cluster for which access is requested.
	// Exactly one of clusterRef or requestRef must be set.
	// This value is immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clusterRef is immutable"
	ClusterRef NamespacedObjectReference `json:"clusterRef"`

	// Permissions are the requested permissions.
	Permissions []PermissionsRequest `json:"permissions"`
}

type PermissionsRequest struct {
	// Namespace is the namespace for which the permissions are requested.
	// If empty, this will result in a ClusterRole, otherwise in a Role in the respective namespace.
	// Note that for a Role, the namespace needs to either exist or a permission to create it must be included in the requested permissions (it will be created automatically then), otherwise the request will be rejected.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Rules are the requested RBAC rules.
	Rules []rbacv1.PolicyRule `json:"rules"`
}

// AccessRequestStatus defines the observed state of AccessRequest
type AccessRequestStatus struct {
	CommonStatus `json:",inline"`

	// Phase is the current phase of the request.
	Phase RequestPhase `json:"phase"`

	// TODO: expose actual access information
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=onboarding"

// AccessRequest is the Schema for the accessrequests API
type AccessRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AccessRequestSpec   `json:"spec,omitempty"`
	Status AccessRequestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AccessRequestList contains a list of AccessRequest
type AccessRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccessRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AccessRequest{}, &AccessRequestList{})
}
