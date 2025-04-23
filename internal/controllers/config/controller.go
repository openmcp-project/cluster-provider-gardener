package config

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/openmcp-project/controller-utils/pkg/conditions"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	errutils "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/controller-utils/pkg/logging"
	testutils "github.com/openmcp-project/controller-utils/pkg/testing"

	clustersv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/clusters/v1alpha1"
	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	cconst "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1/constants"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

const ControllerName = "ProviderConfig"
const ProfileConditionPrefix = "Profile_"

func NewGardenerProviderConfigReconciler(rc *shared.RuntimeConfiguration) *GardenerProviderConfigReconciler {
	return &GardenerProviderConfigReconciler{
		RuntimeConfiguration: rc,
	}
}

type GardenerProviderConfigReconciler struct {
	*shared.RuntimeConfiguration
}

var _ reconcile.Reconciler = &GardenerProviderConfigReconciler{}

type ReconcileResult = ctrlutils.ReconcileResult[*providerv1alpha1.ProviderConfig, providerv1alpha1.ConditionStatus]

func (r *GardenerProviderConfigReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrPanic(ctx).WithName(ControllerName)
	log.Info("Starting reconcile")
	r.Lock.Lock()
	defer r.Lock.Unlock()
	rr, profiles := r.reconcile(ctx, log, req)
	// internal representation update
	if profiles == nil && rr.ReconcileError != nil && rr.ReconcileError.Reason() == cconst.ReasonPlatformClusterInteractionProblem {
		// there was a problem communicating with the platform cluster which prevents us from determining the current state
	} else {
		if profiles != nil {
			// update internal profiles
			log.Info("Registering profiles", "profiles", strings.Join(sets.List(sets.KeySet(profiles)), ", "))
			r.SetProfilesForProviderConfiguration(req.Name, profiles)
			// update internal ProviderConfiguration
			if rr.Object != nil {
				r.SetProviderConfiguration(req.Name, rr.Object)
			}
		} else {
			// remove internal profiles
			log.Info("Unregistering profiles")
			r.UnsetProfilesForProviderConfiguration(req.Name)
			r.UnsetProviderConfiguration(req.Name)
		}
	}
	// status update
	return ctrlutils.NewStatusUpdaterBuilder[*providerv1alpha1.ProviderConfig, providerv1alpha1.ProviderConfigPhase, providerv1alpha1.ConditionStatus]().
		WithNestedStruct("CommonStatus").
		WithFieldOverride(ctrlutils.STATUS_FIELD_PHASE, "Phase").
		WithPhaseUpdateFunc(func(obj *providerv1alpha1.ProviderConfig, rr ctrlutils.ReconcileResult[*providerv1alpha1.ProviderConfig, providerv1alpha1.ConditionStatus]) (providerv1alpha1.ProviderConfigPhase, error) {
			if rr.ReconcileError != nil {
				return providerv1alpha1.PROVIDER_CONFIG_PHASE_UNAVAILABLE, nil
			}
			trueCount := 0
			falseCount := 0
			if len(rr.Conditions) > 0 {
				for _, con := range rr.Conditions {
					if con.GetStatus() == providerv1alpha1.CONDITION_TRUE {
						trueCount++
					} else {
						falseCount++
						if strings.HasPrefix(con.GetType(), ProfileConditionPrefix) {
							rr.ReconcileError = errutils.Join(rr.ReconcileError, errutils.WithReason(fmt.Errorf("profile '%s' not available: %s", strings.TrimPrefix(con.GetType(), ProfileConditionPrefix), con.GetMessage()), con.GetReason()))
						}
					}
				}
			}
			if falseCount == 0 {
				return providerv1alpha1.PROVIDER_CONFIG_PHASE_AVAILABLE, nil
			} else if trueCount == 0 {
				return providerv1alpha1.PROVIDER_CONFIG_PHASE_UNAVAILABLE, nil
			} else {
				return providerv1alpha1.PROVIDER_CONFIG_PHASE_PARTIALLY_AVAILABLE, nil
			}
		}).
		WithConditionUpdater(func() conditions.Condition[providerv1alpha1.ConditionStatus] {
			return &providerv1alpha1.Condition{}
		}, true).
		Build().
		UpdateStatus(ctx, r.PlatformCluster.Client(), rr)
}

