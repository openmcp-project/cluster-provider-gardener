package cluster

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
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

	clustersv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/clusters/v1alpha1"
	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	cconst "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1/constants"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

const ControllerName = "Cluster"
const GardenerDeletionConfirmationAnnotation = "confirmation.gardener.cloud/deletion"

func NewClusterReconciler(rc *shared.RuntimeConfiguration) *ClusterReconciler {
	return &ClusterReconciler{
		RuntimeConfiguration: rc,
	}
}

type ClusterReconciler struct {
	*shared.RuntimeConfiguration
}

var _ reconcile.Reconciler = &ClusterReconciler{}

type ReconcileResult = ctrlutils.ReconcileResult[*clustersv1alpha1.Cluster, clustersv1alpha1.ConditionStatus]

func (r *ClusterReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrPanic(ctx).WithName(ControllerName)
	ctx = logging.NewContext(ctx, log)
	log.Info("Starting reconcile")
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	rr := r.reconcile(ctx, log, req)
	// status update
	return ctrlutils.NewStatusUpdaterBuilder[*clustersv1alpha1.Cluster, clustersv1alpha1.ClusterPhase, clustersv1alpha1.ConditionStatus]().
		WithNestedStruct("CommonStatus").
		WithFieldOverride(ctrlutils.STATUS_FIELD_PHASE, "Phase").
		WithPhaseUpdateFunc(func(obj *clustersv1alpha1.Cluster, rr ctrlutils.ReconcileResult[*clustersv1alpha1.Cluster, clustersv1alpha1.ConditionStatus]) (clustersv1alpha1.ClusterPhase, error) {
			if rr.ReconcileError != nil {
				if !obj.DeletionTimestamp.IsZero() {
					return clustersv1alpha1.CLUSTER_PHASE_DELETING_ERROR, nil
				}
				return clustersv1alpha1.CLUSTER_PHASE_ERROR, nil
			}
			if len(rr.Conditions) == 0 {
				return clustersv1alpha1.CLUSTER_PHASE_UNKNOWN, nil
			}
			if !obj.DeletionTimestamp.IsZero() {
				return clustersv1alpha1.CLUSTER_PHASE_DELETING, nil
			}
			// check if all conditions are true
			for _, con := range rr.Conditions {
				if con.GetStatus() != clustersv1alpha1.CONDITION_TRUE {
					return clustersv1alpha1.CLUSTER_PHASE_NOT_READY, nil
				}
			}
			return clustersv1alpha1.CLUSTER_PHASE_READY, nil
		}).
		WithConditionUpdater(func() conditions.Condition[clustersv1alpha1.ConditionStatus] {
			return &clustersv1alpha1.Condition{}
		}, true).
		Build().
		UpdateStatus(ctx, r.OnboardingCluster.Client(), rr)
}

