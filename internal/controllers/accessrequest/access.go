package accessrequest

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/openmcp-project/controller-utils/pkg/clusteraccess"
	"github.com/openmcp-project/controller-utils/pkg/collections"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	errutils "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/controller-utils/pkg/logging"
	"github.com/openmcp-project/controller-utils/pkg/pairs"
	"github.com/openmcp-project/controller-utils/pkg/resources"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	clusterconst "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1/constants"
	commonapi "github.com/openmcp-project/openmcp-operator/api/common"

	cconst "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1/constants"
	authenticationv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/authentication/v1alpha1"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	oidcv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/external/oidc-webhook-authenticator/apis/authentication/v1alpha1"
	"github.com/openmcp-project/cluster-provider-gardener/api/install"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

const (
	KindRole        = "Role"
	KindClusterRole = "ClusterRole"

	KindRoleBinding        = "RoleBinding"
	KindClusterRoleBinding = "ClusterRoleBinding"
)

// This map is meant for testing purposes only.
// When the AdminKubeconfigRequest sent to the garden cluster returns a kubeconfig,
// it tries to find the raw bytes as a key in this map.
// If found, the corresponding client will be used instead of constructing one from the bytes.
var FakeClientMappingsForTesting = map[string]client.Client{}

// getAdminKubeconfigForShoot uses the AdminKubeconfigRequest subresource of a shoot to get a admin kubeconfig for the given shoot.
func getAdminKubeconfigForShoot(ctx context.Context, c client.Client, shoot *gardenv1beta1.Shoot, desiredValidity time.Duration) ([]byte, error) {
	if shoot == nil {
		return nil, fmt.Errorf("shoot must not be nil")
	}
	expirationSeconds := int64(desiredValidity.Seconds())
	adminKubeconfigRequest := &authenticationv1alpha1.AdminKubeconfigRequest{
		Spec: authenticationv1alpha1.AdminKubeconfigRequestSpec{
			ExpirationSeconds: &expirationSeconds,
		},
	}
	err := c.SubResource("adminkubeconfig").Create(ctx, shoot, adminKubeconfigRequest)
	if err != nil {
		return nil, err
	}
	return adminKubeconfigRequest.Status.Kubeconfig, nil
}

// getTemporaryClientForShoot creates a client.Client for accessing the shoot cluster.
// Also returns the rest.Config used to create the client.
// The token used by the client has a validity of one hour.
func getTemporaryClientForShoot(ctx context.Context, c client.Client, shoot *gardenv1beta1.Shoot) (client.Client, *rest.Config, error) {
	log := logging.FromContextOrPanic(ctx)
	kcfg, err := getAdminKubeconfigForShoot(ctx, c, shoot, time.Hour)
	if err != nil {
		return nil, nil, err
	}
	if fakeClient, ok := FakeClientMappingsForTesting[string(kcfg)]; ok {
		log.Info("Using injected client for testing - you should never see this message outside of unit tests")
		return fakeClient, &rest.Config{}, nil
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kcfg)
	if err != nil {
		return nil, nil, err
	}
	shootClient, err := client.New(cfg, client.Options{Scheme: install.InstallShootAPIs(runtime.NewScheme())})
	if err != nil {
		return nil, nil, err
	}
	return shootClient, cfg, nil
}

func (r *AccessRequestReconciler) cleanupResources(ctx context.Context, getShootAccess shootAccessGetter, keep []client.Object, labels map[string]string) errutils.ReasonableError {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Cleaning up resources that are not required anymore")

	if len(labels) == 0 {
		return errutils.WithReason(fmt.Errorf("no labels provided for cleanup"), clusterconst.ReasonInternalError)
	}
	selector := client.MatchingLabels(labels)

	sac, rerr := getShootAccess()
	if rerr != nil {
		return rerr
	}

	if err := r.cleanupOpenIDConnectResources(ctx, sac, selector, keep); err != nil {
		return err
	}
	if err := r.cleanupRoleBindings(ctx, sac, selector, keep); err != nil {
		return err
	}
	if err := r.cleanupClusterRoleBindings(ctx, sac, selector, keep); err != nil {
		return err
	}
	if err := r.cleanupRoles(ctx, sac, selector, keep); err != nil {
		return err
	}
	if err := r.cleanupClusterRoles(ctx, sac, selector, keep); err != nil {
		return err
	}
	if err := r.cleanupServiceAccounts(ctx, sac, selector, keep); err != nil {
		return err
	}

	return nil
}

