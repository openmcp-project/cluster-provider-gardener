package config

import (
	"context"
	"fmt"
	"slices"

	"github.com/Masterminds/semver/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	errutils "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/controller-utils/pkg/logging"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	clusterconst "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1/constants"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"

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
	ctx = logging.NewContext(ctx, log)
	log.Info("Starting reconcile")
	r.Lock.Lock()
	defer r.Lock.Unlock()
	rr, profile := r.reconcile(ctx, req)
	// internal representation update
	if profile == nil && rr.ReconcileError != nil && rr.ReconcileError.Reason() == clusterconst.ReasonPlatformClusterInteractionProblem {
		// there was a problem communicating with the platform cluster which prevents us from determining the current state
	} else {
		if rr.Object != nil {
			// update internal ProviderConfiguration
			r.SetProviderConfiguration(req.Name, rr.Object)
		} else if rr.ReconcileError == nil {
			// remove ProviderConfiguration from internal representation
			r.UnsetProviderConfiguration(req.Name)
		}
		if profile != nil {
			// update internal profiles
			log.Info("Updating profile registrations")
			oldProfile := r.GetProfileForProviderConfiguration(req.Name)
			r.SetProfileForProviderConfiguration(req.Name, profile)
			if oldProfile == nil {
				// notify clusters about new profile
				// this is required because clusters with unknown profiles are ignored by the controller
				// so they would only be reconciled if somehow triggered by a modification from the outside
				if err := r.notifyClustersAboutNewProfile(ctx, profile); err != nil {
					rr.ReconcileError = errutils.Join(rr.ReconcileError, errutils.Errorf("error notifying clusters about new profile: %s", err, err.Error()))
				}
			}
		} else if rr.ReconcileError == nil {
			// remove profile from internal representation
			log.Info("Removing profile registration")
			r.UnsetProfilesForProviderConfiguration(req.Name)
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
			return providerv1alpha1.PROVIDER_CONFIG_PHASE_AVAILABLE, nil
		}).
		// WithConditionUpdater(func() conditions.Condition[providerv1alpha1.ConditionStatus] {
		// 	return &providerv1alpha1.Condition{}
		// }, true).
		Build().
		UpdateStatus(ctx, r.PlatformCluster.Client(), rr)
}

func (r *GardenerProviderConfigReconciler) reconcile(ctx context.Context, req reconcile.Request) (ReconcileResult, *shared.Profile) {
	log := logging.FromContextOrPanic(ctx)

	// get ProviderConfig resource
	pc := &providerv1alpha1.ProviderConfig{}
	if err := r.PlatformCluster.Client().Get(ctx, req.NamespacedName, pc); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Resource not found")
			return ReconcileResult{}, nil
		}
		return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("unable to get resource '%s' from cluster: %w", req.NamespacedName.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)}, nil
	}

	// check provider name
	if pc.Spec.ProviderRef.Name != shared.ProviderName() {
		log.Debug("Skipping resource because a different provider is responsible for it", "provider", pc.Spec.ProviderRef.Name)
		return ReconcileResult{}, nil
	}

	// handle operation annotation
	if pc.GetAnnotations() != nil {
		op, ok := pc.GetAnnotations()[openmcpconst.OperationAnnotation]
		if ok {
			switch op {
			case openmcpconst.OperationAnnotationValueIgnore:
				log.Info("Ignoring resource due to ignore operation annotation")
				return ReconcileResult{}, nil
			case openmcpconst.OperationAnnotationValueReconcile:
				log.Debug("Removing reconcile operation annotation from resource")
				if err := ctrlutils.EnsureAnnotation(ctx, r.PlatformCluster.Client(), pc, openmcpconst.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
					return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("error removing operation annotation: %w", err), clusterconst.ReasonPlatformClusterInteractionProblem)}, nil
				}
			}
		}
	}

	inDeletion := pc.DeletionTimestamp != nil
	var rr ReconcileResult
	var p *shared.Profile
	if !inDeletion {
		rr, p = r.handleCreateOrUpdate(ctx, req, pc)
	} else {
		rr = r.handleDelete(ctx, req, pc)
	}

	return rr, p
}

