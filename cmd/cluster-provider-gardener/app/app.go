package app

import (
	"fmt"
	"os"
	"slices"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/controller-utils/pkg/logging"

	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"

	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/yaml"
)

func NewClusterProviderGardenerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster-provider-gardener",
		Short: "cluster-provider-gardener manages the Gardener ClusterProvider",
	}
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)

	so := &SharedOptions{
		RawSharedOptions: &RawSharedOptions{},
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

type RawSharedOptions struct {
	Environment  string `json:"environment"`
	ProviderName string `json:"provider-name"`
	DryRun       bool   `json:"dry-run"`
}

type SharedOptions struct {
	*RawSharedOptions
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
	// environment
	cmd.PersistentFlags().StringVar(&o.Environment, "environment", "", "Environment name. Required. This is used to distinguish between different environments that are watching the same Onboarding cluster. Must be globally unique.")
	cmd.PersistentFlags().StringVar(&o.ProviderName, "provider-name", "gardener", "Name of the ClusterProvider resource that created this operator instance. Expected to be unique per environment.")
	cmd.PersistentFlags().BoolVar(&o.DryRun, "dry-run", false, "If set, the command aborts after evaluation of the given flags.")
}

func (o *SharedOptions) PrintRaw(cmd *cobra.Command) {
	data, err := yaml.Marshal(o.RawSharedOptions)
	if err != nil {
		cmd.Println(fmt.Errorf("error marshalling raw shared options: %w", err).Error())
		return
	}
	cmd.Print(string(data))
}

func (o *SharedOptions) PrintCompleted(cmd *cobra.Command) {
	raw := map[string]any{
		"clusters": map[string]any{
			"onboarding": o.Clusters.Onboarding.APIServerEndpoint(),
			"platform":   o.Clusters.Platform.APIServerEndpoint(),
		},
	}
	data, err := yaml.Marshal(raw)
	if err != nil {
		cmd.Println(fmt.Errorf("error marshalling completed shared options: %w", err).Error())
		return
	}
	cmd.Print(string(data))
}

const (
	SKIP_LOGGER                    = "logger"
	SKIP_ONBOARDING_CLUSTER_CONFIG = "onboarding-cluster-config"
	SKIP_PLATFORM_CLUSTER_CONFIG   = "platform-cluster-config"
)

func (o *SharedOptions) Complete(skipCompletion ...string) error {
	if o.Environment == "" {
		return fmt.Errorf("environment must not be empty")
	}
	shared.SetEnvironment(o.Environment)
	if o.ProviderName == "" {
		return fmt.Errorf("provider name must not be empty")
	}
	shared.SetProviderName(o.ProviderName)

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
