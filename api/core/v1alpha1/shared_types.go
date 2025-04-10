package v1alpha1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ConditionStatus string

type Condition struct {
	// Type is the type of the condition.
	// Must be unique within the resource.
	Type string `json:"type"`

	// Status is the status of the condition.
	Status ConditionStatus `json:"status"`

	// Reason is expected to contain a CamelCased string that provides further information regarding the condition.
	// It should have a fixed value set (like an enum) to be machine-readable. The value set depends on the condition type.
	// It is optional, but should be filled at least when Status is not "True".
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message contains further details regarding the condition.
	// It is meant for human users, Reason should be used for programmatic evaluation instead.
	// It is optional, but should be filled at least when Status is not "True".
	// +optional
	Message string `json:"message,omitempty"`

	// LastTransitionTime specifies the time when this condition's status last changed.
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
}

// Implement the Condition interface from our controller-utils library
func (c *Condition) GetType() string {
	return c.Type
}
func (c *Condition) SetType(t string) {
	c.Type = t
}
func (c *Condition) GetStatus() ConditionStatus {
	return c.Status
}
func (c *Condition) SetStatus(s ConditionStatus) {
	c.Status = s
}
func (c *Condition) GetReason() string {
	return c.Reason
}
func (c *Condition) SetReason(r string) {
	c.Reason = r
}
func (c *Condition) GetMessage() string {
	return c.Message
}
func (c *Condition) SetMessage(m string) {
	c.Message = m
}
func (c *Condition) GetLastTransitionTime() time.Time {
	return c.LastTransitionTime.Time
}
func (c *Condition) SetLastTransitionTime(t time.Time) {
	c.LastTransitionTime = metav1.NewTime(t)
}

// ConditionList is a list of Conditions.
type ConditionList []Condition

// ConditionStatusFromBoolPtr converts a bool pointer into the corresponding ConditionStatus.
// If nil, "Unknown" is returned.
func ConditionStatusFromBoolPtr(src *bool) ConditionStatus {
	if src == nil {
		return CONDITION_UNKNOWN
	}
	return ConditionStatusFromBool(*src)
}

// ConditionStatusFromBool converts a bool into the corresponding ConditionStatus.
func ConditionStatusFromBool(src bool) ConditionStatus {
	if src {
		return CONDITION_TRUE
	}
	return CONDITION_FALSE
}

// IsTrue returns true if the Condition's status is "True".
// Note that the status can be "Unknown", so !IsTrue() is not the same as IsFalse().
func (cc Condition) IsTrue() bool {
	return cc.Status == CONDITION_TRUE
}

// IsFalse returns true if the Condition's status is "False".
// Note that the status can be "Unknown", so !IsFalse() is not the same as IsTrue().
func (cc Condition) IsFalse() bool {
	return cc.Status == CONDITION_FALSE
}

// IsUnknown returns true if the Condition's status is "Unknown".
func (cc Condition) IsUnknown() bool {
	return cc.Status == CONDITION_UNKNOWN
}

type ObjectReference struct {
	// Name is the name of the referenced resource.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

type NamespacedObjectReference struct {
	ObjectReference `json:",inline"`

	// Namespace is the namespace of the referenced resource.
	Namespace string `json:"namespace"`
}

// CommonStatus is a status shared by multiple resource.
// Note that a 'phase' is also part of the status, but it cannot be included in this struct.
// The reason is that we want to use string-like types for the phase, but the goddamn code generation does not support generics, no matter which annotations are added.
type CommonStatus struct {
	// ObservedGeneration is the generation of this resource that was last reconciled by the controller.
	ObservedGeneration int64 `json:"observedGeneration"`

	// LastReconcileTime is the time when the resource was last reconciled by the controller.
	LastReconcileTime metav1.Time `json:"lastReconcileTime"`

	// Reason is expected to contain a CamelCased string that provides further information in a machine-readable format.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message contains further details in a human-readable format.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions contains the conditions.
	// +optional
	Conditions ConditionList `json:"conditions,omitempty"`
}

const (
	// CONDITION_UNKNOWN represents an unknown status for the condition.
	CONDITION_UNKNOWN ConditionStatus = "Unknown"
	// CONDITION_TRUE marks the condition as true.
	CONDITION_TRUE ConditionStatus = "True"
	// CONDITION_FALSE marks the condition as false.
	CONDITION_FALSE ConditionStatus = "False"
)
