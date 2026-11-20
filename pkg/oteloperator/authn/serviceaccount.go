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

	"github.com/openmcp-project/extensibility-utils/pkg/tokenrefresh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/resources"
)

const (
	// TokenServiceProvider is the service provider name used to build the token annotation keys,
	// shared by the initial token write in Configure and the tokenrefresh controller.
	TokenServiceProvider = "oteloperator"

	// SecretKeyKubeconfig is the Secret key of the kubeconfig for the CP cluster.
	SecretKeyKubeconfig = "kubeconfig"
)

// ManagedServiceAccount references the managed ServiceAccount object on the CP cluster.
type ManagedServiceAccount struct {
	types.NamespacedName
}

// KubeAPIAccess returns the name of the secret that stores the CP access token on the workload cluster.
func (m *ManagedServiceAccount) KubeAPIAccess() string {
	return fmt.Sprintf("kube-api-access-%s", m.Name)
}

// TokenBinding returns the ServiceAccount and the token Secret in the given workload namespace for the token refresh controller.
func (m *ManagedServiceAccount) TokenBinding(workloadNamespace string) tokenrefresh.Binding {
	return tokenrefresh.Binding{
		ServiceAccount: m.NamespacedName,
		Secret:         types.NamespacedName{Name: m.KubeAPIAccess(), Namespace: workloadNamespace},
	}
}

// Configure adds a managed ServiceAccount to the CP cluster and a token Secret to the workload cluster.
func (m *ManagedServiceAccount) Configure(workloadCluster, cpCluster resources.ManagedCluster) {
	ns := resources.NewManagedObject(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: m.Namespace,
		},
	}, resources.ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, _ client.Object) error { return nil },
		StatusFunc:    resources.SimpleStatus,
	})
	cpCluster.AddObject(ns)

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
	}
	msa := resources.NewManagedObject(sa, resources.ManagedObjectContext{
		DependsOn:     []resources.ManagedObject{ns},
		ReconcileFunc: func(_ context.Context, _ client.Object) error { return nil },
		StatusFunc:    resources.SimpleStatus,
	})
	cpCluster.AddObject(msa)

	wcNamespace := resources.NewManagedObject(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: workloadCluster.GetDefaultNamespace(),
		},
	}, resources.ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, _ client.Object) error { return nil },
		StatusFunc:    resources.SimpleStatus,
	})
	workloadCluster.AddObject(wcNamespace)

	secret := resources.NewManagedObject(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.KubeAPIAccess(),
			Namespace: workloadCluster.GetDefaultNamespace(),
		},
	}, resources.ManagedObjectContext{
		DependsOn: []resources.ManagedObject{msa},
		ReconcileFunc: func(ctx context.Context, o client.Object) error {
			oSecret := o.(*corev1.Secret)
			cfg := cpCluster.GetConfig()
			if cfg == nil {
				return errors.New("rest config of the CP cluster must not be nil")
			}
			kubeconfig, err := tokenKubeconfig(cfg.Host, cpCluster.GetDefaultNamespace())
			if err != nil {
				return err
			}
			if oSecret.Data == nil {
				oSecret.Data = map[string][]byte{}
			}
			oSecret.Data[SecretKeyKubeconfig] = kubeconfig
			// if the token is already present, don't overwrite it, the tokenrefresh controller will handle it.
			if len(oSecret.Data[tokenrefresh.SecretKeyToken]) > 0 {
				return nil
			}
			_, err = tokenrefresh.EnsureToken(ctx, cpCluster.GetCluster(), oSecret,
				m.TokenBinding(workloadCluster.GetDefaultNamespace()), TokenServiceProvider, tokenrefresh.DefaultTokenLifetime)
			return err
		},
		StatusFunc: resources.SimpleStatus,
	})
	workloadCluster.AddObject(secret)
}

// tokenKubeconfig returns a kubeconfig for the CP cluster that reads the token and the CA bundle from the files
// next to it, i.e. the token and ca.crt keys of the same Secret volume. Relative paths are resolved against the
// directory of the kubeconfig file, so the kubeconfig is static and the token can be renewed without rewriting it.
func tokenKubeconfig(host, namespace string) ([]byte, error) {
	cfg := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"cp": {
				Server:               host,
				CertificateAuthority: tokenrefresh.SecretKeyCA,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"otel-operator": {
				TokenFile: tokenrefresh.SecretKeyToken,
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
