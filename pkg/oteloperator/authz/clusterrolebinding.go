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

package authz

import (
	"context"

	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator"
	"github.com/openmcp-project/service-provider-otel-operator/pkg/oteloperator/authn"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const clusterRoleBindingName = "otel-operator-server"

// Configure adds a managed ClusterRoleBinding to the CP cluster granting cluster-admin to the SA.
func Configure(cluster oteloperator.ManagedCluster, msa *authn.ManagedServiceAccount) {
	crb := oteloperator.NewManagedObject(&rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterRoleBindingName,
		},
	}, oteloperator.ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, o client.Object) error {
			oCRB := o.(*rbacv1.ClusterRoleBinding)
			oCRB.Subjects = []rbacv1.Subject{
				{
					Kind:      rbacv1.ServiceAccountKind,
					Name:      msa.Name,
					Namespace: msa.Namespace,
				},
			}
			oCRB.RoleRef = rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     "cluster-admin",
			}
			return nil
		},
		StatusFunc: oteloperator.SimpleStatus,
	})
	cluster.AddObject(crb)
}
