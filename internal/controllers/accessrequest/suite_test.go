package accessrequest_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/openmcp-project/cluster-provider-gardener/internal/controllers/shared"
)

func TestComponentUtils(t *testing.T) {
	RegisterFailHandler(Fail)

	shared.SetProviderName("gardener")
	shared.SetEnvironment("test")
	shared.SetAccessRequestServiceAccountNamespace("accessrequests")

	RunSpecs(t, "AccessRequest Controller Test Suite")
}