func (r *GardenerProviderConfigReconciler) reconcile(ctx context.Context, log logging.Logger, req reconcile.Request) (ReconcileResult, map[string]*shared.Profile) {
	// get ProviderConfig resource
	pc := &providerv1alpha1.ProviderConfig{}
	if err := r.PlatformCluster.Client().Get(ctx, req.NamespacedName, pc); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debug("Resource not found")
			return ReconcileResult{}, nil
		}
		return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("unable to get resource '%s' from cluster: %w", req.NamespacedName.String(), err), cconst.ReasonPlatformClusterInteractionProblem)}, nil
	}

	// check provider name
	if pc.Spec.ProviderRef.Name != shared.ProviderName() {
		log.Debug("Skipping resource because a different provider is responsible for it", "provider", pc.Spec.ProviderRef.Name)
		return ReconcileResult{}, nil
	}

	// handle operation annotation
	if pc.GetAnnotations() != nil {
		op, ok := pc.GetAnnotations()[clustersv1alpha1.OperationAnnotation]
		if ok {
			switch op {
			case clustersv1alpha1.OperationAnnotationValueIgnore:
				log.Info("Ignoring resource due to ignore operation annotation")
				return ReconcileResult{}, nil
			case clustersv1alpha1.OperationAnnotationValueReconcile:
				log.Debug("Removing reconcile operation annotation from resource")
				if err := ctrlutils.EnsureAnnotation(ctx, r.PlatformCluster.Client(), pc, clustersv1alpha1.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
					return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("error removing operation annotation: %w", err), cconst.ReasonPlatformClusterInteractionProblem)}, nil
				}
			}
		}
	}

	rr := ReconcileResult{
		Object:    pc,
		OldObject: pc.DeepCopy(),
	}
	profiles := map[string]*shared.Profile{}

	inDeletion := pc.DeletionTimestamp != nil
	if !inDeletion {

		// CREATE/UPDATE
		log.Info("Creating/updating resource")

		// ensure finalizer
		if controllerutil.AddFinalizer(pc, providerv1alpha1.ProviderConfigFinalizer) {
			log.Info("Adding finalizer")
			if err := r.PlatformCluster.Client().Patch(ctx, pc, client.MergeFrom(rr.OldObject)); err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.NamespacedName.String(), err), cconst.ReasonPlatformClusterInteractionProblem)
				return rr, nil
			}
		}

		cloudProfileCache := map[string]*cachedCloudProfile{}
		cons := map[string]*providerv1alpha1.Condition{}

		// validate profiles
		for _, profile := range pc.Spec.Configurations {
			p := &shared.Profile{
				Config:               profile.DeepCopy(),
				ProviderConfigSource: pc.Name,
			}
			pCon := &providerv1alpha1.Condition{
				Type:   fmt.Sprintf("%s%s", ProfileConditionPrefix, p.GetName()),
				Status: providerv1alpha1.CONDITION_UNKNOWN,
			}

			// check if profile has naming conflict with other profiles
			// (same ProviderConfig)
			if _, ok := profiles[p.GetName()]; ok {
				rr.ReconcileError = errutils.Join(rr.ReconcileError, errutils.WithReason(fmt.Errorf("multiple profiles for landscape '%s' with name '%s' defined, landscape/name combination must be unique", profile.LandscapeRef.Name, profile.Name), cconst.ReasonConfigurationProblem))
				return rr, nil
			}

			// check if Landscape is known
			ls := r.GetLandscape(p.Config.LandscapeRef.Name)
			if ls == nil {
				pCon.SetStatus(providerv1alpha1.CONDITION_FALSE)
				pCon.SetReason(cconst.ReasonUnknownLandscape)
				pCon.SetMessage(fmt.Sprintf("Landscape '%s' not found", p.Config.LandscapeRef.Name))
				cons[p.GetName()] = pCon
				continue
			}

			// check if Project is known for Landscape
			if !slices.Contains(ls.Resource.Status.Projects, p.Config.Project) {
				pCon.SetStatus(providerv1alpha1.CONDITION_FALSE)
				pCon.SetReason(cconst.ReasonInvalidReference)
				pCon.SetMessage(fmt.Sprintf("Landscape '%s' can not manage the project '%s'", p.Config.LandscapeRef.Name, p.Config.Project))
				cons[p.GetName()] = pCon
				continue
			}

			// fetch CloudProfile
			cpName := p.Config.CloudProfile()
			if cpName == "" {
				pCon.SetStatus(providerv1alpha1.CONDITION_FALSE)
				pCon.SetReason(cconst.ReasonConfigurationProblem)
				pCon.SetMessage("Unable to extract CloudProfile name from ShootTemplate")
				cons[p.GetName()] = pCon
				continue
			}
			cp := cloudProfileCache[cpName]
			if cp == nil {
				cp = &cachedCloudProfile{
					CloudProfile:         &gardenv1beta1.CloudProfile{},
					SupportedK8sVersions: []shared.K8sVersion{},
				}
				if err := ls.Cluster.Client().Get(ctx, ctrlutils.ObjectKey(cpName), cp.CloudProfile); err != nil {
					if apierrors.IsNotFound(err) {
						pCon.SetReason(cconst.ReasonUnknownCloudProfile)
						pCon.SetMessage(fmt.Sprintf("Gardener landscape '%s' does not have a CloudProfile '%s'", p.Config.LandscapeRef.Name, cpName))
					} else {
						pCon.SetReason(cconst.ReasonGardenClusterInteractionProblem)
						pCon.SetMessage(fmt.Sprintf("Error while fetching CloudProfile '%s' from landscape '%s': %v", cpName, p.Config.LandscapeRef.Name, err))
					}
					pCon.SetStatus(providerv1alpha1.CONDITION_FALSE)
					cons[p.GetName()] = pCon
					continue
				}

				// extract supported k8s versions
				for _, version := range cp.CloudProfile.Spec.Kubernetes.Versions {
					if version.Classification != nil && (*version.Classification == gardenv1beta1.ClassificationSupported || *version.Classification == gardenv1beta1.ClassificationDeprecated) {
						cp.SupportedK8sVersions = append(cp.SupportedK8sVersions, shared.K8sVersion{
							Version:    version.Version,
							Deprecated: *version.Classification == gardenv1beta1.ClassificationDeprecated,
						})
					}
				}
				cloudProfileCache[cpName] = cp
			}
			p.SupportedK8sVersions = cp.SupportedK8sVersions
			slices.SortStableFunc(p.SupportedK8sVersions, func(a, b shared.K8sVersion) int {
				aParsed, err := semver.NewVersion(a.Version)
				if err != nil {
					return 0
				}
				bParsed, err := semver.NewVersion(b.Version)
				if err != nil {
					return 0
				}
				return aParsed.Compare(bParsed) * (-1) // we want the newest version on the top
			})

			profiles[p.GetName()] = p
			pCon.SetStatus(providerv1alpha1.CONDITION_TRUE)
			cons[p.GetName()] = pCon
		}

		// create/update Profile resources on the Onboarding cluster
		log.Debug("Listing existing ClusterProfiles")
		ownProfiles := &clustersv1alpha1.ClusterProfileList{}
		if err := r.OnboardingCluster.Client().List(ctx, ownProfiles, client.MatchingFields{
			"spec.environment":            shared.Environment(),
			"spec.providerRef.name":       shared.ProviderName(),
			"spec.providerConfigRef.name": pc.Name,
		}); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error listing ClusterProfiles: %w", err), cconst.ReasonOnboardingClusterInteractionProblem)
			return rr, nil
		}
		existingProfiles := map[string]*clustersv1alpha1.ClusterProfile{}
		for _, p := range ownProfiles.Items {
			existingProfiles[p.Spec.Name] = &p
		}
		for _, p := range profiles {
			pCon := cons[p.GetName()]
			existing, exists := existingProfiles[p.GetName()]
			if !exists {
				existing = &clustersv1alpha1.ClusterProfile{}
				existing.Name = ctrlutils.K8sNameHash(shared.ProviderName(), shared.Environment(), p.GetName())
			}
			delete(existingProfiles, p.GetName()) // remove already processed profiles so we can determine leftovers later
			if pCon.GetStatus() != providerv1alpha1.CONDITION_TRUE {
				// profile is not valid, so we skip it
				log.Debug("Skipping update of profile due to unhealthy condition", "profileK8sName", existing.Name, "provider", shared.ProviderName(), "environment", shared.Environment(), "profileName", p.GetName())
				continue
			} else {
				log.Debug("Creating/updating profile", "profileK8sName", existing.Name, "provider", shared.ProviderName(), "environment", shared.Environment(), "profileName", p.GetName())
			}
			// this shouldn't change anything for existing profiles
			existing.Spec.Name = p.GetName()
			existing.Spec.ProviderRef.Name = shared.ProviderName()
			existing.Spec.ProviderConfigRef.Name = pc.Name
			existing.Spec.Environment = shared.Environment()
			// update supported k8s versions
			existing.Spec.SupportedVersions = make([]clustersv1alpha1.SupportedK8sVersion, len(p.SupportedK8sVersions))
			for i, v := range p.SupportedK8sVersions {
				existing.Spec.SupportedVersions[i] = *v.ToResourceRepresentation()
			}
			var err error
			if exists {
				err = r.OnboardingCluster.Client().Update(ctx, existing)
			} else {
				err = r.OnboardingCluster.Client().Create(ctx, existing)
				if err != nil && apierrors.IsAlreadyExists(err) {
					// this should only happen if another ProviderConfig for the same provider creates the same profile
					rr.ReconcileError = errutils.Join(rr.ReconcileError, errutils.WithReason(fmt.Errorf("profile '%s' (provider: '%s' / env: '%s' / name: '%s') already exists, probably created by another ProviderConfig", existing.Name, shared.ProviderName(), shared.Environment(), existing.Spec.Name), cconst.ReasonConfigurationProblem))
					return rr, nil
				}
			}
			if err != nil {
				pCon.SetStatus(providerv1alpha1.CONDITION_FALSE)
				pCon.SetReason(cconst.ReasonOnboardingClusterInteractionProblem)
				pCon.SetMessage(fmt.Sprintf("error while creating/updating ClusterProfile '%s' on onboarding cluster: %v", existing.Name, err))
				// pCon is a pointer, so no need to update the map
			}
		}

		// delete leftover profiles
		for _, p := range existingProfiles {
			log.Info("Deleting leftover profile", "profileK8sName", p.Name, "provider", shared.ProviderName(), "environment", shared.Environment(), "profileName", p.Spec.Name)
			if err := r.OnboardingCluster.Client().Delete(ctx, p); err != nil {
				if apierrors.IsNotFound(err) {
					log.Debug("Profile '%s' already deleted", p.Name)
					continue
				}
				rr.ReconcileError = errutils.Join(rr.ReconcileError, errutils.WithReason(fmt.Errorf("error deleting leftover profile '%s' (provider: '%s' / env: '%s' / name: '%s'): %w", p.Name, p.Spec.ProviderRef.Name, p.Spec.Environment, p.Spec.Name, err), cconst.ReasonOnboardingClusterInteractionProblem))
			}
		}

		// set conditions
		rr.Conditions = make([]conditions.Condition[providerv1alpha1.ConditionStatus], 0, len(cons))
		for _, pCon := range cons {
			rr.Conditions = append(rr.Conditions, pCon)
		}

	} else {

		// DELETE
		log.Info("Deleting resource")

		// delete profiles
		log.Info("Deleting profiles")
		if err := r.OnboardingCluster.Client().DeleteAllOf(ctx, &clustersv1alpha1.ClusterProfile{}, client.MatchingFields{
			"spec.environment":            shared.Environment(),
			"spec.providerRef.name":       shared.ProviderName(),
			"spec.providerConfigRef.name": pc.Name,
		}); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error deleting profiles: %w", err), cconst.ReasonOnboardingClusterInteractionProblem)
			return rr, nil
		}
		log.Debug("Profiles deleted")

		// remove finalizer
		if controllerutil.RemoveFinalizer(pc, providerv1alpha1.LandscapeFinalizer) {
			log.Info("Removing finalizer")
			if err := r.PlatformCluster.Client().Patch(ctx, pc, client.MergeFrom(rr.OldObject)); err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.NamespacedName.String(), err), cconst.ReasonPlatformClusterInteractionProblem)
				return rr, nil
			}
		}
		rr.Object = nil // this prevents the controller from trying to update an already deleted resource
		profiles = nil

	}

	return rr, profiles
}

