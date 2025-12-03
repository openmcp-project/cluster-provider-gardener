package config_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	commonapi "github.com/openmcp-project/openmcp-operator/api/common"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	testutils "github.com/openmcp-project/controller-utils/pkg/testing"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
	"github.com/openmcp-project/cluster-provider-gardener/api/install"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/config"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/landscape"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

const (
	platformCluster = "platform"
	gardenCluster   = "garden"
	pcRec           = "providerconfig"
)

var providerScheme = install.InstallProviderAPIs(runtime.NewScheme())
var gardenScheme = install.InstallGardenerAPIs(runtime.NewScheme())

func defaultTestSetup(testDirPathSegments ...string) (*config.GardenerProviderConfigReconciler, *testutils.ComplexEnvironment) {
	env := testutils.NewComplexEnvironmentBuilder().
		WithFakeClient(platformCluster, providerScheme).
		WithFakeClient(gardenCluster, gardenScheme).
		WithFakeClientBuilderCall(platformCluster, "WithIndex", &clustersv1alpha1.Cluster{}, "spec.profile", func(obj client.Object) []string {
			c, ok := obj.(*clustersv1alpha1.Cluster)
			if !ok {
				panic(fmt.Errorf("indexer function for type %T's spec.profile field received object of type %T, this should never happen", clustersv1alpha1.Cluster{}, obj))
			}
			return []string{c.Spec.Profile}
		}).
		WithInitObjectPath(platformCluster, append(testDirPathSegments, "platform")...).
		WithInitObjectPath(gardenCluster, append(testDirPathSegments, "garden")...).
		WithReconcilerConstructor(pcRec, func(c ...client.Client) reconcile.Reconciler {
			rc := shared.NewRuntimeConfiguration(clusters.NewTestClusterFromClient(platformCluster, c[0]), nil)
			return config.NewGardenerProviderConfigReconciler(rc, nil)
		}, platformCluster).
		Build()

	landscape.FakeClientMappingsForTesting = map[string]client.Client{
		"fake": env.Client(gardenCluster),
	}
	pcr, ok := env.Reconciler(pcRec).(*config.GardenerProviderConfigReconciler)
	Expect(ok).To(BeTrue(), "Reconciler is not of type GardenerProviderConfigReconciler")
	return pcr, env
}

