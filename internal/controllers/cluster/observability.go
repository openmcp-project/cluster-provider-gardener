package cluster

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	errutils "github.com/openmcp-project/controller-utils/pkg/errors"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	clusterconst "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1/constants"

	gardenv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	cconst "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1/constants"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

const (
	gardenerMonitoringSecretSuffix    = ".monitoring"
	gardenerPrometheusURLAnnotation   = "prometheus-url"
	gardenerMonitoringSecretUsername  = "username"
	gardenerMonitoringSecretPassword  = "password"
	shootPrometheusScrapePath         = "/federate"
	shootPrometheusScrapeInterval     = "60s"
	shootPrometheusScrapeConfigPrefix = "shoot-prom-"
)

var scrapeConfigGVK = schema.GroupVersionKind{
	Group:   "monitoring.coreos.com",
	Version: "v1alpha1",
	Kind:    "ScrapeConfig",
}

func shootPrometheusResourceName(c *clustersv1alpha1.Cluster) string {
	return shootPrometheusScrapeConfigPrefix + ctrlutils.NameHashSHAKE128Base32(shared.Environment(), shared.ProviderName(), c.Namespace, c.Name)
}

func shootPrometheusResourceLabels(c *clustersv1alpha1.Cluster, shoot *gardenv1beta1.Shoot, profile *shared.Profile) map[string]string {
	return map[string]string{
		providerv1alpha1.ObservabilityLabel:               providerv1alpha1.ObservabilityLabelValueEnabled,
		providerv1alpha1.ManagedByNameLabel:               c.Name,
		providerv1alpha1.ManagedByNamespaceLabel:          c.Namespace,
		providerv1alpha1.ClusterReferenceLabelName:        c.Name,
		providerv1alpha1.ClusterReferenceLabelNamespace:   c.Namespace,
		providerv1alpha1.ClusterReferenceLabelProvider:    shared.ProviderName(),
		providerv1alpha1.ClusterReferenceLabelEnvironment: shared.Environment(),
		"gardener.clusters.openmcp.cloud/shoot-name":      shoot.Name,
		"gardener.clusters.openmcp.cloud/shoot-namespace": shoot.Namespace,
		"gardener.clusters.openmcp.cloud/project":         profile.Project.Name,
	}
}