func (r *AccessRequestReconciler) cleanupRoleBindings(ctx context.Context, sac *shootAccess, selector client.MatchingLabels, keep []client.Object) errutils.ReasonableError {
	log := logging.FromContextOrPanic(ctx)
	log.Debug("Cleaning up RoleBindings")

	errs := errutils.NewReasonableErrorList()
	rbs := &rbacv1.RoleBindingList{}
	if err := sac.Client.List(ctx, rbs, selector); err != nil {
		errs.Append(errutils.WithReason(fmt.Errorf("error listing RoleBindings: %w", err), cconst.ReasonShootClusterInteractionProblem))
		return errs.Aggregate()
	}
	for _, rb := range rbs.Items {
		keepThis := false
		for _, k := range keep {
			if k.GetName() == rb.Name && k.GetNamespace() == rb.Namespace && k.GetObjectKind().GroupVersionKind().Kind == KindRoleBinding {
				log.Debug("Keeping RoleBinding", "resourceName", rb.Name, "resourceNamespace", rb.Namespace)
				keepThis = true
				break
			}
		}
		if keepThis {
			continue
		}
		log.Debug("Deleting RoleBinding", "resourceName", rb.Name, "resourceNamespace", rb.Namespace)
		if err := sac.Client.Delete(ctx, &rb); err != nil {
			if apierrors.IsNotFound(err) {
				log.Debug("RoleBinding not found", "resourceName", rb.Name, "resourceNamespace", rb.Namespace)
			} else {
				errs.Append(errutils.WithReason(fmt.Errorf("error deleting RoleBinding '%s/%s': %w", rb.Namespace, rb.Name, err), cconst.ReasonShootClusterInteractionProblem))
			}
		}
	}
	return errs.Aggregate()
}

func (r *AccessRequestReconciler) cleanupClusterRoleBindings(ctx context.Context, sac *shootAccess, selector client.MatchingLabels, keep []client.Object) errutils.ReasonableError {
	log := logging.FromContextOrPanic(ctx)
	log.Debug("Cleaning up ClusterRoleBindings")

	errs := errutils.NewReasonableErrorList()
	crbs := &rbacv1.ClusterRoleBindingList{}
	if err := sac.Client.List(ctx, crbs, selector); err != nil {
		errs.Append(errutils.WithReason(fmt.Errorf("error listing ClusterRoleBindings: %w", err), cconst.ReasonShootClusterInteractionProblem))
		return errs.Aggregate()
	}
	for _, crb := range crbs.Items {
		keepThis := false
		for _, k := range keep {
			if k.GetName() == crb.Name && k.GetObjectKind().GroupVersionKind().Kind == KindClusterRoleBinding {
				log.Debug("Keeping ClusterRoleBinding", "resourceName", crb.Name)
				keepThis = true
				break
			}
		}
		if keepThis {
			continue
		}
		log.Debug("Deleting ClusterRoleBinding", "resourceName", crb.Name)
		if err := sac.Client.Delete(ctx, &crb); err != nil {
			if apierrors.IsNotFound(err) {
				log.Debug("ClusterRoleBinding not found", "resourceName", crb.Name)
			} else {
				errs.Append(errutils.WithReason(fmt.Errorf("error deleting ClusterRoleBinding '%s': %w", crb.Name, err), cconst.ReasonShootClusterInteractionProblem))
			}
		}
	}
	return errs.Aggregate()
}

