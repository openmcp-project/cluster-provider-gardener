package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/openmcp-project/controller-utils/pkg/logging"

	// clusterscheme "github.com/openmcp-project/cluster-provider-gardener/api/clusters/install"
	providerscheme "github.com/openmcp-project/cluster-provider-gardener/api/install"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/landscape"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

var setupLog logging.Logger

func NewRunCommand(so *SharedOptions) *cobra.Command {
	opts := &RunOptions{
		SharedOptions: so,
	}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the Gardener ClusterProvider",
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

type RunOptions struct {
	*SharedOptions

	metricsAddr                                      string
	metricsCertPath, metricsCertName, metricsCertKey string
	webhookCertPath, webhookCertName, webhookCertKey string
	enableLeaderElection                             bool
	probeAddr                                        string
	pprofAddr                                        string
	secureMetrics                                    bool
	enableHTTP2                                      bool
	tlsOpts                                          []func(*tls.Config)

	// fields filled in Complete()
	WebhookTLSOpts       []func(*tls.Config)
	MetricsServerOptions metricsserver.Options
	MetricsCertWatcher   *certwatcher.CertWatcher
	WebhookCertWatcher   *certwatcher.CertWatcher
}

func (o *RunOptions) AddFlags(cmd *cobra.Command) {
	// kubebuilder default flags
	cmd.Flags().StringVar(&o.metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	cmd.Flags().StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	cmd.Flags().StringVar(&o.pprofAddr, "pprof-bind-address", "", "The address the pprof endpoint binds to. Expected format is ':<port>'. Leave empty to disable pprof endpoint.")
	cmd.Flags().BoolVar(&o.enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	cmd.Flags().BoolVar(&o.secureMetrics, "metrics-secure", true, "If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	cmd.Flags().StringVar(&o.webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	cmd.Flags().StringVar(&o.webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	cmd.Flags().StringVar(&o.webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	cmd.Flags().StringVar(&o.metricsCertPath, "metrics-cert-path", "", "The directory that contains the metrics server certificate.")
	cmd.Flags().StringVar(&o.metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	cmd.Flags().StringVar(&o.metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	cmd.Flags().BoolVar(&o.enableHTTP2, "enable-http2", false, "If set, HTTP/2 will be enabled for the metrics and webhook servers")
}

func (o *RunOptions) Complete(ctx context.Context) error {
	if err := o.SharedOptions.Complete(); err != nil {
		return err
	}
	setupLog = o.Log.WithName("setup")
	ctrl.SetLogger(o.Log.Logr())

	// kubebuilder default stuff

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !o.enableHTTP2 {
		o.tlsOpts = append(o.tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	o.WebhookTLSOpts = o.tlsOpts

	if len(o.webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates", "webhook-cert-path", o.webhookCertPath, "webhook-cert-name", o.webhookCertName, "webhook-cert-key", o.webhookCertKey)

		var err error
		o.WebhookCertWatcher, err = certwatcher.New(
			filepath.Join(o.webhookCertPath, o.webhookCertName),
			filepath.Join(o.webhookCertPath, o.webhookCertKey),
		)
		if err != nil {
			return fmt.Errorf("failed to initialize webhook certificate watcher: %w", err)
		}

		o.WebhookTLSOpts = append(o.WebhookTLSOpts, func(config *tls.Config) {
			config.GetCertificate = o.WebhookCertWatcher.GetCertificate
		})
	}

	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	o.MetricsServerOptions = metricsserver.Options{
		BindAddress:   o.metricsAddr,
		SecureServing: o.secureMetrics,
		TLSOpts:       o.tlsOpts,
	}

	if o.secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/metrics/filters#WithAuthenticationAndAuthorization
		o.MetricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(o.metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates", "metrics-cert-path", o.metricsCertPath, "metrics-cert-name", o.metricsCertName, "metrics-cert-key", o.metricsCertKey)

		var err error
		o.MetricsCertWatcher, err = certwatcher.New(
			filepath.Join(o.metricsCertPath, o.metricsCertName),
			filepath.Join(o.metricsCertPath, o.metricsCertKey),
		)
		if err != nil {
			return fmt.Errorf("failed to initialize metrics certificate watcher: %w", err)
		}

		o.MetricsServerOptions.TLSOpts = append(o.MetricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = o.MetricsCertWatcher.GetCertificate
		})
	}

	return nil
}

func (o *RunOptions) Run(ctx context.Context) error {
	if err := o.Clusters.Onboarding.InitializeClient(providerscheme.InstallProviderAPIs(runtime.NewScheme())); err != nil {
		return err
	}
	if err := o.Clusters.Platform.InitializeClient(providerscheme.InstallProviderAPIs(runtime.NewScheme())); err != nil {
		return err
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: o.WebhookTLSOpts,
	})

	mgr, err := ctrl.NewManager(o.Clusters.Onboarding.RESTConfig(), ctrl.Options{
		Scheme:                 providerscheme.InstallProviderAPIs(runtime.NewScheme()),
		Metrics:                o.MetricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: o.probeAddr,
		PprofBindAddress:       o.pprofAddr,
		LeaderElection:         o.enableLeaderElection,
		LeaderElectionID:       "github.com/openmcp-project/cluster-provider-gardener",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return fmt.Errorf("unable to create manager: %w", err)
	}
	// add platform cluster to manager
	if err := mgr.Add(o.Clusters.Platform.Cluster()); err != nil {
		return fmt.Errorf("unable to add platform cluster to manager: %w", err)
	}

	// construct shared runtime configuration
	rc := shared.NewRuntimeConfiguration(o.Clusters.Platform, o.Clusters.Onboarding)

	// add Landscape controller to manager
	lsRec := landscape.NewLandscapeReconciler(rc)
	if err := lsRec.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("error registering Landscape controller: %w", err)
	}

	if o.MetricsCertWatcher != nil {
		setupLog.Info("Adding metrics certificate watcher to manager")
		if err := mgr.Add(o.MetricsCertWatcher); err != nil {
			return fmt.Errorf("unable to add metrics certificate watcher to manager: %w", err)
		}
	}

	if o.WebhookCertWatcher != nil {
		setupLog.Info("Adding webhook certificate watcher to manager")
		if err := mgr.Add(o.WebhookCertWatcher); err != nil {
			return fmt.Errorf("unable to add webhook certificate watcher to manager: %w", err)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("problem running manager: %w", err)
	}

	return nil
}
