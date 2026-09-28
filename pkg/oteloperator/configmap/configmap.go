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

package configmap

import (
	"context"
	"errors"
	"fmt"
	"slices"

	openmcpresources "github.com/openmcp-project/controller-utils/pkg/resources"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/openmcp-project/service-provider-otel-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/meta"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/resources"
)

// ErrConfigMapCleanup is a user-facing error that indicates configmap cleanup failures.
var ErrConfigMapCleanup = errors.New("configmap cleanup failed")

// CopyConfig holds the configuration for copying a ConfigMap.
type CopyConfig struct {
	// SourceClient is the client used to read the source ConfigMap.
	SourceClient client.Client
	// SourceNamespace is the namespace of the source ConfigMap.
	SourceNamespace string
	// TargetNamespace is the namespace of the target ConfigMap.
	TargetNamespace string
	// TargetName overrides the name of the target ConfigMap.
	// When empty the source name is used.
	TargetName string
}

// ManageCaConfigMap registers a managed object that syncs the CA ConfigMap to the target cluster.
func ManageCaConfigMap(targetCluster resources.ManagedCluster, caConfigMap corev1.LocalObjectReference, cfg CopyConfig) {
	targetName := caConfigMap.Name
	if cfg.TargetName != "" {
		targetName = cfg.TargetName
	}
	configMap := resources.NewManagedObject(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      targetName,
			Namespace: cfg.TargetNamespace,
		},
	}, resources.ManagedObjectContext{
		ReconcileFunc: func(ctx context.Context, o client.Object) error {
			oConfigMap, ok := o.(*corev1.ConfigMap)
			if !ok {
				return fmt.Errorf("expected *corev1.ConfigMap, got %T", o)
			}
			sourceConfigMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      caConfigMap.Name,
					Namespace: cfg.SourceNamespace,
				},
			}
			if err := cfg.SourceClient.Get(ctx, client.ObjectKeyFromObject(sourceConfigMap), sourceConfigMap); err != nil {
				return err
			}
			mutator := openmcpresources.NewConfigMapMutator(targetName, cfg.TargetNamespace, sourceConfigMap.Data)
			return mutator.Mutate(oConfigMap)
		},
		StatusFunc: ConfigMapStatus,
	})
	targetCluster.AddObject(configMap)
}

// ConfigMapStatus returns the managed status of a ConfigMap object.
func ConfigMapStatus(o client.Object, rl apiv1alpha1.ResourceLocation) resources.Status {
	if !o.GetDeletionTimestamp().IsZero() {
		return resources.Status{
			Phase:    apiv1alpha1.Terminating,
			Message:  "ConfigMap is terminating.",
			Location: rl,
		}
	}
	if o.GetUID() == "" {
		return resources.Status{
			Phase:    apiv1alpha1.Pending,
			Message:  "ConfigMap has not been created yet.",
			Location: rl,
		}
	}
	return resources.Status{
		Phase:    apiv1alpha1.Ready,
		Message:  "ConfigMap exists.",
		Location: rl,
	}
}

var _ resources.OrphanCleaner = &configMapCleaner{}

type configMapCleaner struct {
	cluster          resources.ManagedCluster
	namespace        string
	configMapsToKeep []corev1.LocalObjectReference
}

// NewConfigMapCleaner removes orphaned ConfigMaps in the given target namespace:
// any ConfigMap labeled as managed by service-provider-otel-operator that is not
// in configMapsToKeep will be deleted.
func NewConfigMapCleaner(cluster resources.ManagedCluster, namespace string, configMapsToKeep []corev1.LocalObjectReference) resources.OrphanCleaner {
	return &configMapCleaner{
		cluster:          cluster,
		namespace:        namespace,
		configMapsToKeep: configMapsToKeep,
	}
}

func (c *configMapCleaner) Cleanup(ctx context.Context) ([]resources.Result, error) {
	var results []resources.Result
	configMapCopies := &corev1.ConfigMapList{}
	cl := c.cluster.GetClient()
	if err := cl.List(ctx, configMapCopies,
		client.InNamespace(c.namespace),
		client.MatchingLabels{meta.LabelManagedBy: meta.LabelManagedByValue},
	); err != nil {
		log.FromContext(ctx).Error(err, "failed to list configmaps for orphan cleanup")
		return nil, ErrConfigMapCleanup
	}
	for _, cm := range configMapCopies.Items {
		if !slices.ContainsFunc(c.configMapsToKeep, func(ref corev1.LocalObjectReference) bool {
			return cm.Name == ref.Name
		}) {
			if err := cl.Delete(ctx, &cm); client.IgnoreNotFound(err) != nil {
				results = append(results, c.cleanupErrorResult(&cm, err))
			}
		}
	}
	return results, nil
}

func (c *configMapCleaner) cleanupErrorResult(obj *corev1.ConfigMap, err error) resources.Result {
	return resources.Result{
		Object: resources.NewManagedObject(
			obj,
			resources.ManagedObjectContext{
				StatusFunc:     cleanupErrorStatus,
				DeletionPolicy: resources.Delete,
			}),
		Cluster:         c.cluster,
		OperationResult: resources.OperationResultDeletionFailed,
		Error:           err,
	}
}

func cleanupErrorStatus(_ client.Object, rl apiv1alpha1.ResourceLocation) resources.Status {
	return resources.Status{
		Phase:    apiv1alpha1.Failed,
		Message:  "ConfigMap deletion failed.",
		Location: rl,
	}
}