func (r *AccessRequestReconciler) cleanupRoles(ctx context.Context, sac *shootAccess, selector client.MatchingLabels, keep []client.Object) errutils.ReasonableError {
	log := logging.FromContextOrPanic(ctx)
	log.Debug("Cleaning up Roles")

	errs := errutils.NewReasonableErrorList()
	roles := &rbacv1.RoleList{}
	if err := sac.Client.List(ctx, roles, selector); err != nil {
		errs.Append(errutils.WithReason(fmt.Errorf("error listing Roles: %w", err), cconst.ReasonShootClusterInteractionProblem))
		return errs.Aggregate()
	}
	for _, role := range roles.Items {
		keepThis := false
		for _, k := range keep {
			if k.GetName() == role.Name && k.GetNamespace() == role.Namespace && k.GetObjectKind().GroupVersionKind().Kind == KindRole {
				log.Debug("Keeping Role", "resourceName", role.Name, "resourceNamespace", role.Namespace)
				keepThis = true
				break
			}
		}
		if keepThis {
			continue
		}
		log.Debug("Deleting Role", "resourceName", role.Name, "resourceNamespace", role.Namespace)
		if err := sac.Client.Delete(ctx, &role); err != nil {
			if apierrors.IsNotFound(err) {
				log.Debug("Role not found", "resourceName", role.Name, "resourceNamespace", role.Namespace)
			} else {
				errs.Append(errutils.WithReason(fmt.Errorf("error deleting Role '%s/%s': %w", role.Namespace, role.Name, err), cconst.ReasonShootClusterInteractionProblem))
			}
		}
	}
	return errs.Aggregate()
}

func (r *AccessRequestReconciler) cleanupClusterRoles(ctx context.Context, sac *shootAccess, selector client.MatchingLabels, keep []client.Object) errutils.ReasonableError {
	log := logging.FromContextOrPanic(ctx)
	log.Debug("Cleaning up ClusterRoles")

	errs := errutils.NewReasonableErrorList()
	crs := &rbacv1.ClusterRoleList{}
	if err := sac.Client.List(ctx, crs, selector); err != nil {
		errs.Append(errutils.WithReason(fmt.Errorf("error listing ClusterRoles: %w", err), cconst.ReasonShootClusterInteractionProblem))
		return errs.Aggregate()
	}
	for _, cr := range crs.Items {
		keepThis := false
		for _, k := range keep {
			if k.GetName() == cr.Name && k.GetObjectKind().GroupVersionKind().Kind == KindClusterRole {
				log.Debug("Keeping ClusterRole", "resourceName", cr.Name)
				keepThis = true
				break
			}
		}
		if keepThis {
			continue
		}
		log.Debug("Deleting ClusterRole", "resourceName", cr.Name)
		if err := sac.Client.Delete(ctx, &cr); err != nil {
			if apierrors.IsNotFound(err) {
				log.Debug("ClusterRole not found", "resourceName", cr.Name)
			} else {
				errs.Append(errutils.WithReason(fmt.Errorf("error deleting ClusterRole '%s': %w", cr.Name, err), cconst.ReasonShootClusterInteractionProblem))
			}
		}
	}
	return errs.Aggregate()
}

