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

package authn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator"
)

const (
	annotationTokenExpirationTime = "oteloperator.services.openmcp.cloud/token-expiration-time"
)

var (
	errSANameOrNamespaceEmpty = errors.New("name or namespace in service account reference must not be empty")
	errRestConfigNil          = errors.New("rest config must not be nil")
	errExpirationInvalid      = errors.New("must not specify a duration less than 10 minutes")
)

type serviceAccountToken struct {
	Host        string
	CAData      []byte
	Token       string
	TokenExpiry time.Time
}

func generateToken(ctx context.Context, cp *clusters.Cluster, cfg *rest.Config, svcAccRef types.NamespacedName, expiration time.Duration) (*serviceAccountToken, error) {
	if svcAccRef.Name == "" || svcAccRef.Namespace == "" {
		return nil, errSANameOrNamespaceEmpty
	}
	if cfg == nil {
		return nil, errRestConfigNil
	}
	if expiration < 10*time.Minute {
		return nil, errExpirationInvalid
	}

	sa := &corev1.ServiceAccount{}
	if err := cp.Client().Get(ctx, types.NamespacedName{Name: svcAccRef.Name, Namespace: svcAccRef.Namespace}, sa); err != nil {
		return nil, err
	}

	req := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: new(int64(expiration.Seconds())),
		},
	}
	if err := cp.Client().SubResource("token").Create(ctx, sa, req); err != nil {
		return nil, err
	}

	rc := &serviceAccountToken{
		Host:        cfg.Host,
		Token:       req.Status.Token,
		TokenExpiry: req.Status.ExpirationTimestamp.Time,
		CAData:      cfg.CAData,
	}

	if cfg.CAFile != "" {
		caBytes, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		rc.CAData = caBytes
	}

	return rc, nil
}

// ManagedServiceAccount references the managed ServiceAccount object on the CP cluster.
type ManagedServiceAccount struct {
	types.NamespacedName
}

// KubeAPIAccess returns the name of the secret that stores the CP access token on the workload cluster.
func (m *ManagedServiceAccount) KubeAPIAccess() string {
	return fmt.Sprintf("kube-api-access-%s", m.Name)
}

// Configure adds a managed ServiceAccount to the CP cluster and a token Secret to the workload cluster.
func (m *ManagedServiceAccount) Configure(workloadCluster, cpCluster oteloperator.ManagedCluster, pollInterval time.Duration) {
	ns := oteloperator.NewManagedObject(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: m.Namespace,
		},
	}, oteloperator.ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, _ client.Object) error { return nil },
		StatusFunc:    oteloperator.SimpleStatus,
	})
	cpCluster.AddObject(ns)

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
	}
	msa := oteloperator.NewManagedObject(sa, oteloperator.ManagedObjectContext{
		DependsOn:     []oteloperator.ManagedObject{ns},
		ReconcileFunc: func(_ context.Context, _ client.Object) error { return nil },
		StatusFunc:    oteloperator.SimpleStatus,
	})
	cpCluster.AddObject(msa)

	wcNamespace := oteloperator.NewManagedObject(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: workloadCluster.GetDefaultNamespace(),
		},
	}, oteloperator.ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, _ client.Object) error { return nil },
		StatusFunc:    oteloperator.SimpleStatus,
	})
	workloadCluster.AddObject(wcNamespace)

	secret := oteloperator.NewManagedObject(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.KubeAPIAccess(),
			Namespace: workloadCluster.GetDefaultNamespace(),
		},
	}, oteloperator.ManagedObjectContext{
		DependsOn: []oteloperator.ManagedObject{msa},
		ReconcileFunc: func(ctx context.Context, o client.Object) error {
			oSecret := o.(*corev1.Secret)
			nextReconcile := time.Now().Add(pollInterval).Add(time.Minute)
			expirationTime, err := getTokenExpirationTime(oSecret)
			if err != nil || expirationTime.Before(nextReconcile) {
				rc, err := generateToken(ctx, cpCluster.GetCluster(), cpCluster.GetConfig(), m.NamespacedName, 1*time.Hour)
				if err != nil {
					return err
				}
				kubeconfig, err := tokenKubeconfig(rc, cpCluster.GetDefaultNamespace())
				if err != nil {
					return err
				}
				oSecret.Data = map[string][]byte{
					"token":      []byte(rc.Token),
					"namespace":  []byte(cpCluster.GetDefaultNamespace()),
					"ca.crt":     rc.CAData,
					"kubeconfig": kubeconfig,
				}
				setTokenExpirationTime(oSecret, rc.TokenExpiry)
			}
			return nil
		},
		StatusFunc: oteloperator.SimpleStatus,
	})
	workloadCluster.AddObject(secret)
}

func tokenKubeconfig(token *serviceAccountToken, namespace string) ([]byte, error) {
	cfg := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"cp": {
				Server:                   token.Host,
				CertificateAuthorityData: token.CAData,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"otel-operator": {
				Token: token.Token,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"cp": {
				Cluster:   "cp",
				AuthInfo:  "otel-operator",
				Namespace: namespace,
			},
		},
		CurrentContext: "cp",
	}
	return clientcmd.Write(cfg)
}

func getTokenExpirationTime(obj *corev1.Secret) (time.Time, error) {
	if obj.Annotations == nil {
		return time.Time{}, errors.New("no expiration time set")
	}
	expirationTime := obj.Annotations[annotationTokenExpirationTime]
	return time.Parse(time.RFC3339, expirationTime)
}

func setTokenExpirationTime(obj *corev1.Secret, expTime time.Time) {
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations[annotationTokenExpirationTime] = expTime.Format(time.RFC3339)
}
