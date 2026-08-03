package oteloperator

import (
	"context"
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

func TestManageFluxResources_CreatesOCIRepositoryAndHelmRelease(t *testing.T) {
	cluster := &fakeManagedCluster{ns: "tenant-ns"}
	obj := &apiv1alpha1.OtelOperatorService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mcp", Namespace: "default"},
		Spec:       apiv1alpha1.OtelOperatorServiceSpec{Version: "0.82.0"},
	}
	pc := &apiv1alpha1.ProviderConfig{
		Spec: apiv1alpha1.ProviderConfigSpec{
			ChartURL:     new(string),
			PollInterval: &metav1.Duration{Duration: time.Minute},
		},
	}

	ManageFluxResources(ManageFluxResourcesParams{
		Cluster:        cluster,
		MCPNamespace:   "opentelemetry-operator-system",
		Obj:            obj,
		ProviderConfig: pc,
		ClusterContext: clusteraccess.ClusterContext{
			MCPAccessSecretKey: client.ObjectKey{Name: "mcp-kubeconfig", Namespace: "tenant-ns"},
		},
	})

	if len(cluster.objects) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(cluster.objects))
	}

	ociObj := cluster.objects[0].GetObject()
	if _, ok := ociObj.(*sourcev1.OCIRepository); !ok {
		t.Errorf("first object should be OCIRepository, got %T", ociObj)
	}
	if ociObj.GetName() != "test-mcp" {
		t.Errorf("OCIRepository name: expected test-mcp, got %s", ociObj.GetName())
	}
	if ociObj.GetNamespace() != "tenant-ns" {
		t.Errorf("OCIRepository namespace: expected tenant-ns, got %s", ociObj.GetNamespace())
	}

	hrObj := cluster.objects[1].GetObject()
	if _, ok := hrObj.(*helmv2.HelmRelease); !ok {
		t.Errorf("second object should be HelmRelease, got %T", hrObj)
	}
	if hrObj.GetName() != "test-mcp" {
		t.Errorf("HelmRelease name: expected test-mcp, got %s", hrObj.GetName())
	}
}

func TestManageFluxResources_ReconcilePopulatesSpec(t *testing.T) {
	cluster := &fakeManagedCluster{ns: "tenant-ns"}
	obj := &apiv1alpha1.OtelOperatorService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mcp", Namespace: "default"},
		Spec:       apiv1alpha1.OtelOperatorServiceSpec{Version: "1.2.3"},
	}
	chartURL := "oci://ghcr.io/example/chart"
	pc := &apiv1alpha1.ProviderConfig{
		Spec: apiv1alpha1.ProviderConfigSpec{
			ChartURL:     &chartURL,
			PollInterval: &metav1.Duration{Duration: 2 * time.Minute},
			HelmValues:   &apiextensionsv1.JSON{Raw: []byte(`{"key":"value"}`)},
		},
	}

	ManageFluxResources(ManageFluxResourcesParams{
		Cluster:             cluster,
		MCPNamespace:        "otel-system",
		ChartPullSecretName: "my-secret",
		Obj:                 obj,
		ProviderConfig:      pc,
		ClusterContext: clusteraccess.ClusterContext{
			MCPAccessSecretKey: client.ObjectKey{Name: "mcp-kubeconfig", Namespace: "tenant-ns"},
		},
	})

	ociMO := cluster.objects[0]
	if err := ociMO.Reconcile(context.Background()); err != nil {
		t.Fatalf("OCIRepository reconcile error: %v", err)
	}
	ociRepo := ociMO.GetObject().(*sourcev1.OCIRepository)
	if ociRepo.Spec.URL != chartURL {
		t.Errorf("OCIRepository URL: expected %s, got %s", chartURL, ociRepo.Spec.URL)
	}
	if ociRepo.Spec.Reference == nil || ociRepo.Spec.Reference.Tag != "1.2.3" {
		t.Errorf("OCIRepository tag: expected 1.2.3, got %v", ociRepo.Spec.Reference)
	}
	if ociRepo.Spec.SecretRef == nil || ociRepo.Spec.SecretRef.Name != "my-secret" {
		t.Errorf("OCIRepository secret ref: expected my-secret, got %v", ociRepo.Spec.SecretRef)
	}

	hrMO := cluster.objects[1]
	if err := hrMO.Reconcile(context.Background()); err != nil {
		t.Fatalf("HelmRelease reconcile error: %v", err)
	}
	hr := hrMO.GetObject().(*helmv2.HelmRelease)
	if hr.Spec.TargetNamespace != "otel-system" {
		t.Errorf("HelmRelease TargetNamespace: expected otel-system, got %s", hr.Spec.TargetNamespace)
	}
	if hr.Spec.StorageNamespace != "otel-system" {
		t.Errorf("HelmRelease StorageNamespace: expected otel-system, got %s", hr.Spec.StorageNamespace)
	}
	if hr.Spec.KubeConfig == nil || hr.Spec.KubeConfig.SecretRef.Name != "mcp-kubeconfig" {
		t.Errorf("HelmRelease KubeConfig: expected mcp-kubeconfig, got %v", hr.Spec.KubeConfig)
	}
	if hr.Spec.ChartRef == nil || hr.Spec.ChartRef.Name != "test-mcp" {
		t.Errorf("HelmRelease ChartRef: expected test-mcp, got %v", hr.Spec.ChartRef)
	}
	if hr.Spec.Values == nil || string(hr.Spec.Values.Raw) != `{"key":"value"}` {
		t.Errorf("HelmRelease Values: expected {\"key\":\"value\"}, got %v", hr.Spec.Values)
	}

	deps := hrMO.GetDependencies()
	if len(deps) != 1 {
		t.Fatalf("HelmRelease should depend on 1 object, got %d", len(deps))
	}
	if deps[0].GetObject().GetName() != "test-mcp" {
		t.Errorf("HelmRelease dependency should be OCIRepository test-mcp")
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
	cluster := &fakeManagedCluster{ns: "tenant-ns"}
	obj := &apiv1alpha1.OtelOperatorService{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec:       apiv1alpha1.OtelOperatorServiceSpec{Version: "0.1.0"},
	}
	pc := &apiv1alpha1.ProviderConfig{
		Spec: apiv1alpha1.ProviderConfigSpec{
			ChartURL: new(string),
		},
	}

	ManageFluxResources(ManageFluxResourcesParams{
		Cluster:        cluster,
		MCPNamespace:   "ns",
		Obj:            obj,
		ProviderConfig: pc,
		ClusterContext: clusteraccess.ClusterContext{
			MCPAccessSecretKey: client.ObjectKey{Name: "sec"},
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