func (r *AccessRequestReconciler) cleanupServiceAccounts(ctx context.Context, sac *shootAccess, selector client.MatchingLabels, keep []client.Object) errutils.ReasonableError {
	log := logging.FromContextOrPanic(ctx)
	log.Debug("Cleaning up ServiceAccounts")

	errs := errutils.NewReasonableErrorList()
	sas := &corev1.ServiceAccountList{}
	if err := sac.Client.List(ctx, sas, selector); err != nil {
		errs.Append(errutils.WithReason(fmt.Errorf("error listing ServiceAccounts: %w", err), cconst.ReasonShootClusterInteractionProblem))
		return errs.Aggregate()
	}
	for _, sa := range sas.Items {
		keepThis := false
		for _, k := range keep {
			if k.GetName() == sa.Name && k.GetNamespace() == sa.Namespace && k.GetObjectKind().GroupVersionKind().Kind == "ServiceAccount" {
				log.Debug("Keeping ServiceAccount", "resourceName", sa.Name, "resourceNamespace", sa.Namespace)
				keepThis = true
				break
			}
		}
		if keepThis {
			continue
		}
		log.Debug("Deleting ServiceAccount", "resourceName", sa.Name, "resourceNamespace", sa.Namespace)
		if err := sac.Client.Delete(ctx, &sa); err != nil {
			if apierrors.IsNotFound(err) {
				log.Debug("ServiceAccount not found", "resourceName", sa.Name, "resourceNamespace", sa.Namespace)
			} else {
				errs.Append(errutils.WithReason(fmt.Errorf("error deleting ServiceAccount '%s/%s': %w", sa.Namespace, sa.Name, err), cconst.ReasonShootClusterInteractionProblem))
			}
		}
	}
	return errs.Aggregate()
}

func (r *AccessRequestReconciler) cleanupOpenIDConnectResources(ctx context.Context, sac *shootAccess, selector client.MatchingLabels, keep []client.Object) errutils.ReasonableError {
	log := logging.FromContextOrPanic(ctx)
	log.Debug("Cleaning up OpenIDConnect resources")

	errs := errutils.NewReasonableErrorList()
	oidcs := &oidcv1alpha1.OpenIDConnectList{}
	if err := sac.Client.List(ctx, oidcs, selector); err != nil {
		errs.Append(errutils.WithReason(fmt.Errorf("error listing OpenIDConnect resources: %w", err), cconst.ReasonShootClusterInteractionProblem))
		return errs.Aggregate()
	}
	for _, oidc := range oidcs.Items {
		keepThis := false
		for _, k := range keep {
			if k.GetName() == oidc.Name && k.GetObjectKind().GroupVersionKind().Kind == "OpenIDConnect" {
				log.Debug("Keeping OpenIDConnect resource", "resourceName", oidc.Name)
				keepThis = true
				break
			}
		}
		if keepThis {
			continue
		}
		log.Debug("Deleting OpenIDConnect resource", "resourceName", oidc.Name)
		if err := sac.Client.Delete(ctx, &oidc); err != nil {
			if apierrors.IsNotFound(err) {
				log.Debug("OpenIDConnect resource not found", "resourceName", oidc.Name)
			} else {
				errs.Append(errutils.WithReason(fmt.Errorf("error deleting OpenIDConnect resource '%s': %w", oidc.Name, err), cconst.ReasonShootClusterInteractionProblem))
			}
		}
	}
	return errs.Aggregate()
}

