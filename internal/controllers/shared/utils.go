package shared

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openmcp-project/controller-utils/pkg/controller"
)

// GenerateCreateConditionFunc returns a function that can be used to add a condition to the given ReconcileResult.
func GenerateCreateConditionFunc[Obj client.Object](rr *controller.ReconcileResult[Obj]) func(conType string, status metav1.ConditionStatus, reason, message string) {
	var gen int64 = 0
	if controller.IsNil(rr.Object) {
		gen = rr.Object.GetGeneration()
	}
	return func(conType string, status metav1.ConditionStatus, reason, message string) {
		rr.Conditions = append(rr.Conditions, metav1.Condition{
			Type:               conType,
			Status:             status,
			ObservedGeneration: gen,
			Reason:             reason,
			Message:            message,
		})
	}
}
