package accessrequest

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openmcp-project/controller-utils/pkg/conditions"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	errutils "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/controller-utils/pkg/logging"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	cconst "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1/constants"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/cluster"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	clusterconst "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1/constants"
)

const ControllerName = "AccessRequest"
const RenewTokenAfterValidityPercentagePassed = 0.8
const (
	SecretKeyKubeconfig          = "kubeconfig"
	SecretKeyExpirationTimestamp = "expirationTimestamp"
	SecretKeyCreationTimestamp   = "creationTimestamp"
)

func managedResourcesLabels(ac *clustersv1alpha1.AccessRequest) map[string]string {
	return map[string]string{
		providerv1alpha1.ManagedByNameLabel:      ac.Name,
		providerv1alpha1.ManagedByNamespaceLabel: ac.Namespace,
	}
}

var DefaultRequestedTokenValidityDuration = 30 * 24 * time.Hour // 30 days

func NewAccessRequestReconciler(rc *shared.RuntimeConfiguration) *AccessRequestReconciler {
	return &AccessRequestReconciler{
		RuntimeConfiguration: rc,
	}
}

type AccessRequestReconciler struct {
	*shared.RuntimeConfiguration
}

var _ reconcile.Reconciler = &AccessRequestReconciler{}

type ReconcileResult = ctrlutils.ReconcileResult[*clustersv1alpha1.AccessRequest, clustersv1alpha1.ConditionStatus]

func (r *AccessRequestReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrPanic(ctx).WithName(ControllerName)
	ctx = logging.NewContext(ctx, log)
	log.Info("Starting reconcile")
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	rr := r.reconcile(ctx, req)
	// status update
	return ctrlutils.NewStatusUpdaterBuilder[*clustersv1alpha1.AccessRequest, clustersv1alpha1.RequestPhase, clustersv1alpha1.ConditionStatus]().
		WithNestedStruct("CommonStatus").
		WithFieldOverride(ctrlutils.STATUS_FIELD_PHASE, "Phase").
		WithPhaseUpdateFunc(func(obj *clustersv1alpha1.AccessRequest, rr ReconcileResult) (clustersv1alpha1.RequestPhase, error) {
			if rr.ReconcileError != nil || rr.Object == nil {
				return clustersv1alpha1.REQUEST_PENDING, nil
			}
			return clustersv1alpha1.REQUEST_GRANTED, nil
		}).
		WithConditionUpdater(func() conditions.Condition[clustersv1alpha1.ConditionStatus] {
			return &clustersv1alpha1.Condition{}
		}, true).
		Build().
		UpdateStatus(ctx, r.PlatformCluster.Client(), rr)
}

func (r *AccessRequestReconciler) reconcile(ctx context.Context, req reconcile.Request) ReconcileResult {
	log := logging.FromContextOrPanic(ctx)

	// get AccessRequest resource
	ac := &clustersv1alpha1.AccessRequest{}
	if err := r.PlatformCluster.Client().Get(ctx, req.NamespacedName, ac); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Resource not found")
			return ReconcileResult{}
		}
		return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("unable to get resource '%s' from cluster: %w", req.NamespacedName.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)}
	}

	c, p, rerr := r.checkResponsibility(ctx, ac)
	if c == nil || p == nil || rerr != nil {
		return ReconcileResult{ReconcileError: rerr}
	}

	// handle operation annotation
	enforceReconcile := false
	if ac.GetAnnotations() != nil {
		op, ok := c.GetAnnotations()[clustersv1alpha1.OperationAnnotation]
		if ok {
			switch op {
			case clustersv1alpha1.OperationAnnotationValueIgnore:
				log.Info("Ignoring resource due to ignore operation annotation")
				return ReconcileResult{}
			case clustersv1alpha1.OperationAnnotationValueReconcile:
				enforceReconcile = true
				log.Debug("Removing reconcile operation annotation from resource")
				if err := ctrlutils.EnsureAnnotation(ctx, r.PlatformCluster.Client(), c, clustersv1alpha1.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
					return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("error removing operation annotation: %w", err), clusterconst.ReasonPlatformClusterInteractionProblem)}
				}
			}
		}
	}

	getShootAccess := func() (*shootAccess, errutils.ReasonableError) {
		// get landscape
		ls := r.GetLandscapeForProfile(p)
		if ls == nil {
			return nil, errutils.WithReason(fmt.Errorf("unable to determine landscape"), cconst.ReasonInternalError)
		}

		// get shoot
		shoot, rerr := cluster.GetShoot(ctx, ls, p, c)
		if rerr != nil {
			return nil, rerr
		}

		// get temporary admin access for the cluster
		shootClient, shootREST, err := getTemporaryClientForShoot(ctx, ls.Cluster.Client(), shoot)
		if err != nil {
			return nil, errutils.WithReason(fmt.Errorf("error getting admin access to shoot '%s/%s': %w", shoot.Namespace, shoot.Name, err), cconst.ReasonGardenClusterInteractionProblem)
		}

		return &shootAccess{
			Shoot:   shoot,
			RESTCfg: shootREST,
			Client:  shootClient,
		}, nil
	}

	inDeletion := !ac.DeletionTimestamp.IsZero()
	var rr ReconcileResult
	if !inDeletion {
		rr = r.handleCreateOrUpdate(ctx, req, ac, getShootAccess, enforceReconcile)
	} else {
		rr = r.handleDelete(ctx, req, ac, getShootAccess)
	}

	return rr
}