func (r *ClusterReconciler) reconcile(ctx context.Context, log logging.Logger, req reconcile.Request) ReconcileResult {
	// get Cluster resource
	c := &clustersv1alpha1.Cluster{}
	if err := r.OnboardingCluster.Client().Get(ctx, req.NamespacedName, c); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Resource not found")
			return ReconcileResult{}
		}
		return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("unable to get resource '%s' from cluster: %w", req.NamespacedName.String(), err), cconst.ReasonOnboardingClusterInteractionProblem)}
	}

	// handle operation annotation
	if c.GetAnnotations() != nil {
		op, ok := c.GetAnnotations()[clustersv1alpha1.OperationAnnotation]
		if ok {
			switch op {
			case clustersv1alpha1.OperationAnnotationValueIgnore:
				log.Info("Ignoring resource due to ignore operation annotation")
				return ReconcileResult{}
			case clustersv1alpha1.OperationAnnotationValueReconcile:
				log.Debug("Removing reconcile operation annotation from resource")
				if err := ctrlutils.EnsureAnnotation(ctx, r.OnboardingCluster.Client(), c, clustersv1alpha1.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
					return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("error removing operation annotation: %w", err), cconst.ReasonOnboardingClusterInteractionProblem)}
				}
			}
		}
	}

	rr := ReconcileResult{
		Object:    c,
		OldObject: c.DeepCopy(),
	}

	// fetch profile
	profile := r.GetProfile(c.Spec.ClusterProfileRef.Name)
	if profile == nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("unknown profile '%s'", c.Spec.ClusterProfileRef.Name), cconst.ReasonUnknownProfile)
		return rr
	}
	landscape := r.GetLandscape(profile.ProviderConfig.Spec.LandscapeRef.Name)
	if landscape == nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("unknown landscape '%s'", profile.ProviderConfig.Spec.LandscapeRef.Name), cconst.ReasonUnknownLandscape)
		return rr
	}

	shoot, rerr := GetShoot(ctx, log, landscape, profile, c)
	if rerr != nil {
		rr.ReconcileError = errutils.Errorf("error getting shoot: %w", rerr, rerr)
		return rr
	}
	exists := shoot != nil
	if !exists {
		shoot = &gardenv1beta1.Shoot{}
		shoot.SetGroupVersionKind(gardenv1beta1.SchemeGroupVersion.WithKind("Shoot"))
		shoot.SetName(shared.ShootK8sNameFromCluster(c, profile.Project.Name))
		shoot.SetNamespace(profile.Project.Namespace)
	}
	rr.Conditions = make([]conditions.Condition[clustersv1alpha1.ConditionStatus], len(shoot.Status.Conditions))
	for i, con := range shoot.Status.Conditions {
		rr.Conditions[i] = &clustersv1alpha1.Condition{
			Type:    string(con.Type),
			Status:  clustersv1alpha1.ConditionStatus(con.Status),
			Reason:  con.Reason,
			Message: con.Message,
		}
	}

	inDeletion := !c.DeletionTimestamp.IsZero()
	if !inDeletion {

		// CREATE/UPDATE
		log.Info("Creating/updating resource")

		// ensure finalizer
		if controllerutil.AddFinalizer(c, providerv1alpha1.ClusterFinalizer) {
			log.Info("Adding finalizer")
			if err := r.OnboardingCluster.Client().Patch(ctx, c, client.MergeFrom(rr.OldObject)); err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.NamespacedName.String(), err), cconst.ReasonOnboardingClusterInteractionProblem)
				return rr
			}
		}

		// take over fields from shoot template and update shoot
		if err := UpdateShootFields(ctx, shoot, profile, landscape, c); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error updating shoot fields: %w", err), cconst.ReasonInternalError)
			return rr
		}
		// set shoot in ProviderStatus
		manifest := &gardenv1beta1.ShootTemplate{
			ObjectMeta: *shoot.ObjectMeta.DeepCopy(),
			Spec:       *shoot.Spec.DeepCopy(),
		}
		manifest.ManagedFields = nil
		manifest.ResourceVersion = ""
		manifest.UID = ""
		manifest.OwnerReferences = nil
		manifest.Finalizers = nil
		if err := c.Status.SetProviderStatus(providerv1alpha1.ClusterStatus{Shoot: manifest}); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error setting provider status: %w", err), cconst.ReasonInternalError)
			return rr
		}
		var err error
		if exists {
			log.Info("Updating shoot", "shootName", shoot.Name, "shootNamespace", shoot.Namespace)
			err = landscape.Cluster.Client().Update(ctx, shoot)
		} else {
			log.Info("Creating shoot", "shootName", shoot.Name, "shootNamespace", shoot.Namespace)
			err = landscape.Cluster.Client().Create(ctx, shoot)
		}
		if err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating or updating shoot '%s' in namespace '%s': %w", shoot.Name, shoot.Namespace, err), cconst.ReasonGardenClusterInteractionProblem)
			return rr
		}

	} else {

		// DELETE
		log.Info("Deleting resource")

		if exists {
			// shoot is still there
			if shoot.DeletionTimestamp == nil {
				log.Info("Deleting shoot", "shootName", shoot.Name, "shootNamespace", shoot.Namespace)
				if err := ctrlutils.EnsureAnnotation(ctx, landscape.Cluster.Client(), shoot, GardenerDeletionConfirmationAnnotation, "true", true, ctrlutils.OVERWRITE); err != nil {
					rr.ReconcileError = errutils.WithReason(fmt.Errorf("error adding deletion confirmation annotation to shoot '%s' in namespace '%s': %w", shoot.Name, shoot.Namespace, err), cconst.ReasonGardenClusterInteractionProblem)
					return rr
				}
				if err := landscape.Cluster.Client().Delete(ctx, shoot); err != nil {
					if !apierrors.IsNotFound(err) {
						rr.ReconcileError = errutils.WithReason(fmt.Errorf("error deleting shoot '%s' in namespace '%s': %w", shoot.Name, shoot.Namespace, err), cconst.ReasonGardenClusterInteractionProblem)
						return rr
					}
				}
				// wait for shoot to be deleted
			} else {
				log.Debug("Shoot is being deleted", "shootName", shoot.Name, "shootNamespace", shoot.Namespace)
			}
			rr.Reason = cconst.ReasonWaitingForDeletion
			rr.Message = "Waiting for shoot to be deleted"
			return rr
		}

		// remove finalizer
		if controllerutil.RemoveFinalizer(c, providerv1alpha1.ClusterFinalizer) {
			log.Info("Removing finalizer")
			if err := r.OnboardingCluster.Client().Patch(ctx, c, client.MergeFrom(rr.OldObject)); err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.NamespacedName.String(), err), cconst.ReasonOnboardingClusterInteractionProblem)
				return rr
			}
		}
		rr.Object = nil // this prevents the controller from trying to update an already deleted resource

	}

	return rr
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// watch Cluster resources
		For(&clustersv1alpha1.Cluster{}).
		WithEventFilter(predicate.And(
			predicate.Or(
				predicate.GenerationChangedPredicate{},
				ctrlutils.GotAnnotationPredicate(clustersv1alpha1.OperationAnnotation, clustersv1alpha1.OperationAnnotationValueReconcile),
				ctrlutils.LostAnnotationPredicate(clustersv1alpha1.OperationAnnotation, clustersv1alpha1.OperationAnnotationValueIgnore),
			),
			predicate.Not(
				ctrlutils.HasAnnotationPredicate(clustersv1alpha1.OperationAnnotation, clustersv1alpha1.OperationAnnotationValueIgnore),
			),
		)).
		// watch Shoot resources
		WatchesRawSource(source.TypedChannel(r.ShootWatch, handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, shoot *gardenv1beta1.Shoot) []ctrl.Request {
			if shoot == nil {
				return nil
			}
			clusterName, ok := ctrlutils.GetLabel(shoot, providerv1alpha1.ClusterReferenceLabelName)
			if !ok {
				return nil
			}
			clusterNamespace, ok := ctrlutils.GetLabel(shoot, providerv1alpha1.ClusterReferenceLabelNamespace)
			if !ok {
				return nil
			}
			return []ctrl.Request{
				{
					NamespacedName: client.ObjectKey{
						Name:      clusterName,
						Namespace: clusterNamespace,
					},
				},
			}
		}), source.WithPredicates[*gardenv1beta1.Shoot, ctrl.Request](ctrlutils.ToTypedPredicate[*gardenv1beta1.Shoot](predicate.And(
			ctrlutils.HasLabelPredicate(providerv1alpha1.ClusterReferenceLabelEnvironment, shared.Environment()),
			ctrlutils.HasLabelPredicate(providerv1alpha1.ClusterReferenceLabelProvider, shared.ProviderName()),
		))))).
		// watch Profile resources
		Watches(&clustersv1alpha1.ClusterProfile{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
			if obj == nil {
				return nil
			}
			// reconcile all clusters that reference this profile
			clusters := &clustersv1alpha1.ClusterList{}
			if err := r.OnboardingCluster.Client().List(ctx, clusters, client.MatchingFields{
				"spec.clusterProfileRef.name": obj.GetName(),
			}); err != nil {
				return nil // TODO: find a better option than just ignoring this error
			}
			requests := make([]ctrl.Request, len(clusters.Items))
			for i, cluster := range clusters.Items {
				requests[i] = ctrl.Request{
					NamespacedName: client.ObjectKey{
						Name:      cluster.Name,
						Namespace: cluster.Namespace,
					},
				}
			}
			return requests
		}), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

