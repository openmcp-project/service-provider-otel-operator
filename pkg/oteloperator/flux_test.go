package oteloperator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider/clusteraccess"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1alpha1 "github.com/openmcp-project/service-provider-otel-operator/api/v1alpha1"
)

const (
	testTenantNS = "tenant-ns"
	testMCPName  = "test-mcp"
)

func TestManageFluxResources_CreatesOneOCIRepositoryAndTwoHelmReleases(t *testing.T) {
	cluster := &fakeManagedCluster{ns: testTenantNS}
	obj := &apiv1alpha1.OtelOperator{
		ObjectMeta: metav1.ObjectMeta{Name: testMCPName, Namespace: "default"},
		Spec:       apiv1alpha1.OtelOperatorSpec{Version: "0.82.0"},
	}
	pc := &apiv1alpha1.ProviderConfig{
		Spec: apiv1alpha1.ProviderConfigSpec{
			ChartURL:     new(string),
			PollInterval: &metav1.Duration{Duration: time.Minute},
		},
	}

	ManageFluxResources(ManageFluxResourcesParams{
		Cluster:            cluster,
		CPNamespace:        "opentelemetry-operator-system",
		WorkloadNamespace:  "opentelemetry-operator-system",
		Obj:                obj,
		ProviderConfig:     pc,
		WorkloadHelmValues: mustWorkloadHelmValues(t),
		CRDHelmValues:      mustCRDHelmValues(t),
		ClusterContext: clusteraccess.ClusterContext{
			MCPAccessSecretKey:      client.ObjectKey{Name: "cp-kubeconfig", Namespace: testTenantNS},
			WorkloadAccessSecretKey: client.ObjectKey{Name: "wl-kubeconfig", Namespace: testTenantNS},
		},
	})

	if len(cluster.objects) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(cluster.objects))
	}

	kubeStackOCIObj := cluster.objects[0].GetObject()
	if _, ok := kubeStackOCIObj.(*sourcev1.OCIRepository); !ok {
		t.Errorf("first object should be kube-stack OCIRepository, got %T", kubeStackOCIObj)
	}
	if kubeStackOCIObj.GetName() != testMCPName {
		t.Errorf("kube-stack OCIRepository name: expected test-mcp, got %s", kubeStackOCIObj.GetName())
	}
	if kubeStackOCIObj.GetNamespace() != testTenantNS {
		t.Errorf("kube-stack OCIRepository namespace: expected tenant-ns, got %s", kubeStackOCIObj.GetNamespace())
	}

	crdHRObj := cluster.objects[1].GetObject()
	if _, ok := crdHRObj.(*helmv2.HelmRelease); !ok {
		t.Errorf("third object should be CRD HelmRelease, got %T", crdHRObj)
	}
	if crdHRObj.GetName() != testMCPName+crdHelmReleaseSuffix {
		t.Errorf("CRD HelmRelease name: expected %s, got %s", testMCPName+crdHelmReleaseSuffix, crdHRObj.GetName())
	}

	workloadHRObj := cluster.objects[2].GetObject()
	if _, ok := workloadHRObj.(*helmv2.HelmRelease); !ok {
		t.Errorf("fourth object should be workload HelmRelease, got %T", workloadHRObj)
	}
	if workloadHRObj.GetName() != testMCPName+workloadHelmReleaseSuffix {
		t.Errorf("workload HelmRelease name: expected %s, got %s", testMCPName+workloadHelmReleaseSuffix, workloadHRObj.GetName())
	}
}

