package e2e

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	libutils "github.com/openmcp-project/openmcp-operator/lib/utils"
	"github.com/openmcp-project/openmcp-testing/pkg/clusterutils"
	openmcpconditions "github.com/openmcp-project/openmcp-testing/pkg/conditions"
	"github.com/openmcp-project/openmcp-testing/pkg/resources"
	apiv1alpha1 "github.com/openmcp-project/service-provider-otel-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/instance"
)

// testCPName is the name of the OtelOperator and ControlPlane objects used in tests.
const testCPName = "test-cp"

func captureDiagnosticsOnFailure(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()
		t.Log("Capturing e2e failure diagnostics")
		cmd := exec.CommandContext(ctx, "bash", "../../hack/e2e-diagnostics.sh", "snapshot")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Logf("Failed to capture e2e diagnostics: %v", err)
		}
	})
}

func TestServiceProvider(t *testing.T) {
	var onboardingList unstructured.UnstructuredList
	var cleanupFailed bool
	basicProviderTest := features.New("otel-operator provider test").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			captureDiagnosticsOnFailure(t)
			if _, err := resources.CreateObjectsFromDir(ctx, c, "platform"); err != nil {
				t.Fatalf("failed to create platform cluster objects: %v", err)
			}
			return ctx
		}).
		Assess("create OtelOperator and verify Ready",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				captureDiagnosticsOnFailure(t)
				onboardingConfig, err := clusterutils.OnboardingConfig()
				if err != nil {
					t.Fatal(err)
				}
				var objList *unstructured.UnstructuredList
				if err := wait.For(func(ctx context.Context) (bool, error) {
					var createErr error
					objList, createErr = resources.CreateObjectsFromDir(ctx, onboardingConfig, "onboarding")
					if objList != nil {
						objList.DeepCopyInto(&onboardingList)
					}
					if createErr != nil {
						if strings.Contains(createErr.Error(), "no matches for") {
							return false, nil
						}
						return false, createErr
					}
					return true, nil
				}, wait.WithTimeout(5*time.Minute), wait.WithInterval(5*time.Second)); err != nil {
					t.Fatalf("failed to create onboarding cluster objects: %v", err)
				}
				for i := range objList.Items {
					obj := &objList.Items[i]
					conditionType := "Ready"
					if obj.GetKind() == "ControlPlane" {
						conditionType = "AllAccessReady"
					}
					if err := wait.For(openmcpconditions.Match(obj, onboardingConfig, conditionType, corev1.ConditionTrue),
						wait.WithTimeout(10*time.Minute)); err != nil {
						t.Fatal(err)
					}
				}
				return ctx
			},
		).
		Assess("platform cluster: OCIRepository and HelmReleases are ready",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				captureDiagnosticsOnFailure(t)
				platformConfig, err := clusterutils.ConfigByPrefix("platform", corev1.NamespaceDefault)
				if err != nil {
					t.Fatalf("failed to get platform cluster config: %v", err)
				}
				tenantNamespace, err := libutils.StableMCPNamespace(testCPName, corev1.NamespaceDefault)
				if err != nil {
					t.Fatalf("failed to get tenant namespace: %v", err)
				}

				ociRepo := &sourcev1.OCIRepository{}
				ociRepo.SetName(testCPName)
				ociRepo.SetNamespace(tenantNamespace)
				if err := wait.For(openmcpconditions.Match(ociRepo, platformConfig, "Ready", corev1.ConditionTrue),
					wait.WithTimeout(5*time.Minute)); err != nil {
					t.Fatalf("OCIRepository not ready: %v", err)
				}

				for _, name := range []string{testCPName + "-crds", testCPName + "-workload"} {
					helmRelease := &helmv2.HelmRelease{}
					helmRelease.SetName(name)
					helmRelease.SetNamespace(tenantNamespace)
					if err := wait.For(openmcpconditions.Match(helmRelease, platformConfig, "Ready", corev1.ConditionTrue),
						wait.WithTimeout(5*time.Minute)); err != nil {
						t.Fatalf("HelmRelease %s not ready: %v", name, err)
					}
				}
				return ctx
			},
		).
		Assess("workload cluster: operator deployment exists",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				captureDiagnosticsOnFailure(t)
				workloadConfig, err := clusterutils.ConfigByPrefix("workload", corev1.NamespaceDefault)
				if err != nil {
					t.Fatal(err)
				}
				onboardingConfig, err := clusterutils.OnboardingConfig()
				if err != nil {
					t.Fatal(err)
				}
				if err := apiv1alpha1.AddToScheme(onboardingConfig.Client().Resources().GetScheme()); err != nil {
					t.Fatalf("failed to register OtelOperator scheme: %v", err)
				}
				obj := &apiv1alpha1.OtelOperator{}
				if err := onboardingConfig.Client().Resources().Get(ctx, testCPName, corev1.NamespaceDefault, obj); err != nil {
					t.Fatalf("failed to get OtelOperator %s/%s: %v", corev1.NamespaceDefault, testCPName, err)
				}
				workloadNamespace := instance.Namespace(obj)
				dep := &appsv1.DeploymentList{}
				if err := wait.For(conditions.New(workloadConfig.Client().Resources(workloadNamespace)).
					ResourceListN(dep, 1),
					wait.WithTimeout(5*time.Minute)); err != nil {
					t.Fatalf("operator deployment not found in namespace %s: %v", workloadNamespace, err)
				}
				return ctx
			},
		).
		Assess("delete OtelOperator and verify clean teardown",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				captureDiagnosticsOnFailure(t)
				onboardingConfig, err := clusterutils.OnboardingConfig()
				if err != nil {
					t.Fatal(err)
				}
				for len(onboardingList.Items) > 0 {
					i := len(onboardingList.Items) - 1
					obj := &onboardingList.Items[i]
					if err := resources.DeleteObject(ctx, onboardingConfig, obj, wait.WithTimeout(5*time.Minute)); err != nil {
						cleanupFailed = true
						t.Fatalf("failed to delete %s/%s: %v", obj.GetNamespace(), obj.GetName(), err)
					}
					onboardingList.Items = onboardingList.Items[:i]
				}
				return ctx
			},
		).
		Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			if cleanupFailed {
				return ctx
			}
			onboardingConfig, err := clusterutils.OnboardingConfig()
			if err != nil {
				t.Fatal(err)
			}
			for len(onboardingList.Items) > 0 {
				i := len(onboardingList.Items) - 1
				obj := &onboardingList.Items[i]
				if err := resources.DeleteObject(ctx, onboardingConfig, obj, wait.WithTimeout(5*time.Minute)); err != nil {
					t.Fatalf("failed to delete %s/%s: %v", obj.GetNamespace(), obj.GetName(), err)
				}
				onboardingList.Items = onboardingList.Items[:i]
			}
			return ctx
		})
	testenv.Test(t, basicProviderTest.Feature())
}
