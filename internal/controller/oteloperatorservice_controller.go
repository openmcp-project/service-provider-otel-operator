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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	libutils "github.com/openmcp-project/openmcp-operator/lib/utils"

	apiv1alpha1 "github.com/openmcp-project/service-provider-otel-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/spruntime"
)

const namespaceOtelOperator = "opentelemetry-operator-system"

// OtelOperatorServiceReconciler reconciles an OtelOperatorService object
type OtelOperatorServiceReconciler struct {
	OnboardingCluster *clusters.Cluster
	PlatformCluster   *clusters.Cluster
	PodNamespace      string
}

// CreateOrUpdate is called on every add or update event
func (r *OtelOperatorServiceReconciler) CreateOrUpdate(ctx context.Context, obj *apiv1alpha1.OtelOperatorService, pc *apiv1alpha1.ProviderConfig, clusterCtx spruntime.ClusterContext) (ctrl.Result, error) {
	spruntime.StatusProgressing(obj, "Reconciling", "Reconcile in progress")
	mgr, err := r.createObjectManager(obj, pc, clusterCtx)
	if err != nil {
		spruntime.StatusProgressing(obj, "ReconcileError", err.Error())
		return ctrl.Result{}, err
	}
	results := mgr.Apply(ctx)
	managedResources, resultContainsErrors := resultsToResources(ctx, results)
	obj.Status.Resources = managedResources
	if allResourcesReady(managedResources) {
		spruntime.StatusReady(obj)
	}
	if resultContainsErrors {
		resultWithErrors := errors.New("resources contain reconcile errors")
		spruntime.StatusProgressing(obj, "ReconcileError", resultWithErrors.Error())
		return ctrl.Result{}, resultWithErrors
	}
	return ctrl.Result{}, nil
}

// Delete is called on every delete event
func (r *OtelOperatorServiceReconciler) Delete(ctx context.Context, obj *apiv1alpha1.OtelOperatorService, pc *apiv1alpha1.ProviderConfig, clusterCtx spruntime.ClusterContext) (ctrl.Result, error) {
	spruntime.StatusTerminating(obj)
	mgr, err := r.createObjectManager(obj, pc, clusterCtx)
	if err != nil {
		spruntime.StatusProgressing(obj, "ReconcileError", err.Error())
		return ctrl.Result{}, err
	}
	results := mgr.Delete(ctx)
	managedResources, resultContainsErrors := resultsToResources(ctx, results)
	obj.Status.Resources = managedResources
	if oteloperator.AllDeleted(results) {
		return ctrl.Result{}, nil
	}
	if resultContainsErrors {
		resultWithErrors := errors.New("resources contain reconcile errors")
		spruntime.StatusProgressing(obj, "ReconcileError", resultWithErrors.Error())
		return ctrl.Result{}, resultWithErrors
	}
	return ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

func (r *OtelOperatorServiceReconciler) createObjectManager(obj *apiv1alpha1.OtelOperatorService, pc *apiv1alpha1.ProviderConfig, clusterCtx spruntime.ClusterContext) (oteloperator.Manager, error) {
	tenantNamespace, err := libutils.StableMCPNamespace(obj.Name, obj.Namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to determine tenant namespace: %w", err)
	}
	helmValues, err := oteloperator.ExtractHelmValues(pc.Spec.HelmValues)
	if err != nil {
		return nil, fmt.Errorf("failed to extract helm values: %w", err)
	}

	platformCluster := oteloperator.NewManagedCluster(r.PlatformCluster, r.PlatformCluster.RESTConfig(), tenantNamespace, oteloperator.ClusterTypePlatform)
	otelOperatorNamespace := namespaceOtelOperator
	if helmValues.NamespaceOverride != "" {
		otelOperatorNamespace = helmValues.NamespaceOverride
	}
	mcpCluster := oteloperator.NewManagedCluster(clusterCtx.MCPCluster, clusterCtx.MCPCluster.RESTConfig(), otelOperatorNamespace, oteloperator.ClusterTypeMCP)

	for _, imagePullSecret := range helmValues.Global.ImagePullSecrets {
		oteloperator.ManagePullSecret(mcpCluster, imagePullSecret, oteloperator.SecretCopyConfig{
			SourceClient:    platformCluster.GetClient(),
			SourceNamespace: r.PodNamespace,
			TargetNamespace: otelOperatorNamespace,
			TargetName:      imagePullSecret.Name,
		})
	}

	var prefixedChartPullSecret string
	if pc.Spec.ChartPullSecret != nil {
		prefixedChartPullSecret, err = oteloperator.PrefixSecretName(*pc.Spec.ChartPullSecret)
		if err != nil {
			return nil, fmt.Errorf("error generating secret name: %w", err)
		}
		oteloperator.ManagePullSecret(platformCluster, corev1.LocalObjectReference{Name: *pc.Spec.ChartPullSecret}, oteloperator.SecretCopyConfig{
			SourceClient:    platformCluster.GetClient(),
			SourceNamespace: r.PodNamespace,
			TargetNamespace: tenantNamespace,
			TargetName:      prefixedChartPullSecret,
		})
	}

	oteloperator.ManageFluxResources(oteloperator.ManageFluxResourcesParams{
		Cluster:             platformCluster,
		MCPNamespace:        otelOperatorNamespace,
		ChartPullSecretName: prefixedChartPullSecret,
		Obj:                 obj,
		ProviderConfig:      pc,
		ClusterContext:      clusterCtx,
	})

	mgr := oteloperator.NewManager()
	mgr.AddCluster(mcpCluster)
	mgr.AddCluster(platformCluster)
	return mgr, nil
}

func resultsToResources(ctx context.Context, results []oteloperator.Result) ([]apiv1alpha1.ManagedResource, bool) {
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
			l.Error(res.Error, "objectID", oteloperator.ObjectID(obj))
		}
	}
	return resources, containsError
}

func nilIfEmptyString(str string) *string {
	if str == "" {
		return nil
	}
	return ptr.To(str)
}

func allResourcesReady(resources []apiv1alpha1.ManagedResource) bool {
	for _, res := range resources {
		if res.Phase != apiv1alpha1.Ready {
			return false
		}
	}
	return true
}
