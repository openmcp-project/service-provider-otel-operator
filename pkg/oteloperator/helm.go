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

// AddAuthToHelmValues injects the CP cluster ServiceAccount token secret as a volume and
// overrides KUBERNETES_SERVICE_HOST/PORT so the otel-operator running on the workload cluster
// connects to the CP cluster API instead of the local in-cluster API.
// nolint:gocyclo
func AddAuthToHelmValues(values *apiextensionsv1.JSON, cpCluster ManagedCluster, saSecretName string) (*apiextensionsv1.JSON, error) {
	authVolume := corev1.Volume{
		Name: "kube-api-access",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: saSecretName},
		},
	}
	authVolumeMount := corev1.VolumeMount{
		Name:      "kube-api-access",
		ReadOnly:  true,
		MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
	}
	remoteHost, remotePort := cpCluster.GetHostAndPort()

	root, err := unmarshalRoot(values)
	if err != nil {
		return nil, err
	}

	var extraVolumes []corev1.Volume
	if err := unmarshalIfPresent(root, "extraVolumes", &extraVolumes); err != nil {
		return nil, fmt.Errorf("failed to unmarshal extraVolumes: %w", err)
	}
	extraVolumesRaw, err := json.Marshal(removeConflictingVolumesAndAppend(extraVolumes, authVolume))
	if err != nil {
		return nil, fmt.Errorf("failed to marshal extraVolumes: %w", err)
	}
	root["extraVolumes"] = extraVolumesRaw

	var opValues map[string]json.RawMessage
	if err := unmarshalIfPresent(root, "opentelemetry-operator", &opValues); err != nil {
		return nil, fmt.Errorf("failed to unmarshal opentelemetry-operator: %w", err)
	}
	if opValues == nil {
		opValues = make(map[string]json.RawMessage)
	}
	if err := injectAuthIntoManager(opValues, authVolumeMount, remoteHost, remotePort); err != nil {
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

func injectAuthIntoManager(root map[string]json.RawMessage, mount corev1.VolumeMount, host, port string) error {
	hostEnvVar := corev1.EnvVar{Name: "KUBERNETES_SERVICE_HOST", Value: host}
	portEnvVar := corev1.EnvVar{Name: "KUBERNETES_SERVICE_PORT", Value: port}

	var managerValues map[string]json.RawMessage
	if err := unmarshalIfPresent(root, "manager", &managerValues); err != nil {
		return fmt.Errorf("failed to unmarshal manager: %w", err)
	}
	if managerValues == nil {
		managerValues = make(map[string]json.RawMessage)
	}
	var mounts []corev1.VolumeMount
	var envs []corev1.EnvVar
	if err := unmarshalIfPresent(managerValues, "extraVolumeMounts", &mounts); err != nil {
		return fmt.Errorf("failed to unmarshal manager.extraVolumeMounts: %w", err)
	}
	if err := unmarshalIfPresent(managerValues, "extraEnvs", &envs); err != nil {
		return fmt.Errorf("failed to unmarshal manager.extraEnvs: %w", err)
	}
	mountsRaw, err := json.Marshal(removeConflictingVolumeMountsAndAppend(mounts, mount))
	if err != nil {
		return fmt.Errorf("failed to marshal manager.extraVolumeMounts: %w", err)
	}
	envsRaw, err := json.Marshal(removeConflictingEnvVarsAndAppend(removeConflictingEnvVarsAndAppend(envs, hostEnvVar), portEnvVar))
	if err != nil {
		return fmt.Errorf("failed to marshal manager.extraEnvs: %w", err)
	}
	managerValues["extraVolumeMounts"] = mountsRaw
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

func removeConflictingVolumesAndAppend(volumes []corev1.Volume, newVolume corev1.Volume) []corev1.Volume {
	updated := []corev1.Volume{}
	for _, v := range volumes {
		if v.Name != newVolume.Name {
			updated = append(updated, v)
		}
	}
	return append(updated, newVolume)
}

func removeConflictingVolumeMountsAndAppend(mounts []corev1.VolumeMount, newMount corev1.VolumeMount) []corev1.VolumeMount {
	updated := []corev1.VolumeMount{}
	for _, m := range mounts {
		if m.MountPath != newMount.MountPath && m.Name != newMount.Name {
			updated = append(updated, m)
		}
	}
	return append(updated, newMount)
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
