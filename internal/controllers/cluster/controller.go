package cluster

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
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
	commonapi "github.com/openmcp-project/openmcp-operator/api/common"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	cconst "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1/constants"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	gardenconst "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1/constants"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

const ControllerName = "Cluster"
const GardenerDeletionConfirmationAnnotation = "confirmation.gardener.cloud/deletion"

func NewClusterReconciler(rc *shared.RuntimeConfiguration, eventRecorder record.EventRecorder) *ClusterReconciler {
	return &ClusterReconciler{
		RuntimeConfiguration:    rc,
		eventRecorder:           eventRecorder,
		ClusterConfigReferences: newClusterConfigReferences(),
	}
}

type ClusterReconciler struct {
	*shared.RuntimeConfiguration
	eventRecorder record.EventRecorder
	// ClusterConfigReferences maps ClusterConfig references to the set of clusters that reference them.
	// This is used to trigger reconciliations on Clusters when a ClusterConfig is updated.
	ClusterConfigReferences clusterConfigReferences
}

var _ reconcile.Reconciler = &ClusterReconciler{}

type ReconcileResult = ctrlutils.ReconcileResult[*clustersv1alpha1.Cluster]

func (r *ClusterReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrPanic(ctx).WithName(ControllerName)
	ctx = logging.NewContext(ctx, log)
	log.Info("Starting reconcile")
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	rr := r.reconcile(ctx, req)
	// status update
	return ctrlutils.NewOpenMCPStatusUpdaterBuilder[*clustersv1alpha1.Cluster]().
		WithNestedStruct("Status").
		WithPhaseUpdateFunc(func(obj *clustersv1alpha1.Cluster, rr ReconcileResult) (string, error) {
			if rr.Object != nil && !rr.Object.DeletionTimestamp.IsZero() {
				return commonapi.StatusPhaseTerminating, nil
			}
			if conditions.AllConditionsHaveStatus(metav1.ConditionTrue, obj.Status.Conditions...) {
				return commonapi.StatusPhaseReady, nil
			}
			return commonapi.StatusPhaseProgressing, nil
		}).
		WithConditionUpdater(false).
		WithConditionEvents(r.eventRecorder, conditions.EventPerChange).
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
		Object:     c,
		OldObject:  c.DeepCopy(),
		Conditions: []metav1.Condition{},
	}

	createCon := shared.GenerateCreateConditionFunc(&rr)

	landscape := r.GetLandscape(profile.ProviderConfig.Spec.LandscapeRef.Name)
	if landscape == nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("unknown landscape '%s'", profile.ProviderConfig.Spec.LandscapeRef.Name), cconst.ReasonUnknownLandscape)
		createCon(providerv1alpha1.ConditionLandscapeManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
		return rr
	}
	createCon(providerv1alpha1.ConditionLandscapeManagement, metav1.ConditionTrue, "", "")

	shoot, rerr := GetShoot(ctx, landscape.Cluster.Client(), profile.Project.Namespace, c)
	if rerr != nil {
		rr.ReconcileError = errutils.Errorf("error getting shoot: %w", rerr, rerr)
		createCon(providerv1alpha1.ClusterConditionShootManagement, metav1.ConditionFalse, rerr.Reason(), rerr.Error())
		return rr
	}
	exists := shoot != nil
	if !exists {
		shoot = &gardenv1beta1.Shoot{}
		shoot.SetGroupVersionKind(gardenv1beta1.SchemeGroupVersion.WithKind("Shoot"))
		shoot.SetName(shared.ShootK8sNameFromCluster(c, profile.Project.Name))
		shoot.SetNamespace(profile.Project.Namespace)
	} else {
		createCon(providerv1alpha1.ClusterConditionShootManagement, metav1.ConditionFalse, "ShootNotFound", "Shoot does not exist yet")
	}
	for _, con := range shoot.Status.Conditions {
		sCon := metav1.Condition{
			Type:    "Gardener_" + string(con.Type),
			Reason:  con.Reason,
			Message: con.Message,
		}
		switch con.Status {
		case gardenv1beta1.ConditionTrue:
			sCon.Status = metav1.ConditionTrue
		case gardenv1beta1.ConditionFalse:
			sCon.Status = metav1.ConditionFalse
		case gardenv1beta1.ConditionUnknown:
			sCon.Status = metav1.ConditionUnknown
		case gardenv1beta1.ConditionProgressing:
			sCon.Status = metav1.ConditionFalse
		}
		rr.Conditions = append(rr.Conditions, sCon)
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
				createCon(providerv1alpha1.ConditionMeta, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
				return rr
			}
		}

		// fetch cluster config, if specified
		var clusterConfig *providerv1alpha1.ClusterConfig
		if c.Spec.ClusterConfigRef != nil && c.Spec.ClusterConfigRef.Name != "" {
			log.Info("Fetching cluster config", "clusterConfigName", c.Spec.ClusterConfigRef.Name, "clusterConfigNamespace", c.Namespace)
			r.ClusterConfigReferences.Set(types.NamespacedName{
				Name:      c.Spec.ClusterConfigRef.Name,
				Namespace: c.Namespace,
			}, types.NamespacedName{
				Name:      c.Name,
				Namespace: c.Namespace,
			})
			clusterConfig = &providerv1alpha1.ClusterConfig{}
			if err := r.PlatformCluster.Client().Get(ctx, ctrlutils.ObjectKey(c.Spec.ClusterConfigRef.Name, c.Namespace), clusterConfig); err != nil {
				if apierrors.IsNotFound(err) {
					rr.ReconcileError = errutils.WithReason(fmt.Errorf("cluster config '%s/%s' not found", c.Namespace, c.Spec.ClusterConfigRef.Name), clusterconst.ReasonInvalidReference)
					return rr
				}
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error getting cluster config '%s/%s': %w", c.Namespace, c.Spec.ClusterConfigRef.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
				return rr
			}
		} else {
			r.ClusterConfigReferences.UnsetCluster(types.NamespacedName{
				Name:      c.Name,
				Namespace: c.Namespace,
			})
		}

		// take over fields from shoot template and update shoot
		if err := UpdateShootFields(ctx, shoot, profile, c, clusterConfig); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error updating shoot fields: %w", err), clusterconst.ReasonInternalError)
			createCon(providerv1alpha1.ClusterConditionShootManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
			return rr
		}

		// set labels on the Cluster resource
		if rerr := r.ensureClusterLabels(ctx, c, shoot.Spec.Kubernetes.Version); rerr != nil {
			rr.ReconcileError = rerr
			createCon(providerv1alpha1.ConditionMeta, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
			return rr
		}
		createCon(providerv1alpha1.ConditionMeta, metav1.ConditionTrue, "", "")

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
			createCon(providerv1alpha1.ClusterConditionShootManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
			return rr
		}
		createCon(providerv1alpha1.ClusterConditionShootManagement, metav1.ConditionTrue, "", "")

	} else {

		// DELETE
		log.Info("Deleting resource")

		if exists {
			// shoot is still there
			if shoot.DeletionTimestamp == nil {
				log.Info("Deleting shoot", "shootName", shoot.Name, "shootNamespace", shoot.Namespace)
				if err := ctrlutils.EnsureAnnotation(ctx, landscape.Cluster.Client(), shoot, GardenerDeletionConfirmationAnnotation, "true", true, ctrlutils.OVERWRITE); err != nil {
					rr.ReconcileError = errutils.WithReason(fmt.Errorf("error adding deletion confirmation annotation to shoot '%s' in namespace '%s': %w", shoot.Name, shoot.Namespace, err), cconst.ReasonGardenClusterInteractionProblem)
					createCon(providerv1alpha1.ClusterConditionShootManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
					return rr
				}
				if err := landscape.Cluster.Client().Delete(ctx, shoot); err != nil {
					if !apierrors.IsNotFound(err) {
						rr.ReconcileError = errutils.WithReason(fmt.Errorf("error deleting shoot '%s' in namespace '%s': %w", shoot.Name, shoot.Namespace, err), cconst.ReasonGardenClusterInteractionProblem)
						createCon(providerv1alpha1.ClusterConditionShootManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
						return rr
					}
				}
				// wait for shoot to be deleted
			} else {
				log.Debug("Shoot is being deleted", "shootName", shoot.Name, "shootNamespace", shoot.Namespace)
			}
			createCon(providerv1alpha1.ClusterConditionShootManagement, metav1.ConditionFalse, cconst.ReasonWaitingForDeletion, "Waiting for shoot to be deleted")
			return rr
		}

		// remove finalizer
		if controllerutil.RemoveFinalizer(c, providerv1alpha1.ClusterFinalizer) {
			log.Info("Removing finalizer")
			if err := r.PlatformCluster.Client().Patch(ctx, c, client.MergeFrom(rr.OldObject)); err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)
				createCon(providerv1alpha1.ConditionMeta, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
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
		// watch ClusterConfig resources
		Watches(&providerv1alpha1.ClusterConfig{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
			if obj == nil {
				return nil
			}
			// reconcile all clusters that reference this ClusterConfig
			clusters := r.ClusterConfigReferences.GetClustersForConfig(types.NamespacedName{
				Name:      obj.GetName(),
				Namespace: obj.GetNamespace(),
			})
			requests := make([]ctrl.Request, 0, len(clusters))
			for cluster := range clusters {
				requests = append(requests, ctrl.Request{
					NamespacedName: cluster,
				})
			}
			return requests
		}), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
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

func newClusterConfigReferences() clusterConfigReferences {
	return clusterConfigReferences{
		ConfigToCluster: make(map[types.NamespacedName]sets.Set[types.NamespacedName]),
		ClusterToConfig: make(map[types.NamespacedName]types.NamespacedName),
	}
}

type clusterConfigReferences struct {
	ConfigToCluster map[types.NamespacedName]sets.Set[types.NamespacedName]
	ClusterToConfig map[types.NamespacedName]types.NamespacedName
}

func (ccr clusterConfigReferences) Set(clusterConfig types.NamespacedName, cluster types.NamespacedName) {
	if _, ok := ccr.ConfigToCluster[clusterConfig]; !ok {
		ccr.ConfigToCluster[clusterConfig] = sets.New[types.NamespacedName]()
	}
	ccr.ConfigToCluster[clusterConfig].Insert(cluster)
	ccr.ClusterToConfig[cluster] = clusterConfig
}

func (ccr clusterConfigReferences) UnsetClusterConfig(clusterConfig types.NamespacedName) {
	clusters := ccr.ConfigToCluster[clusterConfig]
	delete(ccr.ConfigToCluster, clusterConfig)
	for cluster := range clusters {
		delete(ccr.ClusterToConfig, cluster)
	}
}

func (ccr clusterConfigReferences) UnsetCluster(cluster types.NamespacedName) {
	if clusterConfig, ok := ccr.ClusterToConfig[cluster]; ok {
		clusters := ccr.ConfigToCluster[clusterConfig]
		clusters.Delete(cluster)
		if clusters.Len() == 0 {
			delete(ccr.ConfigToCluster, clusterConfig)
		}
		delete(ccr.ClusterToConfig, cluster)
	}
}

func (ccr clusterConfigReferences) GetClustersForConfig(clusterConfig types.NamespacedName) sets.Set[types.NamespacedName] {
	return ccr.ConfigToCluster[clusterConfig]
}
