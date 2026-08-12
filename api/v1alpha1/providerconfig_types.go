/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProviderConfigSpec defines the desired state of ProviderConfig
type ProviderConfigSpec struct {
	// ChartURL is a reference to an OCI artifact repository that hosts the opentelemetry-kube-stack Helm chart.
	// The provider uses this chart for both the CP CRD release and workload operator release.
	// +optional
	// +kubebuilder:default="oci://ghcr.io/open-telemetry/opentelemetry-helm-charts/opentelemetry-kube-stack"
	ChartURL *string `json:"chartURL,omitempty"`

	// ChartPullSecret is a reference to the secret containing the credentials to pull the Helm chart.
	// The secret must be of type kubernetes.io/dockerconfigjson.
	// +optional
	ChartPullSecret *string `json:"chartPullSecret,omitempty"`

	// PollInterval at which the controller requeues to detect drift
	// +optional
	// +kubebuilder:default:="1m"
	// +kubebuilder:validation:Format=duration
	PollInterval *metav1.Duration `json:"pollInterval,omitempty"`

	// HelmValues are arbitrary Helm values passed directly to the managed HelmRelease.
	// +optional
	HelmValues *apiextensionsv1.JSON `json:"helmValues,omitempty"`
}

// ProviderConfigStatus defines the observed state of ProviderConfig.
type ProviderConfigStatus struct {
	// conditions represent the current state of the ProviderConfig resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ProviderConfig is the Schema for the providerconfigs API
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=platform"
type ProviderConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of ProviderConfig
	// +required
	Spec ProviderConfigSpec `json:"spec"`

	// status defines the observed state of ProviderConfig
	// +optional
	Status ProviderConfigStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// ProviderConfigList contains a list of ProviderConfig
type ProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfig `json:"items"`
}

// PollInterval returns the poll interval duration from the spec.
func (o *ProviderConfig) PollInterval() time.Duration {
	if o.Spec.PollInterval == nil {
		return time.Minute
	}
	return o.Spec.PollInterval.Duration
}

// ChartURL returns the opentelemetry-kube-stack chart URL used for both CRD and workload installation.
func (o *ProviderConfig) ChartURL() string {
	if o.Spec.ChartURL == nil || *o.Spec.ChartURL == "" {
		return "oci://ghcr.io/open-telemetry/opentelemetry-helm-charts/opentelemetry-kube-stack"
	}
	return *o.Spec.ChartURL
}
