package landscape

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/controller-utils/pkg/logging"
	commonapi "github.com/openmcp-project/openmcp-operator/api/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	"github.com/openmcp-project/cluster-provider-gardener/api/install"
	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

func TestReferencedSecretUpdateReconcilesLandscapeAndReplacesClient(t *testing.T) {
	apiHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api":
			_, _ = fmt.Fprint(w, `{"apiVersion":"v1","kind":"APIVersions","versions":["v1"]}`)
		case "/apis":
			_, _ = fmt.Fprint(w, `{"apiVersion":"v1","kind":"APIGroupList","groups":[{"name":"authorization.k8s.io","versions":[{"groupVersion":"authorization.k8s.io/v1","version":"v1"}],"preferredVersion":{"groupVersion":"authorization.k8s.io/v1","version":"v1"}}]}`)
		case "/apis/authorization.k8s.io/v1":
			_, _ = fmt.Fprint(w, `{"apiVersion":"v1","kind":"APIResourceList","groupVersion":"authorization.k8s.io/v1","resources":[{"name":"selfsubjectrulesreviews","singularName":"","namespaced":false,"kind":"SelfSubjectRulesReview","verbs":["create"]}]}`)
		case "/apis/authorization.k8s.io/v1/selfsubjectrulesreviews":
			if r.Method == http.MethodPost {
				_, _ = fmt.Fprint(w, `{"apiVersion":"authorization.k8s.io/v1","kind":"SelfSubjectRulesReview","metadata":{"name":"test-landscape"},"status":{"resourceRules":[]}}`)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewServer(apiHandler)
	defer server.Close()
	updatedServer := httptest.NewServer(apiHandler)
	defer updatedServer.Close()

	scheme := install.InstallProviderAPIs(runtime.NewScheme())
	landscape := &providerv1alpha1.Landscape{
		ObjectMeta: metav1.ObjectMeta{Name: "test-landscape"},
		Spec: providerv1alpha1.LandscapeSpec{Access: providerv1alpha1.GardenClusterAccess{
			SecretRef: &commonapi.ObjectReference{Namespace: "credentials", Name: "garden"},
		}},
	}
	secret := secretWithServer(server.URL)
	platformClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&providerv1alpha1.Landscape{}).
		WithObjects(landscape, secret).Build()
	r := NewLandscapeReconciler(shared.NewRuntimeConfiguration(clusters.NewTestClusterFromClient("platform", platformClient), nil), nil)
	ctx := logging.NewContext(context.Background(), logging.Discard())

	initialMetadata := secretMetadata("credentials", "garden", "1")
	request := r.mapSecretToLandscapes(ctx, initialMetadata)
	if len(request) != 1 || request[0].Name != landscape.Name {
		t.Fatalf("expected referenced Secret to enqueue Landscape, got %v", request)
	}
	if !secretResourceVersionChangedPredicate().Update(event.UpdateEvent{
		ObjectOld: initialMetadata,
		ObjectNew: secretMetadata("credentials", "garden", "2"),
	}) {
		t.Fatal("expected resourceVersion change to pass the Secret predicate")
	}
	if secretResourceVersionChangedPredicate().Update(event.UpdateEvent{
		ObjectOld: initialMetadata,
		ObjectNew: initialMetadata,
	}) {
		t.Fatal("unchanged resourceVersion passed the Secret predicate")
	}
	if _, err := r.Reconcile(ctx, request[0]); err != nil {
		t.Fatalf("initial reconcile failed: %v", err)
	}
	oldCluster := r.GetLandscape(landscape.Name).Cluster
	if got := oldCluster.RESTConfig().Host; got != server.URL {
		t.Fatalf("initial client host = %q, want %q", got, server.URL)
	}

	updatedSecret := secretWithServer(updatedServer.URL)
	if err := platformClient.Update(ctx, updatedSecret); err != nil {
		t.Fatalf("update Secret: %v", err)
	}
	request = r.mapSecretToLandscapes(ctx, secretMetadata("credentials", "garden", updatedSecret.ResourceVersion))
	if len(request) != 1 {
		t.Fatalf("expected updated referenced Secret to enqueue Landscape, got %v", request)
	}
	if _, err := r.Reconcile(ctx, request[0]); err != nil {
		t.Fatalf("reconcile after Secret update failed: %v", err)
	}
	newCluster := r.GetLandscape(landscape.Name).Cluster
	if newCluster == oldCluster {
		t.Fatal("Landscape retained its previous Garden client after the Secret update")
	}
	if got := newCluster.RESTConfig().Host; got != updatedServer.URL {
		t.Fatalf("updated client host = %q, want %q", got, updatedServer.URL)
	}
}

func TestUnrelatedSecretDoesNotEnqueueLandscape(t *testing.T) {
	scheme := install.InstallProviderAPIs(runtime.NewScheme())
	landscape := &providerv1alpha1.Landscape{
		ObjectMeta: metav1.ObjectMeta{Name: "test-landscape"},
		Spec: providerv1alpha1.LandscapeSpec{Access: providerv1alpha1.GardenClusterAccess{
			SecretRef: &commonapi.ObjectReference{Namespace: "credentials", Name: "garden"},
		}},
	}
	platformClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(landscape).Build()
	r := NewLandscapeReconciler(shared.NewRuntimeConfiguration(clusters.NewTestClusterFromClient("platform", platformClient), nil), nil)
	secret := secretMetadata("other", "unrelated", "1")
	if requests := r.mapSecretToLandscapes(context.Background(), secret); len(requests) != 0 {
		t.Fatalf("unrelated Secret enqueued Landscapes: %v", requests)
	}
}

func TestIgnoreAnnotationStillBlocksLandscapeEvents(t *testing.T) {
	oldLandscape := &providerv1alpha1.Landscape{ObjectMeta: metav1.ObjectMeta{
		Name: "test-landscape", Generation: 1, Annotations: map[string]string{"openmcp.cloud/operation": "ignore"},
	}}
	newLandscape := oldLandscape.DeepCopy()
	newLandscape.Generation = 2
	if landscapeEventPredicate().Update(event.UpdateEvent{ObjectOld: oldLandscape, ObjectNew: newLandscape}) {
		t.Fatal("generation change passed the Landscape predicate while ignore was set")
	}
	newLandscape.Annotations = nil
	if !landscapeEventPredicate().Update(event.UpdateEvent{ObjectOld: oldLandscape, ObjectNew: newLandscape}) {
		t.Fatal("removing ignore did not pass the Landscape predicate")
	}
}

func secretWithServer(server string) *corev1.Secret {
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: garden
  cluster:
    server: %s
users:
- name: garden
  user: {}
contexts:
- name: garden
  context:
    cluster: garden
    user: garden
current-context: garden
`, server)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "credentials", Name: "garden"},
		Data:       map[string][]byte{"kubeconfig": []byte(kubeconfig)},
	}
}

func secretMetadata(namespace, name, resourceVersion string) *metav1.PartialObjectMetadata {
	secret := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
		Namespace: namespace, Name: name, ResourceVersion: resourceVersion,
	}}
	secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	return secret
}
