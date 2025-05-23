package landscape_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	testutils "github.com/openmcp-project/controller-utils/pkg/testing"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	"github.com/openmcp-project/cluster-provider-gardener/api/install"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/landscape"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

const (
	platformCluster = "platform"
	gardenCluster   = "garden"
	lsRec           = "landscape"
)

var providerScheme = install.InstallProviderAPIs(runtime.NewScheme())
var gardenScheme = install.InstallGardenerAPIs(runtime.NewScheme())

func defaultTestSetup(testDirPathSegments ...string) (*landscape.LandscapeReconciler, *testutils.ComplexEnvironment) {
	env := testutils.NewComplexEnvironmentBuilder().
		WithFakeClient(platformCluster, providerScheme).
		WithFakeClient(gardenCluster, gardenScheme).
		WithInitObjectPath(platformCluster, append(testDirPathSegments, "platform")...).
		WithInitObjectPath(gardenCluster, append(testDirPathSegments, "garden")...).
		WithReconcilerConstructor(lsRec, func(c ...client.Client) reconcile.Reconciler {
			rc := shared.NewRuntimeConfiguration(clusters.NewTestClusterFromClient(platformCluster, c[0]), nil)
			return landscape.NewLandscapeReconciler(rc)
		}, platformCluster).
		Build()

	landscape.FakeClientMappingsForTesting = map[string]client.Client{
		"fake": env.Client(gardenCluster),
	}
	lsr, ok := env.Reconciler(lsRec).(*landscape.LandscapeReconciler)
	Expect(ok).To(BeTrue(), "Reconciler is not of type LandscapeReconciler")
	return lsr, env
}

var _ = Describe("Landscape Controller", func() {

	It("should sync a reconciled landscape into the internal cache and notify referencing provider configs", func() {
		lsr, env := defaultTestSetup("testdata", "test-01")
		lsr.SetProviderConfiguration("my-pc", &providerv1alpha1.ProviderConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-pc",
			},
			Spec: providerv1alpha1.ProviderConfigSpec{
				ProviderRef: providerv1alpha1.ObjectReference{
					Name: "gardener",
				},
				LandscapeRef: providerv1alpha1.ObjectReference{
					Name: "my-landscape",
				},
			},
		})
		ls := &providerv1alpha1.Landscape{}
		ls.SetName("my-landscape")
		cls := lsr.GetLandscape(ls.Name)
		Expect(cls).To(BeNil())
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ls), ls)).To(Succeed())
		env.ShouldReconcile(lsRec, testutils.RequestFromObject(ls))
		Expect(env.Client(platformCluster).Get(env.Ctx, client.ObjectKeyFromObject(ls), ls)).To(Succeed())
		Expect(ls.Status.Phase).To(Equal(providerv1alpha1.LANDSCAPE_PHASE_AVAILABLE))
		cls = lsr.GetLandscape(ls.Name)
		Expect(cls).ToNot(BeNil())
		Expect(cls.Name).To(Equal(ls.Name))
		Expect(cls.Resource).To(Equal(ls))

		// check if provider config reconciliation was triggered
		timeoutCtx, cancel := context.WithTimeout(env.Ctx, 1*time.Second)
		select {
		case pce := <-lsr.ReconcileProviderConfig:
			Expect(pce).ToNot(BeNil())
			Expect(pce.Object).ToNot(BeNil())
			Expect(pce.Object.Name).To(Equal("my-pc"))
		case <-timeoutCtx.Done():
			Fail("ReconcileProviderConfig channel should have contained an element")
		}
		cancel()
	})

})