type cachedCloudProfile struct {
	CloudProfile         *gardenv1beta1.CloudProfile
	SupportedK8sVersions []shared.K8sVersion
}

// SetupWithManager sets up the controller with the Manager.
// Uses WatchesRawSource() instead of For() because it doesn't watch the primary cluster of the manager.
func (r *GardenerProviderConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(strings.ToLower(ControllerName)).
		WatchesRawSource(source.Kind(r.PlatformCluster.Cluster().GetCache(), &providerv1alpha1.ProviderConfig{}, handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, ls *providerv1alpha1.ProviderConfig) []ctrl.Request {
			return []ctrl.Request{testutils.RequestFromObject(ls)}
		}), ctrlutils.ToTypedPredicate[*providerv1alpha1.ProviderConfig](predicate.And(
			predicate.NewPredicateFuncs(func(obj client.Object) bool {
				pc, ok := obj.(*providerv1alpha1.ProviderConfig)
				if !ok {
					return false
				}
				return pc.Spec.ProviderRef.Name == shared.ProviderName()
			}),
			predicate.Or(
				predicate.GenerationChangedPredicate{},
				predicate.AnnotationChangedPredicate{},
			),
			predicate.Not(
				ctrlutils.HasAnnotationPredicate(clustersv1alpha1.OperationAnnotation, clustersv1alpha1.OperationAnnotationValueIgnore),
			),
		)))).
		Complete(r)
}
