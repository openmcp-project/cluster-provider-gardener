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

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	clusterconst "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1/constants"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	cconst "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1/constants"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	gardenconst "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1/constants"
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
	rr := r.reconcile(ctx, req)
	// status update
	return ctrlutils.NewStatusUpdaterBuilder[*clustersv1alpha1.Cluster, clustersv1alpha1.ClusterPhase, clustersv1alpha1.ConditionStatus]().
		WithNestedStruct("CommonStatus").
		WithFieldOverride(ctrlutils.STATUS_FIELD_PHASE, "Phase").
		WithPhaseUpdateFunc(func(obj *clustersv1alpha1.Cluster, rr ReconcileResult) (clustersv1alpha1.ClusterPhase, error) {
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
		UpdateStatus(ctx, r.PlatformCluster.Client(), rr)
}

// nolint:gocyclo
func (r *ClusterReconciler) reconcile(ctx context.Context, req reconcile.Request) ReconcileResult {
	log := logging.FromContextOrPanic(ctx)

	// get Cluster resource
	c := &clustersv1alpha1.Cluster{}
	if err := r.PlatformCluster.Client().Get(ctx, req.NamespacedName, c); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Resource not found")
			return ReconcileResult{}
		}
		return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("unable to get resource '%s' from cluster: %w", req.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)}
	}

	// handle operation annotation
	if c.GetAnnotations() != nil {
		op, ok := c.GetAnnotations()[openmcpconst.OperationAnnotation]
		if ok {
			switch op {
			case openmcpconst.OperationAnnotationValueIgnore:
				log.Info("Ignoring resource due to ignore operation annotation")
				return ReconcileResult{}
			case openmcpconst.OperationAnnotationValueReconcile:
				log.Debug("Removing reconcile operation annotation from resource")
				if err := ctrlutils.EnsureAnnotation(ctx, r.PlatformCluster.Client(), c, openmcpconst.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
					return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("error removing operation annotation: %w", err), clusterconst.ReasonPlatformClusterInteractionProblem)}
				}
			}
		}
	}

	// fetch profile
	profile := r.GetProfile(c.Spec.Profile)
	if profile == nil {
		log.Info("Ignoring cluster due to unknown profile", "profile", c.Spec.Profile)
		return ReconcileResult{}
	}

	rr := ReconcileResult{
		Object:    c,
		OldObject: c.DeepCopy(),
	}

	landscape := r.GetLandscape(profile.ProviderConfig.Spec.LandscapeRef.Name)
	if landscape == nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("unknown landscape '%s'", profile.ProviderConfig.Spec.LandscapeRef.Name), cconst.ReasonUnknownLandscape)
		return rr
	}

	shoot, rerr := GetShoot(ctx, landscape.Cluster.Client(), profile.Project.Namespace, c)
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
			if err := r.PlatformCluster.Client().Patch(ctx, c, client.MergeFrom(rr.OldObject)); err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)
				return rr
			}
		}

		// take over fields from shoot template and update shoot
		if err := UpdateShootFields(ctx, shoot, profile, c); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error updating shoot fields: %w", err), clusterconst.ReasonInternalError)
			return rr
		}
		// set labels on the Cluster resource
		if rerr := r.ensureClusterLabels(ctx, c, shoot.Spec.Kubernetes.Version); rerr != nil {
			rr.ReconcileError = rerr
			return rr
		}
		// set shoot in ProviderStatus
		manifest := &gardenv1beta1.ShootTemplate{
			ObjectMeta: *shoot.ObjectMeta.DeepCopy(),
			Spec:       *shoot.Spec.DeepCopy(),
		}
		// set shoot apiserver endpoint in status
		if len(shoot.Status.AdvertisedAddresses) > 0 {
			for _, addr := range shoot.Status.AdvertisedAddresses {
				if addr.Name == gardenconst.AdvertisedAddressExternal {
					rr.Object.Status.APIServer = addr.URL
					break
				}
			}
		}
		manifest.ManagedFields = nil
		manifest.ResourceVersion = ""
		manifest.UID = ""
		manifest.OwnerReferences = nil
		manifest.Finalizers = nil
		if err := c.Status.SetProviderStatus(providerv1alpha1.ClusterStatus{Shoot: manifest}); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error setting provider status: %w", err), clusterconst.ReasonInternalError)
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
			if err := r.PlatformCluster.Client().Patch(ctx, c, client.MergeFrom(rr.OldObject)); err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)
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
				ctrlutils.DeletionTimestampChangedPredicate{},
				ctrlutils.GotAnnotationPredicate(openmcpconst.OperationAnnotation, openmcpconst.OperationAnnotationValueReconcile),
				ctrlutils.LostAnnotationPredicate(openmcpconst.OperationAnnotation, openmcpconst.OperationAnnotationValueIgnore),
			),
			predicate.Not(
				ctrlutils.HasAnnotationPredicate(openmcpconst.OperationAnnotation, openmcpconst.OperationAnnotationValueIgnore),
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
			if err := r.PlatformCluster.Client().List(ctx, clusters, client.MatchingFields{
				"spec.profile": obj.GetName(),
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
		// listen to internally triggered reconciliation requests
		WatchesRawSource(source.TypedChannel(r.ReconcileCluster, handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, c *clustersv1alpha1.Cluster) []ctrl.Request {
			if c == nil {
				return nil
			}
			return []ctrl.Request{
				{
					NamespacedName: client.ObjectKey{
						Name:      c.Name,
						Namespace: c.Namespace,
					},
				},
			}
		}))).
		Complete(r)
}

func (r *ClusterReconciler) ensureClusterLabels(ctx context.Context, c *clustersv1alpha1.Cluster, k8sVersion string) errutils.ReasonableError {
	log := logging.FromContextOrPanic(ctx)
	old := c.DeepCopy()
	changed := false
	labels := c.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	if labels[clustersv1alpha1.K8sVersionLabel] != k8sVersion {
		labels[clustersv1alpha1.K8sVersionLabel] = k8sVersion
		changed = true
	}
	if labels[clustersv1alpha1.ProviderLabel] != shared.ProviderName() {
		labels[clustersv1alpha1.ProviderLabel] = shared.ProviderName()
		changed = true
	}
	if changed {
		c.Labels = labels
		log.Info("Updating labels on Cluster resource", "labels", labels)
		if err := r.PlatformCluster.Client().Patch(ctx, c, client.MergeFrom(old)); err != nil {
			return errutils.WithReason(fmt.Errorf("error patching labels on Cluster: %w", err), clusterconst.ReasonPlatformClusterInteractionProblem)
		}
	}
	return nil
}
