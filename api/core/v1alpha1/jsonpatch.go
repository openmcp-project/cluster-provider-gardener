package v1alpha1

// JSONPatch represents a JSON patch operation.
// Technically, a single JSON patch is already a list of patch operations. This type represents a single operation, use JSONPatches for a list of operations instead.
type JSONPatch struct {
	// Operation is the operation to perform.
	// +kubebuilder:validation:Enum=add;remove;replace;move;copy;test
	// +kubebuilder:validation:Required
	Operation JSONPatchOperation `json:"op"`

	// Path is the path to the target location in the JSON document.
	// +kubebuilder:validation:Required
	Path string `json:"path"`

	// Value is the value to set at the target location.
	// Required for add, replace, and test operations.
	// +optional
	Value *string `json:"value,omitempty"`

	// From is the source location for move and copy operations.
	// +optional
	From *string `json:"from,omitempty"`
}

// JSONPatches is a list of JSON patch operations.
// This is technically a 'JSON patch' as defined in RFC 6902.
type JSONPatches []JSONPatch

type JSONPatchOperation string

const (
	JSONPatchOperationAdd     JSONPatchOperation = "add"
	JSONPatchOperationRemove  JSONPatchOperation = "remove"
	JSONPatchOperationReplace JSONPatchOperation = "replace"
	JSONPatchOperationMove    JSONPatchOperation = "move"
	JSONPatchOperationCopy    JSONPatchOperation = "copy"
	JSONPatchOperationTest    JSONPatchOperation = "test"
)
