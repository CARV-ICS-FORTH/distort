//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestE2E runs the unified e2e test suite to validate DISTORT in the Vagrant environment.
// The environment (VMs, K3s, NVMe disks, SoftRoCE, and Helm deployment)
// is assumed to have been set up by the `make test-env-*` targets prior to running this.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting DISTORT unified E2E Vagrant test suite\n")
	RunSpecs(t, "e2e suite")
}
