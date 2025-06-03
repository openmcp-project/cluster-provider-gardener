package cluster_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	testutils "github.com/openmcp-project/controller-utils/pkg/testing"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	"github.com/openmcp-project/cluster-provider-gardener/api/install"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/cluster"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/landscape"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

const (
	platformCluster = "platform"
	gardenCluster   = "garden"
	cRec            = "cluster"
)

var providerScheme = install.InstallProviderAPIs(runtime.NewScheme())
var gardenScheme = install.InstallGardenerAPIs(runtime.NewScheme())

func defaultTestSetup(testDirPathSegments ...string) (*cluster.ClusterReconciler, *testutils.ComplexEnvironment) {
	env := testutils.NewComplexEnvironmentBuilder().
		WithFakeClient(platformCluster, providerScheme).
		WithFakeClient(gardenCluster, gardenScheme).
		WithInitObjectPath(platformCluster, append(testDirPathSegments, "platform")...).
		WithInitObjectPath(gardenCluster, append(testDirPathSegments, "garden")...).
		WithReconcilerConstructor(cRec, func(c ...client.Client) reconcile.Reconciler {
			rc := shared.NewRuntimeConfiguration(clusters.NewTestClusterFromClient(platformCluster, c[0]), nil)
			return cluster.NewClusterReconciler(rc)
		}, platformCluster).
		Build()

	landscape.FakeClientMappingsForTesting = map[string]client.Client{
		"fake": env.Client(gardenCluster),
	}
	cr, ok := env.Reconciler(cRec).(*cluster.ClusterReconciler)
	Expect(ok).To(BeTrue(), "Reconciler is not of type ClusterReconciler")
	return cr, env
}

var _ = Describe("Cluster Controller", func() {

	var (
		cr  *cluster.ClusterReconciler
		env *testutils.ComplexEnvironment
	)

	BeforeEach(func() {
		cr, env = defaultTestSetup("..", "cluster", "testdata", "test-05")

		// fake landscape
		ls := &providerv1alpha1.Landscape{}
		ls.SetName("my-landscape")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ls), ls)).To(Succeed())
		Expect(cr.SetLandscape(env.Ctx, &shared.Landscape{
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
		cr.SetProfileForProviderConfiguration(pc.Name, p)
	})

	It("should create a shoot for a new cluster", func() {
		c := &clustersv1alpha1.Cluster{}
		c.SetName("basic")
		c.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))

		// verify shoot existence
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		Expect(c.Annotations).To(HaveKeyWithValue(clustersv1alpha1.K8sVersionAnnotation, "1.32.2"))
		Expect(c.Status.ProviderStatus).ToNot(BeNil())
		cs := &providerv1alpha1.ClusterStatus{}
		Expect(c.Status.GetProviderStatus(cs)).To(Succeed())
		Expect(cs.Shoot).ToNot(BeNil())
		shoot := &gardenv1beta1.Shoot{}
		shoot.SetName(cs.Shoot.Name)
		shoot.SetNamespace(cs.Shoot.Namespace)
		Expect(env.Client(gardenCluster).Get(env.Ctx, client.ObjectKeyFromObject(shoot), shoot)).To(Succeed())
	})

	It("should update an existing shoot for an existing cluster", func() {
		c := &clustersv1alpha1.Cluster{}
		c.SetName("advanced")
		c.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())

		// verify shoot existence
		Expect(c.Status.ProviderStatus).ToNot(BeNil())
		cs := &providerv1alpha1.ClusterStatus{}
		Expect(c.Status.GetProviderStatus(cs)).To(Succeed())
		Expect(cs.Shoot).ToNot(BeNil())
		shoot := &gardenv1beta1.Shoot{}
		shoot.SetName(cs.Shoot.Name)
		shoot.SetNamespace(cs.Shoot.Namespace)
		Expect(env.Client(gardenCluster).Get(env.Ctx, client.ObjectKeyFromObject(shoot), shoot)).To(Succeed())
		Expect(shoot.Spec.Kubernetes.Version).ToNot(Equal("1.32.2"))
		Expect(shoot.Spec.Provider.Workers[0].Maximum).ToNot(BeNumerically("==", 10))
		Expect(shoot.Spec.Provider.Workers[0].Machine.Image.Version).ToNot(PointTo(Equal("2000.0.0")))

		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		Expect(c.Annotations).To(HaveKeyWithValue(clustersv1alpha1.K8sVersionAnnotation, "1.32.2"))
		Expect(env.Client(gardenCluster).Get(env.Ctx, client.ObjectKeyFromObject(shoot), shoot)).To(Succeed())

		// verify that the fields were updated
		Expect(shoot.Spec.Kubernetes.Version).To(Equal("1.32.2"))
		Expect(shoot.Spec.Provider.Workers[0].Maximum).To(BeNumerically("==", 10))
		Expect(shoot.Spec.Provider.Workers[0].Machine.Image.Version).To(PointTo(Equal("2000.0.0")))
	})

	It("should delete the shoot when the cluster is deleted", func() {
		c := &clustersv1alpha1.Cluster{}
		c.SetName("advanced")
		c.SetNamespace("clusters")
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())

		// verify shoot existence
		Expect(c.Status.ProviderStatus).ToNot(BeNil())
		cs := &providerv1alpha1.ClusterStatus{}
		Expect(c.Status.GetProviderStatus(cs)).To(Succeed())
		Expect(cs.Shoot).ToNot(BeNil())
		shoot := &gardenv1beta1.Shoot{}
		shoot.SetName(cs.Shoot.Name)
		shoot.SetNamespace(cs.Shoot.Namespace)
		Expect(env.Client(gardenCluster).Get(env.Ctx, client.ObjectKeyFromObject(shoot), shoot)).To(Succeed())

		// delete the cluster
		Expect(env.Client(platformCluster).Delete(env.Ctx, c)).To(Succeed())
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))

		// verify shoot deletion
		Expect(env.Client(gardenCluster).Get(env.Ctx, client.ObjectKeyFromObject(shoot), shoot)).To(MatchError(apierrors.IsNotFound, "shoot should be deleted"))
		// shoot is gone, reconcile again to remove cluster finalizer
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(MatchError(apierrors.IsNotFound, "cluster should be deleted"))
	})

})