func TestManageFluxResources_ReconcilePopulatesSpec(t *testing.T) {
	cluster := &fakeManagedCluster{ns: testTenantNS}
	obj := &apiv1alpha1.OtelOperator{
		ObjectMeta: metav1.ObjectMeta{Name: testMCPName, Namespace: "default"},
		Spec:       apiv1alpha1.OtelOperatorSpec{Version: "1.2.3"},
	}
	chartURL := "oci://ghcr.io/example/kube-stack-chart"
	pc := &apiv1alpha1.ProviderConfig{
		Spec: apiv1alpha1.ProviderConfigSpec{
			ChartURL:     &chartURL,
			PollInterval: &metav1.Duration{Duration: 2 * time.Minute},
		},
	}

	ManageFluxResources(ManageFluxResourcesParams{
		Cluster:             cluster,
		CPNamespace:         "otel-system",
		WorkloadNamespace:   "otel-system",
		ChartPullSecretName: "my-secret",
		Obj:                 obj,
		ProviderConfig:      pc,
		WorkloadHelmValues:  mustWorkloadHelmValues(t),
		CRDHelmValues:       mustCRDHelmValues(t),
		ClusterContext: clusteraccess.ClusterContext{
			MCPAccessSecretKey:      client.ObjectKey{Name: "cp-kubeconfig", Namespace: testTenantNS},
			WorkloadAccessSecretKey: client.ObjectKey{Name: "wl-kubeconfig", Namespace: testTenantNS},
		},
	})

	ociMO := cluster.objects[0]
	if err := ociMO.Reconcile(context.Background()); err != nil {
		t.Fatalf("kube-stack OCIRepository reconcile error: %v", err)
	}
	ociRepo := ociMO.GetObject().(*sourcev1.OCIRepository)
	if ociRepo.Spec.URL != chartURL {
		t.Errorf("kube-stack OCIRepository URL: expected %s, got %s", chartURL, ociRepo.Spec.URL)
	}
	if ociRepo.Spec.Reference == nil || ociRepo.Spec.Reference.Tag != "1.2.3" {
		t.Errorf("kube-stack OCIRepository tag: expected 1.2.3, got %v", ociRepo.Spec.Reference)
	}
	if ociRepo.Spec.SecretRef == nil || ociRepo.Spec.SecretRef.Name != "my-secret" {
		t.Errorf("kube-stack OCIRepository secret ref: expected my-secret, got %v", ociRepo.Spec.SecretRef)
	}

	crdHRMO := cluster.objects[1]
	if err := crdHRMO.Reconcile(context.Background()); err != nil {
		t.Fatalf("CRD HelmRelease reconcile error: %v", err)
	}
	crdHR := crdHRMO.GetObject().(*helmv2.HelmRelease)
	if crdHR.Spec.ReleaseName != testMCPName+crdHelmReleaseSuffix {
		t.Errorf("CRD HelmRelease ReleaseName: expected %s, got %s", testMCPName+crdHelmReleaseSuffix, crdHR.Spec.ReleaseName)
	}
	if crdHR.Spec.KubeConfig == nil || crdHR.Spec.KubeConfig.SecretRef.Name != "cp-kubeconfig" {
		t.Errorf("CRD HelmRelease KubeConfig: expected cp-kubeconfig, got %v", crdHR.Spec.KubeConfig)
	}
	if crdHR.Spec.TargetNamespace != "otel-system" || crdHR.Spec.StorageNamespace != "otel-system" {
		t.Errorf("CRD HelmRelease namespace mismatch: target=%s storage=%s", crdHR.Spec.TargetNamespace, crdHR.Spec.StorageNamespace)
	}
	if crdHR.Spec.ChartRef == nil || crdHR.Spec.ChartRef.Name != testMCPName {
		t.Errorf("CRD HelmRelease ChartRef: expected %s, got %v", testMCPName, crdHR.Spec.ChartRef)
	}
	assertKubeStackCRDValues(t, crdHR.Spec.Values)
	if crdHR.Spec.Install == nil || crdHR.Spec.Install.CRDs != helmv2.Create {
		t.Fatalf("CRD HelmRelease install CRD policy: expected Create, got %#v", crdHR.Spec.Install)
	}
	if crdHR.Spec.Upgrade == nil || crdHR.Spec.Upgrade.CRDs != helmv2.CreateReplace {
		t.Fatalf("CRD HelmRelease upgrade CRD policy: expected CreateReplace, got %#v", crdHR.Spec.Upgrade)
	}
	if len(crdHR.Spec.PostRenderers) != 0 {
		t.Fatalf("CRD HelmRelease should not use post-renderers, got %d", len(crdHR.Spec.PostRenderers))
	}
	if crdHR.Spec.Uninstall == nil || crdHR.Spec.Uninstall.DeletionPropagation == nil || *crdHR.Spec.Uninstall.DeletionPropagation != "orphan" {
		t.Fatalf("CRD HelmRelease should orphan resources on uninstall, got %#v", crdHR.Spec.Uninstall)
	}

	workloadHRMO := cluster.objects[2]
	if err := workloadHRMO.Reconcile(context.Background()); err != nil {
		t.Fatalf("workload HelmRelease reconcile error: %v", err)
	}
	workloadHR := workloadHRMO.GetObject().(*helmv2.HelmRelease)
	if workloadHR.Spec.ReleaseName != testMCPName+workloadHelmReleaseSuffix {
		t.Errorf("workload HelmRelease ReleaseName: expected %s, got %s", testMCPName+workloadHelmReleaseSuffix, workloadHR.Spec.ReleaseName)
	}
	if workloadHR.Spec.TargetNamespace != "otel-system" || workloadHR.Spec.StorageNamespace != "otel-system" {
		t.Errorf("workload HelmRelease namespace mismatch: target=%s storage=%s", workloadHR.Spec.TargetNamespace, workloadHR.Spec.StorageNamespace)
	}
	if workloadHR.Spec.KubeConfig == nil || workloadHR.Spec.KubeConfig.SecretRef.Name != "wl-kubeconfig" {
		t.Errorf("workload HelmRelease KubeConfig: expected wl-kubeconfig, got %v", workloadHR.Spec.KubeConfig)
	}
	if workloadHR.Spec.ChartRef == nil || workloadHR.Spec.ChartRef.Name != testMCPName {
		t.Errorf("workload HelmRelease ChartRef: expected test-mcp, got %v", workloadHR.Spec.ChartRef)
	}
	if len(workloadHR.Spec.PostRenderers) != 0 {
		t.Fatalf("workload HelmRelease should not use post-renderers, got %d", len(workloadHR.Spec.PostRenderers))
	}
	assertKubeStackWorkloadValues(t, workloadHR.Spec.Values)
	if len(workloadHR.Spec.DependsOn) != 1 || workloadHR.Spec.DependsOn[0].Name != testMCPName+crdHelmReleaseSuffix {
		t.Fatalf("workload HelmRelease should depend on CRD HelmRelease, got %#v", workloadHR.Spec.DependsOn)
	}

	deps := workloadHRMO.GetDependencies()
	if len(deps) != 2 {
		t.Fatalf("workload HelmRelease should depend on 2 managed objects, got %d", len(deps))
	}
	if deps[0].GetObject().GetName() != testMCPName || deps[1].GetObject().GetName() != testMCPName+crdHelmReleaseSuffix {
		t.Errorf("workload HelmRelease dependencies should be kube-stack OCIRepository and CRD HelmRelease")
	}
}

