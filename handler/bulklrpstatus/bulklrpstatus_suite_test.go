package bulklrpstatus_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"testing"
)

func TestLrpstats(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Bulk LRP Status Suite")
}
