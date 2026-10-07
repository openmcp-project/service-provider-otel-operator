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

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlerrors "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider/clusteraccess"
	libutils "github.com/openmcp-project/openmcp-operator/lib/utils"

	apiv1alpha1 "github.com/openmcp-project/service-provider-otel-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/authn"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/authz"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/configmap"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/cpresources"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/flux"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/helm"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/instance"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/objectutils"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/resources"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/secret"
)

const namespaceOtelOperator = "opentelemetry-operator-system"

// OtelOperatorReconciler reconciles an OtelOperator object
type OtelOperatorReconciler struct {
	OnboardingCluster *clusters.Cluster
	PlatformCluster   *clusters.Cluster
	PodNamespace      string
}

// CreateOrUpdate is called on every add or update event
func (r *OtelOperatorReconciler) CreateOrUpdate(ctx context.Context, obj *apiv1alpha1.OtelOperator, pc *apiv1alpha1.ProviderConfig, clusterCtx clusteraccess.ClusterContext) (ctrl.Result, error) {
	serviceprovider.StatusProgressing(obj, "Reconciling", "Reconcile in progress")
	err := r.ensureInstanceID(ctx, obj)
	if err != nil {
		serviceprovider.StatusProgressing(obj, "ReconcileError", err.Error())
		return ctrl.Result{}, err
	}
	mgr, kubeStackVersion, err := r.createObjectManager(obj, pc, clusterCtx, false)
	if err != nil {
		serviceprovider.StatusProgressing(obj, "ReconcileError", err.Error())
		return ctrl.Result{}, ctrlerrors.IgnoreInvalidUserInput(err)
	}
	results, cleanerErr := mgr.Apply(ctx)
	managedResources, resultContainsErrors := resultsToResources(ctx, results)
	obj.Status.Resources = managedResources
	if resultContainsErrors || cleanerErr != nil {
		resultWithErrors := errors.New("resources contain reconcile errors")
		if cleanerErr != nil {
			resultWithErrors = fmt.Errorf("resources contain reconcile errors: %w", cleanerErr)
		}
		serviceprovider.StatusProgressing(obj, "ReconcileError", resultWithErrors.Error())
		return ctrl.Result{}, resultWithErrors
	}
	// Only record the version once it has actually been written to the clusters, so a
	// failed upgrade doesn't make deletion assume resources exist under the new version's config.
	obj.Status.InstalledVersion = kubeStackVersion.DeepCopy()
	if allResourcesReady(managedResources) {
		serviceprovider.StatusReady(obj)
	} else {
		serviceprovider.StatusProgressing(obj, "Reconciling", pendingResourcesMessage(managedResources))
	}
	return ctrl.Result{}, nil
}

