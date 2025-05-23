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
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	clusterutils "github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/controller-utils/pkg/conditions"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	errutils "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/controller-utils/pkg/logging"

	clusterconst "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1/constants"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	cconst "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1/constants"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	"github.com/openmcp-project/cluster-provider-gardener/api/install"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

const ControllerName = "Landscape"
const ProjectConditionPrefix = "Project_"

var gardenerScheme = install.InstallGardenerAPIs(runtime.NewScheme())

// This map is meant for testing purposes only.
// If the Landscape controller reads an inline kubeconfig from a Landscape resource,
// it tries to find the raw bytes as a key in this map.
// If found, the corresponding client will be used instead of constructing one from the bytes.
var FakeClientMappingsForTesting = map[string]client.Client{}

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
	r.Lock.Lock()
	defer r.Lock.Unlock()
	rr, lsInt := r.reconcile(ctx, req)

	apiServer := "<unknown>"
	if lsInt != nil && lsInt.Cluster != nil && lsInt.Cluster.HasRESTConfig() {
		apiServer = lsInt.Cluster.APIServerEndpoint()
	}
	oldLsInt := r.GetLandscape(req.Name)
	if lsInt == nil && rr.ReconcileError != nil && rr.ReconcileError.Reason() == clusterconst.ReasonPlatformClusterInteractionProblem {
		// there was a problem communicating with the platform cluster which prevents us from determining the state of the Landscape
		// don't update the internal representation of the Landscape
	} else {
		if lsInt != nil {
			// update internal representation of the Landscape
			if lsInt.Resource == nil && rr.Object != nil {
				lsInt.Resource = rr.Object.DeepCopy()
			}
			log.Info("Registering landscape", "apiServer", apiServer)
			if err := r.SetLandscape(ctx, lsInt); err != nil {
				rr.ReconcileError = errutils.Join(rr.ReconcileError, errutils.WithReason(fmt.Errorf("error setting internal Landscape representation: %w", err), clusterconst.ReasonInternalError))
			}
		} else if oldLsInt != nil {
			// remove internal representation of the Landscape
			log.Info("Unregistering landscape")
			if err := r.UnsetLandscape(ctx, req.Name); err != nil {
				rr.ReconcileError = errutils.Join(rr.ReconcileError, errutils.WithReason(fmt.Errorf("error removing internal Landscape representation: %w", err), clusterconst.ReasonInternalError))
			}
		}
	}
	// status update
	res, err := ctrlutils.NewStatusUpdaterBuilder[*providerv1alpha1.Landscape, providerv1alpha1.LandscapePhase, providerv1alpha1.ConditionStatus]().
		WithNestedStruct("CommonStatus").
		WithFieldOverride(ctrlutils.STATUS_FIELD_PHASE, "Phase").
		WithPhaseUpdateFunc(landscapePhaseUpdate).
		WithConditionUpdater(func() conditions.Condition[providerv1alpha1.ConditionStatus] {
			return &providerv1alpha1.Condition{}
		}, true).
		WithCustomUpdateFunc(func(obj *providerv1alpha1.Landscape, rr ctrlutils.ReconcileResult[*providerv1alpha1.Landscape, providerv1alpha1.ConditionStatus]) error {
			obj.Status.APIServer = apiServer
			return nil
		}).
		Build().
		UpdateStatus(ctx, r.PlatformCluster.Client(), rr)
	if lsInt != nil {
		lsInt.Resource = rr.Object
		r.UpdateLandscapeResource(lsInt.Resource)
	}

	// try to notify ProviderConfigs that use the Landscape, if the Phase of the internal representation changed
	if (lsInt == nil) != (oldLsInt == nil) || (lsInt != nil && oldLsInt != nil && lsInt.Available() != oldLsInt.Available()) {
		oldPhase := "<nil>"
		if oldLsInt != nil {
			oldPhase = string(oldLsInt.Resource.Status.Phase)
		}
		newPhase := "<nil>"
		if lsInt != nil {
			newPhase = string(lsInt.Resource.Status.Phase)
		}
		log.Info("Internal Landscape phase changed, checking for referencing ProviderConfigs", "oldPhase", oldPhase, "newPhase", newPhase)
		pcs := r.GetProviderConfigurations()
		for _, pc := range pcs {
			if pc.Spec.LandscapeRef.Name == req.Name {
				log.Info("Triggering ProviderConfig reconciliation due to Landscape phase change", "providerConfig", pc.Name, "oldPhase", oldPhase, "newPhase", newPhase)
				r.ReconcileProviderConfig <- event.TypedGenericEvent[*providerv1alpha1.ProviderConfig]{Object: pc}
			}
		}
	}

	return res, err
}

