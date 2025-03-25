package app

import (
	"github.com/spf13/cobra"
)

func NewClusterProviderGardenerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster-provider-gardener",
		Short: "cluster-provider-gardener manages the Gardener ClusterProvider",
	}

	cmd.AddCommand(NewInitCommand())
	cmd.AddCommand(NewRunCommand())

	return cmd
}