// Delete is called on every delete event
func (r *OtelOperatorReconciler) Delete(ctx context.Context, obj *apiv1alpha1.OtelOperator, pc *apiv1alpha1.ProviderConfig, clusterCtx clusteraccess.ClusterContext) (ctrl.Result, error) {
	blockingKinds, err := cpresources.BlockingKinds(ctx, clusterCtx.MCPCluster.Client())
	if err != nil {
		serviceprovider.StatusProgressing(obj, "ReconcileError", err.Error())
		return ctrl.Result{}, err
	}
	if len(blockingKinds) > 0 {
		msg := fmt.Sprintf("waiting for user resources to be deleted: %s", strings.Join(blockingKinds, ", "))
		apimeta.SetStatusCondition(obj.GetConditions(), metav1.Condition{
			Type:               serviceprovider.ServiceProviderConditionReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: obj.GetGeneration(),
			Reason:             "UserResourcesExist",
			Message:            msg,
		})
		obj.SetObservedGeneration(obj.GetGeneration())
		obj.SetPhase(serviceprovider.StatusPhaseTerminating)
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}
	serviceprovider.StatusTerminating(obj)
	mgr, _, err := r.createObjectManager(obj, pc, clusterCtx, true)
	if err != nil {
		serviceprovider.StatusProgressing(obj, "ReconcileError", err.Error())
		return ctrl.Result{}, ctrlerrors.IgnoreInvalidUserInput(err)
	}
	results, cleanerErr := mgr.Delete(ctx)
	managedResources, resultContainsErrors := resultsToResources(ctx, results)
	obj.Status.Resources = managedResources
	if resultContainsErrors || cleanerErr != nil {
		resultWithErrors := errors.New("resources contain reconcile errors")
		if cleanerErr != nil {
			resultWithErrors = fmt.Errorf("resources contain reconcile errors: %w", cleanerErr)
		}
		serviceprovider.StatusProgressing(obj, "ReconcileError", resultWithErrors.Error())
		return ctrl.Result{}, resultWithErrors
	}
	if resources.AllDeleted(results) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

func (r *OtelOperatorReconciler) createObjectManager(obj *apiv1alpha1.OtelOperator, pc *apiv1alpha1.ProviderConfig, clusterCtx clusteraccess.ClusterContext, forDelete bool) (resources.Manager, apiv1alpha1.KubeStackVersion, error) {
	tenantNamespace, kubeStackVersion, err := r.prepareInputs(obj, pc, forDelete)
	if err != nil {
		return nil, apiv1alpha1.KubeStackVersion{}, err
	}
	helmValues, err := helm.ExtractHelmValues(kubeStackVersion.HelmValues)
	if err != nil {
		return nil, apiv1alpha1.KubeStackVersion{}, fmt.Errorf("failed to extract helm values: %w", err)
	}

	otelOperatorNamespace := namespaceOtelOperator
	if helmValues.NamespaceOverride != "" {
		otelOperatorNamespace = helmValues.NamespaceOverride
	}
	workloadNamespace := instance.Namespace(obj)

	platformCluster := resources.NewManagedCluster(r.PlatformCluster, r.PlatformCluster.RESTConfig(), tenantNamespace, resources.ClusterTypePlatform)
	cpCluster := resources.NewManagedCluster(clusterCtx.MCPCluster, clusterCtx.MCPCluster.RESTConfig(), otelOperatorNamespace, resources.ClusterTypeCP)
	workloadCluster := resources.NewManagedCluster(clusterCtx.WorkloadCluster, clusterCtx.WorkloadCluster.RESTConfig(), workloadNamespace, resources.ClusterTypeWorkload)

	mgr := resources.NewManager()
	mgr.AddCluster(cpCluster)
	mgr.AddCluster(workloadCluster)
	mgr.AddCluster(platformCluster)

	// ServiceAccount on CP + token Secret on workload so otel-operator connects to CP API.
	cpServiceAccount := &authn.ManagedServiceAccount{
		NamespacedName: k8stypes.NamespacedName{
			Name:      "otel-operator-server",
			Namespace: otelOperatorNamespace,
		},
	}
	cpServiceAccount.Configure(workloadCluster, cpCluster, pc.PollInterval())
	authz.Configure(cpCluster, cpServiceAccount)

	workloadHelmValues, crdHelmValues, err := prepareHelmValues(kubeStackVersion.HelmValues, cpCluster, cpServiceAccount.KubeAPIAccess())
	if err != nil {
		return nil, apiv1alpha1.KubeStackVersion{}, fmt.Errorf("failed to prepare helm values: %w", err)
	}

	workloadHelmValues, caConfigMapsToKeep, err := r.applyCABundle(pc, workloadHelmValues, workloadCluster, workloadNamespace)
	if err != nil {
		return nil, apiv1alpha1.KubeStackVersion{}, fmt.Errorf("failed to apply CA bundle: %w", err)
	}

	var chartPullSecret string
	if kubeStackVersion.ChartPullSecret != nil {
		chartPullSecret = *kubeStackVersion.ChartPullSecret
	}
	prefixedChartPullSecret, chartSecretsToKeep, err := r.syncChartPullSecret(platformCluster, chartPullSecret, tenantNamespace)
	if err != nil {
		return nil, apiv1alpha1.KubeStackVersion{}, err
	}

	imagePullSecretsToKeep := r.syncImagePullSecrets(workloadCluster, obj, helmValues)

	mgr.AddCleaner(configmap.NewConfigMapCleaner(workloadCluster, instance.Namespace(obj), caConfigMapsToKeep))
	mgr.AddCleaner(secret.NewSecretCleaner(workloadCluster, instance.Namespace(obj), append(slices.Clone(imagePullSecretsToKeep), corev1.LocalObjectReference{Name: cpServiceAccount.KubeAPIAccess()})))
	mgr.AddCleaner(secret.NewSecretCleaner(platformCluster, tenantNamespace, chartSecretsToKeep))

	flux.ManageFluxResources(flux.ManageFluxResourcesParams{
		Cluster:             platformCluster,
		CPNamespace:         otelOperatorNamespace,
		WorkloadNamespace:   workloadNamespace,
		ChartPullSecretName: prefixedChartPullSecret,
		Obj:                 obj,
		KubeStackVersion:    kubeStackVersion,
		PollInterval:        pc.PollInterval(),
		WorkloadHelmValues:  workloadHelmValues,
		CRDHelmValues:       crdHelmValues,
		ClusterContext:      clusterCtx,
		SASecretName:        cpServiceAccount.KubeAPIAccess(),
		CABundleRef:         pc.Spec.CABundleRef,
	})

	return mgr, kubeStackVersion, nil
}

func (r *OtelOperatorReconciler) prepareInputs(obj *apiv1alpha1.OtelOperator, pc *apiv1alpha1.ProviderConfig, forDelete bool) (string, apiv1alpha1.KubeStackVersion, error) {
	tenantNamespace, err := libutils.StableMCPNamespace(obj.Name, obj.Namespace)
	if err != nil {
		return "", apiv1alpha1.KubeStackVersion{}, fmt.Errorf("failed to determine tenant namespace: %w", err)
	}
	kubeStackVersion, err := selectKubeStackVersion(obj.Spec.Version, pc)
	if err != nil {
		if forDelete && obj.Status.InstalledVersion != nil {
			// The version may have been removed from the ProviderConfig; fall back to the
			// version config that was actually used to build the managed resources.
			return tenantNamespace, *obj.Status.InstalledVersion, nil
		}
		return "", apiv1alpha1.KubeStackVersion{}, fmt.Errorf("failed to select opentelemetry-kube-stack version: %w", err)
	}
	return tenantNamespace, kubeStackVersion, nil
}

func prepareHelmValues(helmValues *apiextensionsv1.JSON, cpCluster resources.ManagedCluster, saSecretName string) (*apiextensionsv1.JSON, *apiextensionsv1.JSON, error) {
	workloadHelmValues, err := helm.WorkloadHelmValues(helmValues)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to set workload helm values: %w", err)
	}
	workloadHelmValues, err = helm.AddAuthToHelmValues(workloadHelmValues, cpCluster, saSecretName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to inject CP auth into helm values: %w", err)
	}
	crdHelmValues, err := helm.CRDHelmValues(helmValues)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to set CRD helm values: %w", err)
	}
	return workloadHelmValues, crdHelmValues, nil
}

