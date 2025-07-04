package cluster_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	testutils "github.com/openmcp-project/controller-utils/pkg/testing"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	gardenconstants "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1/constants"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/cluster"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

func defaultTestSetupForShootLogic(testDirPathSegments ...string) (*shared.RuntimeConfiguration, *testutils.ComplexEnvironment) {
	env := testutils.NewComplexEnvironmentBuilder().
		WithFakeClient(platformCluster, providerScheme).
		WithFakeClient(gardenCluster, gardenScheme).
		WithInitObjectPath(platformCluster, append(testDirPathSegments, "platform")...).
		WithInitObjectPath(gardenCluster, append(testDirPathSegments, "garden")...).
		Build()

	rc := shared.NewRuntimeConfiguration(clusters.NewTestClusterFromClient(platformCluster, env.Client(platformCluster)), nil)
	return rc, env
}

var _ = Describe("Shoot Logic", func() {

	Context("GetShoot", func() {

		It("should return the shoot referenced in the Cluster status, if set", func() {
			_, env := defaultTestSetupForShootLogic("..", "cluster", "testdata", "test-03")
			c := &clustersv1alpha1.Cluster{}
			c.SetName("with-shoot")
			c.SetNamespace("clusters")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
			shoot, rerr := cluster.GetShoot(env.Ctx, env.Client(gardenCluster), "garden-clusters", c)
			Expect(rerr).ToNot(HaveOccurred())
			Expect(shoot).ToNot(BeNil())
			Expect(shoot.Name).To(Equal("shoot-with-shoot"))
			Expect(shoot.Namespace).To(Equal("garden-clusters"))
		})

		It("should recover the shoot from labels, if the Cluster status does not contain a shoot reference", func() {
			_, env := defaultTestSetupForShootLogic("..", "cluster", "testdata", "test-03")
			c := &clustersv1alpha1.Cluster{}
			c.SetName("from-labels")
			c.SetNamespace("clusters")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
			shoot, rerr := cluster.GetShoot(env.Ctx, env.Client(gardenCluster), "garden-clusters", c)
			Expect(rerr).ToNot(HaveOccurred())
			Expect(shoot).ToNot(BeNil())
			Expect(shoot.Name).To(Equal("shoot-from-labels"))
			Expect(shoot.Namespace).To(Equal("garden-clusters"))
		})

		It("should return nil if no shoot exists for the Cluster", func() {
			_, env := defaultTestSetupForShootLogic("..", "cluster", "testdata", "test-03")
			c := &clustersv1alpha1.Cluster{}
			c.SetName("no-shoot")
			c.SetNamespace("clusters")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())
			shoot, rerr := cluster.GetShoot(env.Ctx, env.Client(gardenCluster), "garden-clusters", c)
			Expect(rerr).ToNot(HaveOccurred())
			Expect(shoot).To(BeNil())
		})

	})

	Context("UpdateShootFields", func() {

		It("should update the shoot fields with the expected values", func() {
			rc, env := defaultTestSetupForShootLogic("..", "cluster", "testdata", "test-04")

			// fake landscape
			ls := &providerv1alpha1.Landscape{}
			ls.SetName("my-landscape")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ls), ls)).To(Succeed())
			Expect(rc.SetLandscape(env.Ctx, &shared.Landscape{
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
			rc.SetProfileForProviderConfiguration(pc.Name, p)

			// fake cluster
			c := &clustersv1alpha1.Cluster{}
			c.SetName("basic")
			c.SetNamespace("clusters")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())

			// fake cluster configuration
			cc := &providerv1alpha1.ClusterConfig{}
			cc.SetName("basic")
			cc.SetNamespace("clusters")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(cc), cc)).To(Succeed())

			// create empty shoot
			shoot := &gardenv1beta1.Shoot{}
			shoot.SetName("basic")
			shoot.SetNamespace("garden-my-project")
			shoot.Annotations = map[string]string{
				"annotation.foo.bar": "baz",
			}
			shoot.Labels = map[string]string{
				"label.foo.bar": "baz",
			}
			oldShoot := shoot.DeepCopy()

			// verify update
			Expect(cluster.UpdateShootFields(env.Ctx, shoot, p, c, cc)).To(Succeed())

			// verify annotations
			expectedAnnotations := map[string]string{
				"shoot.gardener.cloud/cleanup-extended-apis-finalize-grace-period-seconds": "30",
				gardenconstants.AnnotationAuthenticationIssuer:                             gardenconstants.AnnotationAuthenticationIssuerManaged,
			}
			for k, v := range expectedAnnotations {
				Expect(shoot.Annotations).To(HaveKeyWithValue(k, v))
			}
			for k, v := range oldShoot.Annotations {
				Expect(shoot.Annotations).To(HaveKeyWithValue(k, v))
			}

			// verify labels
			expecedLabels := map[string]string{
				providerv1alpha1.ClusterReferenceLabelName:        c.Name,
				providerv1alpha1.ClusterReferenceLabelNamespace:   c.Namespace,
				providerv1alpha1.ClusterReferenceLabelProvider:    shared.ProviderName(),
				providerv1alpha1.ClusterReferenceLabelEnvironment: shared.Environment(),
			}
			for k, v := range expecedLabels {
				Expect(shoot.Labels).To(HaveKeyWithValue(k, v))
			}
			for k, v := range oldShoot.Labels {
				Expect(shoot.Labels).To(HaveKeyWithValue(k, v))
			}

			// verify spec
			Expect(shoot.Spec.Provider).To(Equal(pc.Spec.ShootTemplate.Spec.Provider))
			Expect(shoot.Spec.Kubernetes.Version).To(Equal(pc.Spec.ShootTemplate.Spec.Kubernetes.Version))
			Expect(shoot.Spec.Networking).To(Equal(pc.Spec.ShootTemplate.Spec.Networking))
			Expect(shoot.Spec.CloudProfile).To(Equal(pc.Spec.ShootTemplate.Spec.CloudProfile))
			Expect(shoot.Spec.Region).To(Equal(pc.Spec.ShootTemplate.Spec.Region))
			Expect(shoot.Spec.Maintenance).To(Equal(pc.Spec.ShootTemplate.Spec.Maintenance))
			Expect(shoot.Spec.Extensions).To(ContainElements(MatchFields(IgnoreExtras, Fields{
				"Type": Equal(cluster.GardenerOIDCExtensionType),
			})))

			// verify changes from cluster configuration
			Expect(shoot.Spec.Extensions).To(ContainElements(MatchFields(0, Fields{
				"Type":     Equal("test-extension"),
				"Disabled": PointTo(BeTrue()),
				"ProviderConfig": PointTo(MatchFields(IgnoreExtras, Fields{
					"Raw": Equal([]byte(`{"foo":"bar"}`)),
				})),
			})))
			Expect(shoot.Spec.SeedName).To(PointTo(Equal("test-seed")))
		})

		It("should not update fields in an invalid way", func() {
			rc, env := defaultTestSetupForShootLogic("..", "cluster", "testdata", "test-04")

			// fake landscape
			ls := &providerv1alpha1.Landscape{}
			ls.SetName("my-landscape")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ls), ls)).To(Succeed())
			Expect(rc.SetLandscape(env.Ctx, &shared.Landscape{
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
			rc.SetProfileForProviderConfiguration(pc.Name, p)

			// fake cluster
			c := &clustersv1alpha1.Cluster{}
			c.SetName("basic")
			c.SetNamespace("clusters")
			Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(c), c)).To(Succeed())

			// create shoot
			shoot := &gardenv1beta1.Shoot{}
			shoot.SetName("basic")
			shoot.SetNamespace("garden-my-project")
			shoot.Spec.Kubernetes.Version = "1.50.0" // should not be downgraded
			shoot.Spec.Maintenance = &gardenv1beta1.Maintenance{
				AutoUpdate: &gardenv1beta1.MaintenanceAutoUpdate{
					MachineImageVersion: ptr.To(true), // should be overwritten with false
				},
			}
			shoot.Spec.Provider.ControlPlaneConfig = &runtime.RawExtension{
				Raw: []byte("dummy"), // should not be overwritten
			}
			oldShoot := shoot.DeepCopy()

			// verify update
			Expect(cluster.UpdateShootFields(env.Ctx, shoot, p, c, nil)).To(Succeed())

			// verify spec
			Expect(shoot.Spec.Provider.InfrastructureConfig).To(Equal(pc.Spec.ShootTemplate.Spec.Provider.InfrastructureConfig))
			Expect(shoot.Spec.Provider.Type).To(Equal(pc.Spec.ShootTemplate.Spec.Provider.Type))
			Expect(shoot.Spec.Provider.Workers).To(Equal(pc.Spec.ShootTemplate.Spec.Provider.Workers))
			Expect(shoot.Spec.Provider.WorkersSettings).To(Equal(pc.Spec.ShootTemplate.Spec.Provider.WorkersSettings))
			Expect(shoot.Spec.Provider.ControlPlaneConfig).To(Equal(oldShoot.Spec.Provider.ControlPlaneConfig)) // should not be overwritten
			Expect(shoot.Spec.Kubernetes.Version).To(Equal(oldShoot.Spec.Kubernetes.Version))                   // should not be downgraded
			Expect(shoot.Spec.Networking).To(Equal(pc.Spec.ShootTemplate.Spec.Networking))
			Expect(shoot.Spec.CloudProfile).To(Equal(pc.Spec.ShootTemplate.Spec.CloudProfile))
			Expect(shoot.Spec.Region).To(Equal(pc.Spec.ShootTemplate.Spec.Region))
			Expect(shoot.Spec.Maintenance.AutoUpdate.MachineImageVersion).To(PointTo(BeFalse())) // should be overwritten with false
		})

	})

})