func (r *GardenerProviderConfigReconciler) handleCreateOrUpdate(ctx context.Context, req reconcile.Request, pc *providerv1alpha1.ProviderConfig) (ReconcileResult, *shared.Profile) {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Creating/updating resource")

	rr := ReconcileResult{
		Object:    pc,
		OldObject: pc.DeepCopy(),
	}
	p := &shared.Profile{
		ProviderConfig: pc,
	}

	// ensure finalizer
	if controllerutil.AddFinalizer(pc, providerv1alpha1.ProviderConfigFinalizer) {
		log.Info("Adding finalizer")
		if err := r.PlatformCluster.Client().Patch(ctx, pc, client.MergeFrom(rr.OldObject)); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.NamespacedName.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)
			return rr, nil
		}
	}

	// check if Landscape is known
	ls := r.GetLandscape(pc.Spec.LandscapeRef.Name)
	if ls == nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("Landscape '%s' not found", pc.Spec.LandscapeRef.Name), cconst.ReasonUnknownLandscape)
		return rr, nil
	}

	// check if Project is known for Landscape
	var pData *providerv1alpha1.ProjectData
	for _, project := range ls.Resource.Status.Projects {
		if project.Name == pc.Spec.Project {
			pData = &project
			break
		}
	}
	if pData == nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("Landscape '%s' can not manage the project '%s'", pc.Spec.LandscapeRef.Name, pc.Spec.Project), cconst.ReasonConfigurationProblem)
	}
	p.Project = *pData.DeepCopy()

	// fetch CloudProfile
	p.SupportedK8sVersions = []shared.K8sVersion{}
	cpName := pc.CloudProfile()
	if cpName == "" {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("Unable to extract CloudProfile name from ShootTemplate"), cconst.ReasonConfigurationProblem)
		return rr, nil
	}
	cp := &gardenv1beta1.CloudProfile{}
	if err := ls.Cluster.Client().Get(ctx, ctrlutils.ObjectKey(cpName), cp); err != nil {
		if apierrors.IsNotFound(err) {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("Gardener landscape '%s' does not have a CloudProfile '%s'", pc.Spec.LandscapeRef.Name, cpName), cconst.ReasonUnknownCloudProfile)
		} else {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("Error while fetching CloudProfile '%s' from landscape '%s': %v", cpName, pc.Spec.LandscapeRef.Name, err), cconst.ReasonGardenClusterInteractionProblem)
		}
		return rr, nil
	}

	// extract supported k8s versions
	for _, version := range cp.Spec.Kubernetes.Versions {
		if version.Classification != nil && (*version.Classification == gardenv1beta1.ClassificationSupported || *version.Classification == gardenv1beta1.ClassificationDeprecated) {
			p.SupportedK8sVersions = append(p.SupportedK8sVersions, shared.K8sVersion{
				Version:    version.Version,
				Deprecated: *version.Classification == gardenv1beta1.ClassificationDeprecated,
			})
		}
	}

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

	actual := &clustersv1alpha1.ClusterProfile{}
	actual.SetName(shared.ProfileK8sName(pc.Name))
	log.Info("Creating/updating ClusterProfile", "profileName", actual.Name)
	if _, err := ctrl.CreateOrUpdate(ctx, r.PlatformCluster.Client(), actual, func() error {
		actual.Spec.ProviderRef.Name = shared.ProviderName()
		actual.Spec.ProviderConfigRef.Name = pc.Name
		actual.Spec.SupportedVersions = make([]clustersv1alpha1.SupportedK8sVersion, len(p.SupportedK8sVersions))
		for i, v := range p.SupportedK8sVersions {
			actual.Spec.SupportedVersions[i] = *v.ToResourceRepresentation()
		}
		return nil
	}); err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating/updating ClusterProfile '%s' on onboarding cluster: %w", actual.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
		return rr, nil
	}

	return rr, p
}

