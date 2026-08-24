package cluster_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	corev1 "k8s.io/api/core/v1"

	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	testutils "github.com/openmcp-project/controller-utils/pkg/testing"

	gardenv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
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

var scrapeConfigGVK = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1alpha1", Kind: "ScrapeConfig"}

// newPlatformRESTMapper returns a DefaultRESTMapper that includes the ScrapeConfig CRD so the fake
// platform client's RESTMapper can answer RESTMapping calls for it.
func newPlatformRESTMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{scrapeConfigGVK.GroupVersion()})
	m.Add(scrapeConfigGVK, meta.RESTScopeNamespace)
	return m
}

func init() {
	providerScheme.AddKnownTypeWithName(scrapeConfigGVK, &unstructured.Unstructured{})
}

func defaultTestSetup(testDirPathSegments ...string) *testutils.ComplexEnvironment {
	env := testutils.NewComplexEnvironmentBuilder().
		WithFakeClient(platformCluster, providerScheme).
		WithFakeClientBuilderCall(platformCluster, "WithRESTMapper", newPlatformRESTMapper()).
		WithFakeClient(gardenCluster, gardenScheme).
		WithInitObjectPath(platformCluster, append(testDirPathSegments, "platform")...).
		WithInitObjectPath(gardenCluster, append(testDirPathSegments, "garden")...).
		WithReconcilerConstructor(cRec, func(c ...client.Client) reconcile.Reconciler {
			rc := shared.NewRuntimeConfiguration(clusters.NewTestClusterFromClient(platformCluster, c[0]), nil)
			return cluster.NewClusterReconciler(rc, nil)
		}, platformCluster).
		Build()

	landscape.FakeClientMappingsForTesting = map[string]client.Client{
		"fake": env.Client(gardenCluster),
	}
	cr, err := testutils.ReconcilerAs[*cluster.ClusterReconciler](env, cRec)
	Expect(err).ToNot(HaveOccurred(), "Reconciler is not of type ClusterReconciler")

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

	return env
}

