package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ctrlutil "github.com/openmcp-project/controller-utils/pkg/controller"

	clustersv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/clusters/v1alpha1"
	"github.com/openmcp-project/cluster-provider-gardener/api/crds"
	providerscheme "github.com/openmcp-project/cluster-provider-gardener/api/install"
)

func NewInitCommand(so *SharedOptions) *cobra.Command {
	opts := &InitOptions{
		SharedOptions: so,
	}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the Gardener ClusterProvider",
		Run: func(cmd *cobra.Command, args []string) {
			opts.PrintRawOptions(cmd)
			if err := opts.Complete(cmd.Context()); err != nil {
				panic(fmt.Errorf("error completing options: %w", err))
			}
			opts.PrintCompletedOptions(cmd)
			if opts.DryRun {
				cmd.Println("=== END OF DRY RUN ===")
				return
			}
			if err := opts.Run(cmd.Context()); err != nil {
				panic(err)
			}
		},
	}
	opts.AddFlags(cmd)

	return cmd
}

type InitOptions struct {
	*SharedOptions
}

func (o *InitOptions) AddFlags(cmd *cobra.Command) {}

func (o *InitOptions) PrintRaw(cmd *cobra.Command) {}

func (o *InitOptions) PrintRawOptions(cmd *cobra.Command) {
	cmd.Println("########## RAW OPTIONS START ##########")
	o.SharedOptions.PrintRaw(cmd)
	o.PrintRaw(cmd)
	cmd.Println("########## RAW OPTIONS END ##########")
}

func (o *InitOptions) Complete(ctx context.Context) error {
	if err := o.SharedOptions.Complete(); err != nil {
		return err
	}

	return nil
}

func (o *InitOptions) PrintCompleted(cmd *cobra.Command) {}

func (o *InitOptions) PrintCompletedOptions(cmd *cobra.Command) {
	cmd.Println("########## COMPLETED OPTIONS START ##########")
	o.SharedOptions.PrintCompleted(cmd)
	o.PrintCompleted(cmd)
	cmd.Println("########## COMPLETED OPTIONS END ##########")
}

func (o *InitOptions) Run(ctx context.Context) error {
	if err := o.Clusters.Onboarding.InitializeClient(providerscheme.InstallCRDAPIs(runtime.NewScheme())); err != nil {
		return err
	}
	if err := o.Clusters.Platform.InitializeClient(providerscheme.InstallCRDAPIs(runtime.NewScheme())); err != nil {
		return err
	}

	log := o.Log.WithName("main")
	log.Info("Environment", "value", o.Environment)
	log.Info("ProviderName", "value", o.ProviderName)

	// apply CRDs
	crdList := crds.CRDs()
	var errs error
	for _, crd := range crdList {
		var c client.Client
		clusterLabel, _ := ctrlutil.GetLabel(crd, clustersv1alpha1.ClusterLabel)
		switch clusterLabel {
		case clustersv1alpha1.PURPOSE_ONBOARDING:
			c = o.Clusters.Onboarding.Client()
		case clustersv1alpha1.PURPOSE_PLATFORM:
			c = o.Clusters.Platform.Client()
		default:
			return fmt.Errorf("missing cluster label '%s' or unsupported value '%s' for CRD '%s'", clustersv1alpha1.ClusterLabel, clusterLabel, crd.Name)
		}
		actual := &apiextv1.CustomResourceDefinition{}
		actual.Name = crd.Name
		log.Info("creating/updating CRD", "name", crd.Name, "cluster", clusterLabel)
		_, err := ctrl.CreateOrUpdate(ctx, c, actual, func() error {
			crd.Spec.DeepCopyInto(&actual.Spec)
			return nil
		})
		errs = errors.Join(errs, err)
	}
	if errs != nil {
		return fmt.Errorf("error creating/updating CRDs: %w", errs)
	}

	log.Info("finished init command")
	return nil
}
