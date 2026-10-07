package controller

import (
	"context"
	"slices"
	"testing"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlerrors "github.com/openmcp-project/controller-utils/pkg/errors"
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
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/cpresources"
)

const (
	testObjName      = "test"
	testObjNamespace = "default"
	testVersion      = "0.20.7"
	testVersionNew   = "0.20.8"
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

func cpClientWith(objs ...client.ObjectList) *clusters.Cluster {
	cl := fake.NewClientBuilder().WithLists(objs...).Build()
	return clusters.NewTestClusterFromClient("cp", cl)
}

func cpClientNoCRDs() *clusters.Cluster {
	cl := fake.NewClientBuilder().WithInterceptorFuncs(
		HideCrdInterceptor("OpenTelemetryCollectorList", "InstrumentationList"),
	).Build()
	return clusters.NewTestClusterFromClient("cp", cl)
}

func onboardingClient(objs ...client.Object) *clusters.Cluster {
	mapper := testrestmapper.TestOnlyStaticRESTMapper(onboardingScheme())
	cl := fake.NewClientBuilder().WithRESTMapper(mapper).WithScheme(onboardingScheme()).WithObjects(objs...).Build()
	return clusters.NewTestClusterFromClient("onboarding", cl)
}

func otelCollectorOnCP(ns, name string) client.ObjectList {
	u := unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group: cpresources.OtelGroup, Version: cpresources.OtelVersion, Kind: "OpenTelemetryCollector",
	})
	u.SetNamespace(ns)
	u.SetName(name)
	return &unstructured.UnstructuredList{Items: []unstructured.Unstructured{u}}
}

