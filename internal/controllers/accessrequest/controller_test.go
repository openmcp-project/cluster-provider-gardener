package accessrequest_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	testutils "github.com/openmcp-project/controller-utils/pkg/testing"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	authenticationv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/authentication/v1alpha1"
	oidcv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/external/oidc-webhook-authenticator/apis/authentication/v1alpha1"
	"github.com/openmcp-project/cluster-provider-gardener/api/install"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/accessrequest"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/landscape"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

const (
	platformCluster = "platform"
	gardenCluster   = "garden"
	shootCluster    = "shoot"
	arRec           = "accessrequest"
)

var providerScheme = install.InstallProviderAPIs(runtime.NewScheme())
var gardenScheme = install.InstallGardenerAPIs(runtime.NewScheme())
var shootScheme = install.InstallShootAPIs(runtime.NewScheme())

func defaultTestSetup(testDirPathSegments ...string) (*accessrequest.AccessRequestReconciler, *testutils.ComplexEnvironment) {
	env := testutils.NewComplexEnvironmentBuilder().
		WithFakeClient(platformCluster, providerScheme).
		WithFakeClient(gardenCluster, gardenScheme).
		WithFakeClient(shootCluster, shootScheme).
		WithInitObjectPath(platformCluster, append(testDirPathSegments, "platform")...).
		WithInitObjectPath(gardenCluster, append(testDirPathSegments, "garden")...).
		WithFakeClientBuilderCall(gardenCluster, "WithInterceptorFuncs", interceptor.Funcs{
			SubResourceCreate: func(ctx context.Context, c client.Client, subResourceName string, obj, subResource client.Object, opts ...client.SubResourceCreateOption) error {
				switch subResourceName {
				case "adminkubeconfig": // make adminkubeconfig requests work with fake client
					adminKubeconfigRequest, ok := subResource.(*authenticationv1alpha1.AdminKubeconfigRequest)
					if !ok {
						return fmt.Errorf("unexpected object type %T", subResource)
					}
					adminKubeconfigRequest.Status.Kubeconfig = []byte("fake")
					adminKubeconfigRequest.Status.ExpirationTimestamp = metav1.Time{Time: time.Now().Add(time.Hour)}
					return nil
				}
				// use default logic
				return c.SubResource(subResourceName).Create(ctx, obj, subResource, opts...)
			},
		}).
		WithFakeClientBuilderCall(shootCluster, "WithInterceptorFuncs", interceptor.Funcs{
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
		}).
		WithFakeClientBuilderCall(platformCluster, "WithInterceptorFuncs", interceptor.Funcs{
			Create: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if s, ok := obj.(*corev1.Secret); ok {
					if len(s.StringData) > 0 && len(s.Data) == 0 {
						// convert StringData to Data
						s.Data = make(map[string][]byte, len(s.StringData))
						for k, v := range s.StringData {
							s.Data[k] = []byte(v)
						}
						s.StringData = nil
					}
					return client.Create(ctx, s, opts...)
				}
				// use default logic for all other objects
				return client.Create(ctx, obj, opts...)
			},
			Update: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if s, ok := obj.(*corev1.Secret); ok {
					if len(s.StringData) > 0 && len(s.Data) == 0 {
						// convert StringData to Data
						s.Data = make(map[string][]byte, len(s.StringData))
						for k, v := range s.StringData {
							s.Data[k] = []byte(v)
						}
						s.StringData = nil
					}
					return client.Update(ctx, s, opts...)
				}
				// use default logic for all other objects
				return client.Update(ctx, obj, opts...)
			},
		}).
		WithReconcilerConstructor(arRec, func(c ...client.Client) reconcile.Reconciler {
			rc := shared.NewRuntimeConfiguration(clusters.NewTestClusterFromClient(platformCluster, c[0]), nil)
			return accessrequest.NewAccessRequestReconciler(rc, nil)
		}, platformCluster).
		Build()

	landscape.FakeClientMappingsForTesting = map[string]client.Client{
		"fake": env.Client(gardenCluster),
	}
	accessrequest.FakeClientMappingsForTesting = map[string]client.Client{
		"fake": env.Client(shootCluster),
	}
	arr, ok := env.Reconciler(arRec).(*accessrequest.AccessRequestReconciler)
	Expect(ok).To(BeTrue(), "Reconciler is not of type AccessRequestReconciler")
	return arr, env
}