func (r *AccessRequestReconciler) renewToken(ctx context.Context, ar *clustersv1alpha1.AccessRequest, getShootAccess shootAccessGetter, rr ReconcileResult) ([]client.Object, ReconcileResult) {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Creating new token")

	sac, rerr := getShootAccess()
	if rerr != nil {
		rr.ReconcileError = rerr
		return nil, rr
	}

	// ensure namespace
	_, err := clusteraccess.EnsureNamespace(ctx, sac.Client, shared.AccessRequestServiceAccountNamespace())
	if err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error ensuring AccessRequest namespace '%s' in shoot '%s/%s': %w", shared.AccessRequestServiceAccountNamespace(), sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem)
		return nil, rr
	}

	expectedLabels := pairs.MapToPairs(managedResourcesLabels(ar))
	keep := []client.Object{}

	// ensure service account
	name := ctrlutils.K8sNameUUIDUnsafe(shared.Environment(), shared.ProviderName(), ar.Namespace, ar.Name)
	sa, err := clusteraccess.EnsureServiceAccount(ctx, sac.Client, name, shared.AccessRequestServiceAccountNamespace(), expectedLabels...)
	if err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error ensuring service account '%s/%s' in shoot '%s/%s': %w", shared.AccessRequestServiceAccountNamespace(), name, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem)
		return nil, rr
	}
	if sa.GroupVersionKind().Kind == "" {
		sa.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ServiceAccount"))
	}
	keep = append(keep, sa)

	// ensure roles + bindings
	subjects := []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa.Name, Namespace: sa.Namespace}}
	errs := errutils.NewReasonableErrorList()
	for i, permission := range ar.Spec.Permissions {
		roleName := permission.Name
		if roleName == "" {
			roleName = fmt.Sprintf("openmcp:permission:%s:%d", ctrlutils.K8sNameUUIDUnsafe(shared.Environment(), shared.ProviderName(), ar.Namespace, ar.Name), i)
		}
		if permission.Namespace != "" {
			// ensure role + binding
			log.Debug("Ensuring Role and RoleBinding", "roleName", roleName, "namespace", permission.Namespace)
			rb, r, err := clusteraccess.EnsureRoleAndBinding(ctx, sac.Client, roleName, permission.Namespace, subjects, permission.Rules, expectedLabels...)
			if err != nil {
				errs.Append(errutils.WithReason(fmt.Errorf("error ensuring role '%s/%s' in shoot '%s/%s': %w", permission.Namespace, roleName, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem))
				continue
			}
			if rb.GroupVersionKind().Kind == "" {
				rb.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind(KindRoleBinding))
			}
			keep = append(keep, rb)
			if r.GroupVersionKind().Kind == "" {
				r.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind(KindRole))
			}
			keep = append(keep, r)
		} else {
			// ensure cluster role + binding
			log.Debug("Ensuring ClusterRole and ClusterRoleBinding", "roleName", roleName)
			crb, cr, err := clusteraccess.EnsureClusterRoleAndBinding(ctx, sac.Client, roleName, subjects, permission.Rules, expectedLabels...)
			if err != nil {
				errs.Append(errutils.WithReason(fmt.Errorf("error ensuring cluster role '%s' in shoot '%s/%s': %w", roleName, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem))
				continue
			}
			if crb.GroupVersionKind().Kind == "" {
				crb.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind(KindClusterRoleBinding))
			}
			keep = append(keep, crb)
			if cr.GroupVersionKind().Kind == "" {
				cr.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind(KindClusterRole))
			}
			keep = append(keep, cr)
		}
	}

	// ensure ServiceAccount is bound to (Cluster)Roles
	for i, roleRef := range ar.Spec.RoleRefs {
		roleBindingName := fmt.Sprintf("openmcp:roleref:%s:%d", ctrlutils.K8sNameUUIDUnsafe(shared.Environment(), shared.ProviderName(), ar.Namespace, ar.Name), i)
		if roleRef.Kind == KindRole {
			// Role
			rb, err := clusteraccess.EnsureRoleBinding(ctx, sac.Client, roleBindingName, roleRef.Namespace, roleRef.Name, subjects, expectedLabels...)
			if err != nil {
				errs.Append(errutils.WithReason(fmt.Errorf("error ensuring rolebinding '%s/%s' in shoot '%s/%s': %w", roleRef.Namespace, roleBindingName, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem))
				continue
			}
			if rb.GroupVersionKind().Kind == "" {
				rb.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind(KindRoleBinding))
			}
			keep = append(keep, rb)
		} else {
			// ClusterRole
			crb, err := clusteraccess.EnsureClusterRoleBinding(ctx, sac.Client, roleBindingName, roleRef.Name, subjects, expectedLabels...)
			if err != nil {
				errs.Append(errutils.WithReason(fmt.Errorf("error ensuring clusterrolebinding '%s' in shoot '%s/%s': %w", roleBindingName, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem))
				continue
			}
			if crb.GroupVersionKind().Kind == "" {
				crb.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind(KindClusterRoleBinding))
			}
			keep = append(keep, crb)
		}
	}

	if err := errs.Aggregate(); err != nil {
		rr.ReconcileError = err
		return nil, rr
	}

	// generate token
	token, err := clusteraccess.CreateTokenForServiceAccount(ctx, sac.Client, sa, &DefaultRequestedTokenValidityDuration)
	if err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating token for service account '%s/%s' in shoot '%s/%s': %w", sa.Namespace, sa.Name, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem)
		return nil, rr
	}
	rr.Result.RequeueAfter = time.Until(clusteraccess.ComputeTokenRenewalTimeWithRatio(token.CreationTimestamp, token.ExpirationTimestamp, RenewTokenAfterValidityPercentagePassed))

	// create kubeconfig
	kcfg, err := clusteraccess.CreateTokenKubeconfig(shared.ProviderName(), sac.RESTCfg.Host, sac.RESTCfg.CAData, token.Token)
	if err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating kubeconfig for service account '%s/%s' in shoot '%s/%s': %w", sa.Namespace, sa.Name, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonInternalError)
		return nil, rr
	}

	// create/update secret
	sm := resources.NewSecretMutatorWithStringData(defaultSecretName(ar), ar.Namespace, map[string]string{
		clustersv1alpha1.SecretKeyKubeconfig:          string(kcfg),
		clustersv1alpha1.SecretKeyExpirationTimestamp: strconv.FormatInt(token.ExpirationTimestamp.Unix(), 10),
		clustersv1alpha1.SecretKeyCreationTimestamp:   strconv.FormatInt(token.CreationTimestamp.Unix(), 10),
	}, corev1.SecretTypeOpaque)
	sm.MetadataMutator().WithOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion: clustersv1alpha1.GroupVersion.String(),
			Kind:       "AccessRequest",
			Name:       ar.Name,
			UID:        ar.UID,
			Controller: ptr.To(true),
		},
	})
	s := sm.Empty()
	if err := resources.CreateOrUpdateResource(ctx, r.PlatformCluster.Client(), sm); err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating/updating secret '%s/%s': %w", s.Namespace, s.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
		return nil, rr
	}
	rr.Object.Status.SecretRef = &commonapi.ObjectReference{}
	rr.Object.Status.SecretRef.Name = s.Name
	rr.Object.Status.SecretRef.Namespace = s.Namespace
	rr.Object.Status.Phase = clustersv1alpha1.REQUEST_GRANTED

	return keep, rr
}