var _ = Describe("Cluster Controller", func() {

	It("should create a shoot for a new cluster", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-05")

		c := &clustersv1alpha1.Cluster{}
		c.SetName("basic")
		c.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))

		// verify shoot existence
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		Expect(c.Labels).To(HaveKeyWithValue(clustersv1alpha1.K8sVersionLabel, "1.32.2"))
		Expect(c.Labels).To(HaveKeyWithValue(clustersv1alpha1.ProviderLabel, shared.ProviderName()))
		Expect(c.Status.ProviderStatus).ToNot(BeNil())
		cs := &providerv1alpha1.ClusterStatus{}
		Expect(c.Status.GetProviderStatus(cs)).To(Succeed())
		Expect(cs.Shoot).ToNot(BeNil())
		shoot := &gardenv1beta1.Shoot{}
		shoot.SetName(cs.Shoot.Name)
		shoot.SetNamespace(cs.Shoot.Namespace)
		Expect(env.Client(gardenCluster).Get(env.Ctx, client.ObjectKeyFromObject(shoot), shoot)).To(Succeed())
	})

	It("should create a shoot for a new cluster with an overwritten name", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-05")

		c := &clustersv1alpha1.Cluster{}
		c.SetName("basic-with-name")
		c.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))

		// verify shoot existence
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		Expect(c.Labels).To(HaveKeyWithValue(clustersv1alpha1.K8sVersionLabel, "1.32.2"))
		Expect(c.Labels).To(HaveKeyWithValue(clustersv1alpha1.ProviderLabel, shared.ProviderName()))
		Expect(c.Status.ProviderStatus).ToNot(BeNil())
		cs := &providerv1alpha1.ClusterStatus{}
		Expect(c.Status.GetProviderStatus(cs)).To(Succeed())
		Expect(cs.Shoot).ToNot(BeNil())
		Expect(cs.Shoot.Name).To(Equal("custom-shoot-name"))
		shoot := &gardenv1beta1.Shoot{}
		shoot.SetName(cs.Shoot.Name)
		shoot.SetNamespace(cs.Shoot.Namespace)
		Expect(env.Client(gardenCluster).Get(env.Ctx, client.ObjectKeyFromObject(shoot), shoot)).To(Succeed())
	})

	It("should fail if the shoot name is overwritten but there is an existing shoot with that name belonging to another cluster", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-05")

		c := &clustersv1alpha1.Cluster{}
		c.SetName("basic-with-name")
		c.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))

		c2 := &clustersv1alpha1.Cluster{}
		c2.SetName("another-name")
		c2.SetNamespace("clusters")
		c2.Labels = c.Labels
		c2.Spec = *c.Spec.DeepCopy()
		Expect(env.Client(platformCluster).Create(env.Ctx, c2)).To(Succeed())
		env.ShouldNotReconcileWithError(cRec, testutils.RequestFromObject(c2), MatchError(And(ContainSubstring("already exists"), ContainSubstring("basic-with-name"))))
	})

	It("should update an existing shoot for an existing cluster", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-05")

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
		Expect(c.Labels).To(HaveKeyWithValue(clustersv1alpha1.K8sVersionLabel, "1.32.2"))
		Expect(c.Labels).To(HaveKeyWithValue(clustersv1alpha1.ProviderLabel, shared.ProviderName()))
		Expect(env.Client(gardenCluster).Get(env.Ctx, client.ObjectKeyFromObject(shoot), shoot)).To(Succeed())

		// verify that the fields were updated
		Expect(shoot.Spec.Kubernetes.Version).To(Equal("1.32.2"))
		Expect(shoot.Spec.Provider.Workers[0].Maximum).To(BeNumerically("==", 10))
		Expect(shoot.Spec.Provider.Workers[0].Machine.Image.Version).To(PointTo(Equal("2000.0.0")))
	})

	It("should expose shoot endpoints in cluster status", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-05")

		c := &clustersv1alpha1.Cluster{}
		c.SetName("advanced")
		c.SetNamespace("clusters")
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())

		Expect(c.Status.ProviderStatus).ToNot(BeNil())
		cs := &providerv1alpha1.ClusterStatus{}
		Expect(c.Status.GetProviderStatus(cs)).To(Succeed())
		Expect(cs.Shoot).ToNot(BeNil())
		shoot := &gardenv1beta1.Shoot{}
		shoot.SetName(cs.Shoot.Name)
		shoot.SetNamespace(cs.Shoot.Namespace)
		Expect(env.Client(gardenCluster).Get(env.Ctx, client.ObjectKeyFromObject(shoot), shoot)).To(Succeed())
		Expect(shoot.Status.AdvertisedAddresses).ToNot(BeEmpty())
		Expect(c.Status.Endpoints).To(HaveLen(len(shoot.Status.AdvertisedAddresses)))
		for _, addr := range shoot.Status.AdvertisedAddresses {
			url, exists := c.Status.Endpoints.Get(addr.Name)
			Expect(exists).To(BeTrue(), "endpoint with name %s should exist in cluster status", addr.Name)
			Expect(url).To(Equal(addr.URL), "endpoint URL should match the advertised address URL")
		}
	})

	It("should create a ScrapeConfig for an observable cluster", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-05")

		c := &clustersv1alpha1.Cluster{}
		c.SetName("advanced")
		c.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		c.Labels = map[string]string{providerv1alpha1.ObservabilityLabel: providerv1alpha1.ObservabilityLabelValueEnabled}
		Expect(env.Client(platformCluster).Update(env.Ctx, c)).To(Succeed())

		// Monitoring secret lives in the shoot's namespace (= project namespace in Gardener)
		monitoringSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:        "shoot-advanced.monitoring",
			Namespace:   "garden-clusters",
			Annotations: map[string]string{"prometheus-url": "https://prometheus-shoot.example.com"},
		}, Data: map[string][]byte{"username": []byte("admin"), "password": []byte("secret")}}
		Expect(env.Client(gardenCluster).Create(env.Ctx, monitoringSecret)).To(Succeed())

		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))

		name := "shoot-prom-" + ctrlutils.NameHashSHAKE128Base32(shared.Environment(), shared.ProviderName(), c.Namespace, c.Name)
		forwardedSecret := &corev1.Secret{}
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKey{Name: name + "-auth", Namespace: c.Namespace}, forwardedSecret)).To(Succeed())
		Expect(forwardedSecret.Labels).To(HaveKeyWithValue(providerv1alpha1.ObservabilityLabel, providerv1alpha1.ObservabilityLabelValueEnabled))
		Expect(forwardedSecret.Data).To(HaveKeyWithValue("username", []byte("admin")))
		Expect(forwardedSecret.Data).To(HaveKeyWithValue("password", []byte("secret")))

		scrapeConfig := &unstructured.Unstructured{}
		scrapeConfig.SetGroupVersionKind(scrapeConfigGVK)
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKey{Name: name, Namespace: c.Namespace}, scrapeConfig)).To(Succeed())
		Expect(scrapeConfig.GetLabels()).To(HaveKeyWithValue(providerv1alpha1.ObservabilityLabel, providerv1alpha1.ObservabilityLabelValueEnabled))
		Expect(scrapeConfig.Object).To(HaveKey("spec"))
		spec := scrapeConfig.Object["spec"].(map[string]any)
		Expect(spec).To(MatchKeys(IgnoreExtras, Keys{
			"metricsPath": Equal("/federate"),
			"scheme":      Equal("https"),
			"jobName":     Equal("gardener-shoot-prometheus-garden-clusters-shoot-advanced"),
		}))
	})

	It("should derive Prometheus URL from shoot advertised addresses when annotation is absent", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-05")

		c := &clustersv1alpha1.Cluster{}
		c.SetName("advanced")
		c.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		c.Labels = map[string]string{providerv1alpha1.ObservabilityLabel: providerv1alpha1.ObservabilityLabelValueEnabled}
		Expect(env.Client(platformCluster).Update(env.Ctx, c)).To(Succeed())

		// No prometheus-url annotation; shoot has no prometheus advertised address either
		// so we expect a ConfigurationProblem condition
		monitoringSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      "shoot-advanced.monitoring",
			Namespace: "garden-clusters",
			// deliberately no prometheus-url annotation
		}, Data: map[string][]byte{"username": []byte("admin"), "password": []byte("secret")}}
		Expect(env.Client(gardenCluster).Create(env.Ctx, monitoringSecret)).To(Succeed())

		env.ShouldNotReconcile(cRec, testutils.RequestFromObject(c))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		condition := findCondition(c.Status.Conditions, providerv1alpha1.ClusterConditionShootObservability)
		Expect(condition).ToNot(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
	})

	It("should set condition False when monitoring secret is missing", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-05")

		c := &clustersv1alpha1.Cluster{}
		c.SetName("advanced")
		c.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		c.Labels = map[string]string{providerv1alpha1.ObservabilityLabel: providerv1alpha1.ObservabilityLabelValueEnabled}
		Expect(env.Client(platformCluster).Update(env.Ctx, c)).To(Succeed())

		// No monitoring secret created
		env.ShouldNotReconcile(cRec, testutils.RequestFromObject(c))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		condition := findCondition(c.Status.Conditions, providerv1alpha1.ClusterConditionShootObservability)
		Expect(condition).ToNot(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
	})

	It("should clean up ScrapeConfig and auth Secret when observability label is removed", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-05")

		c := &clustersv1alpha1.Cluster{}
		c.SetName("advanced")
		c.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		c.Labels = map[string]string{providerv1alpha1.ObservabilityLabel: providerv1alpha1.ObservabilityLabelValueEnabled}
		Expect(env.Client(platformCluster).Update(env.Ctx, c)).To(Succeed())

		monitoringSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:        "shoot-advanced.monitoring",
			Namespace:   "garden-clusters",
			Annotations: map[string]string{"prometheus-url": "https://prometheus-shoot.example.com"},
		}, Data: map[string][]byte{"username": []byte("admin"), "password": []byte("secret")}}
		Expect(env.Client(gardenCluster).Create(env.Ctx, monitoringSecret)).To(Succeed())

		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))
		name := "shoot-prom-" + ctrlutils.NameHashSHAKE128Base32(shared.Environment(), shared.ProviderName(), c.Namespace, c.Name)

		// Verify resources were created
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKey{Name: name + "-auth", Namespace: c.Namespace}, &corev1.Secret{})).To(Succeed())
		sc := &unstructured.Unstructured{}
		sc.SetGroupVersionKind(scrapeConfigGVK)
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKey{Name: name, Namespace: c.Namespace}, sc)).To(Succeed())

		// Remove the observability label
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		delete(c.Labels, providerv1alpha1.ObservabilityLabel)
		Expect(env.Client(platformCluster).Update(env.Ctx, c)).To(Succeed())

		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))

		// Resources must be gone
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKey{Name: name + "-auth", Namespace: c.Namespace}, &corev1.Secret{})).
			To(MatchError(apierrors.IsNotFound, "auth secret should be deleted"))
		sc2 := &unstructured.Unstructured{}
		sc2.SetGroupVersionKind(scrapeConfigGVK)
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKey{Name: name, Namespace: c.Namespace}, sc2)).
			To(MatchError(apierrors.IsNotFound, "ScrapeConfig should be deleted"))

		// Condition should be True (cleanup succeeded)
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		condition := findCondition(c.Status.Conditions, providerv1alpha1.ClusterConditionShootObservability)
		Expect(condition).ToNot(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionTrue))
	})

	It("should set OwnerReference on observability resources so GC deletes them with the cluster", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-05")

		c := &clustersv1alpha1.Cluster{}
		c.SetName("advanced")
		c.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		c.Labels = map[string]string{providerv1alpha1.ObservabilityLabel: providerv1alpha1.ObservabilityLabelValueEnabled}
		Expect(env.Client(platformCluster).Update(env.Ctx, c)).To(Succeed())

		monitoringSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:        "shoot-advanced.monitoring",
			Namespace:   "garden-clusters",
			Annotations: map[string]string{"prometheus-url": "https://prometheus-shoot.example.com"},
		}, Data: map[string][]byte{"username": []byte("admin"), "password": []byte("secret")}}
		Expect(env.Client(gardenCluster).Create(env.Ctx, monitoringSecret)).To(Succeed())

		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())

		name := "shoot-prom-" + ctrlutils.NameHashSHAKE128Base32(shared.Environment(), shared.ProviderName(), c.Namespace, c.Name)
		ownerMatcher := ContainElement(MatchFields(IgnoreExtras, Fields{
			"Name":       Equal(c.Name),
			"Kind":       Equal("Cluster"),
			"Controller": PointTo(BeTrue()),
		}))

		authSecret := &corev1.Secret{}
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKey{Name: name + "-auth", Namespace: c.Namespace}, authSecret)).To(Succeed())
		Expect(authSecret.OwnerReferences).To(ownerMatcher)

		sc := &unstructured.Unstructured{}
		sc.SetGroupVersionKind(scrapeConfigGVK)
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKey{Name: name, Namespace: c.Namespace}, sc)).To(Succeed())
		Expect(sc.GetOwnerReferences()).To(ownerMatcher)
	})

	It("should delete the shoot when the cluster is deleted", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-05")

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

	It("should not start with shoot deletion if there are foreign finalizers on the Cluster", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-05")

		c := &clustersv1alpha1.Cluster{}
		c.SetName("advanced")
		c.SetNamespace("clusters")
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		c.Finalizers = append(c.Finalizers, "example.com/finalizer")
		Expect(env.Client(platformCluster).Update(env.Ctx, c)).To(Succeed())

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
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())

		// shoot should not be deleted because of the unknown finalizer
		Expect(env.Client(gardenCluster).Get(env.Ctx, client.ObjectKeyFromObject(shoot), shoot)).To(Succeed())
		Expect(shoot.DeletionTimestamp.IsZero()).To(BeTrue())

		// there should be a condition indicating that there are foreign finalizers
		Expect(c.Status.Conditions).To(ContainElement(MatchFields(IgnoreExtras, Fields{
			"Type":    Equal(providerv1alpha1.ClusterConditionForeignFinalizers),
			"Status":  Equal(metav1.ConditionFalse),
			"Message": ContainSubstring("example.com/finalizer"),
		})))

		// remove the foreign finalizer
		Expect(controllerutil.RemoveFinalizer(c, "example.com/finalizer")).To(BeTrue())
		Expect(env.Client(platformCluster).Update(env.Ctx, c)).To(Succeed())
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))

		// verify shoot deletion
		Expect(env.Client(gardenCluster).Get(env.Ctx, client.ObjectKeyFromObject(shoot), shoot)).To(MatchError(apierrors.IsNotFound, "shoot should be deleted"))
		// shoot is gone, reconcile again to remove cluster finalizer
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(MatchError(apierrors.IsNotFound, "cluster should be deleted"))
	})

	It("should handle cluster configs correctly", func() {
		env := defaultTestSetup("..", "cluster", "testdata", "test-04")

		c := &clustersv1alpha1.Cluster{}
		c.SetName("basic")
		c.SetNamespace("clusters")
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())

		cc1 := &providerv1alpha1.ClusterConfig{}
		cc1.SetName("basic-1")
		cc1.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(cc1), cc1)).To(Succeed())
		cc2 := &providerv1alpha1.ClusterConfig{}
		cc2.SetName("basic-2")
		cc2.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(cc2), cc2)).To(Succeed())
		cc3 := &providerv1alpha1.ClusterConfig{}
		cc3.SetName("ext-1")
		cc3.SetNamespace("clusters")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(cc3), cc3)).To(Succeed())
		clusterConfigs := []*providerv1alpha1.ClusterConfig{cc1, cc2, cc3}

		Expect(c.Annotations).To(HaveKey(providerv1alpha1.ClusterConfigHashAnnotation))
		oldHash := c.Annotations[providerv1alpha1.ClusterConfigHashAnnotation]
		for _, cc := range clusterConfigs {
			Expect(cc.OwnerReferences).To(ContainElement(MatchFields(IgnoreExtras, Fields{
				"Name":       Equal(c.Name),
				"Kind":       Equal("Cluster"),
				"APIVersion": Equal(clustersv1alpha1.GroupVersion.String()),
			})))
		}

		// remove one of the cluster configs and verify that the owner reference is removed
		c.Spec.ClusterConfigs = c.Spec.ClusterConfigs[:2]
		Expect(env.Client(platformCluster).Update(env.Ctx, c)).To(Succeed())
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(cc1), cc1)).To(Succeed())
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(cc2), cc2)).To(Succeed())
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(cc3), cc3)).To(Succeed())
		Expect(c.Annotations).To(HaveKey(providerv1alpha1.ClusterConfigHashAnnotation))
		Expect(c.Annotations[providerv1alpha1.ClusterConfigHashAnnotation]).ToNot(Equal(oldHash))
		for _, cc := range clusterConfigs[:2] {
			Expect(cc.OwnerReferences).To(ContainElement(MatchFields(IgnoreExtras, Fields{
				"Name":       Equal(c.Name),
				"Kind":       Equal("Cluster"),
				"APIVersion": Equal(clustersv1alpha1.GroupVersion.String()),
			})))
		}
		Expect(cc3.OwnerReferences).To(BeEmpty())

		// delete the Cluster and verify that the owner references are removed and the cluster configs are not deleted
		Expect(env.Client(platformCluster).Delete(env.Ctx, c)).To(Succeed())
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c)) // should delete the shoot
		env.ShouldReconcile(cRec, testutils.RequestFromObject(c)) // should remove owner references and the finalizer
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(MatchError(apierrors.IsNotFound, "cluster should be deleted"))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(cc1), cc1)).To(Succeed())
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(cc2), cc2)).To(Succeed())
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(cc3), cc3)).To(Succeed())
		Expect(cc1.OwnerReferences).To(BeEmpty())
		Expect(cc2.OwnerReferences).To(BeEmpty())
		Expect(cc3.OwnerReferences).To(BeEmpty())
	})

})

// findCondition returns the condition with the given type from the list, or nil.
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}