var _ = Describe("AccessRequest Controller", func() {

	var (
		arr *accessrequest.AccessRequestReconciler
		env *testutils.ComplexEnvironment
	)

	BeforeEach(func() {
		arr, env = defaultTestSetup("..", "cluster", "testdata", "test-05")

		// fake landscape
		ls := &providerv1alpha1.Landscape{}
		ls.SetName("my-landscape")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ls), ls)).To(Succeed())
		Expect(arr.SetLandscape(env.Ctx, &shared.Landscape{
			Name:     ls.Name,
			Cluster:  clusters.NewTestClusterFromClient(gardenCluster, env.Client(gardenCluster)),
			Resource: ls,
		})).To(Succeed())

		// fake profile
		pc := &providerv1alpha1.ProviderConfig{}
		pc.SetName("gcp")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(pc), pc)).To(Succeed())
		p := &shared.Profile{
			ProviderConfig: pc,
			RuntimeData: shared.RuntimeData{
				Project: providerv1alpha1.ProjectData{
					Name:      "my-project",
					Namespace: "garden-my-project",
				},
			},
		}
		arr.SetProfileForProviderConfiguration(pc.Name, p)
	})

	Context("Token-based access", func() {

		It("should grant access to a cluster", func() {
			ar := &clustersv1alpha1.AccessRequest{}
			ar.SetName("my-access")
			ar.SetNamespace("foo")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ar), ar)).To(Succeed())
			now := time.Now()
			rr := env.ShouldReconcile(arRec, testutils.RequestFromObject(ar))
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ar), ar)).To(Succeed())

			// verify access request status
			Expect(ar.Status.Phase).To(Equal(clustersv1alpha1.REQUEST_GRANTED))
			Expect(ar.Status.SecretRef).ToNot(BeNil())
			sName := ar.Status.SecretRef.Name
			sNamespace := ar.Status.SecretRef.Namespace
			Expect(sName).ToNot(BeEmpty())
			Expect(sNamespace).ToNot(BeEmpty())

			// verify secret
			s := &corev1.Secret{}
			s.SetName(sName)
			s.SetNamespace(sNamespace)
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(s), s)).To(Succeed())
			Expect(s.OwnerReferences).To(ConsistOf(MatchFields(IgnoreExtras, Fields{
				"APIVersion": Equal(clustersv1alpha1.GroupVersion.String()),
				"Kind":       Equal("AccessRequest"),
				"Name":       Equal(ar.Name),
				"UID":        Equal(ar.UID),
				"Controller": PointTo(BeTrue()),
			})))
			Expect(s.Data).To(HaveKey(clustersv1alpha1.SecretKeyKubeconfig))
			Expect(s.Data).To(HaveKey(clustersv1alpha1.SecretKeyCreationTimestamp))
			ctr, err := strconv.Atoi(string(s.Data[clustersv1alpha1.SecretKeyCreationTimestamp]))
			Expect(err).ToNot(HaveOccurred())
			ct := time.Unix(int64(ctr), 0)
			Expect(ct).To(BeTemporally("~", now, time.Second))
			Expect(s.Data).To(HaveKey(clustersv1alpha1.SecretKeyExpirationTimestamp))
			etr, err := strconv.Atoi(string(s.Data[clustersv1alpha1.SecretKeyExpirationTimestamp]))
			Expect(err).ToNot(HaveOccurred())
			et := time.Unix(int64(etr), 0)
			Expect(et).To(BeTemporally("~", now.Add(accessrequest.DefaultRequestedTokenValidityDuration), time.Second))

			// verify renewal time
			Expect(rr.RequeueAfter).To(BeNumerically("<", et.Sub(ct)))

			// verify resources on shoot cluster
			labelSelector := client.MatchingLabels{
				providerv1alpha1.ManagedByNameLabel:      ar.Name,
				providerv1alpha1.ManagedByNamespaceLabel: ar.Namespace,
			}

			// namespace for service accounts
			arns := &corev1.Namespace{}
			arns.SetName(shared.AccessRequestServiceAccountNamespace())
			Expect(env.Client(shootCluster).Get(env.Ctx, client.ObjectKeyFromObject(arns), arns)).To(Succeed())

			// service account
			sal := &corev1.ServiceAccountList{}
			Expect(env.Client(shootCluster).List(env.Ctx, sal, labelSelector, client.InNamespace(shared.AccessRequestServiceAccountNamespace()))).To(Succeed())
			Expect(sal.Items).To(HaveLen(1))
			sa := &sal.Items[0]

			// clusterrole + binding
			crl := &rbacv1.ClusterRoleList{}
			Expect(env.Client(shootCluster).List(env.Ctx, crl, labelSelector)).To(Succeed())
			Expect(crl.Items).To(HaveLen(1))
			cr := &crl.Items[0]
			Expect(cr.Rules).To(BeEquivalentTo(ar.Spec.Permissions[0].Rules))
			crbl := &rbacv1.ClusterRoleBindingList{}
			Expect(env.Client(shootCluster).List(env.Ctx, crbl, labelSelector)).To(Succeed())
			Expect(crbl.Items).To(HaveLen(1))
			crb := &crbl.Items[0]
			Expect(crb.RoleRef.Name).To(Equal(cr.Name))
			Expect(crb.Subjects).To(HaveLen(1))
			Expect(crb.Subjects[0].Kind).To(Equal(rbacv1.ServiceAccountKind))
			Expect(crb.Subjects[0].Name).To(Equal(sa.Name))
			Expect(crb.Subjects[0].Namespace).To(Equal(sa.Namespace))

			// role + binding
			rl := &rbacv1.RoleList{}
			Expect(env.Client(shootCluster).List(env.Ctx, rl, labelSelector, client.InNamespace(ar.Spec.Permissions[1].Namespace))).To(Succeed())
			Expect(rl.Items).To(HaveLen(1))
			r := &rl.Items[0]
			Expect(r.Rules).To(BeEquivalentTo(ar.Spec.Permissions[1].Rules))
			rbl := &rbacv1.RoleBindingList{}
			Expect(env.Client(shootCluster).List(env.Ctx, rbl, labelSelector, client.InNamespace(ar.Spec.Permissions[1].Namespace))).To(Succeed())
			Expect(r.Name).To(Equal(ar.Spec.Permissions[1].Name))
			Expect(rbl.Items).To(HaveLen(1))
			rb := &rbl.Items[0]
			Expect(rb.RoleRef.Name).To(Equal(r.Name))
			Expect(rb.Subjects).To(HaveLen(1))
			Expect(rb.Subjects[0].Kind).To(Equal(rbacv1.ServiceAccountKind))
			Expect(rb.Subjects[0].Name).To(Equal(sa.Name))
			Expect(rb.Subjects[0].Namespace).To(Equal(sa.Namespace))
		})

		It("should remove all resources again when the accessrequest is deleted", func() {
			ar := &clustersv1alpha1.AccessRequest{}
			ar.SetName("my-access")
			ar.SetNamespace("foo")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ar), ar)).To(Succeed())
			env.ShouldReconcile(arRec, testutils.RequestFromObject(ar))
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ar), ar)).To(Succeed())
			Expect(ar.Status.Phase).To(Equal(clustersv1alpha1.REQUEST_GRANTED))
			Expect(ar.Status.SecretRef).ToNot(BeNil())
			sName := ar.Status.SecretRef.Name
			sNamespace := ar.Status.SecretRef.Namespace
			Expect(sName).ToNot(BeEmpty())
			Expect(sNamespace).ToNot(BeEmpty())
			s := &corev1.Secret{}
			s.SetName(sName)
			s.SetNamespace(sNamespace)
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(s), s)).To(Succeed())

			// delete access request
			Expect(env.Client(platformCluster).Delete(env.Ctx, ar)).To(Succeed())
			rr := env.ShouldReconcile(arRec, testutils.RequestFromObject(ar))
			Expect(rr.RequeueAfter).To(BeZero(), "Reconciliation should not requeue after deletion")

			// verify access request deletion
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ar), ar)).To(MatchError(apierrors.IsNotFound, "accessequest should be deleted"))

			// verify that resources on the shoot cluster have been deleted
			labelSelector := client.MatchingLabels{
				providerv1alpha1.ManagedByNameLabel:      ar.Name,
				providerv1alpha1.ManagedByNamespaceLabel: ar.Namespace,
			}

			// namespace for serviceaccounts should not be deleted
			arns := &corev1.Namespace{}
			arns.SetName(shared.AccessRequestServiceAccountNamespace())
			Expect(env.Client(shootCluster).Get(env.Ctx, client.ObjectKeyFromObject(arns), arns)).To(Succeed())

			// serviceaccount
			sal := &corev1.ServiceAccountList{}
			Expect(env.Client(shootCluster).List(env.Ctx, sal, labelSelector, client.InNamespace(shared.AccessRequestServiceAccountNamespace()))).To(Succeed())
			Expect(sal.Items).To(BeEmpty(), "ServiceAccount should be deleted")

			// clusterrole + binding
			crl := &rbacv1.ClusterRoleList{}
			Expect(env.Client(shootCluster).List(env.Ctx, crl, labelSelector)).To(Succeed())
			Expect(crl.Items).To(BeEmpty(), "ClusterRole should be deleted")
			crbl := &rbacv1.ClusterRoleBindingList{}
			Expect(env.Client(shootCluster).List(env.Ctx, crbl, labelSelector)).To(Succeed())
			Expect(crbl.Items).To(BeEmpty(), "ClusterRoleBinding should be deleted")

			// role + binding
			rl := &rbacv1.RoleList{}
			Expect(env.Client(shootCluster).List(env.Ctx, rl, labelSelector, client.InNamespace(ar.Spec.Permissions[1].Namespace))).To(Succeed())
			Expect(rl.Items).To(BeEmpty(), "Role should be deleted")
			rbl := &rbacv1.RoleBindingList{}
			Expect(env.Client(shootCluster).List(env.Ctx, rbl, labelSelector, client.InNamespace(ar.Spec.Permissions[1].Namespace))).To(Succeed())
			Expect(rbl.Items).To(BeEmpty(), "RoleBinding should be deleted")
		})

		It("should recreate the secret if it got deleted", func() {
			ar := &clustersv1alpha1.AccessRequest{}
			ar.SetName("my-access")
			ar.SetNamespace("foo")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ar), ar)).To(Succeed())
			env.ShouldReconcile(arRec, testutils.RequestFromObject(ar))
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ar), ar)).To(Succeed())

			// verify access request status
			Expect(ar.Status.Phase).To(Equal(clustersv1alpha1.REQUEST_GRANTED))
			Expect(ar.Status.SecretRef).ToNot(BeNil())
			sName := ar.Status.SecretRef.Name
			sNamespace := ar.Status.SecretRef.Namespace
			Expect(sName).ToNot(BeEmpty())
			Expect(sNamespace).ToNot(BeEmpty())

			// verify secret
			s := &corev1.Secret{}
			s.SetName(sName)
			s.SetNamespace(sNamespace)
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(s), s)).To(Succeed())

			// delete secret
			Expect(env.Client(platformCluster).Delete(env.Ctx, s)).To(Succeed())
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(s), s)).To(MatchError(apierrors.IsNotFound, "secret should be deleted"))
			env.ShouldReconcile(arRec, testutils.RequestFromObject(ar))
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(s), s)).To(Succeed())
		})

	})

	Context("OIDC-based access", func() {

		It("should grant access to a cluster", func() {
			ar := &clustersv1alpha1.AccessRequest{}
			ar.SetName("my-access-oidc")
			ar.SetNamespace("foo")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ar), ar)).To(Succeed())
			env.ShouldReconcile(arRec, testutils.RequestFromObject(ar))
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ar), ar)).To(Succeed())

			// verify access request status
			Expect(ar.Status.Phase).To(Equal(clustersv1alpha1.REQUEST_GRANTED))
			Expect(ar.Status.SecretRef).ToNot(BeNil())
			sName := ar.Status.SecretRef.Name
			sNamespace := ar.Status.SecretRef.Namespace
			Expect(sName).ToNot(BeEmpty())
			Expect(sNamespace).ToNot(BeEmpty())

			// verify secret
			s := &corev1.Secret{}
			s.SetName(sName)
			s.SetNamespace(sNamespace)
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(s), s)).To(Succeed())
			Expect(s.OwnerReferences).To(ConsistOf(MatchFields(IgnoreExtras, Fields{
				"APIVersion": Equal(clustersv1alpha1.GroupVersion.String()),
				"Kind":       Equal("AccessRequest"),
				"Name":       Equal(ar.Name),
				"UID":        Equal(ar.UID),
				"Controller": PointTo(BeTrue()),
			})))
			Expect(s.Data).To(HaveKey(clustersv1alpha1.SecretKeyKubeconfig))

			// verify resources on shoot cluster
			labelSelector := client.MatchingLabels{
				providerv1alpha1.ManagedByNameLabel:      ar.Name,
				providerv1alpha1.ManagedByNamespaceLabel: ar.Namespace,
			}

			// OpenIDConnect resource
			oidc := &oidcv1alpha1.OpenIDConnect{}
			oidc.Name = ctrlutils.K8sNameUUIDUnsafe(shared.Environment(), shared.ProviderName(), ar.Namespace, ar.Name)
			Expect(env.Client(shootCluster).Get(env.Ctx, client.ObjectKeyFromObject(oidc), oidc)).To(Succeed())
			Expect(oidc.Spec.IssuerURL).To(Equal(ar.Spec.OIDCProvider.Issuer))
			Expect(oidc.Spec.ClientID).To(Equal(ar.Spec.OIDCProvider.ClientID))
			usernamePrefix := ar.Spec.OIDCProvider.UsernamePrefix
			if !strings.HasSuffix(usernamePrefix, ":") {
				usernamePrefix += ":"
			}
			Expect(*oidc.Spec.UsernamePrefix).To(Equal(usernamePrefix))
			groupsPrefix := ar.Spec.OIDCProvider.GroupsPrefix
			if !strings.HasSuffix(groupsPrefix, ":") {
				groupsPrefix += ":"
			}
			Expect(*oidc.Spec.GroupsPrefix).To(Equal(groupsPrefix))
			Expect(*oidc.Spec.UsernameClaim).To(Equal(ar.Spec.OIDCProvider.UsernameClaim))
			Expect(*oidc.Spec.GroupsClaim).To(Equal(ar.Spec.OIDCProvider.GroupsClaim))

			// clusterrole + binding
			crl := &rbacv1.ClusterRoleList{}
			Expect(env.Client(shootCluster).List(env.Ctx, crl, labelSelector)).To(Succeed())
			Expect(crl.Items).To(HaveLen(1))
			cr := &crl.Items[0]
			Expect(cr.Rules).To(BeEquivalentTo(ar.Spec.Permissions[0].Rules))
			Expect(cr.Name).To(Equal(ar.Spec.Permissions[0].Name))
			crbl := &rbacv1.ClusterRoleBindingList{}
			Expect(env.Client(shootCluster).List(env.Ctx, crbl, labelSelector)).To(Succeed())
			Expect(crbl.Items).To(HaveLen(1))
			crb := &crbl.Items[0]
			Expect(crb.RoleRef.Name).To(Equal(cr.Name))
			Expect(crb.Subjects).To(HaveLen(len(ar.Spec.OIDCProvider.RoleBindings[0].Subjects)))
			for _, subject := range ar.Spec.OIDCProvider.RoleBindings[0].Subjects {
				expected := rbacv1.Subject{
					Kind:      subject.Kind,
					Namespace: subject.Namespace,
				}
				switch expected.Kind {
				case rbacv1.GroupKind:
					expected.Name = ar.Spec.OIDCProvider.Default().GroupsPrefix + subject.Name
				case rbacv1.UserKind:
					expected.Name = ar.Spec.OIDCProvider.Default().UsernamePrefix + subject.Name
				default:
					expected.Name = subject.Name
				}
				Expect(crb.Subjects).To(ContainElement(expected))
			}

			// role + binding
			rl := &rbacv1.RoleList{}
			Expect(env.Client(shootCluster).List(env.Ctx, rl, labelSelector, client.InNamespace(ar.Spec.Permissions[1].Namespace))).To(Succeed())
			Expect(rl.Items).To(HaveLen(1))
			r := &rl.Items[0]
			Expect(r.Name).To(Equal(ar.Spec.Permissions[1].Name))
			Expect(r.Rules).To(BeEquivalentTo(ar.Spec.Permissions[1].Rules))
			rbl := &rbacv1.RoleBindingList{}
			Expect(env.Client(shootCluster).List(env.Ctx, rbl, labelSelector, client.InNamespace(ar.Spec.Permissions[1].Namespace))).To(Succeed())
			Expect(rbl.Items).To(HaveLen(1))
			rb := &rbl.Items[0]
			Expect(rb.RoleRef.Name).To(Equal(r.Name))
			Expect(rb.Subjects).To(HaveLen(len(ar.Spec.OIDCProvider.RoleBindings[0].Subjects)))
			for _, subject := range ar.Spec.OIDCProvider.RoleBindings[0].Subjects {
				expected := rbacv1.Subject{
					Kind:      subject.Kind,
					Namespace: subject.Namespace,
				}
				switch expected.Kind {
				case rbacv1.GroupKind:
					expected.Name = ar.Spec.OIDCProvider.Default().GroupsPrefix + subject.Name
				case rbacv1.UserKind:
					expected.Name = ar.Spec.OIDCProvider.Default().UsernamePrefix + subject.Name
				default:
					expected.Name = subject.Name
				}
				Expect(rb.Subjects).To(ContainElement(expected))
			}
		})

		It("should remove all resources again when the accessrequest is deleted", func() {
			ar := &clustersv1alpha1.AccessRequest{}
			ar.SetName("my-access-oidc")
			ar.SetNamespace("foo")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ar), ar)).To(Succeed())
			env.ShouldReconcile(arRec, testutils.RequestFromObject(ar))
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ar), ar)).To(Succeed())
			Expect(ar.Status.Phase).To(Equal(clustersv1alpha1.REQUEST_GRANTED))
			Expect(ar.Status.SecretRef).ToNot(BeNil())
			sName := ar.Status.SecretRef.Name
			sNamespace := ar.Status.SecretRef.Namespace
			Expect(sName).ToNot(BeEmpty())
			Expect(sNamespace).ToNot(BeEmpty())
			s := &corev1.Secret{}
			s.SetName(sName)
			s.SetNamespace(sNamespace)
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(s), s)).To(Succeed())

			// delete access request
			Expect(env.Client(platformCluster).Delete(env.Ctx, ar)).To(Succeed())
			rr := env.ShouldReconcile(arRec, testutils.RequestFromObject(ar))
			Expect(rr.RequeueAfter).To(BeZero(), "Reconciliation should not requeue after deletion")

			// verify access request deletion
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ar), ar)).To(MatchError(apierrors.IsNotFound, "accessequest should be deleted"))

			// verify that resources on the shoot cluster have been deleted
			labelSelector := client.MatchingLabels{
				providerv1alpha1.ManagedByNameLabel:      ar.Name,
				providerv1alpha1.ManagedByNamespaceLabel: ar.Namespace,
			}

			// OpenIDConnect resource
			oidcs := &oidcv1alpha1.OpenIDConnectList{}
			Expect(env.Client(shootCluster).List(env.Ctx, oidcs, labelSelector)).To(Succeed())
			Expect(oidcs.Items).To(BeEmpty(), "OpenIDConnect resource should be deleted")

			// clusterrole + binding
			crl := &rbacv1.ClusterRoleList{}
			Expect(env.Client(shootCluster).List(env.Ctx, crl, labelSelector)).To(Succeed())
			Expect(crl.Items).To(BeEmpty(), "ClusterRole should be deleted")
			crbl := &rbacv1.ClusterRoleBindingList{}
			Expect(env.Client(shootCluster).List(env.Ctx, crbl, labelSelector)).To(Succeed())
			Expect(crbl.Items).To(BeEmpty(), "ClusterRoleBinding should be deleted")

			// role + binding
			rl := &rbacv1.RoleList{}
			Expect(env.Client(shootCluster).List(env.Ctx, rl, labelSelector, client.InNamespace(ar.Spec.Permissions[1].Namespace))).To(Succeed())
			Expect(rl.Items).To(BeEmpty(), "Role should be deleted")
			rbl := &rbacv1.RoleBindingList{}
			Expect(env.Client(shootCluster).List(env.Ctx, rbl, labelSelector, client.InNamespace(ar.Spec.Permissions[1].Namespace))).To(Succeed())
			Expect(rbl.Items).To(BeEmpty(), "RoleBinding should be deleted")
		})

	})

})
