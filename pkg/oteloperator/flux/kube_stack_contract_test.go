package flux

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/yaml"
)

// renovate: datasource=github-tags depName=open-telemetry/opentelemetry-helm-charts extractVersion=^opentelemetry-kube-stack-(?<version>.+)$
const kubeStackContractVersion = "0.20.8"

const kubeStackChartURL = "oci://ghcr.io/open-telemetry/opentelemetry-helm-charts/opentelemetry-kube-stack"

func TestKubeStackChartContract(t *testing.T) {
	output := helmTemplateKubeStack(t)
	deployment := operatorDeployment(t, output)

	assertAccessTokenVolume(t, deployment)
	assertAccessTokenMount(t, deployment)
}

func helmTemplateKubeStack(t *testing.T) []byte {
	t.Helper()
	cmd := exec.Command("helm", "template", "chart-contract", kubeStackChartURL,
		"--version", kubeStackContractVersion,
		"--set", "crds.installOtel=false",
		"--set", "crds.installPrometheus=false",
		"--set", "opentelemetry-operator.enabled=true",
		"--set", "opentelemetry-operator.crds.create=false",
		"--set", "opentelemetry-operator.automountServiceAccountToken=false",
		"--set", "cleanupJob.enabled=false",
		"--set", "clusterRole.enabled=false",
		"--set", "defaultCRConfig.enabled=false",
		"--set", "instrumentation.enabled=false",
		"--set", "kubeStateMetrics.enabled=false",
		"--set", "nodeExporter.enabled=false",
		"--set", "collectors.daemon.enabled=false",
	)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("render opentelemetry-kube-stack %s: %v", kubeStackContractVersion, err)
	}
	return output
}

func operatorDeployment(t *testing.T, manifest []byte) map[string]any {
	t.Helper()
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	var deployments []map[string]any
	for {
		var object map[string]any
		err := decoder.Decode(&object)
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode rendered manifest: %v", err)
		}
		if object["kind"] == "Deployment" {
			deployments = append(deployments, object)
		}
	}
	if len(deployments) != 1 {
		t.Fatalf("expected exactly one Deployment for cpAccessPostRenderers, got %d", len(deployments))
	}
	if !strings.Contains(fmt.Sprint(deployments[0]["metadata"]), "opentelemetry-operator") {
		t.Fatalf("expected opentelemetry-operator Deployment, got %#v", deployments[0]["metadata"])
	}
	return deployments[0]
}

func assertAccessTokenVolume(t *testing.T, deployment map[string]any) {
	t.Helper()
	volumes := nestedSlice(t, deployment, "spec", "template", "spec", "volumes")
	for _, volume := range volumes {
		entry, ok := volume.(map[string]any)
		if ok && entry["name"] == "access-token" && entry["projected"] != nil {
			return
		}
	}
	t.Fatal(`opentelemetry-operator Deployment must contain the projected "access-token" volume patched by cpAccessPostRenderers`)
}

func assertAccessTokenMount(t *testing.T, deployment map[string]any) {
	t.Helper()
	containers := nestedSlice(t, deployment, "spec", "template", "spec", "containers")
	for _, container := range containers {
		entry, ok := container.(map[string]any)
		if !ok {
			continue
		}
		for _, mount := range nestedSlice(t, entry, "volumeMounts") {
			mount, ok := mount.(map[string]any)
			if ok && mount["name"] == "access-token" && mount["mountPath"] == "/var/run/secrets/kubernetes.io/serviceaccount" {
				return
			}
		}
	}
	t.Fatal(`opentelemetry-operator Deployment must mount "access-token" at /var/run/secrets/kubernetes.io/serviceaccount`)
}

func nestedSlice(t *testing.T, value map[string]any, path ...string) []any {
	t.Helper()
	var current any = value
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("expected object at %s", strings.Join(path, "."))
		}
		current = object[key]
	}
	values, ok := current.([]any)
	if !ok {
		t.Fatalf("expected array at %s", strings.Join(path, "."))
	}
	return values
}
