package oteloperator

import (
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
