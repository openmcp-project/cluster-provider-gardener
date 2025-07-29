package app

import (
	"fmt"
	"os"

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
		PlatformCluster:  clusters.New("platform"),
	}
	so.AddPersistentFlags(cmd)
	cmd.AddCommand(NewInitCommand(so))
	cmd.AddCommand(NewRunCommand(so))

	return cmd
}

type RawSharedOptions struct {
	Environment string `json:"environment"`
	DryRun      bool   `json:"dry-run"`
}

type SharedOptions struct {
	*RawSharedOptions
	PlatformCluster *clusters.Cluster

	// fields filled in Complete()
	Log          logging.Logger
	ProviderName string
}

func (o *SharedOptions) AddPersistentFlags(cmd *cobra.Command) {
	// logging
	logging.InitFlags(cmd.PersistentFlags())
	// clusters
	o.PlatformCluster.RegisterSingleConfigPathFlag(cmd.PersistentFlags())
	// environment
	cmd.PersistentFlags().StringVar(&o.Environment, "environment", "", "Environment name. Required. This is used to distinguish between different environments that are watching the same Onboarding cluster. Must be globally unique.")
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
		"platformCluster": o.PlatformCluster.APIServerEndpoint(),
		"providerName":    o.ProviderName,
	}
	data, err := yaml.Marshal(raw)
	if err != nil {
		cmd.Println(fmt.Errorf("error marshalling completed shared options: %w", err).Error())
		return
	}
	cmd.Print(string(data))
}

func (o *SharedOptions) Complete() error {
	if o.Environment == "" {
		return fmt.Errorf("environment must not be empty")
	}
	shared.SetEnvironment(o.Environment)

	o.ProviderName = os.Getenv("OPENMCP_PROVIDER_NAME")
	if o.ProviderName == "" {
		o.ProviderName = "gardener"
	}
	shared.SetProviderName(o.ProviderName)

	// build logger
	log, err := logging.GetLogger()
	if err != nil {
		return err
	}
	o.Log = log
	ctrl.SetLogger(o.Log.Logr())

	if err := o.PlatformCluster.InitializeRESTConfig(); err != nil {
		return err
	}

	return nil
}