func (r *LandscapeReconciler) reconcile(ctx context.Context, req reconcile.Request) (ReconcileResult, *shared.Landscape) {
	log := logging.FromContextOrPanic(ctx)

	// get Landscape resource
	ls := &providerv1alpha1.Landscape{}
	if err := r.PlatformCluster.Client().Get(ctx, req.NamespacedName, ls); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Resource not found")
			return ReconcileResult{}, nil
		}
		return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("unable to get resource '%s' from cluster: %w", req.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)}, nil
	}

	// handle operation annotation
	if ls.GetAnnotations() != nil {
		op, ok := ls.GetAnnotations()[openmcpconst.OperationAnnotation]
		if ok {
			switch op {
			case openmcpconst.OperationAnnotationValueIgnore:
				log.Info("Ignoring resource due to ignore operation annotation")
				return ReconcileResult{}, nil
			case openmcpconst.OperationAnnotationValueReconcile:
				log.Debug("Removing reconcile operation annotation from resource")
				if err := ctrlutils.EnsureAnnotation(ctx, r.PlatformCluster.Client(), ls, openmcpconst.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
					return ReconcileResult{ReconcileError: errutils.WithReason(fmt.Errorf("error removing operation annotation: %w", err), clusterconst.ReasonPlatformClusterInteractionProblem)}, nil
				}
			}
		}
	}

	inDeletion := ls.DeletionTimestamp != nil
	var rr ReconcileResult
	var lsInt *shared.Landscape
	if !inDeletion {
		rr, lsInt = r.handleCreateOrUpdate(ctx, req, ls)
	} else {
		rr, lsInt = r.handleDelete(ctx, req, ls)
	}

	return rr, lsInt
}