func (r *AccessRequestReconciler) handleCreateOrUpdate(ctx context.Context, req reconcile.Request, ac *clustersv1alpha1.AccessRequest, getShootAccess shootAccessGetter, enforceReconcile bool) ReconcileResult {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Creating/updating resource")

	rr := ReconcileResult{
		Object:    ac,
		OldObject: ac.DeepCopy(),
	}
	if rr.Object.Status.Phase == "" {
		rr.Object.Status.Phase = clustersv1alpha1.REQUEST_PENDING
	}

	// ensure finalizer
	if controllerutil.AddFinalizer(ac, providerv1alpha1.AccessRequestFinalizer) {
		log.Info("Adding finalizer")
		if err := r.PlatformCluster.Client().Patch(ctx, ac, client.MergeFrom(rr.OldObject)); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.NamespacedName.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)
			return rr
		}
	}

	// check if the request is already granted and nothing has changed
	if ac.Status.Phase == clustersv1alpha1.REQUEST_GRANTED && ac.Status.ObservedGeneration == ac.Generation && !enforceReconcile {
		// check if the secret exists and is valid
		s := &corev1.Secret{}
		if err := r.PlatformCluster.Client().Get(ctx, ctrlutils.ObjectKey(ac.Status.SecretRef.Name, ac.Status.SecretRef.Namespace), s); err != nil {
			if !apierrors.IsNotFound(err) {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error getting secret '%s/%s': %w", ac.Status.SecretRef.Namespace, ac.Status.SecretRef.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
				return rr
			}
			s = nil
		}

		if s != nil {
			creationTimestamp := base64.StdEncoding.EncodeToString(s.Data[SecretKeyCreationTimestamp])
			expirationTimestamp := base64.StdEncoding.EncodeToString(s.Data[SecretKeyExpirationTimestamp])
			if creationTimestamp != "" && expirationTimestamp != "" {
				tmp, err := strconv.ParseInt(creationTimestamp, 10, 64)
				if err != nil {
					rr.ReconcileError = errutils.WithReason(fmt.Errorf("error parsing creation timestamp from secret '%s/%s': %w", s.Namespace, s.Name, err), cconst.ReasonInternalError)
					return rr
				}
				createdAt := time.Unix(tmp, 0)
				tmp, err = strconv.ParseInt(expirationTimestamp, 10, 64)
				if err != nil {
					rr.ReconcileError = errutils.WithReason(fmt.Errorf("error parsing expiration timestamp from secret '%s/%s': %w", s.Namespace, s.Name, err), cconst.ReasonInternalError)
					return rr
				}
				expiredAt := time.Unix(tmp, 0)
				tokenRenewalTime := createdAt.Add(time.Duration(float64(expiredAt.Sub(createdAt)) * RenewTokenAfterValidityPercentagePassed))
				if time.Now().Before(tokenRenewalTime) {
					// the request is granted, the secret still exists and the token is still valid - nothing to do
					log.Info("Request is already granted, secret still exists, token is still valid - nothing to do")
					rr.Result.RequeueAfter = time.Until(tokenRenewalTime)
					return rr
				}
			}
		}
	}

	sac, rerr := getShootAccess()
	if rerr != nil {
		rr.ReconcileError = rerr
		return rr
	}
	getShootAccess = staticShootAccessGetter(sac)

	keep, rr := r.renewToken(ctx, ac, getShootAccess, rr)
	if rr.ReconcileError != nil {
		return rr
	}

	// cleanup resources which might have been created by a previous version of this request
	if rerr := r.cleanupResources(ctx, getShootAccess, keep, managedResourcesLabels(ac)); rerr != nil {
		rr.ReconcileError = errutils.Errorf("error cleaning up resources on shoot cluster '%s/%s': %s", rerr, sac.Shoot.Namespace, sac.Shoot.Name, rerr.Error())
		return rr
	}

	return rr
}

func (r *AccessRequestReconciler) handleDelete(ctx context.Context, req reconcile.Request, ac *clustersv1alpha1.AccessRequest, getShootAccess shootAccessGetter) ReconcileResult {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Deleting resource")

	rr := ReconcileResult{
		Object:    ac,
		OldObject: ac.DeepCopy(),
	}

	// no need to delete secret, since it has an owner reference
	// delete resources on the shoot cluster

	sac, rerr := getShootAccess()
	if rerr != nil {
		rr.ReconcileError = rerr
		return rr
	}
	getShootAccess = staticShootAccessGetter(sac)

	if rerr := r.cleanupResources(ctx, getShootAccess, nil, managedResourcesLabels(ac)); rerr != nil {
		rr.ReconcileError = errutils.Errorf("error cleaning up resources on shoot cluster '%s/%s': %s", rerr, sac.Shoot.Namespace, sac.Shoot.Name, rerr.Error())
		return rr
	}

	// remove finalizer
	if controllerutil.RemoveFinalizer(ac, providerv1alpha1.ClusterFinalizer) {
		log.Info("Removing finalizer")
		if err := r.PlatformCluster.Client().Patch(ctx, ac, client.MergeFrom(rr.OldObject)); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.NamespacedName.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)
			return rr
		}
	}
	rr.Object = nil // this prevents the controller from trying to update an already deleted resource

	return rr
}

