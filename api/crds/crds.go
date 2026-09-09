package crds

import (
	"embed"

	crdutil "github.com/openmcp-project/controller-utils/pkg/crds"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// CRDFS is the embedded filesystem containing the manifests directory with CRD files.
//
//go:embed manifests
var CRDFS embed.FS

// CRDs retruns all embedded CustomResourceDefinitions.
func CRDs() ([]*apiextv1.CustomResourceDefinition, error) {
	return crdutil.CRDsFromFileSystem(CRDFS, "manifests")
}
