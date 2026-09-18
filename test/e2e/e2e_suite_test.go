//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"distort/test/utils"
)

// TestE2E runs the unified e2e test suite to validate DISTORT in the Vagrant environment.
// The environment (VMs, K3s, NVMe disks, SoftRoCE, and Helm deployment)
// is assumed to have been set up by the `make test-env-*` targets prior to running this.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting DISTORT unified E2E Vagrant test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	By("verifying the suite is connected to the isolated three-node lab")
	out, err := utils.Run(exec.Command("kubectl", "config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}"))
	Expect(err).NotTo(HaveOccurred())
	Expect(out).To(Equal("https://192.168.56.10:6443"))

	out, err = utils.Run(exec.Command("kubectl", "get", "nodes", "-o", "jsonpath={.items[*].metadata.name}"))
	Expect(err).NotTo(HaveOccurred())
	Expect(out).To(And(
		ContainSubstring("distort-master"),
		ContainSubstring("distort-worker-1"),
		ContainSubstring("distort-worker-2"),
	))
})

var _ = ReportAfterEach(func(report SpecReport) {
	if !report.Failed() {
		return
	}
	_, _ = fmt.Fprintf(GinkgoWriter, "\n=== failure diagnostics for %s ===\n", report.FullText())
	commands := [][]string{
		{"get", "nodes", "-o", "wide"},
		{"get", "pods", "-n", "distort-system", "-o", "wide"},
		{"get", "nvmedevices,rdmastoragenodes,nvmedeviceclaims,nvmepartitions", "-A", "-o", "wide"},
		{"get", "events", "-A", "--sort-by=.lastTimestamp"},
		{"logs", "-n", "distort-system", "-l", "app.kubernetes.io/name=distort", "--all-containers=true", "--prefix=true", "--tail=100"},
	}
	for _, args := range commands {
		out, err := utils.Run(exec.Command("kubectl", args...))
		_, _ = fmt.Fprintf(GinkgoWriter, "$ kubectl %v\n%s\nerror: %v\n", args, out, err)
	}
})
