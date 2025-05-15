package crds

import (
	"embed"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	crdutil "github.com/openmcp-project/controller-utils/pkg/crds"
)

//go:embed manifests
var CRDFS embed.FS

func CRDs() ([]*apiextv1.CustomResourceDefinition, error) {
	return crdutil.CRDsFromFileSystem(CRDFS, "manifests")
}
