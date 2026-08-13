package oteloperator

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestExtractHelmValues_Nil(t *testing.T) {
	vals, err := ExtractHelmValues(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vals.NamespaceOverride != "" {
		t.Errorf("expected empty NamespaceOverride, got %q", vals.NamespaceOverride)
	}
	if len(vals.Global.ImagePullSecrets) != 0 {
		t.Errorf("expected no image pull secrets, got %d", len(vals.Global.ImagePullSecrets))
	}
}

func TestExtractHelmValues_EmptyRaw(t *testing.T) {
	vals, err := ExtractHelmValues(&apiextensionsv1.JSON{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vals.NamespaceOverride != "" {
		t.Errorf("expected empty NamespaceOverride, got %q", vals.NamespaceOverride)
	}
}

func TestExtractHelmValues_NamespaceOverride(t *testing.T) {
	raw := []byte(`{"namespaceOverride":"custom-ns"}`)
	vals, err := ExtractHelmValues(&apiextensionsv1.JSON{Raw: raw})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vals.NamespaceOverride != "custom-ns" {
		t.Errorf("expected NamespaceOverride=custom-ns, got %q", vals.NamespaceOverride)
	}
}

func TestExtractHelmValues_ImagePullSecrets(t *testing.T) {
	raw := []byte(`{"global":{"imagePullSecrets":[{"name":"regcred"},{"name":"other"}]}}`)
	vals, err := ExtractHelmValues(&apiextensionsv1.JSON{Raw: raw})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals.Global.ImagePullSecrets) != 2 {
		t.Fatalf("expected 2 image pull secrets, got %d", len(vals.Global.ImagePullSecrets))
	}
	expected := []corev1.LocalObjectReference{{Name: "regcred"}, {Name: "other"}}
	for i, s := range vals.Global.ImagePullSecrets {
		if s.Name != expected[i].Name {
			t.Errorf("secret[%d]: expected name %q, got %q", i, expected[i].Name, s.Name)
		}
	}
}

func TestExtractHelmValues_InvalidJSON(t *testing.T) {
	raw := []byte(`{invalid}`)
	_, err := ExtractHelmValues(&apiextensionsv1.JSON{Raw: raw})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestExtractHelmValues_UnknownFieldsIgnored(t *testing.T) {
	raw := []byte(`{"admissionWebhooks":{"enabled":true},"namespaceOverride":"test"}`)
	vals, err := ExtractHelmValues(&apiextensionsv1.JSON{Raw: raw})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vals.NamespaceOverride != "test" {
		t.Errorf("expected NamespaceOverride=test, got %q", vals.NamespaceOverride)
	}
}

func TestWorkloadHelmValues_ConfiguresKubeStackOperatorOnlyRelease(t *testing.T) {
	out, err := WorkloadHelmValues(&apiextensionsv1.JSON{Raw: []byte(`{"opentelemetry-operator":{"manager":{"image":{"tag":"x"}}}}`)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(out.Raw, &root); err != nil {
		t.Fatalf("invalid output JSON: %v", err)
	}
	var crds struct {
		InstallOtel       bool `json:"installOtel"`
		InstallPrometheus bool `json:"installPrometheus"`
	}
	if err := json.Unmarshal(root["crds"], &crds); err != nil {
		t.Fatalf("invalid crds JSON: %v", err)
	}
	if crds.InstallOtel || crds.InstallPrometheus {
		t.Fatalf("unexpected kube-stack crds values: %#v", crds)
	}
	var op struct {
		Enabled bool `json:"enabled"`
		CRDs    struct {
			Create bool `json:"create"`
		} `json:"crds"`
	}
	if err := json.Unmarshal(root["opentelemetry-operator"], &op); err != nil {
		t.Fatalf("invalid opentelemetry-operator JSON: %v", err)
	}
	if !op.Enabled {
		t.Fatal("expected opentelemetry-operator.enabled=true")
	}
	if op.CRDs.Create {
		t.Fatal("expected opentelemetry-operator.crds.create=false")
	}
	var opValues map[string]json.RawMessage
	if err := json.Unmarshal(root["opentelemetry-operator"], &opValues); err != nil {
		t.Fatalf("invalid opentelemetry-operator JSON: %v", err)
	}
	if _, ok := opValues["manager"]; !ok {
		t.Fatal("expected opentelemetry-operator.manager values to be preserved")
	}
	var automount bool
	if err := json.Unmarshal(opValues["automountServiceAccountToken"], &automount); err != nil {
		t.Fatalf("invalid automountServiceAccountToken JSON: %v", err)
	}
	if automount {
		t.Fatal("expected opentelemetry-operator.automountServiceAccountToken=false")
	}
}

func TestCRDHelmValues_ConfiguresKubeStackCRDOnlyRelease(t *testing.T) {
	out, err := CRDHelmValues(&apiextensionsv1.JSON{Raw: []byte(`{"crds":{"create":false},"manager":{"image":{"tag":"x"}}}`)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(out.Raw, &root); err != nil {
		t.Fatalf("invalid output JSON: %v", err)
	}
	var op struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(root["opentelemetry-operator"], &op); err != nil {
		t.Fatalf("invalid opentelemetry-operator JSON: %v", err)
	}
	if op.Enabled {
		t.Fatal("expected opentelemetry-operator.enabled=false")
	}
	var crds struct {
		InstallOtel       bool `json:"installOtel"`
		InstallPrometheus bool `json:"installPrometheus"`
	}
	if err := json.Unmarshal(root["crds"], &crds); err != nil {
		t.Fatalf("invalid crds JSON: %v", err)
	}
	if !crds.InstallOtel {
		t.Fatal("expected crds.installOtel=true")
	}
	if crds.InstallPrometheus {
		t.Fatal("expected crds.installPrometheus=false")
	}
	for _, key := range []string{"defaultCRConfig", "cleanupJob", "clusterRole", "instrumentation", "kubeStateMetrics", "nodeExporter"} {
		var v struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(root[key], &v); err != nil {
			t.Fatalf("invalid %s JSON: %v", key, err)
		}
		if v.Enabled {
			t.Fatalf("expected %s.enabled=false", key)
		}
	}
	if _, ok := root["manager"]; ok {
		t.Fatal("did not expect opentelemetry-operator subchart values to be passed at the kube-stack root")
	}
}

func TestAddAuthToHelmValues_UsesKubeStackOperatorManagerExtraEnvs(t *testing.T) {
	cluster := &fakeManagedCluster{}
	out, err := AddAuthToHelmValues(&apiextensionsv1.JSON{Raw: []byte(`{"opentelemetry-operator":{"manager":{"extraEnvs":[{"name":"KEEP","value":"1"}]}}}`)}, cluster, "sa-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(out.Raw, &root); err != nil {
		t.Fatalf("invalid output JSON: %v", err)
	}
	var opValues map[string]json.RawMessage
	if err := json.Unmarshal(root["opentelemetry-operator"], &opValues); err != nil {
		t.Fatalf("invalid opentelemetry-operator JSON: %v", err)
	}
	var manager map[string]json.RawMessage
	if err := json.Unmarshal(opValues["manager"], &manager); err != nil {
		t.Fatalf("invalid manager JSON: %v", err)
	}
	if _, ok := manager["extraEnv"]; ok {
		t.Fatal("did not expect legacy extraEnv key")
	}
	var envs []corev1.EnvVar
	if err := json.Unmarshal(manager["extraEnvs"], &envs); err != nil {
		t.Fatalf("invalid manager.extraEnvs JSON: %v", err)
	}
	want := map[string]string{
		"KEEP":                    "1",
		"KUBERNETES_SERVICE_HOST": "localhost",
		"KUBERNETES_SERVICE_PORT": "6443",
	}
	for _, env := range envs {
		if _, ok := want[env.Name]; ok && want[env.Name] == env.Value {
			delete(want, env.Name)
		}
	}
	if len(want) > 0 {
		t.Fatalf("missing env vars: %#v", want)
	}
}