func (r *AccessRequestReconciler) ensureOIDCAccess(ctx context.Context, ar *clustersv1alpha1.AccessRequest, getShootAccess shootAccessGetter, rr ReconcileResult) ([]client.Object, ReconcileResult) {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Ensuring OIDC access")

	if ar.Spec.OIDCProvider == nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("OIDCProvider is not specified in AccessRequest '%s/%s'", ar.Namespace, ar.Name), cconst.ReasonInternalError)
		return nil, rr
	}

	expectedLabels := pairs.MapToPairs(managedResourcesLabels(ar))
	keep := []client.Object{}

	sac, rerr := getShootAccess()
	if rerr != nil {
		rr.ReconcileError = rerr
		return nil, rr
	}

	// create or update OpenIDConnect resource
	oidc := &oidcv1alpha1.OpenIDConnect{}
	oidcConfig := ar.Spec.OIDCProvider.Default()
	oidc.Name = ctrlutils.K8sNameUUIDUnsafe(shared.Environment(), shared.ProviderName(), ar.Namespace, ar.Name)
	if _, err := controllerutil.CreateOrUpdate(ctx, sac.Client, oidc, func() error {
		if oidc.Labels == nil {
			oidc.Labels = make(map[string]string, len(expectedLabels))
		}
		maps.Copy(oidc.Labels, pairs.PairsToMap(expectedLabels))
		oidc.Spec.IssuerURL = oidcConfig.Issuer
		oidc.Spec.GroupsClaim = &oidcConfig.GroupsClaim
		oidc.Spec.GroupsPrefix = &oidcConfig.GroupsPrefix
		oidc.Spec.UsernameClaim = &oidcConfig.UsernameClaim
		oidc.Spec.UsernamePrefix = &oidcConfig.UsernamePrefix
		oidc.Spec.ClientID = oidcConfig.ClientID
		return nil
	}); err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating/updating OpenIDConnect resource '%s' in shoot '%s/%s': %w", oidc.Name, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem)
		return nil, rr
	}
	if oidc.GroupVersionKind().Kind == "" {
		oidc.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind("OpenIDConnect"))
	}
	keep = append(keep, oidc)

	errs := errutils.NewReasonableErrorList()
	// create (cluster) roles
	if len(ar.Spec.Permissions) == 0 {
		log.Debug("No permissions specified, skipping (Cluster)Role creation")
	} else {
		for i, permission := range ar.Spec.Permissions {
			roleName := permission.Name
			if roleName == "" {
				roleName = fmt.Sprintf("openmcp:%s:%d", ctrlutils.K8sNameUUIDUnsafe(shared.Environment(), shared.ProviderName(), ar.Namespace, ar.Name), i)
			}
			if permission.Namespace != "" {
				log.Debug("Ensuring Role", "roleName", roleName, "namespace", permission.Namespace)
				r, err := clusteraccess.EnsureRole(ctx, sac.Client, roleName, permission.Namespace, permission.Rules, expectedLabels...)
				if err != nil {
					errs.Append(errutils.WithReason(fmt.Errorf("error ensuring Role '%s/%s' in shoot '%s/%s': %w", permission.Namespace, roleName, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem))
					continue
				}
				if r.GroupVersionKind().Kind == "" {
					r.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind(KindRole))
				}
				keep = append(keep, r)
			} else {
				log.Debug("Ensuring ClusterRole", "roleName", roleName)
				cr, err := clusteraccess.EnsureClusterRole(ctx, sac.Client, roleName, permission.Rules, expectedLabels...)
				if err != nil {
					errs.Append(errutils.WithReason(fmt.Errorf("error ensuring ClusterRole '%s' in shoot '%s/%s': %w", roleName, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem))
					continue
				}
				if cr.GroupVersionKind().Kind == "" {
					cr.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind(KindClusterRole))
				}
				keep = append(keep, cr)
			}
		}
	}

	// create specified (Cluster)RoleBindings
	if len(ar.Spec.OIDCProvider.RoleBindings) == 0 {
		log.Debug("No RoleBindings specified, skipping (Cluster)RoleBinding creation")
	} else {
		for i, roleBinding := range ar.Spec.OIDCProvider.RoleBindings {
			// append username prefix and groups prefix to subjects
			subjects := collections.ProjectSliceToSlice(roleBinding.Subjects, func(sub rbacv1.Subject) rbacv1.Subject {
				switch sub.Kind {
				case rbacv1.UserKind:
					sub.Name = ar.Spec.OIDCProvider.UsernamePrefix + sub.Name
				case rbacv1.GroupKind:
					sub.Name = ar.Spec.OIDCProvider.GroupsPrefix + sub.Name
				}
				return sub
			})
			// ensure (Cluster)RoleBindings
			for j, roleRef := range roleBinding.RoleRefs {
				roleBindingName := fmt.Sprintf("openmcp:%s:%d:%d", ctrlutils.K8sNameUUIDUnsafe(shared.Environment(), shared.ProviderName(), ar.Namespace, ar.Name), i, j)
				if roleRef.Kind == KindRole {
					log.Debug("Ensuring RoleBinding", "roleBindingName", roleBindingName, "namespace", roleRef.Namespace)
					rb, err := clusteraccess.EnsureRoleBinding(ctx, sac.Client, roleBindingName, roleRef.Namespace, roleRef.Name, subjects, expectedLabels...)
					if err != nil {
						errs.Append(errutils.WithReason(fmt.Errorf("error ensuring RoleBinding '%s/%s' in shoot '%s/%s': %w", roleRef.Namespace, roleBindingName, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem))
						continue
					}
					if rb.GroupVersionKind().Kind == "" {
						rb.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind(KindRoleBinding))
					}
					keep = append(keep, rb)
				} else if roleRef.Kind == KindClusterRole {
					log.Debug("Ensuring ClusterRoleBinding", "roleBindingName", roleBindingName)
					crb, err := clusteraccess.EnsureClusterRoleBinding(ctx, sac.Client, roleBindingName, roleRef.Name, subjects, expectedLabels...)
					if err != nil {
						errs.Append(errutils.WithReason(fmt.Errorf("error ensuring ClusterRoleBinding '%s' in shoot '%s/%s': %w", roleBindingName, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem))
						continue
					}
					if crb.GroupVersionKind().Kind == "" {
						crb.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind(KindClusterRoleBinding))
					}
					keep = append(keep, crb)
				} else {
					errs.Append(errutils.WithReason(fmt.Errorf("unknown RoleRef kind '%s' in RoleBinding '%s'", roleRef.Kind, roleBindingName), cconst.ReasonInternalError))
				}
			}
		}
	}
	if err := errs.Aggregate(); err != nil {
		rr.ReconcileError = err
		return nil, rr
	}

	// create kubeconfig
	kcfgOptions := []clusteraccess.CreateOIDCKubeconfigOption{
		clusteraccess.UsePKCE(),
		clusteraccess.WithClusterName(fmt.Sprintf("%s--%s", ar.Spec.ClusterRef.Namespace, ar.Spec.ClusterRef.Name)),
		clusteraccess.WithContextName(fmt.Sprintf("%s--%s--%s", ar.Spec.ClusterRef.Namespace, ar.Spec.ClusterRef.Name, ar.Spec.OIDCProvider.Name)),
	}
	for _, extraScope := range ar.Spec.OIDCProvider.ExtraScopes {
		kcfgOptions = append(kcfgOptions, clusteraccess.WithExtraScope(extraScope))
	}
	kcfg, err := clusteraccess.CreateOIDCKubeconfig(ar.Spec.OIDCProvider.Name,
		sac.RESTCfg.Host,
		sac.RESTCfg.CAData,
		ar.Spec.OIDCProvider.Issuer,
		ar.Spec.OIDCProvider.ClientID,
		kcfgOptions...)
	if err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating kubeconfig for oidc provider '%s' in shoot '%s/%s': %w", ar.Spec.OIDCProvider.Name, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonInternalError)
		return nil, rr
	}

	// create/update secret
	sm := resources.NewSecretMutatorWithStringData(defaultSecretName(ar), ar.Namespace, map[string]string{
		clustersv1alpha1.SecretKeyKubeconfig: string(kcfg),
	}, corev1.SecretTypeOpaque)
	sm.MetadataMutator().WithOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion: clustersv1alpha1.GroupVersion.String(),
			Kind:       "AccessRequest",
			Name:       ar.Name,
			UID:        ar.UID,
			Controller: ptr.To(true),
		},
	})
	s := sm.Empty()
	if err := resources.CreateOrUpdateResource(ctx, r.PlatformCluster.Client(), sm); err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating/updating secret '%s/%s': %w", s.Namespace, s.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
		return nil, rr
	}
	rr.Object.Status.SecretRef = &commonapi.ObjectReference{}
	rr.Object.Status.SecretRef.Name = s.Name
	rr.Object.Status.SecretRef.Namespace = s.Namespace
	rr.Object.Status.Phase = clustersv1alpha1.REQUEST_GRANTED

	return keep, rr
}