func GetShoot(ctx context.Context, log logging.Logger, landscape *shared.Landscape, profile *shared.Profile, c *clustersv1alpha1.Cluster) (*gardenv1beta1.Shoot, errutils.ReasonableError) {
	// check if shoot already exists
	shoot := &gardenv1beta1.Shoot{}
	exists := false
	// first, look into the status of the Cluster resource
	if c.Status.ProviderStatus != nil {
		cs := &providerv1alpha1.ClusterStatus{}
		if err := c.Status.GetProviderStatus(cs); err != nil {
			return nil, errutils.WithReason(fmt.Errorf("error unmarshalling provider status: %w", err), cconst.ReasonInternalError)
		}
		log.Debug("Provider status found, checking for shoot manifest")
		if cs.Shoot != nil {
			log.Debug("Found shoot in provider status", "shootName", cs.Shoot.GetName(), "shootNamespace", cs.Shoot.GetNamespace())
			shoot.SetName(cs.Shoot.GetName())
			shoot.SetNamespace(cs.Shoot.GetNamespace())
			if err := landscape.Cluster.Client().Get(ctx, client.ObjectKeyFromObject(shoot), shoot); err != nil {
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
		log.Debug("Shoot name and namespace could not be recovered from provider status, checking shoots in project namespace for fitting cluster reference labels", "projectNamespace", profile.Project.Namespace)
		shoots := &gardenv1beta1.ShootList{}
		if err := landscape.Cluster.Client().List(ctx, shoots, client.InNamespace(profile.Project.Namespace), client.MatchingLabels{
			providerv1alpha1.ClusterReferenceLabelName:      c.Name,
			providerv1alpha1.ClusterReferenceLabelNamespace: c.Namespace,
		}); err != nil {
			return nil, errutils.WithReason(fmt.Errorf("error listing shoots in namespace '%s': %w", profile.Project.Namespace, err), cconst.ReasonGardenClusterInteractionProblem)
		}
		if len(shoots.Items) > 1 {
			return nil, errutils.WithReason(fmt.Errorf("found multiple shoots referencing cluster '%s'/'%s' in namespace '%s', there should never be more than one", c.Namespace, c.Name, profile.Project.Namespace), cconst.ReasonInternalError)
		}
		if len(shoots.Items) == 1 {
			shoot = &shoots.Items[0]
			log.Info("Found shoot from cluster reference labels", "shootName", shoot.Name, "shootNamespace", shoot.Namespace)
			exists = true
		} else {
			log.Info("No shoot found from cluster reference labels", "namespace", profile.Project.Namespace)
		}
	}
	if !exists {
		shoot = nil
	}
	return shoot, nil
}
