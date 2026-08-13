package oteloperator

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

const helmKeyEnabled = "enabled"

// HelmValues defines the helm values that are explicitly processed during reconciliation.
type HelmValues struct {
	NamespaceOverride string `json:"namespaceOverride,omitempty"`
	Global            Global `json:"global,omitempty"`
}

// Global defines the global settings that are explicitly processed during reconciliation.
type Global struct {
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// ExtractHelmValues extracts helm values required for processing.
func ExtractHelmValues(values *apiextensionsv1.JSON) (*HelmValues, error) {
	if values == nil || len(values.Raw) == 0 {
		return &HelmValues{}, nil
	}
	vals := &HelmValues{}
	if err := json.Unmarshal(values.Raw, vals); err != nil {
		return nil, err
	}
	return vals, nil
}

// WorkloadHelmValues sets helm values required for the workload-cluster Helm release.
// The OpenTelemetry CRDs are installed by the CP Helm release, so the workload release
// must render only the operator/runtime objects from the opentelemetry-kube-stack chart.
// automountServiceAccountToken is disabled so the chart creates an explicit "access-token"
// projected volume that the post-renderer can replace with the CP credential secret.
func WorkloadHelmValues(values *apiextensionsv1.JSON) (*apiextensionsv1.JSON, error) {
	root, err := unmarshalRoot(values)
	if err != nil {
		return nil, err
	}
	setKubeStackDisabledDefaults(root)
	setRawValue(root, "crds", map[string]any{
		"installOtel":       false,
		"installPrometheus": false,
	})
	opValues := map[string]json.RawMessage{}
	if err := unmarshalIfPresent(root, "opentelemetry-operator", &opValues); err != nil {
		return nil, fmt.Errorf("failed to unmarshal opentelemetry-operator: %w", err)
	}
	setRawValue(opValues, helmKeyEnabled, true)
	setRawValue(opValues, "crds", map[string]any{"create": false})
	// Disable automounting of the workload-cluster SA token. The chart then creates
	// an explicit "access-token" projected volume which the Flux post-renderer
	// replaces with the CP credential secret.
	setRawValue(opValues, "automountServiceAccountToken", false)
	opValuesRaw, err := json.Marshal(opValues)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal opentelemetry-operator: %w", err)
	}
	root["opentelemetry-operator"] = opValuesRaw
	return marshalRoot(root, "workload helm values")
}

// CRDHelmValues sets helm values required for the CP Helm release. It uses the
// opentelemetry-kube-stack chart only for its otel-crds subchart and disables all runtime resources.
func CRDHelmValues(_ *apiextensionsv1.JSON) (*apiextensionsv1.JSON, error) {
	root := map[string]json.RawMessage{}
	setKubeStackDisabledDefaults(root)
	setRawValue(root, "crds", map[string]any{
		"installOtel":       true,
		"installPrometheus": false,
	})
	setRawValue(root, "opentelemetry-operator", map[string]any{
		helmKeyEnabled: false,
	})
	return marshalRoot(root, "CRD helm values")
}

// AddAuthToHelmValues injects KUBERNETES_SERVICE_HOST/PORT env vars into the
// opentelemetry-operator manager so it connects to the CP cluster API.
// The SA token and CA cert are supplied by the post-renderer-patched volume mount
// (see cpAccessPostRenderers in flux.go), so no volume configuration is needed here.
// nolint:gocyclo
func AddAuthToHelmValues(values *apiextensionsv1.JSON, cpCluster ManagedCluster, _ string) (*apiextensionsv1.JSON, error) {
	remoteHost, remotePort := cpCluster.GetHostAndPort()

	root, err := unmarshalRoot(values)
	if err != nil {
		return nil, err
	}

	var opValues map[string]json.RawMessage
	if err := unmarshalIfPresent(root, "opentelemetry-operator", &opValues); err != nil {
		return nil, fmt.Errorf("failed to unmarshal opentelemetry-operator: %w", err)
	}
	if opValues == nil {
		opValues = make(map[string]json.RawMessage)
	}
	if err := injectAuthIntoManager(opValues, remoteHost, remotePort); err != nil {
		return nil, err
	}
	opValuesRaw, err := json.Marshal(opValues)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal opentelemetry-operator: %w", err)
	}
	root["opentelemetry-operator"] = opValuesRaw

	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal helm values: %w", err)
	}
	return &apiextensionsv1.JSON{Raw: out}, nil
}

