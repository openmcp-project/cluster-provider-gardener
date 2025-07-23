package accessrequest

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
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

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	clusterconst "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1/constants"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	cconst "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1/constants"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/cluster"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
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

func NewAccessRequestReconciler(rc *shared.RuntimeConfiguration, eventRecorder record.EventRecorder) *AccessRequestReconciler {
	return &AccessRequestReconciler{
		RuntimeConfiguration: rc,
		eventRecorder:        eventRecorder,
	}
}

type AccessRequestReconciler struct {
	*shared.RuntimeConfiguration
	eventRecorder record.EventRecorder
}

var _ reconcile.Reconciler = &AccessRequestReconciler{}

type ReconcileResult = ctrlutils.ReconcileResult[*clustersv1alpha1.AccessRequest]

func (r *AccessRequestReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrPanic(ctx).WithName(ControllerName)
	ctx = logging.NewContext(ctx, log)
	log.Info("Starting reconcile")
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	rr := r.reconcile(ctx, req)
	// status update
	return ctrlutils.NewOpenMCPStatusUpdaterBuilder[*clustersv1alpha1.AccessRequest]().
		WithNestedStruct("Status").
		WithPhaseUpdateFunc(func(obj *clustersv1alpha1.AccessRequest, rr ReconcileResult) (string, error) {
			if rr.ReconcileError != nil || rr.Object == nil {
				return clustersv1alpha1.REQUEST_PENDING, nil
			}
			return clustersv1alpha1.REQUEST_GRANTED, nil
		}).
		WithConditionUpdater(false).
		WithConditionEvents(r.eventRecorder, conditions.EventPerChange).
		Build().
		UpdateStatus(ctx, r.PlatformCluster.Client(), rr)
}

func (r *AccessRequestReconciler) reconcile(ctx context.Context, req reconcile.Request) ReconcileResult {
	log := logging.FromContextOrPanic(ctx)

	// get AccessRequest resource
	ar := &clustersv1alpha1.AccessRequest{}
	if err := r.PlatformCluster.Client().Get(ctx, req.NamespacedName, ar); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Resource not found")
			return ReconcileResult{}
		}
		return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("unable to get resource '%s' from cluster: %w", req.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)}
	}

	// handle operation annotation
	enforceReconcile := false
	if ar.GetAnnotations() != nil {
		op, ok := ar.GetAnnotations()[openmcpconst.OperationAnnotation]
		if ok {
			switch op {
			case openmcpconst.OperationAnnotationValueIgnore:
				log.Info("Ignoring resource due to ignore operation annotation")
				return ReconcileResult{}
			case openmcpconst.OperationAnnotationValueReconcile:
				enforceReconcile = true
				log.Debug("Removing reconcile operation annotation from resource")
				if err := ctrlutils.EnsureAnnotation(ctx, r.PlatformCluster.Client(), ar, openmcpconst.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
					return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("error removing operation annotation: %w", err), clusterconst.ReasonPlatformClusterInteractionProblem)}
				}
			}
		}
	}

	rr := ReconcileResult{
		Object:     ar,
		OldObject:  ar.DeepCopy(),
		Conditions: []metav1.Condition{},
	}

	c, p, rerr := r.getClusterAndProfile(ctx, ar)
	if rerr != nil {
		rr.Conditions = append(rr.Conditions, metav1.Condition{
			Type:               providerv1alpha1.AccessRequestConditionFoundClusterAndProfile,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: ar.Generation,
			Reason:             rerr.Reason(),
			Message:            rerr.Error(),
		})
		rr.ReconcileError = rerr
		return rr
	}
	rr.Conditions = append(rr.Conditions, metav1.Condition{
		Type:               providerv1alpha1.AccessRequestConditionFoundClusterAndProfile,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: ar.Generation,
	})

	getShootAccess := func() (*shootAccess, errutils.ReasonableError) {
		// get landscape
		ls := r.GetLandscapeForProfile(p)
		if ls == nil {
			return nil, errutils.WithReason(fmt.Errorf("unable to determine landscape"), clusterconst.ReasonInternalError)
		}

		// get shoot
		shoot, rerr := cluster.GetShoot(ctx, ls.Cluster.Client(), p.Project.Namespace, c)
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

	inDeletion := !ar.DeletionTimestamp.IsZero()
	if !inDeletion {
		rr = r.handleCreateOrUpdate(ctx, req, ar, getShootAccess, enforceReconcile, rr)
	} else {
		rr = r.handleDelete(ctx, req, ar, getShootAccess, rr)
	}

	return rr
}