func TestDelete_BlockedWhileOtelCRsExist(t *testing.T) {
	obj := &apiv1alpha1.OtelOperator{}
	obj.Name = testObjName
	obj.Namespace = testObjNamespace

	r := &OtelOperatorReconciler{OnboardingCluster: onboardingClient(obj)}

	cp := cpClientWith(otelCollectorOnCP(testObjNamespace, "my-collector"))
	result, err := r.Delete(context.Background(), obj, &apiv1alpha1.ProviderConfig{}, clusteraccess.ClusterContext{
		MCPCluster: cp,
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
	obj.Name = testObjName
	obj.Namespace = testObjNamespace

	// Provide a stub PlatformCluster so createObjectManager doesn't nil-deref on RESTConfig.
	// The test only cares that the deletion guard (BlockingKinds) doesn't block — errors from
	// createObjectManager are expected and irrelevant here.
	r := &OtelOperatorReconciler{
		OnboardingCluster: onboardingClient(),
		PlatformCluster:   stubCluster(t, "platform"),
	}

	cp := cpClientNoCRDs()
	wl := stubCluster(t, "workload")
	result, _ := r.Delete(context.Background(), obj, &apiv1alpha1.ProviderConfig{}, clusteraccess.ClusterContext{
		MCPCluster:      cp,
		WorkloadCluster: wl,
	})

	// Guard must not block (RequeueAfter == 0). Errors from missing helm/flux config are fine.
	assert.Equal(t, float64(0), result.RequeueAfter.Seconds(), "guard must not block when CRDs are absent")
}

func TestDelete_ProceedsWhenNoOtelCRs(t *testing.T) {
	obj := &apiv1alpha1.OtelOperator{}
	obj.Name = testObjName
	obj.Namespace = testObjNamespace

	r := &OtelOperatorReconciler{
		OnboardingCluster: onboardingClient(),
		PlatformCluster:   stubCluster(t, "platform"),
	}

	cp := cpClientWith() // CRDs present, no CRs
	wl := stubCluster(t, "workload")
	result, _ := r.Delete(context.Background(), obj, &apiv1alpha1.ProviderConfig{}, clusteraccess.ClusterContext{
		MCPCluster:      cp,
		WorkloadCluster: wl,
	})

	assert.Equal(t, float64(0), result.RequeueAfter.Seconds(), "guard must not block when no CRs exist")
}

func TestSelectKubeStackVersion(t *testing.T) {
	versions := []apiv1alpha1.KubeStackVersion{{Version: testVersion}, {Version: testVersionNew}}
	version, err := selectKubeStackVersion(testVersionNew, &apiv1alpha1.ProviderConfig{
		Spec: apiv1alpha1.ProviderConfigSpec{Versions: versions},
	})
	require.NoError(t, err)
	assert.Equal(t, testVersionNew, version.Version)

	_, err = selectKubeStackVersion("v0.158.0", &apiv1alpha1.ProviderConfig{
		Spec: apiv1alpha1.ProviderConfigSpec{Versions: versions},
	})
	require.ErrorIs(t, err, ctrlerrors.ErrInvalidUserInput)
	require.ErrorContains(t, err, "v0.158.0")
}

func TestPrepareInputs_DeleteFallsBackToInstalledVersionWhenRemovedFromProviderConfig(t *testing.T) {
	obj := &apiv1alpha1.OtelOperator{}
	obj.Name = testObjName
	obj.Namespace = testObjNamespace
	obj.Spec.Version = testVersion
	obj.Status.InstalledVersion = &apiv1alpha1.KubeStackVersion{Version: testVersion}

	r := &OtelOperatorReconciler{}
	pc := &apiv1alpha1.ProviderConfig{Spec: apiv1alpha1.ProviderConfigSpec{
		Versions: []apiv1alpha1.KubeStackVersion{{Version: testVersionNew}}, // 0.20.7 no longer offered
	}}

	_, version, err := r.prepareInputs(obj, pc, true)
	require.NoError(t, err, "deletion must not fail just because the version was removed")
	assert.Equal(t, testVersion, version.Version, "must fall back to the recorded installed version")
}

func TestPrepareInputs_DeleteErrorsWithoutRecordedInstalledVersion(t *testing.T) {
	obj := &apiv1alpha1.OtelOperator{}
	obj.Name = testObjName
	obj.Namespace = testObjNamespace
	obj.Spec.Version = testVersion
	// No Status.InstalledVersion recorded, e.g. the instance never got past a failed apply.

	r := &OtelOperatorReconciler{}
	pc := &apiv1alpha1.ProviderConfig{} // version not offered

	_, _, err := r.prepareInputs(obj, pc, true)
	require.ErrorIs(t, err, ctrlerrors.ErrInvalidUserInput, "nothing to fall back to, so this must still error")
}

func TestPrepareInputs_ApplyNeverFallsBackToInstalledVersion(t *testing.T) {
	obj := &apiv1alpha1.OtelOperator{}
	obj.Name = testObjName
	obj.Namespace = testObjNamespace
	obj.Spec.Version = testVersion
	obj.Status.InstalledVersion = &apiv1alpha1.KubeStackVersion{Version: testVersion}

	r := &OtelOperatorReconciler{}
	pc := &apiv1alpha1.ProviderConfig{Spec: apiv1alpha1.ProviderConfigSpec{
		Versions: []apiv1alpha1.KubeStackVersion{{Version: testVersionNew}}, // 0.20.7 no longer offered
	}}

	_, _, err := r.prepareInputs(obj, pc, false)
	require.ErrorIs(t, err, ctrlerrors.ErrInvalidUserInput, "apply must require the version to still be offered")
}

func TestPrepareInputs_DeletePrefersLiveVersionOverStaleStatus(t *testing.T) {
	obj := &apiv1alpha1.OtelOperator{}
	obj.Name = testObjName
	obj.Namespace = testObjNamespace
	obj.Spec.Version = testVersionNew
	// Status still references an older, no-longer-requested version from a previous install.
	obj.Status.InstalledVersion = &apiv1alpha1.KubeStackVersion{Version: testVersion}

	r := &OtelOperatorReconciler{}
	pc := &apiv1alpha1.ProviderConfig{Spec: apiv1alpha1.ProviderConfigSpec{
		Versions: []apiv1alpha1.KubeStackVersion{{Version: testVersionNew}},
	}}

	_, version, err := r.prepareInputs(obj, pc, true)
	require.NoError(t, err)
	assert.Equal(t, testVersionNew, version.Version, "must use the live, requested version when it's still available")
}

func TestCreateOrUpdate_DoesNotPersistInstalledVersionOnApplyFailure(t *testing.T) {
	obj := &apiv1alpha1.OtelOperator{}
	obj.Name = testObjName
	obj.Namespace = testObjNamespace
	obj.Spec.Version = testVersion

	r := &OtelOperatorReconciler{
		OnboardingCluster: onboardingClient(obj),
		PlatformCluster:   stubCluster(t, "platform"),
	}
	pc := &apiv1alpha1.ProviderConfig{Spec: apiv1alpha1.ProviderConfigSpec{
		Versions: []apiv1alpha1.KubeStackVersion{{Version: testVersion}},
	}}

	// Stub clusters don't have the Flux/Helm schemes registered, so mgr.Apply is
	// expected to fail while reconciling the HelmRelease/OCIRepository objects.
	_, err := r.CreateOrUpdate(context.Background(), obj, pc, clusteraccess.ClusterContext{
		MCPCluster:      stubCluster(t, "cp"),
		WorkloadCluster: stubCluster(t, "workload"),
	})

	require.Error(t, err, "expected the apply to fail due to missing schemes")
	assert.Nil(t, obj.Status.InstalledVersion, "a failed apply must not record the version as installed")
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

// stubCluster returns a cluster with a fake client and no RESTConfig, suitable for tests
// that need to pass a non-nil PlatformCluster to avoid nil-deref without actually talking to it.
func stubCluster(t *testing.T, id string) *clusters.Cluster {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	cl := fake.NewClientBuilder().WithScheme(s).Build()
	return clusters.NewTestClusterFromClient(id, cl)
}