func (r *LandscapeReconciler) handleCreateOrUpdate(ctx context.Context, req reconcile.Request, ls *providerv1alpha1.Landscape) (ReconcileResult, *shared.Landscape) {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Creating/updating resource")

	rr := ReconcileResult{
		Object:    ls,
		OldObject: ls.DeepCopy(),
	}
	// build internal Landscape object
	lsInt := &shared.Landscape{
		Name: req.Name,
	}

	// ensure finalizer
	if controllerutil.AddFinalizer(ls, providerv1alpha1.LandscapeFinalizer) {
		log.Info("Adding finalizer")
		if err := r.PlatformCluster.Client().Patch(ctx, ls, client.MergeFrom(rr.OldObject)); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)
			return rr, nil
		}
	}

	// load Garden cluster kubeconfig
	var restCfg *rest.Config
	var testClient client.Client
	if ls.Spec.Access.Inline != "" {
		log.Debug("Garden cluster access via inline kubeconfig")
		if fakeClient, ok := FakeClientMappingsForTesting[ls.Spec.Access.Inline]; ok {
			testClient = fakeClient
			log.Info("Using injected client for testing - you should never see this message outside of unit tests")
		} else {
			var err error
			restCfg, err = clientcmd.RESTConfigFromKubeConfig([]byte(ls.Spec.Access.Inline))
			if err != nil {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error loading inline kubeconfig for Landscape '%s': %w", ls.Name, err), cconst.ReasonKubeconfigError)
				return rr, lsInt
			}
		}
	} else if ls.Spec.Access.SecretRef != nil {
		log.Debug("Garden cluster access via secret reference", "secret", fmt.Sprintf("%s/%s", ls.Spec.Access.SecretRef.Namespace, ls.Spec.Access.SecretRef.Name))
		ref := ls.Spec.Access.SecretRef
		secret := &corev1.Secret{}
		if err := r.PlatformCluster.Client().Get(ctx, ctrlutils.ObjectKey(ref.Name, ref.Namespace), secret); err != nil {
			if apierrors.IsNotFound(err) {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("kubeconfig secret '%s/%s' not found for Landscape '%s': %w", ref.Namespace, ref.Name, ls.Name, err), clusterconst.ReasonInvalidReference)
				return rr, lsInt
			} else {
				rr.ReconcileError = errutils.WithReason(fmt.Errorf("error getting kubeconfig secret '%s/%s' for Landscape '%s': %w", ref.Namespace, ref.Name, ls.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
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

	if testClient == nil {
		log.Debug("REST config for Garden cluster created", "apiServer", restCfg.Host)
		lsInt.Cluster = clusterutils.New(ls.Name).WithRESTConfig(restCfg)

		if err := lsInt.Cluster.InitializeClient(gardenerScheme); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error initializing client for Landscape '%s': %w", ls.Name, err), cconst.ReasonKubeconfigError)
			return rr, lsInt
		}
	} else {
		lsInt.Cluster = clusters.NewTestClusterFromClient("garden", testClient)
	}

	// verify that kubeconfig is working by checking access to projects
	ssrr := &authzv1.SelfSubjectRulesReview{
		Spec: authzv1.SelfSubjectRulesReviewSpec{
			Namespace: "*",
		},
	}
	ssrr.SetName(ls.Name)
	if err := lsInt.Cluster.Client().Create(ctx, ssrr); err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating SelfSubjectRulesReview for Landscape '%s': %w", ls.Name, err), cconst.ReasonGardenClusterInteractionProblem)
	}

	rr.Conditions = []conditions.Condition[providerv1alpha1.ConditionStatus]{}
	ls.Status.Projects = []providerv1alpha1.ProjectData{}
	for _, rule := range ssrr.Status.ResourceRules {
		// search for projects where the user has admin access
		// Note that this is a somewhat hacky shortcut that abuses the fact that the permissions to manage shoots are coupled with the permissions to update the containing project in Gardener.
		// Technically, we should check the permissions regarding shoots for all projects that we have access to, but this is a good enough approximation for now.
		if slices.Contains(rule.APIGroups, gardenv1beta1.GroupName) && slices.Contains(rule.Resources, "projects") && slices.Contains(rule.Verbs, "update") && slices.Contains(rule.Verbs, "get") {
			for _, prName := range rule.ResourceNames {
				// fetch project to extract project namespace
				log.Debug("Fetching project", "project", prName)
				pr := &gardenv1beta1.Project{}
				prCon := &providerv1alpha1.Condition{
					Type: ProjectConditionPrefix + prName,
				}
				if err := lsInt.Cluster.Client().Get(ctx, ctrlutils.ObjectKey(prName, ""), pr); err != nil {
					prCon.SetStatus(providerv1alpha1.CONDITION_FALSE)
					prCon.SetReason(cconst.ReasonGardenClusterInteractionProblem)
					prCon.SetMessage(fmt.Sprintf("Error getting project '%s': %s", prName, err.Error()))
					log.Debug("Error getting project", "project", prName, "error", err)
					rr.Conditions = append(rr.Conditions, prCon)
					continue
				}
				prNamespace := pr.Spec.Namespace
				if prNamespace == nil || *prNamespace == "" {
					prCon.SetStatus(providerv1alpha1.CONDITION_FALSE)
					prCon.SetReason(cconst.ReasonGardenClusterInteractionProblem)
					prCon.SetMessage(fmt.Sprintf("Project '%s' has no namespace", prName))
					log.Debug("Project has no namespace", "project", prName)
					rr.Conditions = append(rr.Conditions, prCon)
					continue
				}
				prCon.SetStatus(providerv1alpha1.CONDITION_TRUE)
				rr.Conditions = append(rr.Conditions, prCon)
				log.Debug("Project found", "project", prName, "projectNamespace", prNamespace)
				ls.Status.Projects = append(ls.Status.Projects, providerv1alpha1.ProjectData{
					Name:      prName,
					Namespace: *prNamespace,
				})
			}
		}
	}
	// we want to use the cluster's informer to watch for shoot changes
	// but we only have watch permissions in the project namespaces
	// so we set the default namespaces in the cluster cache
	projectNamespaces := map[string]cache.Config{}
	for _, pr := range ls.Status.Projects {
		projectNamespaces[pr.Namespace] = cache.Config{}
	}
	if testClient == nil {
		lsInt.Cluster.WithClusterOptions(clusterutils.DefaultClusterOptions(gardenerScheme), func(o *cluster.Options) {
			o.Cache.DefaultNamespaces = projectNamespaces
		})
		if err := lsInt.Cluster.InitializeClient(gardenerScheme); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error initializing client with cache options for Landscape '%s': %w", ls.Name, err), clusterconst.ReasonInternalError)
			return rr, lsInt
		}
	}

	return rr, lsInt
}