var _ = Describe("ProviderConfig Controller", func() {

	It("should sync a reconciled providerconfig into the internal cache and notify Clusters referencing the profile", func() {
		pcr, env := defaultTestSetup("..", "cluster", "testdata", "test-02")
		ls := &providerv1alpha1.Landscape{}
		ls.SetName("my-landscape")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ls), ls)).To(Succeed())
		Expect(pcr.SetLandscape(env.Ctx, &shared.Landscape{
			Name:     ls.Name,
			Cluster:  clusters.NewTestClusterFromClient(gardenCluster, env.Client(gardenCluster)),
			Resource: ls,
		})).To(Succeed())

		pc := &providerv1alpha1.ProviderConfig{}
		pc.SetName("my-config")
		cpcs := pcr.GetProviderConfigurations()
		Expect(cpcs).To(BeEmpty(), "ProviderConfig cache should be empty before reconciliation")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(pc), pc)).To(Succeed())
		env.ShouldReconcile(pcRec, testutils.RequestFromObject(pc))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(pc), pc)).To(Succeed())
		Expect(pc.Status.Phase).To(Equal(commonapi.StatusPhaseReady))

		cpcs = pcr.GetProviderConfigurations()
		Expect(cpcs).To(HaveKey("my-config"))
		cpc := cpcs["my-config"]
		Expect(cpc).ToNot(BeNil())
		Expect(cpc.Spec).To(Equal(pc.Spec))
	})

	It("should create a profile and notify Clusters referencing that profile", func() {
		pcr, env := defaultTestSetup("..", "cluster", "testdata", "test-02")
		ls := &providerv1alpha1.Landscape{}
		ls.SetName("my-landscape")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ls), ls)).To(Succeed())
		Expect(pcr.SetLandscape(env.Ctx, &shared.Landscape{
			Name:     ls.Name,
			Cluster:  clusters.NewTestClusterFromClient(gardenCluster, env.Client(gardenCluster)),
			Resource: ls,
		})).To(Succeed())

		pc := &providerv1alpha1.ProviderConfig{}
		pc.SetName("my-config")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(pc), pc)).To(Succeed())
		env.ShouldReconcile(pcRec, testutils.RequestFromObject(pc))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(pc), pc)).To(Succeed())
		Expect(pc.Status.Phase).To(Equal(commonapi.StatusPhaseReady))

		// verify profile
		p := &clustersv1alpha1.ClusterProfile{}
		p.SetName(fmt.Sprintf("%s.%s.%s", shared.Environment(), shared.ProviderName(), pc.Name))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		Expect(p.Spec.ProviderRef.Name).To(Equal(shared.ProviderName()))
		Expect(p.Spec.ProviderConfigRef.Name).To(Equal(pc.Name))
		Expect(p.Spec.SupportedVersions).To(ConsistOf(
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.32.4",
				Deprecated: false,
			},
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.32.3",
				Deprecated: true,
			},
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.31.8",
				Deprecated: false,
			},
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.31.7",
				Deprecated: true,
			},
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.31.6",
				Deprecated: true,
			},
		))
		pCached := pcr.GetProfile(p.Name)
		Expect(pCached).ToNot(BeNil())
		Expect(pCached.ProviderConfig.Spec).To(Equal(pc.Spec))
		Expect(pCached.Project.Name).To(Equal(pc.Spec.Project))

		// check if cluster reconciliation was triggered
		timeouted := false
		timeoutCtx, cancel := context.WithTimeout(env.Ctx, 1*time.Second)
		for !timeouted {
			select {
			case ce := <-pcr.ReconcileCluster:
				Expect(ce).ToNot(BeNil())
				Expect(ce.Object).ToNot(BeNil())
				Expect(ce.Object.Name).To(Equal("my-cluster"))
				Expect(ce.Object.Namespace).To(Equal("my-namespace"))
			case <-timeoutCtx.Done():
				timeouted = true
			}
		}
		cancel()
	})

	It("should fail if the referenced landscape is not known", func() {
		_, env := defaultTestSetup("..", "cluster", "testdata", "test-02")
		ls := &providerv1alpha1.Landscape{}
		ls.SetName("my-landscape")
		Expect(env.Client(platformCluster).Delete(env.Ctx, ls)).To(Succeed())

		pc := &providerv1alpha1.ProviderConfig{}
		pc.SetName("my-config")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(pc), pc)).To(Succeed())
		env.ShouldNotReconcile(pcRec, testutils.RequestFromObject(pc))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(pc), pc)).To(Succeed())
		Expect(pc.Status.Phase).To(Equal(commonapi.StatusPhaseProgressing))
		// Expect(pc.Status.Reason).To(Equal(cconst.ReasonUnknownLandscape))
		// Expect(pc.Status.Message).To(ContainSubstring("Landscape"))
	})

	It("should update the profile if the Gardener CloudProfile changed", func() {
		pcr, env := defaultTestSetup("..", "cluster", "testdata", "test-02")
		ls := &providerv1alpha1.Landscape{}
		ls.SetName("my-landscape")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ls), ls)).To(Succeed())
		Expect(pcr.SetLandscape(env.Ctx, &shared.Landscape{
			Name:     ls.Name,
			Cluster:  clusters.NewTestClusterFromClient(gardenCluster, env.Client(gardenCluster)),
			Resource: ls,
		})).To(Succeed())

		pc := &providerv1alpha1.ProviderConfig{}
		pc.SetName("my-config")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(pc), pc)).To(Succeed())
		env.ShouldReconcile(pcRec, testutils.RequestFromObject(pc))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(pc), pc)).To(Succeed())
		Expect(pc.Status.Phase).To(Equal(commonapi.StatusPhaseReady))

		// verify profile
		p := &clustersv1alpha1.ClusterProfile{}
		p.SetName(fmt.Sprintf("%s.%s.%s", shared.Environment(), shared.ProviderName(), pc.Name))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		Expect(p.Spec.SupportedVersions).To(ConsistOf(
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.32.4",
				Deprecated: false,
			},
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.32.3",
				Deprecated: true,
			},
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.31.8",
				Deprecated: false,
			},
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.31.7",
				Deprecated: true,
			},
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.31.6",
				Deprecated: true,
			},
		))

		// modify CloudProfile
		cp := gardenv1beta1.CloudProfile{}
		cp.SetName("gcp")
		Expect(env.Client(gardenCluster).Get(env.Ctx, client.ObjectKeyFromObject(&cp), &cp)).To(Succeed())
		// delete k8s version 1.31.6
		idx := -1
		for i, v := range cp.Spec.Kubernetes.Versions {
			if v.Version == "1.31.6" {
				idx = i
				break
			}
		}
		Expect(idx).ToNot(Equal(-1), "k8s version 1.31.6 should be present in CloudProfile")
		cp.Spec.Kubernetes.Versions = append(cp.Spec.Kubernetes.Versions[:idx], cp.Spec.Kubernetes.Versions[idx+1:]...)
		Expect(env.Client(gardenCluster).Update(env.Ctx, &cp)).To(Succeed())
		env.ShouldReconcile(pcRec, testutils.RequestFromObject(pc))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		Expect(p.Spec.SupportedVersions).To(ConsistOf(
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.32.4",
				Deprecated: false,
			},
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.32.3",
				Deprecated: true,
			},
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.31.8",
				Deprecated: false,
			},
			clustersv1alpha1.SupportedK8sVersion{
				Version:    "1.31.7",
				Deprecated: true,
			},
		))
	})

	It("should delete the profile if the ProviderConfig is deleted", func() {
		pcr, env := defaultTestSetup("..", "cluster", "testdata", "test-02")
		ls := &providerv1alpha1.Landscape{}
		ls.SetName("my-landscape")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ls), ls)).To(Succeed())
		Expect(pcr.SetLandscape(env.Ctx, &shared.Landscape{
			Name:     ls.Name,
			Cluster:  clusters.NewTestClusterFromClient(gardenCluster, env.Client(gardenCluster)),
			Resource: ls,
		})).To(Succeed())

		pc := &providerv1alpha1.ProviderConfig{}
		pc.SetName("my-config")
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(pc), pc)).To(Succeed())
		env.ShouldReconcile(pcRec, testutils.RequestFromObject(pc))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(pc), pc)).To(Succeed())
		Expect(pc.Status.Phase).To(Equal(commonapi.StatusPhaseReady))

		// verify profile
		p := &clustersv1alpha1.ClusterProfile{}
		p.SetName(fmt.Sprintf("%s.%s.%s", shared.Environment(), shared.ProviderName(), pc.Name))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())

		// delete ProviderConfig
		Expect(env.Client(platformCluster).Delete(env.Ctx, pc)).To(Succeed())
		env.ShouldReconcile(pcRec, testutils.RequestFromObject(pc))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(pc), pc)).To(MatchError(apierrors.IsNotFound, "ProviderConfig should be deleted"))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(p), p)).To(MatchError(apierrors.IsNotFound, "ClusterProfile should be deleted"))
	})

})

