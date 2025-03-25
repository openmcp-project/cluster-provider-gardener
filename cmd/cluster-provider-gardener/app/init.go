package app

import (
	"context"
	"errors"
	goflag "flag"
	"fmt"

	"github.com/spf13/cobra"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openmcp-project/controller-utils/pkg/logging"

	"github.com/openmcp-project/cluster-provider-gardener/api/crds"
)

func NewInitCommand() *cobra.Command {
	opts := &InitOptions{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the Gardener ClusterProvider",
		Run: func(cmd *cobra.Command, args []string) {
			if err := opts.Complete(cmd.Context()); err != nil {
				panic(fmt.Errorf("error completing options: %w", err))
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
	Log logging.Logger
}

func (o *InitOptions) AddFlags(cmd *cobra.Command) {
	// logging flags
	logging.InitFlags(cmd.Flags())

	// add '--kubeconfig' flag
	cmd.Flags().AddGoFlagSet(goflag.CommandLine)
	// and modify its help text to match the rest of the project
	cmd.Flag("kubeconfig").Usage = "path to the kubeconfig file to use (only required if out-of-cluster)"
}

func (o *InitOptions) Complete(ctx context.Context) error {
	// build logger
	log, err := logging.GetLogger()
	if err != nil {
		return err
	}
	o.Log = log
	ctrl.SetLogger(o.Log.Logr())
	return nil
}

func (o *InitOptions) Run(ctx context.Context) error {
	sc := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sc))
	utilruntime.Must(apiextv1.AddToScheme(sc))
	k8sClient, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: sc})
	if err != nil {
		return fmt.Errorf("failed to create k8s client: %w", err)
	}

	log := o.Log.WithName("main")

	// apply CRDs
	crdList := crds.CRDs()
	var errs error
	for _, crd := range crdList {
		actual := &apiextv1.CustomResourceDefinition{}
		actual.Name = crd.Name
		log.Info("creating/updating CRD", "name", crd.Name)
		_, err := ctrl.CreateOrUpdate(ctx, k8sClient, actual, func() error {
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
