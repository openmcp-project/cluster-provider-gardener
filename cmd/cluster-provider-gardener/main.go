package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openmcp-project/controller-utils/pkg/fips"

	"github.com/openmcp-project/cluster-provider-gardener/cmd/cluster-provider-gardener/app"
)

func main() {
	fips.Verify(context.Background())

	cmd := app.NewClusterProviderGardenerCommand()

	if err := cmd.Execute(); err != nil {
		fmt.Print(err)
		os.Exit(1)
	}
}