func (r *AccessRequestReconciler) handleCreateOrUpdate(ctx context.Context, req reconcile.Request, ar *clustersv1alpha1.AccessRequest, getShootAccess shootAccessGetter, enforceReconcile bool, rr ReconcileResult) ReconcileResult {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Creating/updating resource")

	if rr.Object.Status.Phase == "" {
		rr.Object.Status.Phase = clustersv1alpha1.REQUEST_PENDING
	}

	createCon := shared.GenerateCreateConditionFunc(&rr)

	// ensure finalizer
	if controllerutil.AddFinalizer(ar, providerv1alpha1.AccessRequestFinalizer) {
		log.Info("Adding finalizer")
		if err := r.PlatformCluster.Client().Patch(ctx, ar, client.MergeFrom(rr.OldObject)); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)
			createCon(providerv1alpha1.ConditionMeta, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
			return rr
		}
	}
	createCon(providerv1alpha1.ConditionMeta, metav1.ConditionTrue, "", "")

	// check if the request is already granted and nothing has changed
	if ar.Status.Phase == clustersv1alpha1.REQUEST_GRANTED && ar.Status.ObservedGeneration == ar.Generation && !enforceReconcile {
		// check if the secret exists and is valid
		s := &corev1.Secret{}
		if err := r.PlatformCluster.Client().Get(ctx, ctrlutils.ObjectKey(ar.Status.SecretRef.Name, ar.Status.SecretRef.Namespace), s); err != nil {
			if !apierrors.IsNotFound(err) {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error getting secret '%s/%s': %w", ar.Status.SecretRef.Namespace, ar.Status.SecretRef.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
				createCon(providerv1alpha1.AccessRequestConditionSecretExistsAndIsValid, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
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
					rr.ReconcileError = errutils.WithReason(fmt.Errorf("error parsing creation timestamp from secret '%s/%s': %w", s.Namespace, s.Name, err), clusterconst.ReasonInternalError)
					createCon(providerv1alpha1.AccessRequestConditionSecretExistsAndIsValid, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
					return rr
				}
				createdAt := time.Unix(tmp, 0)
				tmp, err = strconv.ParseInt(expirationTimestamp, 10, 64)
				if err != nil {
					rr.ReconcileError = errutils.WithReason(fmt.Errorf("error parsing expiration timestamp from secret '%s/%s': %w", s.Namespace, s.Name, err), clusterconst.ReasonInternalError)
					createCon(providerv1alpha1.AccessRequestConditionSecretExistsAndIsValid, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
					return rr
				}
				expiredAt := time.Unix(tmp, 0)
				tokenRenewalTime := createdAt.Add(time.Duration(float64(expiredAt.Sub(createdAt)) * RenewTokenAfterValidityPercentagePassed))
				if time.Now().Before(tokenRenewalTime) {
					// the request is granted, the secret still exists and the token is still valid - nothing to do
					log.Info("Request is already granted, secret still exists, token is still valid - nothing to do")
					rr.Result.RequeueAfter = time.Until(tokenRenewalTime)
					createCon(providerv1alpha1.AccessRequestConditionSecretExistsAndIsValid, metav1.ConditionTrue, "", "")
					return rr
				}
			}
		}
	}

	sac, rerr := getShootAccess()
	if rerr != nil {
		rr.ReconcileError = rerr
		createCon(providerv1alpha1.AccessRequestConditionShootAccess, metav1.ConditionFalse, rerr.Reason(), rerr.Error())
		return rr
	}
	getShootAccess = staticShootAccessGetter(sac)
	createCon(providerv1alpha1.AccessRequestConditionShootAccess, metav1.ConditionTrue, "", "")

	keep, rr := r.renewToken(ctx, ar, getShootAccess, rr)
	if rr.ReconcileError != nil {
		createCon(providerv1alpha1.AccessRequestConditionSecretExistsAndIsValid, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
		return rr
	}
	createCon(providerv1alpha1.AccessRequestConditionSecretExistsAndIsValid, metav1.ConditionTrue, "", "")

	// cleanup resources which might have been created by a previous version of this request
	if rerr := r.cleanupResources(ctx, getShootAccess, keep, managedResourcesLabels(ar)); rerr != nil {
		rr.ReconcileError = errutils.Errorf("error cleaning up resources on shoot cluster '%s/%s': %s", rerr, sac.Shoot.Namespace, sac.Shoot.Name, rerr.Error())
		createCon(providerv1alpha1.AccessRequestConditionCleanup, metav1.ConditionFalse, rerr.Reason(), rerr.Error())
		return rr
	}
	createCon(providerv1alpha1.AccessRequestConditionCleanup, metav1.ConditionTrue, "", "")

	return rr
}

func (r *AccessRequestReconciler) handleDelete(ctx context.Context, req reconcile.Request, ar *clustersv1alpha1.AccessRequest, getShootAccess shootAccessGetter, rr ReconcileResult) ReconcileResult {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Deleting resource")

	// no need to delete secret, since it has an owner reference
	// delete resources on the shoot cluster

	createCon := shared.GenerateCreateConditionFunc(&rr)

	sac, rerr := getShootAccess()
	if rerr != nil {
		rr.ReconcileError = rerr
		createCon(providerv1alpha1.AccessRequestConditionShootAccess, metav1.ConditionFalse, rerr.Reason(), rerr.Error())
		return rr
	}
	getShootAccess = staticShootAccessGetter(sac)
	createCon(providerv1alpha1.AccessRequestConditionShootAccess, metav1.ConditionTrue, "", "")

	if rerr := r.cleanupResources(ctx, getShootAccess, nil, managedResourcesLabels(ar)); rerr != nil {
		rr.ReconcileError = errutils.Errorf("error cleaning up resources on shoot cluster '%s/%s': %s", rerr, sac.Shoot.Namespace, sac.Shoot.Name, rerr.Error())
		createCon(providerv1alpha1.AccessRequestConditionCleanup, metav1.ConditionFalse, rerr.Reason(), rerr.Error())
		return rr
	}
	createCon(providerv1alpha1.AccessRequestConditionCleanup, metav1.ConditionTrue, "", "")

	// remove finalizer
	if controllerutil.RemoveFinalizer(ar, providerv1alpha1.AccessRequestFinalizer) {
		log.Info("Removing finalizer")
		if err := r.PlatformCluster.Client().Patch(ctx, ar, client.MergeFrom(rr.OldObject)); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)
			createCon(providerv1alpha1.ConditionMeta, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
			return rr
		}
	}
	rr.Object = nil // this prevents the controller from trying to update an already deleted resource

	return rr
}

func (r *AccessRequestReconciler) getClusterAndProfile(ctx context.Context, ar *clustersv1alpha1.AccessRequest) (*clustersv1alpha1.Cluster, *shared.Profile, errutils.ReasonableError) {
	log := logging.FromContextOrPanic(ctx)

	// get Cluster that the request refers to
	c := &clustersv1alpha1.Cluster{}
	if ar.Spec.ClusterRef == nil {
		return nil, nil, errutils.WithReason(fmt.Errorf("spec.clusterRef is not set"), cconst.ReasonConfigurationProblem)
	}
	c.SetName(ar.Spec.ClusterRef.Name)
	c.SetNamespace(ar.Spec.ClusterRef.Namespace)

	// fetch Cluster resource
	log.Debug("Fetching Cluster resource", "clusterName", c.Name, "clusterNamespace", c.Namespace)
	if err := r.PlatformCluster.Client().Get(ctx, client.ObjectKeyFromObject(c), c); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, errutils.WithReason(fmt.Errorf("Cluster '%s/%s' not found", c.Namespace, c.Name), clusterconst.ReasonInvalidReference) // nolint:staticcheck
		}
		return nil, nil, errutils.WithReason(fmt.Errorf("unable to get Cluster '%s/%s': %w", c.Namespace, c.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
	}

	p := r.GetProfile(c.Spec.Profile)
	if p == nil {
		return nil, nil, errutils.WithReason(fmt.Errorf("unknown profile '%s' for Cluster '%s/%s'", c.Spec.Profile, c.Namespace, c.Name), cconst.ReasonConfigurationProblem)
	}

	return c, p, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AccessRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// watch AccessRequest resources
		For(&clustersv1alpha1.AccessRequest{}).
		WithEventFilter(predicate.And(
			ctrlutils.HasLabelPredicate(clustersv1alpha1.ProviderLabel, shared.ProviderName()),
			ctrlutils.HasLabelPredicate(clustersv1alpha1.ProfileLabel, ""),
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