var _ = Describe("isProfileUpdated", func() {
	It("should return false when ProviderConfig.Spec is identical", func() {
		spec := providerv1alpha1.ProviderConfigSpec{
			ProviderRef:  commonapi.LocalObjectReference{Name: "gardener"},
			LandscapeRef: commonapi.LocalObjectReference{Name: "landscape-1"},
			Project:      "my-project",
		}
		oldProfile := &shared.Profile{
			ProviderConfig: &providerv1alpha1.ProviderConfig{
				Spec: spec,
			},
		}
		newProfile := &shared.Profile{
			ProviderConfig: &providerv1alpha1.ProviderConfig{
				Spec: spec,
			},
		}
		result := config.IsProfileUpdated(oldProfile, newProfile)
		Expect(result).To(BeFalse())
	})

	It("should return true when ProviderConfig.Spec differs", func() {
		oldProfile := &shared.Profile{
			ProviderConfig: &providerv1alpha1.ProviderConfig{
				Spec: providerv1alpha1.ProviderConfigSpec{
					Project: "project-1",
				},
			},
		}
		newProfile := &shared.Profile{
			ProviderConfig: &providerv1alpha1.ProviderConfig{
				Spec: providerv1alpha1.ProviderConfigSpec{
					Project: "project-2",
				},
			},
		}
		result := config.IsProfileUpdated(oldProfile, newProfile)
		Expect(result).To(BeTrue())
	})
})
