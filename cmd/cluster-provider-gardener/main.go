package main

import (
	"fmt"
	"os"

	"github.com/openmcp-project/cluster-provider-gardener/cmd/cluster-provider-gardener/app"
)

func main() {
	cmd := app.NewClusterProviderGardenerCommand()

	if err := cmd.Execute(); err != nil {
		fmt.Print(err)
		os.Exit(1)
	}
}
