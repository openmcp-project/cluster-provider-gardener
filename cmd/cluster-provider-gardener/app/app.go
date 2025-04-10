package app

import (
	"slices"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/controller-utils/pkg/logging"

	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"
)

func NewClusterProviderGardenerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster-provider-gardener",
		Short: "cluster-provider-gardener manages the Gardener ClusterProvider",
	}

	so := &SharedOptions{
		Clusters: &Clusters{
			Onboarding: clusters.New("onboarding"),
			Platform:   clusters.New("platform"),
		},
	}
	so.AddPersistentFlags(cmd)
	cmd.AddCommand(NewInitCommand(so))
	cmd.AddCommand(NewRunCommand(so))

	return cmd
}

type SharedOptions struct {
	Clusters *Clusters

	// fields filled in Complete()
	Log logging.Logger
}

func (o *SharedOptions) AddPersistentFlags(cmd *cobra.Command) {
	// logging
	logging.InitFlags(cmd.PersistentFlags())
	// clusters
	o.Clusters.Onboarding.RegisterConfigPathFlag(cmd.PersistentFlags())
	o.Clusters.Platform.RegisterConfigPathFlag(cmd.PersistentFlags())
}

const (
	SKIP_LOGGER                    = "logger"
	SKIP_ONBOARDING_CLUSTER_CONFIG = "onboarding-cluster-config"
	SKIP_PLATFORM_CLUSTER_CONFIG   = "platform-cluster-config"
)

func (o *SharedOptions) Complete(skipCompletion ...string) error {
	if !slices.Contains(skipCompletion, SKIP_LOGGER) {
		// build logger
		log, err := logging.GetLogger()
		if err != nil {
			return err
		}
		o.Log = log
		ctrl.SetLogger(o.Log.Logr())
	}

	if !slices.Contains(skipCompletion, SKIP_PLATFORM_CLUSTER_CONFIG) {
		if err := o.Clusters.Platform.InitializeRESTConfig(); err != nil {
			return err
		}
	}

	if !slices.Contains(skipCompletion, SKIP_ONBOARDING_CLUSTER_CONFIG) {
		if err := o.Clusters.Onboarding.InitializeRESTConfig(); err != nil {
			return err
		}
	}

	return nil
}

type Clusters struct {
	Onboarding *clusters.Cluster
	Platform   *clusters.Cluster
}
