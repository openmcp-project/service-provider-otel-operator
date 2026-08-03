package controller

import (
	"context"
	"slices"
	"testing"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider/clusteraccess"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/meta/testrestmapper"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	apiv1alpha1 "github.com/openmcp-project/service-provider-otel-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/mcpresources"
)

// onboardingScheme includes OtelOperator so the fake onboarding client accepts it.
func onboardingScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = apiv1alpha1.AddToScheme(s)
	return s
}

// HideCrdInterceptor simulates absent CRDs by returning NoKindMatchError for listed Kinds.
func HideCrdInterceptor(hiddenCRDs ...string) interceptor.Funcs {
	return interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			gvk := list.GetObjectKind().GroupVersionKind()
			if slices.Contains(hiddenCRDs, gvk.Kind) {
				return &meta.NoKindMatchError{GroupKind: gvk.GroupKind()}
			}
			return cl.List(ctx, list, opts...)
		},
	}
}

func mcpClientWith(objs ...client.ObjectList) *clusters.Cluster {
	cl := fake.NewClientBuilder().WithLists(objs...).Build()
	return clusters.NewTestClusterFromClient("mcp", cl)
}

func mcpClientNoCRDs() *clusters.Cluster {
	cl := fake.NewClientBuilder().WithInterceptorFuncs(
		HideCrdInterceptor("OpenTelemetryCollectorList", "InstrumentationList"),
	).Build()
	return clusters.NewTestClusterFromClient("mcp", cl)
}

func onboardingClient(objs ...client.Object) *clusters.Cluster {
	mapper := testrestmapper.TestOnlyStaticRESTMapper(onboardingScheme())
	cl := fake.NewClientBuilder().WithRESTMapper(mapper).WithScheme(onboardingScheme()).WithObjects(objs...).Build()
	return clusters.NewTestClusterFromClient("onboarding", cl)
}

func otelCollectorOnMCP(ns, name string) client.ObjectList {
	u := unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group: mcpresources.OtelGroup, Version: mcpresources.OtelVersion, Kind: "OpenTelemetryCollector",
	})
	u.SetNamespace(ns)
	u.SetName(name)
	return &unstructured.UnstructuredList{Items: []unstructured.Unstructured{u}}
}

func TestDelete_BlockedWhileOtelCRsExist(t *testing.T) {
	obj := &apiv1alpha1.OtelOperator{}
	obj.Name = "test"
	obj.Namespace = "default"

	r := &OtelOperatorReconciler{OnboardingCluster: onboardingClient(obj)}

	mcp := mcpClientWith(otelCollectorOnMCP("default", "my-collector"))
	result, err := r.Delete(context.Background(), obj, &apiv1alpha1.ProviderConfig{}, clusteraccess.ClusterContext{
		MCPCluster: mcp,
	})

	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter.Seconds(), float64(0), "must requeue while CRs exist")

	cond := meta.FindStatusCondition(obj.Status.Conditions, serviceprovider.ServiceProviderConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, "waiting for user resources to be deleted: OpenTelemetryCollector", cond.Message)
	assert.Equal(t, "UserResourcesExist", cond.Reason)
}

func TestDelete_ProceedsWhenNoCRDsInstalled(t *testing.T) {
	obj := &apiv1alpha1.OtelOperator{}
	obj.Name = "test"
	obj.Namespace = "default"

	// Provide a stub PlatformCluster so createObjectManager doesn't nil-deref on RESTConfig.
	// The test only cares that the deletion guard (BlockingKinds) doesn't block — errors from
	// createObjectManager are expected and irrelevant here.
	r := &OtelOperatorReconciler{
		OnboardingCluster: onboardingClient(),
		PlatformCluster:   stubCluster(t, "platform"),
	}

	mcp := mcpClientNoCRDs()
	result, _ := r.Delete(context.Background(), obj, &apiv1alpha1.ProviderConfig{}, clusteraccess.ClusterContext{
		MCPCluster: mcp,
	})

	// Guard must not block (RequeueAfter == 0). Errors from missing helm/flux config are fine.
	assert.Equal(t, float64(0), result.RequeueAfter.Seconds(), "guard must not block when CRDs are absent")
}

func TestDelete_ProceedsWhenNoOtelCRs(t *testing.T) {
	obj := &apiv1alpha1.OtelOperator{}
	obj.Name = "test"
	obj.Namespace = "default"

	r := &OtelOperatorReconciler{
		OnboardingCluster: onboardingClient(),
		PlatformCluster:   stubCluster(t, "platform"),
	}

	mcp := mcpClientWith() // CRDs present, no CRs
	result, _ := r.Delete(context.Background(), obj, &apiv1alpha1.ProviderConfig{}, clusteraccess.ClusterContext{
		MCPCluster: mcp,
	})

	assert.Equal(t, float64(0), result.RequeueAfter.Seconds(), "guard must not block when no CRs exist")
}

func TestPendingResourcesMessage(t *testing.T) {
	resources := []apiv1alpha1.ManagedResource{
		{
			Phase:   apiv1alpha1.Ready,
			Message: "Resource is ready",
		},
		{
			Phase:   apiv1alpha1.Pending,
			Message: "install retries exhausted",
		},
	}
	resources[0].Kind = "OCIRepository"
	resources[0].Name = "test-mcp"
	ns := "tenant"
	resources[0].Namespace = &ns
	resources[1].Kind = "HelmRelease"
	resources[1].Name = "test-mcp"
	resources[1].Namespace = &ns

	got := pendingResourcesMessage(resources)
	want := "HelmRelease tenant/test-mcp is Pending: install retries exhausted"
	assert.Equal(t, want, got)
}

func TestJoinStrings(t *testing.T) {
	assert.Equal(t, "", joinStrings(nil))
	assert.Equal(t, "a", joinStrings([]string{"a"}))
	assert.Equal(t, "a, b, c", joinStrings([]string{"a", "b", "c"}))
}

// stubCluster returns a cluster with a fake client and no RESTConfig, suitable for tests
// that need to pass a non-nil PlatformCluster to avoid nil-deref without actually talking to it.
func stubCluster(t *testing.T, id string) *clusters.Cluster {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	cl := fake.NewClientBuilder().WithScheme(s).Build()
	return clusters.NewTestClusterFromClient(id, cl)
}