func TestFluxStatusUsesFluxConditionMessage(t *testing.T) {
	repo := &sourcev1.OCIRepository{
		Status: sourcev1.OCIRepositoryStatus{
			Conditions: []metav1.Condition{
				{
					Type:    meta.ReadyCondition,
					Status:  metav1.ConditionFalse,
					Reason:  "AuthenticationFailed",
					Message: "failed to login to registry",
				},
			},
		},
	}

	status := FluxStatus(repo, apiv1alpha1.LocationPlatform)
	if status.Phase != apiv1alpha1.Pending {
		t.Fatalf("FluxStatus phase = %q, want %q", status.Phase, apiv1alpha1.Pending)
	}
	if status.Message != "failed to login to registry" {
		t.Fatalf("FluxStatus message = %q, want condition message", status.Message)
	}
}

func TestManageFluxResources_NoChartPullSecret(t *testing.T) {
	cluster := &fakeManagedCluster{ns: testTenantNS}
	obj := &apiv1alpha1.OtelOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec:       apiv1alpha1.OtelOperatorSpec{Version: "0.1.0"},
	}
	pc := &apiv1alpha1.ProviderConfig{Spec: apiv1alpha1.ProviderConfigSpec{ChartURL: new(string)}}

	ManageFluxResources(ManageFluxResourcesParams{
		Cluster:            cluster,
		CPNamespace:        "ns",
		WorkloadNamespace:  "ns",
		Obj:                obj,
		ProviderConfig:     pc,
		WorkloadHelmValues: mustWorkloadHelmValues(t),
		CRDHelmValues:      mustCRDHelmValues(t),
		ClusterContext: clusteraccess.ClusterContext{
			MCPAccessSecretKey:      client.ObjectKey{Name: "cp-sec"},
			WorkloadAccessSecretKey: client.ObjectKey{Name: "sec"},
		},
	})

	ociMO := cluster.objects[0]
	if err := ociMO.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	ociRepo := ociMO.GetObject().(*sourcev1.OCIRepository)
	if ociRepo.Spec.SecretRef != nil {
		t.Errorf("expected no SecretRef when ChartPullSecretName is empty, got %v", ociRepo.Spec.SecretRef)
	}
}