func (r *OtelOperatorReconciler) syncImagePullSecrets(workloadCluster resources.ManagedCluster, obj *apiv1alpha1.OtelOperator, helmValues *helm.Values) []corev1.LocalObjectReference {
	secret.ManageSecrets(workloadCluster, helmValues.ImagePullSecrets, secret.CopyConfig{
		SourceClient:    r.PlatformCluster.Client(),
		SourceNamespace: r.PodNamespace,
		TargetNamespace: instance.Namespace(obj),
	})
	return helmValues.ImagePullSecrets
}

func (r *OtelOperatorReconciler) syncChartPullSecret(platformCluster resources.ManagedCluster, chartPullSecret, tenantNamespace string) (string, []corev1.LocalObjectReference, error) {
	if chartPullSecret == "" {
		return "", nil, nil
	}
	prefixedName, err := secret.PrefixSecretName(chartPullSecret)
	if err != nil {
		return "", nil, fmt.Errorf("error generating secret name: %w", err)
	}
	secret.ManageSecrets(platformCluster, []corev1.LocalObjectReference{
		{Name: chartPullSecret},
	}, secret.CopyConfig{
		SourceClient:    r.PlatformCluster.Client(),
		SourceNamespace: r.PodNamespace,
		TargetNamespace: tenantNamespace,
		TargetName:      prefixedName,
	})
	return prefixedName, []corev1.LocalObjectReference{{Name: prefixedName}}, nil
}

