package oteloperator

import (
	"context"
	"fmt"

	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	openmcpresources "github.com/openmcp-project/controller-utils/pkg/resources"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SecretCopyConfig holds the configuration for copying a secret.
type SecretCopyConfig struct {
	SourceClient    client.Client
	SourceNamespace string
	TargetNamespace string
	TargetName      string
}

const secretNamePrefix = "sp-otelop-"

// ManagePullSecret syncs a pull secret to the target cluster.
func ManagePullSecret(targetCluster ManagedCluster, pullSecret corev1.LocalObjectReference, config SecretCopyConfig) {
	secret := NewManagedObject(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.TargetName,
			Namespace: config.TargetNamespace,
		},
	}, ManagedObjectContext{
		ReconcileFunc: func(ctx context.Context, o client.Object) error {
			oSecret, ok := o.(*corev1.Secret)
			if !ok {
				return fmt.Errorf("expected *corev1.Secret, got %T", o)
			}
			sourceSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pullSecret.Name,
					Namespace: config.SourceNamespace,
				},
			}
			if err := config.SourceClient.Get(ctx, client.ObjectKeyFromObject(sourceSecret), sourceSecret); err != nil {
				return err
			}
			mutator := openmcpresources.NewSecretMutator(config.TargetName, config.TargetNamespace, sourceSecret.Data, corev1.SecretTypeDockerConfigJson)
			return mutator.Mutate(oSecret)
		},
		StatusFunc: SimpleStatus,
	})
	targetCluster.AddObject(secret)
}

// PrefixSecretName adds a prefix to prevent name collisions.
func PrefixSecretName(secretName string) (string, error) {
	return ctrlutils.ShortenToXCharacters(fmt.Sprintf("%s%s", secretNamePrefix, secretName), ctrlutils.K8sMaxNameLength)
}
