package oteloperator

import (
	"context"
	"fmt"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/conditions"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider/clusteraccess"

	apiv1alpha1 "github.com/openmcp-project/service-provider-otel-operator/api/v1alpha1"
)

// ManageFluxResourcesParams groups all parameters to create the required Flux resources.
type ManageFluxResourcesParams struct {
	Cluster             ManagedCluster
	CPNamespace         string
	WorkloadNamespace   string
	ChartPullSecretName string
	Obj                 *apiv1alpha1.OtelOperator
	ProviderConfig      *apiv1alpha1.ProviderConfig
	WorkloadHelmValues  *apiextensionsv1.JSON
	CRDHelmValues       *apiextensionsv1.JSON
	ClusterContext      clusteraccess.ClusterContext
}

const (
	workloadHelmReleaseSuffix = "-workload"
	crdHelmReleaseSuffix      = "-crds"
)

// ManageFluxResources configures one opentelemetry-kube-stack OCIRepository and two HelmReleases:
// CRDs to CP, opentelemetry-operator runtime resources to workload.
func ManageFluxResources(p ManageFluxResourcesParams) {
	kubeStackOCIRepo := newOCIRepository(p.Obj.Name, p.ProviderConfig.ChartURL(), p.Obj.Spec.Version, p)
	p.Cluster.AddObject(kubeStackOCIRepo)

	crdHelmRelease := NewManagedObject(&helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      helmReleaseName(p.Obj.Name, crdHelmReleaseSuffix),
			Namespace: p.Cluster.GetDefaultNamespace(),
		},
	}, ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, o client.Object) error {
			release, ok := o.(*helmv2.HelmRelease)
			if !ok {
				return fmt.Errorf("expected *helmv2.HelmRelease, got %T", o)
			}
			release.Spec = baseHelmReleaseSpec(p, p.CRDHelmValues, p.CPNamespace, kubeStackOCIRepo.GetObject().GetName())
			release.Spec.KubeConfig = &meta.KubeConfigReference{
				SecretRef: &meta.SecretKeyReference{
					Name: p.ClusterContext.MCPAccessSecretKey.Name,
					Key:  "kubeconfig",
				},
			}
			release.Spec.ReleaseName = helmReleaseName(p.Obj.Name, crdHelmReleaseSuffix)
			release.Spec.Install.CRDs = helmv2.Create
			release.Spec.Upgrade.CRDs = helmv2.CreateReplace
			release.Spec.Uninstall = orphanUninstall()
			return nil
		},
		DependsOn:      []ManagedObject{kubeStackOCIRepo},
		DeletionPolicy: Delete,
		StatusFunc:     FluxStatus,
	})
	p.Cluster.AddObject(crdHelmRelease)

	workloadHelmRelease := NewManagedObject(&helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      helmReleaseName(p.Obj.Name, workloadHelmReleaseSuffix),
			Namespace: p.Cluster.GetDefaultNamespace(),
		},
	}, ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, o client.Object) error {
			release, ok := o.(*helmv2.HelmRelease)
			if !ok {
				return fmt.Errorf("expected *helmv2.HelmRelease, got %T", o)
			}
			release.Spec = baseHelmReleaseSpec(p, p.WorkloadHelmValues, p.WorkloadNamespace, kubeStackOCIRepo.GetObject().GetName())
			release.Spec.KubeConfig = &meta.KubeConfigReference{
				SecretRef: &meta.SecretKeyReference{
					Name: p.ClusterContext.WorkloadAccessSecretKey.Name,
					Key:  "kubeconfig",
				},
			}
			release.Spec.ReleaseName = helmReleaseName(p.Obj.Name, workloadHelmReleaseSuffix)
			release.Spec.DependsOn = []helmv2.DependencyReference{
				{Name: helmReleaseName(p.Obj.Name, crdHelmReleaseSuffix)},
			}
			return nil
		},
		DependsOn:      []ManagedObject{kubeStackOCIRepo, crdHelmRelease},
		DeletionPolicy: Delete,
		StatusFunc:     FluxStatus,
	})
	p.Cluster.AddObject(workloadHelmRelease)
}

func newOCIRepository(name, url, tag string, p ManageFluxResourcesParams) ManagedObject {
	return NewManagedObject(&sourcev1.OCIRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
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
				URL:      url,
				LayerSelector: &sourcev1.OCILayerSelector{
					MediaType: "application/vnd.cncf.helm.chart.content.v1.tar+gzip",
					Operation: "extract",
				},
			}
			if tag != "" {
				repo.Spec.Reference = &sourcev1.OCIRepositoryRef{Tag: tag}
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
}

func baseHelmReleaseSpec(p ManageFluxResourcesParams, values *apiextensionsv1.JSON, targetNamespace, chartRefName string) helmv2.HelmReleaseSpec {
	return helmv2.HelmReleaseSpec{
		Interval: metav1.Duration{Duration: p.ProviderConfig.PollInterval()},
		ChartRef: &helmv2.CrossNamespaceSourceReference{
			Kind:      "OCIRepository",
			Name:      chartRefName,
			Namespace: p.Cluster.GetDefaultNamespace(),
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
		Values:           values,
		TargetNamespace:  targetNamespace,
		StorageNamespace: targetNamespace,
	}
}

func helmReleaseName(name, suffix string) string {
	return name + suffix
}

func orphanUninstall() *helmv2.Uninstall {
	policy := "orphan"
	return &helmv2.Uninstall{DeletionPropagation: &policy}
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
