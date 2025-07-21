package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	jpapi "github.com/openmcp-project/controller-utils/api/jsonpatch"

	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
)

type ClusterConfigSpec struct {
	// PatchOptions allows to configure how the patches are applied to the shoot manifest.
	PatchOptions *PatchOptions `json:"patchOptions,omitempty"`
	// Patches contains a list of JSON patches that are applied to the shoot manifest before it is sent to the Gardener API server.
	// These patches are applied to the shoot manifest in the order they are defined.
	Patches jpapi.JSONPatches `json:"patches,omitempty"`

	// Extensions is a list of Gardener extensions that are to be ensured on the shoot.
	Extensions []gardenv1beta1.Extension `json:"extensions,omitempty"`

	// Resources is a list of resource references that are to be ensured on the shoot.
	Resources []gardenv1beta1.NamedResourceReference `json:"resources,omitempty"`
}

type PatchOptions struct {
	// IgnoreMissingOnRemove instructs the patch to ignore missing fields when removing them.
	IgnoreMissingOnRemove *bool `json:"ignoreMissingOnRemove,omitempty"`
	// CreateMissingOnAdd instructs the patch to create missing parent fields on add operations.
	CreateMissingOnAdd *bool `json:"createMissingOnAdd,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=gccfg
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=platform"

type ClusterConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ClusterConfigSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

type ClusterConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterConfig{}, &ClusterConfigList{})
}
