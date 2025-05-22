package accessrequest

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openmcp-project/controller-utils/pkg/clusteraccess"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	errutils "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/controller-utils/pkg/logging"
	"github.com/openmcp-project/controller-utils/pkg/pairs"
	"github.com/openmcp-project/controller-utils/pkg/resources"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	clusterconst "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1/constants"

	cconst "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1/constants"
	authenticationv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/authentication/v1alpha1"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

// getAdminKubeconfigForShoot uses the AdminKubeconfigRequest subresource of a shoot to get a admin kubeconfig for the given shoot.
func getAdminKubeconfigForShoot(ctx context.Context, c client.Client, shoot *gardenv1beta1.Shoot, desiredValidity time.Duration) ([]byte, error) {
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
	kcfg, err := getAdminKubeconfigForShoot(ctx, c, shoot, time.Hour)
	if err != nil {
		return nil, nil, err
	}
	if bytes.Equal(kcfg, []byte("fake")) {
		// inject fake client for tests
		return fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
			SubResourceCreate: func(ctx context.Context, c client.Client, subResourceName string, obj, subResource client.Object, opts ...client.SubResourceCreateOption) error {
				switch subResourceName {
				case "token":
					tr, ok := subResource.(*authenticationv1.TokenRequest)
					if !ok {
						return fmt.Errorf("unexpected object type %T", subResource)
					}
					tr.Status.Token = "fake"
					tr.Status.ExpirationTimestamp = metav1.Time{Time: time.Now().Add(time.Duration(*tr.Spec.ExpirationSeconds * int64(time.Second)))}
					return nil
				}
				// use default logic
				return c.SubResource(subResourceName).Create(ctx, obj, subResource, opts...)
			},
		}).Build(), &rest.Config{}, nil
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kcfg)
	if err != nil {
		return nil, nil, err
	}
	shootClient, err := client.New(cfg, client.Options{})
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
			if k.GetName() == rb.Name && k.GetNamespace() == rb.Namespace && k.GetObjectKind().GroupVersionKind().Kind == "RoleBinding" {
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
			if k.GetName() == crb.Name && k.GetObjectKind().GroupVersionKind().Kind == "ClusterRoleBinding" {
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
			if k.GetName() == role.Name && k.GetNamespace() == role.Namespace && k.GetObjectKind().GroupVersionKind().Kind == "Role" {
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
			if k.GetName() == cr.Name && k.GetObjectKind().GroupVersionKind().Kind == "ClusterRole" {
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

func (r *AccessRequestReconciler) renewToken(ctx context.Context, ac *clustersv1alpha1.AccessRequest, getShootAccess shootAccessGetter, rr ReconcileResult) ([]client.Object, ReconcileResult) {
	log := logging.FromContextOrPanic(ctx)
	log.Info("Creating new token")

	sac, rerr := getShootAccess()
	if rerr != nil {
		rr.ReconcileError = rerr
		return nil, rr
	}

	// ensure namespace
	_, err := clusteraccess.EnsureNamespace(ctx, sac.Client, shared.AccessRequestSANamespace())
	if err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error ensuring AccessRequest namespace '%s' in shoot '%s/%s': %w", shared.AccessRequestSANamespace(), sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem)
		return nil, rr
	}

	expectedLabels := pairs.MapToPairs(managedResourcesLabels(ac))
	keep := []client.Object{}

	// ensure service account
	name := ctrlutils.K8sNameHash(shared.Environment(), shared.ProviderName(), ac.Namespace, ac.Name)
	sa, err := clusteraccess.EnsureServiceAccount(ctx, sac.Client, name, shared.AccessRequestSANamespace(), expectedLabels...)
	if err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error ensuring service account '%s/%s' in shoot '%s/%s': %w", shared.AccessRequestSANamespace(), name, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem)
		return nil, rr
	}
	if sa.GroupVersionKind().Kind == "" {
		sa.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ServiceAccount"))
	}
	keep = append(keep, sa)

	// ensure roles + bindings
	subjects := []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa.Name, Namespace: sa.Namespace}}
	errs := errutils.NewReasonableErrorList()
	for i, permission := range ac.Spec.Permissions {
		roleName := "openmcp:" + ctrlutils.K8sNameHash(shared.Environment(), shared.ProviderName(), ac.Namespace, ac.Name, strconv.Itoa(i))
		if permission.Namespace != "" {
			// ensure role + binding
			rb, r, err := clusteraccess.EnsureRoleAndBinding(ctx, sac.Client, roleName, permission.Namespace, subjects, permission.Rules, expectedLabels...)
			if err != nil {
				errs.Append(errutils.WithReason(fmt.Errorf("error ensuring role '%s/%s' in shoot '%s/%s': %w", permission.Namespace, roleName, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem))
				continue
			}
			if rb.GroupVersionKind().Kind == "" {
				rb.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind("RoleBinding"))
			}
			keep = append(keep, rb)
			if r.GroupVersionKind().Kind == "" {
				r.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind("Role"))
			}
			keep = append(keep, r)
		} else {
			// ensure cluster role + binding
			crb, cr, err := clusteraccess.EnsureClusterRoleAndBinding(ctx, sac.Client, roleName, subjects, permission.Rules, expectedLabels...)
			if err != nil {
				errs.Append(errutils.WithReason(fmt.Errorf("error ensuring cluster role '%s' in shoot '%s/%s': %w", roleName, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem))
				continue
			}
			if crb.GroupVersionKind().Kind == "" {
				crb.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind("ClusterRoleBinding"))
			}
			keep = append(keep, crb)
			if cr.GroupVersionKind().Kind == "" {
				cr.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind("ClusterRole"))
			}
			keep = append(keep, cr)
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
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating kubeconfig for service account '%s/%s' in shoot '%s/%s': %w", sa.Namespace, sa.Name, sac.Shoot.Namespace, sac.Shoot.Name, err), cconst.ReasonShootClusterInteractionProblem)
		return nil, rr
	}

	// create/update secret
	sm := resources.NewSecretMutatorWithStringData(defaultSecretName(ac), ac.Namespace, map[string]string{
		SecretKeyKubeconfig:          string(kcfg),
		SecretKeyExpirationTimestamp: strconv.FormatInt(token.ExpirationTimestamp.Unix(), 10),
		SecretKeyCreationTimestamp:   strconv.FormatInt(token.CreationTimestamp.Unix(), 10),
	}, corev1.SecretTypeOpaque)
	sm.MetadataMutator().WithOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion: clustersv1alpha1.GroupVersion.String(),
			Kind:       "AccessRequest",
			Name:       ac.Name,
			UID:        ac.UID,
			Controller: ptr.To(true),
		},
	})
	s := sm.Empty()
	if err := resources.CreateOrUpdateResource(ctx, r.PlatformCluster.Client(), sm); err != nil {
		rr.ReconcileError = errutils.WithReason(fmt.Errorf("error creating/updating secret '%s/%s': %w", s.Namespace, s.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
		return nil, rr
	}
	rr.Object.Status.SecretRef = &clustersv1alpha1.NamespacedObjectReference{}
	rr.Object.Status.SecretRef.Name = s.Name
	rr.Object.Status.SecretRef.Namespace = s.Namespace
	rr.Object.Status.Phase = clustersv1alpha1.REQUEST_GRANTED

	return keep, rr
}
