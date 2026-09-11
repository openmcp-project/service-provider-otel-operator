package secret

import (
	"context"
	"errors"
	"fmt"
	"slices"

	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	openmcpresources "github.com/openmcp-project/controller-utils/pkg/resources"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/openmcp-project/service-provider-otel-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/meta"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/resources"
)

// CopyConfig holds the configuration for copying secrets.
type CopyConfig struct {
	// SourceClient is the client to read the source secret from.
	SourceClient client.Client
	// SourceNamespace is the namespace of the source secret.
	SourceNamespace string
	// TargetNamespace is the namespace of the target secret.
	TargetNamespace string
	// TargetName is an optional value to adjust the name of the target secret
	// instead of using the source secret name.
	TargetName string
}

const secretNamePrefix = "sp-oo-"

// ErrSecretCleanup is an user-facing error that indicates secret cleanup failures
var ErrSecretCleanup = errors.New("secret cleanup failed")

// ManageSecrets syncs every secret to the target cluster.
func ManageSecrets(targetCluster resources.ManagedCluster, secrets []corev1.LocalObjectReference, config CopyConfig) {
	for _, secret := range secrets {
		secretName := secret.Name
		if config.TargetName != "" {
			secretName = config.TargetName
		}
		s := resources.NewManagedObject(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: config.TargetNamespace,
			},
		}, resources.ManagedObjectContext{
			ReconcileFunc: func(ctx context.Context, o client.Object) error {
				oSecret, ok := o.(*corev1.Secret)
				if !ok {
					return fmt.Errorf("expected *corev1.Secret, got %T", o)
				}
				sourceSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      secret.Name,
						Namespace: config.SourceNamespace,
					},
				}
				if err := config.SourceClient.Get(ctx, client.ObjectKeyFromObject(sourceSecret), sourceSecret); err != nil {
					return err
				}
				mutator := openmcpresources.NewSecretMutator(secretName, config.TargetNamespace, sourceSecret.Data, corev1.SecretTypeDockerConfigJson)
				return mutator.Mutate(oSecret)
			},
			StatusFunc: Status,
		})
		targetCluster.AddObject(s)
	}
}

// Status returns the status of a secret object.
func Status(o client.Object, rl apiv1alpha1.ResourceLocation) resources.Status {
	if !o.GetDeletionTimestamp().IsZero() {
		return resources.Status{
			Phase:    apiv1alpha1.Terminating,
			Message:  "Secret is terminating.",
			Location: rl,
		}
	}
	if o.GetUID() == "" {
		return resources.Status{
			Phase:    apiv1alpha1.Pending,
			Message:  "Secret has not been created yet.",
			Location: rl,
		}
	}
	return resources.Status{
		Phase:    apiv1alpha1.Ready,
		Message:  "Secret exists.",
		Location: rl,
	}
}

// PrefixSecretName adds a prefix to prevent name collisions.
func PrefixSecretName(secretName string) (string, error) {
	return ctrlutils.ShortenToXCharacters(fmt.Sprintf("%s%s", secretNamePrefix, secretName), ctrlutils.K8sMaxNameLength)
}

var _ resources.OrphanCleaner = &secretCleaner{}

type secretCleaner struct {
	cluster       resources.ManagedCluster
	namespace     string
	secretsToKeep []corev1.LocalObjectReference
}

// NewSecretCleaner removes redundant pull secrets in the given target namespace
// by removing any secret labeled as managed by service-provider-otel-operator that is not in secretsToKeep.
func NewSecretCleaner(cluster resources.ManagedCluster, namespace string, secretsToKeep []corev1.LocalObjectReference) resources.OrphanCleaner {
	return &secretCleaner{
		cluster:       cluster,
		namespace:     namespace,
		secretsToKeep: secretsToKeep,
	}
}

func (c *secretCleaner) Cleanup(ctx context.Context) ([]resources.Result, error) {
	results := []resources.Result{}
	secretCopies := &corev1.SecretList{}
	cl := c.cluster.GetClient()
	if err := cl.List(ctx, secretCopies,
		client.InNamespace(c.namespace),
		client.MatchingLabels{meta.LabelManagedBy: meta.LabelManagedByValue},
	); err != nil {
		log.FromContext(ctx).Error(err, "failed to list secrets for orphan cleanup")
		return nil, ErrSecretCleanup
	}
	for _, s := range secretCopies.Items {
		if !slices.ContainsFunc(c.secretsToKeep, func(ref corev1.LocalObjectReference) bool { return s.Name == ref.Name }) {
			if err := cl.Delete(ctx, &s); client.IgnoreNotFound(err) != nil {
				results = append(results, c.cleanupErrorResult(&s, err))
			}
		}
	}
	return results, nil
}

func (c *secretCleaner) cleanupErrorResult(obj *corev1.Secret, err error) resources.Result {
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
		Phase:    apiv1alpha1.Terminating,
		Message:  "Secret cleanup failed",
		Location: rl,
	}
}
