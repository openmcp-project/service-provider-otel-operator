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

package cpresources

import (
	"context"
	"fmt"
	"slices"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// OTel API group and version constants for CRDs installed by the opentelemetry-operator.
const (
	OtelGroup   = "opentelemetry.io"
	OtelVersion = "v1alpha1"
)

// otelListGVKs are the list GVKs for CRs that the opentelemetry-operator installs on the control plane.
// Keep in sync with https://github.com/open-telemetry/opentelemetry-operator/tree/main/apis
var otelListGVKs = []schema.GroupVersionKind{
	{Group: OtelGroup, Version: OtelVersion, Kind: "OpenTelemetryCollectorList"},
	{Group: OtelGroup, Version: OtelVersion, Kind: "InstrumentationList"},
	{Group: OtelGroup, Version: "v1beta1", Kind: "OpenTelemetryCollectorList"},
}

// BlockingKinds lists the CRD kinds that still have instances on the cluster, blocking deletion.
// Returns an empty slice when none exist or the CRDs are not installed.
func BlockingKinds(ctx context.Context, cl client.Client) ([]string, error) {
	var blocking []string
	for _, gvk := range otelListGVKs {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)
		if err := cl.List(ctx, list); err != nil {
			if apimeta.IsNoMatchError(err) {
				continue
			}
			return nil, fmt.Errorf("listing %s: %w", gvk.Kind, err)
		}
		if len(list.Items) > 0 {
			kind := strings.TrimSuffix(gvk.Kind, "List")
			// deduplicate (v1alpha1 and v1beta1 both cover OpenTelemetryCollector)
			if !containsString(blocking, kind) {
				blocking = append(blocking, kind)
			}
		}
	}
	return blocking, nil
}

func containsString(slice []string, s string) bool {
	return slices.Contains(slice, s)
}