func mustCRDHelmValues(t *testing.T) *apiextensionsv1.JSON {
	t.Helper()
	values, err := CRDHelmValues(nil)
	if err != nil {
		t.Fatalf("CRDHelmValues failed: %v", err)
	}
	return values
}

func mustWorkloadHelmValues(t *testing.T) *apiextensionsv1.JSON {
	t.Helper()
	values, err := WorkloadHelmValues(nil)
	if err != nil {
		t.Fatalf("WorkloadHelmValues failed: %v", err)
	}
	return values
}

func assertKubeStackWorkloadValues(t *testing.T, values *apiextensionsv1.JSON) {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(values.Raw, &root); err != nil {
		t.Fatalf("invalid values JSON: %v", err)
	}
	var crds struct {
		InstallOtel       bool `json:"installOtel"`
		InstallPrometheus bool `json:"installPrometheus"`
	}
	if err := json.Unmarshal(root["crds"], &crds); err != nil {
		t.Fatalf("invalid crds JSON: %v", err)
	}
	if crds.InstallOtel || crds.InstallPrometheus {
		t.Fatalf("unexpected kube-stack workload crds values: %#v", crds)
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
	if !op.Enabled || op.CRDs.Create {
		t.Fatalf("unexpected opentelemetry-operator workload values: %#v", op)
	}
}

func assertKubeStackCRDValues(t *testing.T, values *apiextensionsv1.JSON) {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(values.Raw, &root); err != nil {
		t.Fatalf("invalid values JSON: %v", err)
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
	if !crds.InstallOtel || crds.InstallPrometheus {
		t.Fatalf("unexpected kube-stack crds values: %#v", crds)
	}
}

type fakeManagedCluster struct {
	ns      string
	objects []ManagedObject
}

var _ ManagedCluster = &fakeManagedCluster{}

func (f *fakeManagedCluster) AddObject(o ManagedObject)        { f.objects = append(f.objects, o) }
func (f *fakeManagedCluster) GetObjects() []ManagedObject      { return f.objects }
func (f *fakeManagedCluster) GetDefaultNamespace() string      { return f.ns }
func (f *fakeManagedCluster) GetHostAndPort() (string, string) { return "localhost", "6443" }
func (f *fakeManagedCluster) GetConfig() *rest.Config          { return nil }
func (f *fakeManagedCluster) GetClient() client.Client         { return nil }
func (f *fakeManagedCluster) GetCluster() *clusters.Cluster    { return nil }
func (f *fakeManagedCluster) GetClusterType() ClusterType      { return ClusterTypePlatform }
