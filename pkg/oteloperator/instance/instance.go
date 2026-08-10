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

package instance

import (
	"crypto/sha1" //nolint:gosec
	"encoding/base32"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1alpha1 "github.com/openmcp-project/service-provider-otel-operator/api/v1alpha1"
)

const (
	labelInstanceID          = "oteloperator.services.openmcp.cloud/instance-id"
	base32EncodeStdLowerCase = "abcdefghijklmnopqrstuvwxyz234567"
)

// GetID returns the instance id of the OtelOperator object.
func GetID(o client.Object) string {
	if o.GetLabels() == nil {
		return ""
	}
	return o.GetLabels()[labelInstanceID]
}

// SetID sets the instance id of the OtelOperator object.
func SetID(o *apiv1alpha1.OtelOperator, id string) {
	if o.Labels == nil {
		o.Labels = map[string]string{}
	}
	o.Labels[labelInstanceID] = id
}

// GenerateID generates a stable instance id from the object's namespace and name.
func GenerateID(o client.Object) string {
	h := sha1.New() //nolint:gosec
	_, _ = fmt.Fprintf(h, "%s/%s", o.GetNamespace(), o.GetName())
	return base32.NewEncoding(base32EncodeStdLowerCase).WithPadding(base32.NoPadding).EncodeToString(h.Sum(nil))
}

// Namespace returns the workload namespace for the OtelOperator instance.
func Namespace(o *apiv1alpha1.OtelOperator) string {
	return fmt.Sprintf("otel-operator-%s", GetID(o))
}
