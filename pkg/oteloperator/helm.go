package oteloperator

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

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

// AddAuthToHelmValues injects the CP cluster ServiceAccount token secret as a volume and
// overrides KUBERNETES_SERVICE_HOST/PORT so the otel-operator running on the workload cluster
// connects to the CP cluster API instead of the local in-cluster API.
func AddAuthToHelmValues(values *apiextensionsv1.JSON, cpCluster ManagedCluster, saSecretName string) (*apiextensionsv1.JSON, error) {
	authVolume := corev1.Volume{
		Name: "kube-api-access",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: saSecretName,
			},
		},
	}
	authVolumeMount := corev1.VolumeMount{
		Name:      "kube-api-access",
		ReadOnly:  true,
		MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
	}
	remoteHost, remotePort := cpCluster.GetHostAndPort()
	hostEnvVar := corev1.EnvVar{Name: "KUBERNETES_SERVICE_HOST", Value: remoteHost}
	portEnvVar := corev1.EnvVar{Name: "KUBERNETES_SERVICE_PORT", Value: remotePort}

	var root = map[string]json.RawMessage{}
	if values != nil && len(values.Raw) > 0 {
		if err := json.Unmarshal(values.Raw, &root); err != nil {
			return nil, fmt.Errorf("failed to unmarshal helm values: %w", err)
		}
		if root == nil {
			root = make(map[string]json.RawMessage)
		}
	}

	var extraVolumes []corev1.Volume
	if err := unmarshalIfPresent(root, "extraVolumes", &extraVolumes); err != nil {
		return nil, fmt.Errorf("failed to unmarshal extraVolumes: %w", err)
	}
	extraVolumes = removeConflictingVolumesAndAppend(extraVolumes, authVolume)
	extraVolumesRaw, err := json.Marshal(extraVolumes)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal extraVolumes: %w", err)
	}
	root["extraVolumes"] = extraVolumesRaw

	for _, section := range []string{"manager"} {
		var sectionValues map[string]json.RawMessage
		if err := unmarshalIfPresent(root, section, &sectionValues); err != nil {
			return nil, fmt.Errorf("failed to unmarshal %s: %w", section, err)
		}
		if sectionValues == nil {
			sectionValues = make(map[string]json.RawMessage)
		}
		var mounts []corev1.VolumeMount
		var envs []corev1.EnvVar
		if err := unmarshalIfPresent(sectionValues, "extraVolumeMounts", &mounts); err != nil {
			return nil, fmt.Errorf("failed to unmarshal %s.extraVolumeMounts: %w", section, err)
		}
		if err := unmarshalIfPresent(sectionValues, "extraEnv", &envs); err != nil {
			return nil, fmt.Errorf("failed to unmarshal %s.extraEnv: %w", section, err)
		}
		mounts = removeConflictingVolumeMountsAndAppend(mounts, authVolumeMount)
		envs = removeConflictingEnvVarsAndAppend(envs, hostEnvVar)
		envs = removeConflictingEnvVarsAndAppend(envs, portEnvVar)
		mountsRaw, err := json.Marshal(mounts)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal %s.extraVolumeMounts: %w", section, err)
		}
		envsRaw, err := json.Marshal(envs)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal %s.extraEnv: %w", section, err)
		}
		sectionValues["extraVolumeMounts"] = mountsRaw
		sectionValues["extraEnv"] = envsRaw
		sectionValuesRaw, err := json.Marshal(sectionValues)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal %s: %w", section, err)
		}
		root[section] = sectionValuesRaw
	}

	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal helm values: %w", err)
	}
	return &apiextensionsv1.JSON{Raw: out}, nil
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
