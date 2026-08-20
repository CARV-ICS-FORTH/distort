package contracts

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"distort/test/knownfailure"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepositoryFile(t *testing.T, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), relative))
	if err != nil {
		t.Fatalf("reading %s: %v", relative, err)
	}
	return string(data)
}

func TestRepositoryContainsRequiredDistributionArtifacts(t *testing.T) {
	paths := []string{
		"deploy/charts/distort/Chart.yaml",
		"deploy/charts/distort/values.yaml",
		"deploy/charts/distort/templates/manager-deployment.yaml",
		"deploy/charts/distort/templates/agent-daemonset.yaml",
		"deploy/charts/distort/templates/csi-controller.yaml",
		"deploy/charts/distort/templates/csi-daemonset.yaml",
		"config/crd/bases/storage.distort.io_nvmedevices.yaml",
		"config/crd/bases/storage.distort.io_nvmedeviceclaims.yaml",
		"config/crd/bases/storage.distort.io_nvmepartitions.yaml",
		"config/crd/bases/storage.distort.io_nvmevolumeattachments.yaml",
		"config/crd/bases/storage.distort.io_rdmastoragenodes.yaml",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			info, err := os.Stat(filepath.Join(repositoryRoot(t), path))
			if err != nil || info.Size() == 0 {
				t.Fatalf("required artifact %s is missing or empty: %v", path, err)
			}
		})
	}
}

func TestChartEnablesControllerAttachmentFencing(t *testing.T) {
	driver := readRepositoryFile(t, "deploy/charts/distort/templates/csidriver.yaml")
	if !regexp.MustCompile(`(?m)^\s*attachRequired:\s*true\s*$`).MatchString(driver) {
		t.Fatal("CSIDriver must require ControllerPublishVolume before node staging")
	}

	controller := readRepositoryFile(t, "deploy/charts/distort/templates/csi-controller.yaml")
	if !strings.Contains(controller, "name: csi-attacher") {
		t.Fatal("CSI controller deployment has no external-attacher sidecar")
	}

	rbac := readRepositoryFile(t, "deploy/charts/distort/templates/rbac.yaml")
	for _, resource := range []string{"nvmevolumeattachments", "nvmevolumeattachments/status", "volumeattachments", "volumeattachments/status"} {
		if !strings.Contains(rbac, "- "+resource) {
			t.Errorf("chart RBAC does not include %s", resource)
		}
	}
}

func TestSampleManifestsContainUsableSpecs(t *testing.T) {
	knownfailure.Require(t, "F21")
	samples, err := filepath.Glob(filepath.Join(repositoryRoot(t), "config", "samples", "storage_*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 4 {
		t.Fatalf("found %d resource samples, want 4", len(samples))
	}
	for _, sample := range samples {
		data, err := os.ReadFile(sample)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "TODO(user)") {
			t.Errorf("%s still contains a scaffold TODO", filepath.Base(sample))
		}
		scanner := bufio.NewScanner(strings.NewReader(text))
		inSpec, concreteField := false, false
		for scanner.Scan() {
			line := scanner.Text()
			if line == "spec:" {
				inSpec = true
				continue
			}
			isField := strings.HasPrefix(line, "  ") &&
				!strings.HasPrefix(strings.TrimSpace(line), "#") &&
				strings.Contains(line, ":")
			if inSpec && isField {
				concreteField = true
			}
		}
		if !concreteField {
			t.Errorf("%s has no concrete spec fields", filepath.Base(sample))
		}
	}
}

func TestChartUsesSeparateServiceAccounts(t *testing.T) {
	knownfailure.Require(t, "F18")
	rbac := readRepositoryFile(t, "deploy/charts/distort/templates/rbac.yaml")
	if count := strings.Count(rbac, "apiVersion: v1\nkind: ServiceAccount"); count < 4 {
		t.Fatalf(
			"chart defines %d ServiceAccounts, want at least one per manager, agent, CSI controller, and CSI node",
			count,
		)
	}

	workloads := []string{
		"deploy/charts/distort/templates/manager-deployment.yaml",
		"deploy/charts/distort/templates/agent-daemonset.yaml",
		"deploy/charts/distort/templates/csi-controller.yaml",
		"deploy/charts/distort/templates/csi-daemonset.yaml",
	}
	accounts := map[string]struct{}{}
	pattern := regexp.MustCompile(`serviceAccountName:\s*(.+)`)
	for _, workload := range workloads {
		match := pattern.FindStringSubmatch(readRepositoryFile(t, workload))
		if len(match) != 2 {
			t.Fatalf("%s has no serviceAccountName", workload)
		}
		accounts[strings.TrimSpace(match[1])] = struct{}{}
	}
	if len(accounts) != len(workloads) {
		t.Fatalf(
			"%d workloads resolve through only %d distinct service account expressions: %#v",
			len(workloads), len(accounts), accounts,
		)
	}
}

func TestCRDDoesNotAdvertiseUnimplementedLVM(t *testing.T) {
	knownfailure.Require(t, "F20")
	crd := readRepositoryFile(t, "config/crd/bases/storage.distort.io_nvmepartitions.yaml")
	if regexp.MustCompile(`(?m)^\s*- lvm\s*$`).MatchString(crd) {
		t.Fatal("NVMePartition CRD admits lvm although no lvm plugin is registered")
	}
}

func TestCRDRejectsNonPositivePartitionSizes(t *testing.T) {
	crd := readRepositoryFile(t, "config/crd/bases/storage.distort.io_nvmepartitions.yaml")
	start := strings.Index(crd, "\n              size:\n")
	end := strings.Index(crd, "\n              targetBackend:\n")
	if start < 0 || end <= start {
		t.Fatal("could not locate NVMePartition size schema")
	}
	sizeSchema := crd[start:end]
	if !strings.Contains(sizeSchema, "x-kubernetes-validations:") {
		t.Fatal("NVMePartition size has no positive-capacity schema validation")
	}
}

func TestDocumentationMatchesGoDirective(t *testing.T) {
	knownfailure.Require(t, "F24")
	goMod := readRepositoryFile(t, "go.mod")
	match := regexp.MustCompile(`(?m)^go\s+([0-9]+\.[0-9]+)`).FindStringSubmatch(goMod)
	if len(match) != 2 {
		t.Fatal("go.mod has no parseable go directive")
	}
	want := "Go " + match[1]
	for _, document := range []string{"docs/content/architecture.md", "docs/content/contributing.md"} {
		text := readRepositoryFile(t, document)
		if !strings.Contains(text, want) {
			t.Errorf("%s does not mention the module Go version %s", document, match[1])
		}
	}
}