// applyCABundle copies the CA bundle ConfigMap from the provider pod namespace to the
// workload cluster instance namespace and injects the matching volume/mount/env into
// workloadHelmValues. Returns the (possibly updated) helm values and the list of
// ConfigMap names that must be kept (for orphan cleanup).
func (r *OtelOperatorReconciler) applyCABundle(pc *apiv1alpha1.ProviderConfig, workloadHelmValues *apiextensionsv1.JSON, workloadCluster resources.ManagedCluster, workloadNamespace string) (*apiextensionsv1.JSON, []corev1.LocalObjectReference, error) {
	if pc.Spec.CABundleRef == nil {
		return workloadHelmValues, nil, nil
	}
	var err error
	workloadHelmValues, err = helm.AddCAHelmValues(workloadHelmValues, pc.Spec.CABundleRef)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to add CA volume to helm values: %w", err)
	}
	configmap.ManageCaConfigMap(workloadCluster, pc.Spec.CABundleRef.LocalObjectReference, configmap.CopyConfig{
		SourceClient:    r.PlatformCluster.Client(),
		SourceNamespace: r.PodNamespace,
		TargetNamespace: workloadNamespace,
		TargetName:      helm.CustomCABundleConfigMapName,
	})
	return workloadHelmValues, []corev1.LocalObjectReference{{Name: helm.CustomCABundleConfigMapName}}, nil
}

func selectKubeStackVersion(requestedVersion string, pc *apiv1alpha1.ProviderConfig) (apiv1alpha1.KubeStackVersion, error) {
	for _, v := range pc.Spec.Versions {
		if v.Version == requestedVersion {
			return v, nil
		}
	}
	return apiv1alpha1.KubeStackVersion{}, fmt.Errorf("%w: requested opentelemetry-kube-stack version (%s) is not available", ctrlerrors.ErrInvalidUserInput, requestedVersion)
}

func resultsToResources(ctx context.Context, results []resources.Result) ([]apiv1alpha1.ManagedResource, bool) {
	l := log.FromContext(ctx)
	containsError := false
	resources := make([]apiv1alpha1.ManagedResource, 0, len(results))
	for _, res := range results {
		obj := res.Object.GetObject()
		status := res.Object.GetStatus(apiv1alpha1.ResourceLocation(res.Cluster.GetClusterType()))
		resources = append(resources, apiv1alpha1.ManagedResource{
			TypedObjectReference: corev1.TypedObjectReference{
				Kind:      reflect.TypeOf(obj).Elem().Name(),
				Name:      obj.GetName(),
				Namespace: nilIfEmptyString(obj.GetNamespace()),
			},
			Phase:    status.Phase,
			Message:  status.Message,
			Location: status.Location,
		})
		if res.Error != nil {
			containsError = true
			l.Error(res.Error, "objectID", objectutils.ObjectID(obj))
		}
	}
	return resources, containsError
}

func nilIfEmptyString(str string) *string {
	if str == "" {
		return nil
	}
	return &str
}

func allResourcesReady(resources []apiv1alpha1.ManagedResource) bool {
	for _, res := range resources {
		if res.Phase != apiv1alpha1.Ready {
			return false
		}
	}
	return true
}

func pendingResourcesMessage(resources []apiv1alpha1.ManagedResource) string {
	for _, res := range resources {
		if res.Phase == apiv1alpha1.Ready {
			continue
		}
		message := res.Message
		if message == "" {
			message = "Resource is not ready"
		}
		return fmt.Sprintf("%s %s/%s is %s: %s", res.Kind, ptr.Deref(res.Namespace, ""), res.Name, res.Phase, message)
	}
	return "Reconcile in progress"
}

func (r *OtelOperatorReconciler) ensureInstanceID(ctx context.Context, obj *apiv1alpha1.OtelOperator) error {
	generated := instance.GenerateID(obj)
	if instance.GetID(obj) != generated {
		instance.SetID(obj, generated)
		if err := r.OnboardingCluster.Client().Update(ctx, obj); err != nil {
			return fmt.Errorf("failed to set instance id of otel operator resource %s/%s: %w", obj.Namespace, obj.Name, err)
		}
	}
	return nil
}