func (r *LandscapeReconciler) handleDelete(ctx context.Context, req reconcile.Request, ls *providerv1alpha1.Landscape) (ReconcileResult, *shared.Landscape) {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Deleting resource")

	rr := ReconcileResult{
		Object:    ls,
		OldObject: ls.DeepCopy(),
	}
	// build internal Landscape object
	lsInt := &shared.Landscape{
		Name: req.Name,
	}

	// check if the landscape is still in use by any provider configs
	referencingProviderConfigs := sets.New[string]()
	pcs := r.GetProviderConfigurations()
	for _, pc := range pcs {
		if pc.Spec.LandscapeRef.Name == ls.Name {
			referencingProviderConfigs.Insert(pc.Name)
		}
	}
	if referencingProviderConfigs.Len() > 0 {
		refsPrint := strings.Join(sets.List(referencingProviderConfigs), ", ")
		log.Info("Waiting for ProviderConfigs to stop referencing the Landscape", "providerConfigs", refsPrint)
		rr.Message = fmt.Sprintf("Landscape is still referenced by the following ProviderConfigs: %s.", refsPrint)
		rr.Reason = cconst.ReasonWaitingForDeletion
		return rr, lsInt
	}
	// remove own finalizer and remove from internal list
	if controllerutil.RemoveFinalizer(ls, providerv1alpha1.LandscapeFinalizer) {
		log.Info("Removing finalizer")
		if err := r.PlatformCluster.Client().Patch(ctx, ls, client.MergeFrom(rr.OldObject)); err != nil {
			rr.ReconcileError = errutils.WithReason(fmt.Errorf("error patching finalizer on resource '%s': %w", req.String(), err), clusterconst.ReasonPlatformClusterInteractionProblem)
			return rr, nil
		}
	}
	rr.Object = nil // this prevents the controller from trying to update an already deleted resource

	return rr, nil
}

func landscapePhaseUpdate(obj *providerv1alpha1.Landscape, rr ctrlutils.ReconcileResult[*providerv1alpha1.Landscape, providerv1alpha1.ConditionStatus]) (providerv1alpha1.LandscapePhase, error) {
	if rr.ReconcileError != nil {
		return providerv1alpha1.LANDSCAPE_PHASE_UNAVAILABLE, nil
	}
	trueCount := 0
	falseCount := 0
	for _, con := range rr.Conditions {
		if con.GetStatus() == providerv1alpha1.CONDITION_TRUE {
			trueCount++
		} else {
			falseCount++
		}
	}
	if trueCount > 0 && falseCount == 0 {
		return providerv1alpha1.LANDSCAPE_PHASE_AVAILABLE, nil
	} else if trueCount > 0 && falseCount > 0 {
		return providerv1alpha1.LANDSCAPE_PHASE_PARTIALLY_AVAILABLE, nil
	} else if trueCount == 0 && falseCount > 0 {
		return providerv1alpha1.LANDSCAPE_PHASE_UNAVAILABLE, nil
	}
	return providerv1alpha1.LANDSCAPE_PHASE_AVAILABLE, nil
}

// SetupWithManager sets up the controller with the Manager.
// Uses WatchesRawSource() instead of For() because it doesn't watch the primary cluster of the manager.
func (r *LandscapeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// watch Landscape resources on the platform cluster
		For(&providerv1alpha1.Landscape{}).
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
		// listen to internally triggered reconciliation requests
		WatchesRawSource(source.TypedChannel(r.ReconcileLandscape, handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, ls *providerv1alpha1.Landscape) []ctrl.Request {
			if ls == nil {
				return nil
			}
			return []ctrl.Request{
				{
					NamespacedName: client.ObjectKey{
						Name: ls.Name,
					},
				},
			}
		}))).
		Complete(r)
}