func (r *ClusterReconciler) ensureShootPrometheusObservability(ctx context.Context, c *clustersv1alpha1.Cluster, shoot *gardenv1beta1.Shoot, profile *shared.Profile, landscape *shared.Landscape) errutils.ReasonableError {
	if _, err := r.PlatformCluster.Client().RESTMapper().RESTMapping(scrapeConfigGVK.GroupKind(), scrapeConfigGVK.Version); err != nil {
		if meta.IsNoMatchError(err) {
			return errutils.WithReason(fmt.Errorf("ScrapeConfig CRD %s is not installed on platform cluster", scrapeConfigGVK.GroupVersion().String()), cconst.ReasonConfigurationProblem)
		}
		return errutils.WithReason(fmt.Errorf("error checking ScrapeConfig CRD on platform cluster: %w", err), clusterconst.ReasonPlatformClusterInteractionProblem)
	}

	monitoringSecret := &corev1.Secret{}
	monitoringSecret.SetName(shoot.Name + gardenerMonitoringSecretSuffix)
	monitoringSecret.SetNamespace(shoot.Namespace)
	if err := landscape.Cluster.Client().Get(ctx, client.ObjectKeyFromObject(monitoringSecret), monitoringSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return errutils.WithReason(fmt.Errorf("gardener monitoring secret '%s/%s' does not exist yet", monitoringSecret.Namespace, monitoringSecret.Name), cconst.ReasonConfigurationProblem)
		}
		return errutils.WithReason(fmt.Errorf("error getting Gardener monitoring secret '%s/%s': %w", monitoringSecret.Namespace, monitoringSecret.Name, err), cconst.ReasonGardenClusterInteractionProblem)
	}

	username, ok := monitoringSecret.Data[gardenerMonitoringSecretUsername]
	if !ok || len(username) == 0 {
		return errutils.WithReason(fmt.Errorf("gardener monitoring secret '%s/%s' does not contain key %q", monitoringSecret.Namespace, monitoringSecret.Name, gardenerMonitoringSecretUsername), cconst.ReasonConfigurationProblem)
	}
	password, ok := monitoringSecret.Data[gardenerMonitoringSecretPassword]
	if !ok || len(password) == 0 {
		return errutils.WithReason(fmt.Errorf("gardener monitoring secret '%s/%s' does not contain key %q", monitoringSecret.Namespace, monitoringSecret.Name, gardenerMonitoringSecretPassword), cconst.ReasonConfigurationProblem)
	}

	prometheusURL := monitoringSecret.Annotations[gardenerPrometheusURLAnnotation]
	if prometheusURL == "" {
		prometheusURL = prometheusURLFromAdvertisedAddresses(shoot.Status.AdvertisedAddresses)
	}
	target, scheme, err := scrapeTargetFromPrometheusURL(prometheusURL)
	if err != nil {
		return errutils.WithReason(fmt.Errorf("error deriving scrape target from Prometheus URL for shoot '%s/%s': %w", shoot.Namespace, shoot.Name, err), cconst.ReasonConfigurationProblem)
	}

	name := shootPrometheusResourceName(c)
	labels := shootPrometheusResourceLabels(c, shoot, profile)
	ownerReferences := []metav1.OwnerReference{{
		APIVersion: clustersv1alpha1.GroupVersion.String(),
		Kind:       "Cluster",
		Name:       c.Name,
		UID:        c.UID,
		Controller: new(true),
	}}

	forwardedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + "-auth",
			Namespace: c.Namespace,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.PlatformCluster.Client(), forwardedSecret, func() error {
		forwardedSecret.Labels = labels
		forwardedSecret.OwnerReferences = ownerReferences
		forwardedSecret.Type = corev1.SecretTypeOpaque
		forwardedSecret.Data = map[string][]byte{
			gardenerMonitoringSecretUsername: username,
			gardenerMonitoringSecretPassword: password,
		}
		return nil
	}); err != nil {
		return errutils.WithReason(fmt.Errorf("error creating/updating forwarded Prometheus auth secret '%s/%s': %w", forwardedSecret.Namespace, forwardedSecret.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
	}

	scrapeConfig := shootPrometheusScrapeConfig(name, c.Namespace, labels, ownerReferences, forwardedSecret.Name, target, scheme, c, shoot, profile)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.PlatformCluster.Client(), scrapeConfig, func() error {
		desired := shootPrometheusScrapeConfig(name, c.Namespace, labels, ownerReferences, forwardedSecret.Name, target, scheme, c, shoot, profile)
		scrapeConfig.SetLabels(desired.GetLabels())
		scrapeConfig.SetOwnerReferences(desired.GetOwnerReferences())
		scrapeConfig.Object["spec"] = desired.Object["spec"]
		return nil
	}); err != nil {
		return errutils.WithReason(fmt.Errorf("error creating/updating ScrapeConfig '%s/%s': %w", c.Namespace, name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
	}

	return nil
}

func (r *ClusterReconciler) cleanupShootPrometheusObservability(ctx context.Context, c *clustersv1alpha1.Cluster) errutils.ReasonableError {
	name := shootPrometheusResourceName(c)
	secret := &corev1.Secret{}
	secret.SetName(name + "-auth")
	secret.SetNamespace(c.Namespace)
	if err := client.IgnoreNotFound(r.PlatformCluster.Client().Delete(ctx, secret)); err != nil {
		return errutils.WithReason(fmt.Errorf("error deleting forwarded Prometheus auth secret '%s/%s': %w", secret.Namespace, secret.Name, err), clusterconst.ReasonPlatformClusterInteractionProblem)
	}

	if _, err := r.PlatformCluster.Client().RESTMapper().RESTMapping(scrapeConfigGVK.GroupKind(), scrapeConfigGVK.Version); err != nil {
		if meta.IsNoMatchError(err) {
			return nil
		}
		return errutils.WithReason(fmt.Errorf("error checking ScrapeConfig CRD on platform cluster: %w", err), clusterconst.ReasonPlatformClusterInteractionProblem)
	}
	scrapeConfig := &unstructured.Unstructured{}
	scrapeConfig.SetGroupVersionKind(scrapeConfigGVK)
	scrapeConfig.SetName(name)
	scrapeConfig.SetNamespace(c.Namespace)
	if err := client.IgnoreNotFound(r.PlatformCluster.Client().Delete(ctx, scrapeConfig)); err != nil {
		return errutils.WithReason(fmt.Errorf("error deleting ScrapeConfig '%s/%s': %w", scrapeConfig.GetNamespace(), scrapeConfig.GetName(), err), clusterconst.ReasonPlatformClusterInteractionProblem)
	}
	return nil
}

func shootPrometheusScrapeConfig(name, namespace string, labels map[string]string, ownerReferences []metav1.OwnerReference, secretName, target, scheme string, c *clustersv1alpha1.Cluster, shoot *gardenv1beta1.Shoot, profile *shared.Profile) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": scrapeConfigGVK.GroupVersion().String(),
		"kind":       scrapeConfigGVK.Kind,
		"spec": map[string]any{
			"jobName":        "gardener-shoot-prometheus-" + shoot.Namespace + "-" + shoot.Name,
			"honorLabels":    true,
			"scheme":         scheme,
			"metricsPath":    shootPrometheusScrapePath,
			"scrapeInterval": shootPrometheusScrapeInterval,
			"params": map[string]any{
				"match[]": []any{
					`{__name__=~"etcd_disk_wal_fsync_duration_seconds(_bucket|_sum|_count)?"}`,
					`{__name__="etcd_mvcc_db_total_size_in_bytes"}`,
				},
			},
			"staticConfigs": []any{
				map[string]any{
					"targets": []any{target},
					"labels": map[string]any{
						"cluster":                    c.Name,
						"cluster_namespace":          c.Namespace,
						"gardener_project":           profile.Project.Name,
						"gardener_project_namespace": profile.Project.Namespace,
						"gardener_shoot":             shoot.Name,
						"gardener_shoot_namespace":   shoot.Namespace,
					},
				},
			},
			"basicAuth": map[string]any{
				"username": map[string]any{
					"name": secretName,
					"key":  gardenerMonitoringSecretUsername,
				},
				"password": map[string]any{
					"name": secretName,
					"key":  gardenerMonitoringSecretPassword,
				},
			},
		},
	}}
	obj.SetGroupVersionKind(scrapeConfigGVK)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	obj.SetLabels(labels)
	obj.SetOwnerReferences(ownerReferences)
	return obj
}

func prometheusURLFromAdvertisedAddresses(addresses []gardenv1beta1.ShootAdvertisedAddress) string {
	for _, addr := range addresses {
		if strings.Contains(strings.ToLower(addr.Name), "prometheus") || strings.Contains(strings.ToLower(addr.URL), "prometheus") {
			return addr.URL
		}
	}
	return ""
}

func scrapeTargetFromPrometheusURL(rawURL string) (target, scheme string, err error) {
	if rawURL == "" {
		return "", "", fmt.Errorf("prometheus URL is empty")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("prometheus URL %q must include scheme and host", rawURL)
	}
	scheme = strings.ToLower(u.Scheme)
	host := u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		switch scheme {
		case "https":
			host = net.JoinHostPort(host, "443")
		case "http":
			host = net.JoinHostPort(host, "80")
		default:
			return "", "", fmt.Errorf("unsupported scheme %q", u.Scheme)
		}
	}
	return host, scheme, nil
}
