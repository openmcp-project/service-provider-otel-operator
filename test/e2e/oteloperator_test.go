package e2e

import (
	"context"
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

func TestServiceProvider(t *testing.T) {
	var onboardingList unstructured.UnstructuredList
	basicProviderTest := features.New("otel-operator provider test").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			if _, err := resources.CreateObjectsFromDir(ctx, c, "platform"); err != nil {
				t.Errorf("failed to create platform cluster objects: %v", err)
			}
			return ctx
		}).
		Assess("create OtelOperator and verify Ready",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				onboardingConfig, err := clusterutils.OnboardingConfig()
				if err != nil {
					t.Error(err)
					return ctx
				}
				var objList *unstructured.UnstructuredList
				if err := wait.For(func(ctx context.Context) (bool, error) {
					var createErr error
					objList, createErr = resources.CreateObjectsFromDir(ctx, onboardingConfig, "onboarding")
					if createErr != nil {
						if strings.Contains(createErr.Error(), "no matches for") {
							return false, nil
						}
						return false, createErr
					}
					return true, nil
				}, wait.WithTimeout(5*time.Minute), wait.WithInterval(5*time.Second)); err != nil {
					t.Errorf("failed to create onboarding cluster objects: %v", err)
					return ctx
				}
				for i := range objList.Items {
					obj := &objList.Items[i]
					conditionType := "Ready"
					if obj.GetKind() == "ControlPlane" {
						conditionType = "AllAccessReady"
					}
					if err := wait.For(openmcpconditions.Match(obj, onboardingConfig, conditionType, corev1.ConditionTrue),
						wait.WithTimeout(10*time.Minute)); err != nil {
						t.Error(err)
					}
				}
				objList.DeepCopyInto(&onboardingList)
				return ctx
			},
		).
		Assess("platform cluster: OCIRepository and HelmReleases are ready",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				platformConfig, err := clusterutils.ConfigByPrefix("platform", corev1.NamespaceDefault)
				if err != nil {
					t.Errorf("failed to get platform cluster config: %v", err)
					return ctx
				}
				tenantNamespace, err := libutils.StableMCPNamespace(testCPName, corev1.NamespaceDefault)
				if err != nil {
					t.Errorf("failed to get tenant namespace: %v", err)
					return ctx
				}

				ociRepo := &sourcev1.OCIRepository{}
				ociRepo.SetName(testCPName)
				ociRepo.SetNamespace(tenantNamespace)
				if err := wait.For(openmcpconditions.Match(ociRepo, platformConfig, "Ready", corev1.ConditionTrue),
					wait.WithTimeout(5*time.Minute)); err != nil {
					t.Errorf("OCIRepository not ready: %v", err)
				}

				for _, name := range []string{testCPName + "-crds", testCPName + "-workload"} {
					helmRelease := &helmv2.HelmRelease{}
					helmRelease.SetName(name)
					helmRelease.SetNamespace(tenantNamespace)
					if err := wait.For(openmcpconditions.Match(helmRelease, platformConfig, "Ready", corev1.ConditionTrue),
						wait.WithTimeout(5*time.Minute)); err != nil {
						t.Errorf("HelmRelease %s not ready: %v", name, err)
					}
				}
				return ctx
			},
		).
		Assess("workload cluster: operator deployment exists",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				workloadConfig, err := clusterutils.ConfigByPrefix("workload", corev1.NamespaceDefault)
				if err != nil {
					t.Error(err)
					return ctx
				}
				onboardingConfig, err := clusterutils.OnboardingConfig()
				if err != nil {
					t.Error(err)
					return ctx
				}
				if err := apiv1alpha1.AddToScheme(onboardingConfig.Client().Resources().GetScheme()); err != nil {
					t.Errorf("failed to register OtelOperator scheme: %v", err)
					return ctx
				}
				obj := &apiv1alpha1.OtelOperator{}
				if err := onboardingConfig.Client().Resources().Get(ctx, testCPName, corev1.NamespaceDefault, obj); err != nil {
					t.Errorf("failed to get OtelOperator %s/%s: %v", corev1.NamespaceDefault, testCPName, err)
					return ctx
				}
				workloadNamespace := instance.Namespace(obj)
				dep := &appsv1.DeploymentList{}
				if err := wait.For(conditions.New(workloadConfig.Client().Resources(workloadNamespace)).
					ResourceListN(dep, 1),
					wait.WithTimeout(5*time.Minute)); err != nil {
					t.Errorf("operator deployment not found in namespace %s: %v", workloadNamespace, err)
				}
				return ctx
			},
		).
		Assess("delete OtelOperator and verify clean teardown",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				onboardingConfig, err := clusterutils.OnboardingConfig()
				if err != nil {
					t.Error(err)
					return ctx
				}
				for i := range onboardingList.Items {
					obj := &onboardingList.Items[i]
					if err := resources.DeleteObject(ctx, onboardingConfig, obj, wait.WithTimeout(5*time.Minute)); err != nil {
						t.Errorf("failed to delete %s/%s: %v", obj.GetNamespace(), obj.GetName(), err)
					}
				}
				// Clear list so Teardown doesn't try again.
				onboardingList = unstructured.UnstructuredList{}
				return ctx
			},
		).
		Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			onboardingConfig, err := clusterutils.OnboardingConfig()
			if err != nil {
				t.Error(err)
				return ctx
			}
			for _, obj := range onboardingList.Items {
				_ = onboardingConfig.Client().Resources().Delete(ctx, &obj)
			}
			return ctx
		})
	testenv.Test(t, basicProviderTest.Feature())
}