// checkResponsibility checks if this ClusterProvider is responsible for the given AccessRequest.
// If yes, the returned Cluster and Profile are both non-nil, otherwise both are nil.
func (r *AccessRequestReconciler) checkResponsibility(ctx context.Context, ac *clustersv1alpha1.AccessRequest) (*clustersv1alpha1.Cluster, *shared.Profile, errutils.ReasonableError) {
	log := logging.FromContextOrPanic(ctx)

	// get Cluster that the request refers to
	c := &clustersv1alpha1.Cluster{}
	if ac.Spec.ClusterRef != nil {
		c.SetName(ac.Spec.ClusterRef.Name)
		c.SetNamespace(ac.Spec.ClusterRef.Namespace)
	} else if ac.Spec.RequestRef != nil {
		// fetch request to lookup the cluster reference
		cr := &clustersv1alpha1.ClusterRequest{}
		cr.SetName(ac.Spec.RequestRef.Name)
		cr.SetNamespace(ac.Spec.RequestRef.Namespace)
		if err := r.PlatformCluster.Client().Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil, errutils.WithReason(fmt.Errorf("ClusterRequest '%s/%s' not found", cr.Namespace, cr.Name), cconst.ReasonInvalidReference)
			}
			return nil, nil, errutils.WithReason(fmt.Errorf("unable to get ClusterRequest '%s/%s': %w", cr.Namespace, cr.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
		}
		if cr.Status.Phase != clustersv1alpha1.REQUEST_GRANTED {
			return nil, nil, errutils.WithReason(fmt.Errorf("ClusterRequest '%s/%s' is not granted", cr.Namespace, cr.Name), cconst.ReasonInvalidReference)
		}
		if cr.Status.Cluster == nil {
			return nil, nil, errutils.WithReason(fmt.Errorf("ClusterRequest '%s/%s' is granted but does not reference a cluster", cr.Namespace, cr.Name), cconst.ReasonInternalError)
		}
		c.SetName(cr.Status.Cluster.Name)
		c.SetNamespace(cr.Status.Cluster.Namespace)
	} else {
		return nil, nil, errutils.WithReason(fmt.Errorf("invalid AccessRequest resource '%s/%s': neither clusterRef nor requestRef is set", ac.Namespace, ac.Name), cconst.ReasonConfigurationProblem)
	}

	// fetch Cluster resource
	if err := r.PlatformCluster.Client().Get(ctx, client.ObjectKeyFromObject(c), c); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, errutils.WithReason(fmt.Errorf("Cluster '%s/%s' not found", c.Namespace, c.Name), cconst.ReasonInvalidReference)
		}
		return nil, nil, errutils.WithReason(fmt.Errorf("unable to get Cluster '%s/%s': %w", c.Namespace, c.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
	}

	// check if this ClusterProvider is responsible for the Cluster
	p := r.GetProfile(c.Spec.Profile)
	if p == nil {
		log.Info("Ignoring resource due to unknown profile")
		return nil, nil, nil
	}

	return c, p, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AccessRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// watch AccessRequest resources
		For(&clustersv1alpha1.AccessRequest{}).
		WithEventFilter(predicate.And(
			predicate.Or(
				predicate.GenerationChangedPredicate{},
				ctrlutils.DeletionTimestampChangedPredicate{},
				ctrlutils.GotAnnotationPredicate(clustersv1alpha1.OperationAnnotation, clustersv1alpha1.OperationAnnotationValueReconcile),
				ctrlutils.LostAnnotationPredicate(clustersv1alpha1.OperationAnnotation, clustersv1alpha1.OperationAnnotationValueIgnore),
			),
			predicate.Not(
				ctrlutils.HasAnnotationPredicate(clustersv1alpha1.OperationAnnotation, clustersv1alpha1.OperationAnnotationValueIgnore),
			),
		)).
		Owns(&corev1.Secret{}, builder.WithPredicates(
			predicate.Funcs{
				CreateFunc: func(tce event.CreateEvent) bool {
					return false
				},
				UpdateFunc: func(tue event.UpdateEvent) bool {
					return false
				},
				DeleteFunc: func(tde event.TypedDeleteEvent[client.Object]) bool {
					return true
				},
			},
		)).
		Complete(r)
}

func defaultSecretName(ac *clustersv1alpha1.AccessRequest) string {
	return ac.Name
}

type shootAccessGetter func() (*shootAccess, errutils.ReasonableError)

func staticShootAccessGetter(access *shootAccess) shootAccessGetter {
	return func() (*shootAccess, errutils.ReasonableError) {
		return access, nil
	}
}

type shootAccess struct {
	Shoot   *gardenv1beta1.Shoot
	RESTCfg *rest.Config
	Client  client.Client
}