func (r *GardenerProviderConfigReconciler) handleDelete(ctx context.Context, req reconcile.Request, pc *providerv1alpha1.ProviderConfig) ReconcileResult {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Deleting resource")

	rr := ReconcileResult{
		Object:    pc,
		OldObject: pc.DeepCopy(),
	}

	// delete profiles
	cp := &clustersv1alpha1.ClusterProfile{}
	cp.SetName(shared.ProfileK8sName(pc.Name))
	log.Info("Deleting ClusterProfile", "profileName", cp.Name)
	if err := r.PlatformCluster.Client().Delete(ctx, cp); err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error deleting profiles: %w", err), clusterconst.ReasonPlatformClusterInteractionProblem)
		return rr
	}
	log.Debug("Profile deleted", "profileName", cp.Name)

	// remove finalizer
	if controllerutil.RemoveFinalizer(pc, providerv1alpha1.ProviderConfigFinalizer) {
		log.Info("Removing finalizer")
		if err := r.PlatformCluster.Client().Patch(ctx, pc, client.MergeFrom(rr.OldObject)); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.NamespacedName.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)
			return rr
		}
	}
	rr.Object = nil // this prevents the controller from trying to update an already deleted resource

	return rr
}

func (r *GardenerProviderConfigReconciler) notifyClustersAboutNewProfile(ctx context.Context, profile *shared.Profile) errutils.ReasonableError {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Notifying clusters about new profile")

	// list all clusters that reference the new profile
	clusters := &clustersv1alpha1.ClusterList{}
	if err := r.PlatformCluster.Client().List(ctx, clusters, client.MatchingFields{
		"spec.profile": shared.ProfileK8sName(profile.ProviderConfig.Name),
	}); err != nil {
		return errutils.WithReason(fmt.Errorf("error listing clusters: %w", err), clusterconst.ReasonPlatformClusterInteractionProblem)
	}

	if len(clusters.Items) == 0 {
		log.Debug("No clusters found that reference the new profile")
		return nil
	}
	for _, c := range clusters.Items {
		log.Info("Notifying cluster", "clusterName", c.Name, "clusterNamespace", c.Namespace)
		r.ReconcileCluster <- event.TypedGenericEvent[*clustersv1alpha1.Cluster]{Object: &c}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
// Uses WatchesRawSource() instead of For() because it doesn't watch the primary cluster of the manager.
func (r *GardenerProviderConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// watch ProviderConfig resources on the platform cluster
		For(&providerv1alpha1.ProviderConfig{}).
		WithEventFilter(predicate.And(
			predicate.NewPredicateFuncs(func(obj client.Object) bool {
				pc, ok := obj.(*providerv1alpha1.ProviderConfig)
				if !ok {
					return false
				}
				return pc.Spec.ProviderRef.Name == shared.ProviderName()
			}),
			predicate.Or(
				predicate.GenerationChangedPredicate{},
				ctrlutils.DeletionTimestampChangedPredicate{},
				ctrlutils.GotAnnotationPredicate(openmcpconst.OperationAnnotation, openmcpconst.OperationAnnotationValueReconcile),
				ctrlutils.LostAnnotationPredicate(openmcpconst.OperationAnnotation, openmcpconst.OperationAnnotationValueIgnore),
			),
			predicate.Not(
				ctrlutils.HasAnnotationPredicate(openmcpconst.OperationAnnotation, openmcpconst.OperationAnnotationValueIgnore),
			),
		)).
		// listen to internally triggered reconciliation requests
		WatchesRawSource(source.TypedChannel(r.ReconcileProviderConfig, handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, pc *providerv1alpha1.ProviderConfig) []ctrl.Request {
			if pc == nil {
				return nil
			}
			return []ctrl.Request{
				{
					NamespacedName: client.ObjectKey{
						Name: pc.Name,
					},
				},
			}
		}))).
		Complete(r)
}
