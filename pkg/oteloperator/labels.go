package oteloperator

import "sigs.k8s.io/controller-runtime/pkg/client"

// Managed-by labels.
const (
	LabelManagedBy      = "app.kubernetes.io/managed-by"
	LabelManagedByValue = "service-provider-otel-operator"
)

// SetManagedBy sets the managed-by label of the given client.Object.
func SetManagedBy(o client.Object) {
	labels := o.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[LabelManagedBy] = LabelManagedByValue
	o.SetLabels(labels)
}
