package cluster

import (
	"context"
	"fmt"

	"dario.cat/mergo"
	"github.com/Masterminds/semver/v3"
	maputils "github.com/openmcp-project/controller-utils/pkg/collections/maps"
	"github.com/openmcp-project/controller-utils/pkg/logging"

	clustersv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/clusters/v1alpha1"
	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	gardenconstants "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1/constants"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

// UpdateShootFields updates the shoot with the values from the profile.
// It tries to avoid invalid changes, such as downgrading the kubernetes version or removing required fields.
func UpdateShootFields(ctx context.Context, shoot *gardenv1beta1.Shoot, profile *shared.Profile, landscape *shared.Landscape, cluster *clustersv1alpha1.Cluster) error {
	log := logging.FromContextOrPanic(ctx).WithName("UpdateShootFields")
	tmpl := profile.ProviderConfig.Spec.ShootTemplate

	// annotations
	enforcedAnnotations := maputils.Merge(tmpl.Annotations, map[string]string{
		clustersv1alpha1.ProfileNameAnnotation:                                     profile.ProviderConfig.Name,
		clustersv1alpha1.EnvironmentAnnotation:                                     shared.Environment(),
		clustersv1alpha1.ProviderAnnotation:                                        shared.ProviderName(),
		"shoot.gardener.cloud/cleanup-extended-apis-finalize-grace-period-seconds": "30",
		gardenconstants.AnnotationAuthenticationIssuer:                             gardenconstants.AnnotationAuthenticationIssuerManaged,
	})
	existingAnnotations := shoot.GetAnnotations()
	if existingAnnotations == nil {
		shoot.SetAnnotations(enforcedAnnotations)
	} else {
		for k, v := range enforcedAnnotations {
			val, exists := existingAnnotations[k]
			if !exists || val != v {
				shoot.SetAnnotations(maputils.Merge(existingAnnotations, enforcedAnnotations))
				break
			}
		}
	}

	// labels
	enforcedLabels := maputils.Merge(tmpl.Labels, map[string]string{
		providerv1alpha1.ClusterReferenceLabelName:        cluster.Name,
		providerv1alpha1.ClusterReferenceLabelNamespace:   cluster.Namespace,
		providerv1alpha1.ClusterReferenceLabelProvider:    shared.ProviderName(),
		providerv1alpha1.ClusterReferenceLabelEnvironment: shared.Environment(),
	})
	if project, ok := cluster.Labels["openmcp.cloud/project"]; ok { // TODO: use constant
		enforcedLabels["openmcp.cloud/project"] = project
	}
	if workspace, ok := cluster.Labels["openmcp.cloud/workspace"]; ok { // TODO: use constant
		enforcedLabels["openmcp.cloud/workspace"] = workspace
	}
	existingLabels := shoot.GetLabels()
	if existingLabels == nil {
		shoot.SetLabels(enforcedLabels)
	} else {
		for k, v := range enforcedLabels {
			val, exists := existingLabels[k]
			if !exists || val != v {
				shoot.SetLabels(maputils.Merge(existingLabels, enforcedLabels))
				break
			}
		}
	}

	// compute new k8s version
	existingK8sVersion := shoot.Spec.Kubernetes.Version
	newK8sVersion := computeK8sVersion(tmpl.Spec.Kubernetes.Version, existingK8sVersion)
	if existingK8sVersion != newK8sVersion {
		log.Debug("Updating k8s version", "oldVersion", existingK8sVersion, "newVersion", newK8sVersion)
	} else {
		log.Debug("Keeping k8s version", "version", existingK8sVersion)
	}

	if err := mergo.Merge(&shoot.Spec, tmpl.Spec, mergo.WithOverride); err != nil {
		return fmt.Errorf("error merging shoot spec from template into shoot: %w", err)
	}
	shoot.Spec.Kubernetes.Version = newK8sVersion
	shoot.ObjectMeta.Annotations[clustersv1alpha1.K8sVersionAnnotation] = newK8sVersion

	return nil
}

// computeK8sVersion computes which k8s version should be rendered into the generated shoot manifest.
// It takes a k8s version which is configured and one which comes from an already existing shoot.
// The logic is as follows:
// - If both are empty, the result is empty.
// - If only one is empty, the result is the non-empty one.
// - If neither is empty, the higher one is returned. To not cause any unplanned shoot updates, a configured version without a patch number is considered to be less than an existing version with a patch number (that is otherwise identical).
func computeK8sVersion(configured, existing string) string {
	if existing == "" && configured != "" {
		return configured
	} else if existing != "" && configured != "" {
		configuredK8sVersion := semver.MustParse(configured)
		existingK8sVersion := semver.MustParse(existing)

		if configuredK8sVersion.GreaterThan(existingK8sVersion) {
			return configuredK8sVersion.Original()
		}

	}

	return existing
}
