package landscape

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	clusterutils "github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	errutils "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/controller-utils/pkg/logging"
	testutils "github.com/openmcp-project/controller-utils/pkg/testing"

	clustersv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/clusters/v1alpha1"
	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	cconst "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1/constants"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	"github.com/openmcp-project/cluster-provider-gardener/api/install"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

const ControllerName = "Landscape"

var gardenerScheme = install.InstallGardenerAPIs(runtime.NewScheme())

func NewLandscapeReconciler(rc *shared.RuntimeConfiguration) *LandscapeReconciler {
	return &LandscapeReconciler{
		RuntimeConfiguration: rc,
		TmpKubeconfigDir:     filepath.Join(os.TempDir(), "garden-kubeconfigs"),
	}
}

type LandscapeReconciler struct {
	*shared.RuntimeConfiguration
	// TmpKubeconfigDir is a path to a directory where temporary kubeconfig files can be stored.
	// The directory will be created during startup, if it doesn't exist.
	TmpKubeconfigDir string
}

var _ reconcile.Reconciler = &LandscapeReconciler{}

type ReconcileResult = ctrlutils.ReconcileResult[*providerv1alpha1.Landscape, providerv1alpha1.ConditionStatus]

func (r *LandscapeReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrPanic(ctx).WithName(ControllerName)
	ctx = logging.NewContext(ctx, log)
	log.Info("Starting reconcile")
	rr, lsInt := r.reconcile(ctx, log, req)
	if lsInt == nil && rr.ReconcileError != nil && rr.ReconcileError.Reason() == cconst.ReasonPlatformClusterInteractionProblem {
		// there was a problem communicating with the platform cluster which prevents us from determining the state of the Landscape
		// don't update the internal representation of the Landscape
	} else {
		if lsInt != nil {
			// update internal representation of the Landscape
			apiServer := "<unknown>"
			if lsInt.Cluster != nil && lsInt.Cluster.HasRESTConfig() {
				apiServer = lsInt.Cluster.APIServerEndpoint()
			}
			log.Info("Registering landscape", "apiServer", apiServer, "available", lsInt.Available)
			r.SetLandscape(lsInt)
		} else {
			// remove internal representation of the Landscape
			log.Info("Unregistering landscape")
			r.UnsetLandscape(req.Name)
		}
	}
	// status update
	log.Debug("Updating status")
	return ctrlutils.NewStatusUpdaterBuilder[*providerv1alpha1.Landscape, providerv1alpha1.LandscapePhase, providerv1alpha1.ConditionStatus]().
		WithNestedStruct("CommonStatus").
		WithFieldOverride(ctrlutils.STATUS_FIELD_PHASE, "Phase").
		WithPhaseUpdateFunc(func(obj *providerv1alpha1.Landscape, rr ctrlutils.ReconcileResult[*providerv1alpha1.Landscape, providerv1alpha1.ConditionStatus]) (providerv1alpha1.LandscapePhase, error) {
			if rr.ReconcileError != nil {
				return providerv1alpha1.LANDSCAPE_PHASE_UNAVAILABLE, nil
			}
			// add code below if we start using conditions
			// if len(rr.Conditions) > 0 {
			// 	for _, con := range rr.Conditions {
			// 		if con.GetStatus() != providerv1alpha1.CONDITION_TRUE {
			// 			return providerv1alpha1.LANDSCAPE_PHASE_UNAVAILABLE, nil
			// 		}
			// 	}
			// }
			return providerv1alpha1.LANDSCAPE_PHASE_AVAILABLE, nil
		}).
		// replace the following line with the commented-out code below it if we start using conditions
		WithoutFields(ctrlutils.STATUS_FIELD_CONDITIONS).
		// WithConditionUpdater(func() conditions.Condition[providerv1alpha1.ConditionStatus] {
		// 	return &providerv1alpha1.Condition{}
		// }, true).
		Build().
		UpdateStatus(ctx, r.PlatformCluster.Client(), rr)
}

