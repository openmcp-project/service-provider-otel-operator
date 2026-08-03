package oteloperator

import (
	"context"
	"fmt"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/conditions"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider/clusteraccess"

	apiv1alpha1 "github.com/openmcp-project/service-provider-otel-operator/api/v1alpha1"
)

// ManageFluxResourcesParams groups all parameters to create the required Flux resources.
type ManageFluxResourcesParams struct {
	Cluster             ManagedCluster
	MCPNamespace        string
	ChartPullSecretName string
	Obj                 *apiv1alpha1.OtelOperator
	ProviderConfig      *apiv1alpha1.ProviderConfig
	ClusterContext      clusteraccess.ClusterContext
}

// ManageFluxResources configures OCIRepository and HelmRelease for the otel-operator chart.
func ManageFluxResources(p ManageFluxResourcesParams) {
	ociRepo := NewManagedObject(&sourcev1.OCIRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.Obj.Name,
			Namespace: p.Cluster.GetDefaultNamespace(),
		},
	}, ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, o client.Object) error {
			repo, ok := o.(*sourcev1.OCIRepository)
			if !ok {
				return fmt.Errorf("expected *sourcev1.OCIRepository, got %T", o)
			}
			repo.Spec = sourcev1.OCIRepositorySpec{
				Interval: metav1.Duration{Duration: p.ProviderConfig.PollInterval()},
				URL:      *p.ProviderConfig.Spec.ChartURL,
				Reference: &sourcev1.OCIRepositoryRef{
					Tag: p.Obj.Spec.Version,
				},
				LayerSelector: &sourcev1.OCILayerSelector{
					MediaType: "application/vnd.cncf.helm.chart.content.v1.tar+gzip",
					Operation: "extract",
				},
			}
			if p.ChartPullSecretName != "" {
				repo.Spec.SecretRef = &meta.LocalObjectReference{
					Name: p.ChartPullSecretName,
				}
			}
			return nil
		},
		DependsOn:      []ManagedObject{},
		DeletionPolicy: Delete,
		StatusFunc:     FluxStatus,
	})
	p.Cluster.AddObject(ociRepo)

	helmRelease := NewManagedObject(&helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.Obj.Name,
			Namespace: p.Cluster.GetDefaultNamespace(),
		},
	}, ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, o client.Object) error {
			release, ok := o.(*helmv2.HelmRelease)
			if !ok {
				return fmt.Errorf("expected *helmv2.HelmRelease, got %T", o)
			}
			release.Spec = helmv2.HelmReleaseSpec{
				Interval: metav1.Duration{Duration: p.ProviderConfig.PollInterval()},
				ChartRef: &helmv2.CrossNamespaceSourceReference{
					Kind:      "OCIRepository",
					Name:      p.Obj.Name,
					Namespace: p.Cluster.GetDefaultNamespace(),
				},
				KubeConfig: &meta.KubeConfigReference{
					SecretRef: &meta.SecretKeyReference{
						Name: p.ClusterContext.MCPAccessSecretKey.Name,
						Key:  "kubeconfig",
					},
				},
				Install: &helmv2.Install{
					Remediation: &helmv2.InstallRemediation{
						Retries: 3,
					},
					CreateNamespace:         true,
					DisableSchemaValidation: true,
				},
				Upgrade: &helmv2.Upgrade{
					DisableSchemaValidation: true,
				},
				Values:           p.ProviderConfig.Spec.HelmValues,
				TargetNamespace:  p.MCPNamespace,
				StorageNamespace: p.MCPNamespace,
			}
			return nil
		},
		DependsOn:      []ManagedObject{ociRepo},
		DeletionPolicy: Delete,
		StatusFunc:     FluxStatus,
	})
	p.Cluster.AddObject(helmRelease)
}

// FluxStatus indicates whether the given Flux object is terminating, pending, or ready.
func FluxStatus(o client.Object, rl apiv1alpha1.ResourceLocation) Status {
	fluxObject := o.(conditions.Getter)
	if !o.GetDeletionTimestamp().IsZero() {
		return Status{Phase: apiv1alpha1.Terminating, Message: "Resource is terminating.", Location: rl}
	}
	if conditions.IsReady(fluxObject) {
		return Status{Phase: apiv1alpha1.Ready, Message: fluxStatusMessage(fluxObject, "Resource is ready"), Location: rl}
	}
	return Status{Phase: apiv1alpha1.Pending, Message: fluxStatusMessage(fluxObject, "Resource is not ready"), Location: rl}
}

func fluxStatusMessage(fluxObject conditions.Getter, fallback string) string {
	for _, conditionType := range []string{meta.ReadyCondition, meta.StalledCondition, meta.ReconcilingCondition} {
		if message := conditions.GetMessage(fluxObject, conditionType); message != "" {
			return message
		}
	}
	return fallback
}