func injectAuthIntoManager(root map[string]json.RawMessage, host, port string) error {
	hostEnvVar := corev1.EnvVar{Name: "KUBERNETES_SERVICE_HOST", Value: host}
	portEnvVar := corev1.EnvVar{Name: "KUBERNETES_SERVICE_PORT", Value: port}

	var managerValues map[string]json.RawMessage
	if err := unmarshalIfPresent(root, "manager", &managerValues); err != nil {
		return fmt.Errorf("failed to unmarshal manager: %w", err)
	}
	if managerValues == nil {
		managerValues = make(map[string]json.RawMessage)
	}
	var envs []corev1.EnvVar
	if err := unmarshalIfPresent(managerValues, "extraEnvs", &envs); err != nil {
		return fmt.Errorf("failed to unmarshal manager.extraEnvs: %w", err)
	}
	envsRaw, err := json.Marshal(removeConflictingEnvVarsAndAppend(removeConflictingEnvVarsAndAppend(envs, hostEnvVar), portEnvVar))
	if err != nil {
		return fmt.Errorf("failed to marshal manager.extraEnvs: %w", err)
	}
	managerValues["extraEnvs"] = envsRaw
	managerValuesRaw, err := json.Marshal(managerValues)
	if err != nil {
		return fmt.Errorf("failed to marshal manager: %w", err)
	}
	root["manager"] = managerValuesRaw
	return nil
}

func unmarshalRoot(values *apiextensionsv1.JSON) (map[string]json.RawMessage, error) {
	root := map[string]json.RawMessage{}
	if values != nil && len(values.Raw) > 0 {
		if err := json.Unmarshal(values.Raw, &root); err != nil {
			return nil, fmt.Errorf("failed to unmarshal helm values: %w", err)
		}
		if root == nil {
			root = make(map[string]json.RawMessage)
		}
	}
	return root, nil
}

func unmarshalIfPresent(obj map[string]json.RawMessage, key string, out any) error {
	raw, ok := obj[key]
	if !ok || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("invalid %s JSON: %w", key, err)
	}
	return nil
}

func setKubeStackDisabledDefaults(root map[string]json.RawMessage) {
	for key, value := range map[string]any{
		"cleanupJob":       map[string]any{helmKeyEnabled: false},
		"clusterRole":      map[string]any{helmKeyEnabled: false},
		"defaultCRConfig":  map[string]any{helmKeyEnabled: false},
		"instrumentation":  map[string]any{helmKeyEnabled: false},
		"kubeStateMetrics": map[string]any{helmKeyEnabled: false},
		"nodeExporter":     map[string]any{helmKeyEnabled: false},
		"collectors":       map[string]any{"daemon": map[string]any{helmKeyEnabled: false}},
	} {
		setRawValue(root, key, value)
	}
}

func setRawValue(root map[string]json.RawMessage, key string, value any) {
	raw, _ := json.Marshal(value)
	root[key] = raw
}

func marshalRoot(root map[string]json.RawMessage, context string) (*apiextensionsv1.JSON, error) {
	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal %s: %w", context, err)
	}
	return &apiextensionsv1.JSON{Raw: out}, nil
}

func removeConflictingEnvVarsAndAppend(envVars []corev1.EnvVar, newEnvVar corev1.EnvVar) []corev1.EnvVar {
	updated := []corev1.EnvVar{}
	for _, e := range envVars {
		if e.Name != newEnvVar.Name {
			updated = append(updated, e)
		}
	}
	return append(updated, newEnvVar)
}