func (r *LandscapeReconciler) reconcile(ctx context.Context, log logging.Logger, req reconcile.Request) (ReconcileResult, *shared.Landscape) {
	// build internal Landscape object
	lsInt := &shared.Landscape{
		Name: req.Name,
	}

	// get Landscape resource
	ls := &providerv1alpha1.Landscape{}
	if err := r.PlatformCluster.Client().Get(ctx, req.NamespacedName, ls); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debug("Resource not found")
			return ReconcileResult{}, nil
		}
		return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("unable to get resource '%s' from cluster: %w", req.NamespacedName.String(), err), cconst.ReasonPlatformClusterInteractionProblem)}, nil
	}

	// handle operation annotation
	if ls.GetAnnotations() != nil {
		op, ok := ls.GetAnnotations()[clustersv1alpha1.OperationAnnotation]
		if ok {
			switch op {
			case clustersv1alpha1.OperationAnnotationValueIgnore:
				log.Info("Ignoring resource due to ignore operation annotation")
				return ReconcileResult{}, nil
			case clustersv1alpha1.OperationAnnotationValueReconcile:
				log.Debug("Removing reconcile operation annotation from resource")
				if err := ctrlutils.EnsureAnnotation(ctx, r.PlatformCluster.Client(), ls, clustersv1alpha1.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
					return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("error removing operation annotation: %w", err), cconst.ReasonPlatformClusterInteractionProblem)}, nil
				}
			}
		}
	}

	rr := ReconcileResult{
		Object:    ls,
		OldObject: ls.DeepCopy(),
	}

	inDeletion := ls.DeletionTimestamp != nil
	if !inDeletion {
		// CREATE/UPDATE
		log.Info("Creating/updating resource")

		// ensure finalizer
		if controllerutil.AddFinalizer(ls, providerv1alpha1.LandscapeFinalizer) {
			log.Info("Adding finalizer")
			if err := r.PlatformCluster.Client().Patch(ctx, ls, client.MergeFrom(rr.OldObject)); err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.NamespacedName.String(), err), cconst.ReasonPlatformClusterInteractionProblem)
				return rr, nil
			}
		}

		// load Garden cluster kubeconfig
		var restCfg *rest.Config
		if ls.Spec.Access.Inline != "" {
			log.Debug("Garden cluster access via inline kubeconfig")
			var err error
			restCfg, err = clientcmd.RESTConfigFromKubeConfig([]byte(ls.Spec.Access.Inline))
			if err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error loading inline kubeconfig for Landscape '%s': %w", ls.Name, err), cconst.ReasonKubeconfigError)
				return rr, lsInt
			}
		} else if ls.Spec.Access.SecretRef != nil {
			log.Debug("Garden cluster access via secret reference", "secret", fmt.Sprintf("%s/%s", ls.Spec.Access.SecretRef.Namespace, ls.Spec.Access.SecretRef.Name))
			ref := ls.Spec.Access.SecretRef
			secret := &corev1.Secret{}
			if err := r.PlatformCluster.Client().Get(ctx, ctrlutils.ObjectKey(ref.Name, ref.Namespace), secret); err != nil {
				if apierrors.IsNotFound(err) {
					rr.ReconcileError = errutils.WithReason(fmt.Errorf("kubeconfig secret '%s/%s' not found for Landscape '%s': %w", ref.Namespace, ref.Name, ls.Name, err), cconst.ReasonInvalidReference)
					return rr, lsInt
				} else {
					rr.ReconcileError = errutils.WithReason(fmt.Errorf("error getting kubeconfig secret '%s/%s' for Landscape '%s': %w", ref.Namespace, ref.Name, ls.Name, err), cconst.ReasonPlatformClusterInteractionProblem)
					return rr, lsInt
				}
			}
			tmpDir := filepath.Join(r.TmpKubeconfigDir, ref.Namespace, ref.Name)
			if err := os.MkdirAll(tmpDir, os.ModeDir|os.ModePerm); err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating temporary directory '%s' for kubeconfig secret '%s/%s' for Landscape '%s': %w", tmpDir, ref.Namespace, ref.Name, ls.Name, err), cconst.ReasonOperatingSystemProblem)
				return rr, lsInt
			}
			for k, v := range secret.Data {
				if err := os.WriteFile(filepath.Join(tmpDir, k), v, os.ModePerm); err != nil {
					rr.ReconcileError = errutils.WithReason(fmt.Errorf("error writing kubeconfig file '%s/%s' for Landscape '%s': %w", tmpDir, k, ls.Name, err), cconst.ReasonOperatingSystemProblem)
					return rr, lsInt
				}
			}
			log.Debug("Secret contents identified", "keys", strings.Join(sets.List(sets.KeySet(secret.Data)), ", "))
			var err error
			restCfg, err = ctrlutils.LoadKubeconfig(tmpDir)
			if err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error loading kubeconfig for Landscape '%s': %w", ls.Name, err), cconst.ReasonKubeconfigError)
				return rr, lsInt
			}
		} else {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("no access information found for Landscape '%s'", ls.Name), cconst.ReasonConfigurationProblem)
			return rr, lsInt
		}

		log.Debug("REST config for Garden cluster created", "apiServer", restCfg.Host)
		lsInt.Cluster = clusterutils.New(ls.Name).WithRESTConfig(restCfg)

		if err := lsInt.Cluster.InitializeClient(gardenerScheme); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error initializing client for Landscape '%s': %w", ls.Name, err), cconst.ReasonKubeconfigError)
			return rr, lsInt
		}

		// verify that kubeconfig is working by checking access to projects
		ssrr := &authzv1.SelfSubjectRulesReview{
			Spec: authzv1.SelfSubjectRulesReviewSpec{
				Namespace: "*",
			},
		}
		if err := lsInt.Cluster.Client().Create(ctx, ssrr); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating SelfSubjectRulesReview for Landscape '%s': %w", ls.Name, err), cconst.ReasonGardenClusterInteractionProblem)
		}

		ls.Status.Projects = []string{}
		for _, rule := range ssrr.Status.ResourceRules {
			// search for projects where the user has admin access
			if slices.Contains(rule.APIGroups, gardenv1beta1.GroupName) && slices.Contains(rule.Resources, "projects") && slices.Contains(rule.Verbs, "update") {
				ls.Status.Projects = append(ls.Status.Projects, rule.ResourceNames...)
			}
		}
	} else {
		// DELETE
		log.Info("Deleting resource")

		// check if there are any ProviderConfig finalizers left
		pcfs := sets.New[string]()
		for _, f := range ls.Finalizers {
			if strings.HasPrefix(f, providerv1alpha1.ProviderConfigLandscapeFinalizerPrefix) {
				pcfs.Insert(strings.TrimPrefix(f, providerv1alpha1.ProviderConfigLandscapeFinalizerPrefix))
			}
		}
		if pcfs.Len() > 0 {
			remainingFins := strings.Join(sets.List(pcfs), ", ")
			log.Info("Waiting for ProviderConfig finalizers to be removed", "providerConfigs", remainingFins)
			rr.Message = fmt.Sprintf("Landscape is still referenced by the following ProviderConfigs: %s.", remainingFins)
			rr.Reason = cconst.ReasonWaitingForDeletion
			return rr, lsInt
		}
		// remove own finalizer and remove from internal list
		if controllerutil.RemoveFinalizer(ls, providerv1alpha1.LandscapeFinalizer) {
			log.Info("Removing finalizer")
			if err := r.PlatformCluster.Client().Patch(ctx, ls, client.MergeFrom(rr.OldObject)); err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.NamespacedName.String(), err), cconst.ReasonPlatformClusterInteractionProblem)
				return rr, nil
			}
		}
		lsInt = nil     // this causes the internal representation to be removed
		rr.Object = nil // this prevents the controller from trying to update an already deleted resource
	}

	return rr, lsInt
}

// SetupWithManager sets up the controller with the Manager.
// Uses WatchesRawSource() instead of For() because it doesn't watch the primary cluster of the manager.
func (r *LandscapeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(strings.ToLower(ControllerName)).
		WatchesRawSource(source.Kind(r.PlatformCluster.Cluster().GetCache(), &providerv1alpha1.Landscape{}, handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, ls *providerv1alpha1.Landscape) []ctrl.Request {
			return []ctrl.Request{testutils.RequestFromObject(ls)}
		}), ctrlutils.ToTypedPredicate[*providerv1alpha1.Landscape](predicate.And(
			predicate.Or(
				predicate.GenerationChangedPredicate{},
				predicate.AnnotationChangedPredicate{},
				predicate.LabelChangedPredicate{},
			),
			predicate.Not(
				ctrlutils.HasAnnotationPredicate(clustersv1alpha1.OperationAnnotation, clustersv1alpha1.OperationAnnotationValueIgnore),
			),
		)))).
		Complete(r)
}
