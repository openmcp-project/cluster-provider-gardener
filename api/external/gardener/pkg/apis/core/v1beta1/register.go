// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupName is the name of the core API group.
const GroupName = "core.gardener.cloud"

// SchemeGroupVersion is group version used to register these objects
var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1beta1"}

// Kind takes an unqualified kind and returns a Group qualified GroupKind.
func Kind(kind string) schema.GroupKind {
	return SchemeGroupVersion.WithKind(kind).GroupKind()
}

// Resource takes an unqualified resource and returns a Group qualified GroupResource.
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

var (
	// SchemeBuilder is a new Scheme Builder which registers our API.
	SchemeBuilder      = runtime.NewSchemeBuilder(addKnownTypes)
	localSchemeBuilder = &SchemeBuilder
	// AddToScheme is a reference to the Scheme Builder's AddToScheme function.
	AddToScheme = SchemeBuilder.AddToScheme
)

// Adds the list of known types to the given scheme.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&BackupBucket{},
		&BackupBucketList{},
		&BackupEntry{},
		&BackupEntryList{},
		&CloudProfile{},
		&CloudProfileList{},
		&ControllerRegistration{},
		&ControllerRegistrationList{},
		&ControllerDeployment{},
		&ControllerDeploymentList{},
		&ControllerInstallation{},
		&ControllerInstallationList{},
		&ExposureClass{},
		&ExposureClassList{},
		&InternalSecret{},
		&InternalSecretList{},
		&NamespacedCloudProfile{},
		&NamespacedCloudProfileList{},
		&Project{},
		&ProjectList{},
		&Quota{},
		&QuotaList{},
		&SecretBinding{},
		&SecretBindingList{},
		&Seed{},
		&SeedList{},
		&Shoot{},
		&ShootList{},
		&ShootState{},
		&ShootStateList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)

	return nil
}

func addDefaultingFuncs(scheme *runtime.Scheme) error {
	return nil
}
