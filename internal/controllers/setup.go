package controllers

import (
	"fmt"

	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/cluster"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/config"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/landscape"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

// SetupClusterControllersWithManager is a helper function that groups the controllers that are necessary to reconcile Cluster resources.
// It initializes the Landscape, ProviderConfig, and Cluster controllers and registers them with the provided manager.
func SetupClusterControllersWithManager(mgr ctrl.Manager, rc *shared.RuntimeConfiguration, eventRecorders map[string]events.EventRecorder) (*landscape.LandscapeReconciler, *config.GardenerProviderConfigReconciler, *cluster.ClusterReconciler, error) {
	lsRec := landscape.NewLandscapeReconciler(rc, eventRecorders[landscape.ControllerName])
	if err := lsRec.SetupWithManager(mgr); err != nil {
		return lsRec, nil, nil, fmt.Errorf("error registering Landscape controller: %w", err)
	}
	pcRec := config.NewGardenerProviderConfigReconciler(rc, eventRecorders[config.ControllerName])
	if err := pcRec.SetupWithManager(mgr); err != nil {
		return lsRec, pcRec, nil, fmt.Errorf("error registering ProviderConfig controller: %w", err)
	}
	cRec := cluster.NewClusterReconciler(rc, eventRecorders[cluster.ControllerName])
	if err := cRec.SetupWithManager(mgr); err != nil {
		return lsRec, pcRec, cRec, fmt.Errorf("error registering Cluster controller: %w", err)
	}
	return lsRec, pcRec, cRec, nil
}
