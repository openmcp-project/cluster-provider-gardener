package cluster

import (
	"context"
	"fmt"

	"dario.cat/mergo"
	"github.com/Masterminds/semver/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	maputils "github.com/openmcp-project/controller-utils/pkg/collections/maps"
	errutils "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/controller-utils/pkg/jsonpatch"
	"github.com/openmcp-project/controller-utils/pkg/logging"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	clusterconst "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1/constants"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	cconst "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1/constants"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	gardenconst "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1/constants"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

const GardenerOIDCExtensionType = "shoot-oidc-service"

func GetShoot(ctx context.Context, landscapeClient client.Client, projectNamespace string, c *clustersv1alpha1.Cluster) (*gardenv1beta1.Shoot, errutils.ReasonableError) {
	log := logging.FromContextOrPanic(ctx)
	// check if shoot already exists
	shoot := &gardenv1beta1.Shoot{}
	exists := false
	// first, look into the status of the Cluster resource
	if c.Status.ProviderStatus != nil {
		cs := &providerv1alpha1.ClusterStatus{}
		if err := c.Status.GetProviderStatus(cs); err != nil {
			return nil, errutils.WithReason(fmt.Errorf("error unmarshalling provider status: %w", err), clusterconst.ReasonInternalError)
		}
		log.Debug("Provider status found, checking for shoot manifest")
		if cs.Shoot != nil {
			log.Debug("Found shoot in provider status", "shootName", cs.Shoot.GetName(), "shootNamespace", cs.Shoot.GetNamespace())
			shoot.SetName(cs.Shoot.GetName())
			shoot.SetNamespace(cs.Shoot.GetNamespace())
			if err := landscapeClient.Get(ctx, client.ObjectKeyFromObject(shoot), shoot); err != nil {
				if apierrors.IsNotFound(err) {
					log.Info("Found shoot reference in provider status, but shoot does not exist", "shootName", shoot.Name, "shootNamespace", shoot.Namespace)
				} else {
					return nil, errutils.WithReason(fmt.Errorf("error getting shoot '%s' in namespace '%s': %w", shoot.Name, shoot.Namespace, err), cconst.ReasonGardenClusterInteractionProblem)
				}
			} else {
				log.Info("Found shoot from reference in provider status", "shootName", shoot.Name, "shootNamespace", shoot.Namespace)
				exists = true
			}
		} else {
			log.Debug("No shoot found in provider status")
		}
	}
	if shoot.Name == "" {
		// search for shoot with fitting labels in project
		log.Debug("Shoot name and namespace could not be recovered from provider status, checking shoots in project namespace for fitting cluster reference labels", "projectNamespace", projectNamespace)
		shoots := &gardenv1beta1.ShootList{}
		if err := landscapeClient.List(ctx, shoots, client.InNamespace(projectNamespace), client.MatchingLabels{
			providerv1alpha1.ClusterReferenceLabelName:      c.Name,
			providerv1alpha1.ClusterReferenceLabelNamespace: c.Namespace,
		}); err != nil {
			return nil, errutils.WithReason(fmt.Errorf("error listing shoots in namespace '%s': %w", projectNamespace, err), cconst.ReasonGardenClusterInteractionProblem)
		}
		if len(shoots.Items) > 1 {
			return nil, errutils.WithReason(fmt.Errorf("found multiple shoots referencing cluster '%s'/'%s' in namespace '%s', there should never be more than one", c.Namespace, c.Name, projectNamespace), clusterconst.ReasonInternalError)
		}
		if len(shoots.Items) == 1 {
			shoot = &shoots.Items[0]
			log.Info("Found shoot from cluster reference labels", "shootName", shoot.Name, "shootNamespace", shoot.Namespace)
			exists = true
		} else {
			log.Info("No shoot found from cluster reference labels", "namespace", projectNamespace)
		}
	}
	if !exists {
		shoot = nil
	}
	return shoot, nil
}

// UpdateShootFields updates the shoot with the values from the profile.
// It tries to avoid invalid changes, such as downgrading the kubernetes version or removing required fields.
func UpdateShootFields(ctx context.Context, shoot *gardenv1beta1.Shoot, profile *shared.Profile, cluster *clustersv1alpha1.Cluster, clusterConfig *providerv1alpha1.ClusterConfig) error {
	log := logging.FromContextOrPanic(ctx).WithName("UpdateShootFields")
	tmpl := profile.ProviderConfig.Spec.ShootTemplate
	oldShoot := shoot.DeepCopy()

	// annotations
	enforcedAnnotations := maputils.Merge(tmpl.Annotations, map[string]string{
		"shoot.gardener.cloud/cleanup-extended-apis-finalize-grace-period-seconds": "30",
		gardenconst.AnnotationAuthenticationIssuer:                                 gardenconst.AnnotationAuthenticationIssuerManaged,
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

	// don't use the default merge logic for some fields
	shoot.Spec.Kubernetes.Version = newK8sVersion
	if oldShoot.Spec.Provider.ControlPlaneConfig != nil {
		shoot.Spec.Provider.ControlPlaneConfig = oldShoot.Spec.Provider.ControlPlaneConfig
	}
	if shoot.Spec.Maintenance != nil {
		if shoot.Spec.Maintenance.AutoUpdate != nil {
			if shoot.Spec.Maintenance.AutoUpdate.MachineImageVersion != nil && *shoot.Spec.Maintenance.AutoUpdate.MachineImageVersion {
				// if Gardener updates the machine image version, the merge logic would try to revert it, which doesn't work
				// but since the machine image version is part of the provider-specific config, it is more complicated to avoid reverting it here than to just prevent Gardener from updating it
				shoot.Spec.Maintenance.AutoUpdate.MachineImageVersion = ptr.To(false)
			}
		}
	}

	// as long as we don't have our own OIDC solution, let's ensure that the Gardener OIDC extension is enabled
	found := false
	for _, ext := range shoot.Spec.Extensions {
		if ext.Type == GardenerOIDCExtensionType {
			found = true
			break
		}
	}
	if !found {
		if shoot.Spec.Extensions == nil {
			shoot.Spec.Extensions = []gardenv1beta1.Extension{}
		}
		shoot.Spec.Extensions = append(shoot.Spec.Extensions, gardenv1beta1.Extension{
			Type: GardenerOIDCExtensionType,
		})
	}

	// apply cluster config, if not nil
	if clusterConfig != nil {
		log.Debug("Evaluating cluster config", "ccName", clusterConfig.Name, "ccNamespace", clusterConfig.Namespace)
		if len(clusterConfig.Spec.Patches) > 0 {
			log.Debug("Applying patches from cluster config", "ccName", clusterConfig.Name, "ccNamespace", clusterConfig.Namespace)
			patch := jsonpatch.NewTyped[*gardenv1beta1.Shoot](clusterConfig.Spec.Patches...)
			opts := []jsonpatch.Option{}
			if clusterConfig.Spec.PatchOptions != nil {
				if clusterConfig.Spec.PatchOptions.IgnoreMissingOnRemove != nil {
					opts = append(opts, jsonpatch.AllowMissingPathOnRemove(*clusterConfig.Spec.PatchOptions.IgnoreMissingOnRemove))
				}
				if clusterConfig.Spec.PatchOptions.CreateMissingOnAdd != nil {
					opts = append(opts, jsonpatch.EnsurePathExistsOnAdd(*clusterConfig.Spec.PatchOptions.CreateMissingOnAdd))
				}
			}
			patchedShoot, err := patch.Apply(shoot, opts...)
			if err != nil {
				return fmt.Errorf("error applying patches from cluster config '%s/%s': %w", clusterConfig.Namespace, clusterConfig.Name, err)
			}
			*shoot = *patchedShoot
		}
	}

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
