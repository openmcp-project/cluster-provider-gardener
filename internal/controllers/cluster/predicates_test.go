package cluster

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
)

func TestClusterEventPredicate(t *testing.T) {
	for _, tt := range []struct {
		name       string
		oldValue   string
		newValue   string
		operation  string
		generation int64
		want       bool
	}{
		{name: "enable", newValue: "enabled", want: true},
		{name: "remove", oldValue: "enabled", want: true},
		{name: "disable by value", oldValue: "enabled", newValue: "disabled", want: true},
		{name: "enable from disabled", oldValue: "disabled", newValue: "enabled", want: true},
		{name: "unchanged enabled", oldValue: "enabled", newValue: "enabled"},
		{name: "unrelated label only"},
		{name: "ignore still wins", newValue: "enabled", operation: openmcpconst.OperationAnnotationValueIgnore},
		{name: "explicit reconcile", operation: openmcpconst.OperationAnnotationValueReconcile, want: true},
		{name: "spec change", generation: 2, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			old := &clustersv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Generation: 1, Labels: map[string]string{}}}
			if tt.oldValue != "" {
				old.Labels[providerv1alpha1.ObservabilityLabel] = tt.oldValue
			}
			updated := old.DeepCopy()
			updated.Labels["unrelated"] = "changed"
			delete(updated.Labels, providerv1alpha1.ObservabilityLabel)
			if tt.newValue != "" {
				updated.Labels[providerv1alpha1.ObservabilityLabel] = tt.newValue
			}
			if tt.operation != "" {
				updated.Annotations = map[string]string{openmcpconst.OperationAnnotation: tt.operation}
			}
			if tt.generation != 0 {
				updated.Generation = tt.generation
			}
			if got := clusterEventPredicate().Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}); got != tt.want {
				t.Fatalf("Update() = %v, want %v", got, tt.want)
			}
		})
	}
}
