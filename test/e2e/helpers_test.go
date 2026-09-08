//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"distort/test/utils"
)

func kubectl(args ...string) (string, error) {
	return utils.Run(exec.Command("kubectl", args...))
}

func applyManifest(manifest string) (string, error) {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	return utils.Run(cmd)
}

func requireKnownE2E(finding string) {
	if os.Getenv("DISTORT_RUN_KNOWN_FAILURES") != "1" {
		Skip(finding + " is a known defect")
	}
	filter := strings.TrimSpace(os.Getenv("DISTORT_FINDING"))
	if filter == "" {
		return
	}
	for selected := range strings.SplitSeq(filter, ",") {
		if strings.EqualFold(strings.TrimSpace(selected), finding) {
			return
		}
	}
	Skip(finding + " is not selected")
}

func serialForNode(node string) string {
	out, err := kubectl(
		"get", "nvmedevices",
		"-o", `jsonpath={range .items[?(@.spec.nodeName=="`+node+`")]}{.spec.serialNumber}{"\n"}{end}`,
	)
	Expect(err).NotTo(HaveOccurred())
	serials := strings.Fields(out)
	Expect(serials).NotTo(BeEmpty(), "no NVMeDevice was discovered on %s", node)
	return serials[0]
}
