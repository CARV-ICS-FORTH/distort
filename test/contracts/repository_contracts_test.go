package contracts

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
		"docs/content/review-findings.md",
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
	resources := []string{
		"nvmevolumeattachments", "nvmevolumeattachments/status",
		"volumeattachments", "volumeattachments/status",
	}
	for _, resource := range resources {
		if !strings.Contains(rbac, `"`+resource+`"`) {
			t.Errorf("chart RBAC does not include %s", resource)
		}
	}
}

func TestChartWiresWorkloadHealthChecks(t *testing.T) {
	manager := readRepositoryFile(t, "deploy/charts/distort/templates/manager-deployment.yaml")
	for _, requiredText := range []string{"livenessProbe:", "readinessProbe:", "path: /healthz", "path: /readyz"} {
		if !strings.Contains(manager, requiredText) {
			t.Errorf("manager deployment is missing %q", requiredText)
		}
	}

	agent := readRepositoryFile(t, "deploy/charts/distort/templates/agent-daemonset.yaml")
	for _, requiredText := range []string{
		"--metrics-bind-address=0", "--health-probe-bind-address=:18081",
		"livenessProbe:", "readinessProbe:", "path: /healthz", "path: /readyz",
	} {
		if !strings.Contains(agent, requiredText) {
			t.Errorf("agent DaemonSet is missing %q", requiredText)
		}
	}

	for _, workload := range []string{
		"deploy/charts/distort/templates/csi-controller.yaml",
		"deploy/charts/distort/templates/csi-daemonset.yaml",
	} {
		manifest := readRepositoryFile(t, workload)
		requiredTexts := []string{
			"name: liveness-probe", "sig-storage/livenessprobe:",
			"livenessProbe:", "path: /healthz",
		}
		for _, requiredText := range requiredTexts {
			if !strings.Contains(manifest, requiredText) {
				t.Errorf("%s is missing %q", workload, requiredText)
			}
		}
	}
}

func TestChartRequiresQualifiedVersionedImage(t *testing.T) {
	chart := readRepositoryFile(t, "deploy/charts/distort/Chart.yaml")
	if !regexp.MustCompile(`(?m)^version:\s*0\.5\.0\s*$`).MatchString(chart) ||
		!regexp.MustCompile(`(?m)^appVersion:\s*"0\.5\.0"\s*$`).MatchString(chart) {
		t.Fatal("chart version and appVersion must both be 0.5.0")
	}

	values := readRepositoryFile(t, "deploy/charts/distort/values.yaml")
	if !regexp.MustCompile(`(?m)^\s*repository:\s*""\s*$`).MatchString(values) {
		t.Fatal("chart must not default to an unpublished or unqualified image repository")
	}
	if regexp.MustCompile(`(?m)^\s*tag:\s*["']?latest["']?\s*$`).MatchString(values) {
		t.Fatal("chart must not default to the mutable latest tag")
	}

	helpers := readRepositoryFile(t, "deploy/charts/distort/templates/_helpers.tpl")
	for _, requiredText := range []string{
		`required "image.repository`, `image.repository must be a fully qualified repository`,
		`image.tag must be versioned; latest is not allowed`, `image.digest must be an immutable sha256 digest`,
	} {
		if !strings.Contains(helpers, requiredText) {
			t.Errorf("chart image helper is missing %q", requiredText)
		}
	}
	for _, workload := range []string{
		"deploy/charts/distort/templates/manager-deployment.yaml",
		"deploy/charts/distort/templates/agent-daemonset.yaml",
		"deploy/charts/distort/templates/csi-controller.yaml",
		"deploy/charts/distort/templates/csi-daemonset.yaml",
	} {
		if !strings.Contains(readRepositoryFile(t, workload), `include "distort.image"`) {
			t.Errorf("%s does not use the validated image helper", workload)
		}
	}
}

func TestCanonicalDocsDescribeCurrentAttachmentModel(t *testing.T) {
	architecture := readRepositoryFile(t, "docs/content/architecture.md")
	if !strings.Contains(architecture, "five Custom Resource Definitions") ||
		!strings.Contains(architecture, "NVMeVolumeAttachment") {
		t.Fatal("architecture must document all five DISTORT CRDs")
	}

	for _, document := range []string{"docs/content/architecture.md", "docs/content/internals.md"} {
		text := readRepositoryFile(t, document)
		requiredTexts := []string{
			"attachRequired: true", "ControllerPublishVolume",
			"ControllerUnpublishVolume", "NVMeVolumeAttachment",
		}
		for _, requiredText := range requiredTexts {
			if !strings.Contains(text, requiredText) {
				t.Errorf("%s does not describe %s", document, requiredText)
			}
		}
		for _, staleText := range []string{"attachRequired: false", "Controller-side attachment fencing is absent"} {
			if strings.Contains(text, staleText) {
				t.Errorf("%s retains stale assertion %q", document, staleText)
			}
		}
	}
}

func TestRedeployTargetExpandsToBuildAndDeploymentWorkflow(t *testing.T) {
	cmd := exec.Command("make", "-n", "test-env-redeploy", "CONTROLLER_GEN=/bin/true")
	cmd.Dir = repositoryRoot(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run test-env-redeploy: %v\n%s", err, output)
	}
	text := string(output)
	commands := []string{
		"docker build", "docker save", "helm upgrade --install",
		"rollout restart", "rollout status",
	}
	for _, command := range commands {
		if !strings.Contains(text, command) {
			t.Errorf("test-env-redeploy dry-run does not emit %q", command)
		}
	}
}

func TestReadinessAndFencingGatesAreNotOverstated(t *testing.T) {
	smoke := readRepositoryFile(t, "vagrant/smoke-test.sh")
	requiredSmokeChecks := []string{
		"condition=Ready", "condition=NVMeInventoryReady", "lastHeartbeatTime", "rdmaIP", "heartbeat_age",
	}
	for _, requiredText := range requiredSmokeChecks {
		if !strings.Contains(smoke, requiredText) {
			t.Errorf("smoke test does not validate %s", requiredText)
		}
	}

	e2e := readRepositoryFile(t, "test/e2e/regression_e2e_test.go")
	f25 := regexp.MustCompile(`Describe\("Single-writer attachment fencing", Label\(([^\n]+)`).FindStringSubmatch(e2e)
	if len(f25) != 2 {
		t.Fatal("could not locate F25 labels")
	}
	if strings.Contains(f25[1], `"green"`) || strings.Contains(f25[1], `"release-gate"`) {
		t.Fatalf("F25 must not be green or a release gate before corrected hardware verification: %s", f25[1])
	}
	if !strings.Contains(f25[1], `"known-failure"`) || !strings.Contains(e2e, `requireKnownE2E("F25")`) {
		t.Fatal("F25 must remain explicitly selected while hardware verification is pending")
	}
	if !strings.Contains(e2e, "f25-old-io-failed") {
		t.Fatal("F25 does not prove old-node I/O failure during takeover")
	}
}

func TestSampleManifestsContainUsableSpecs(t *testing.T) {
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
	kustomization := readRepositoryFile(t, "config/samples/kustomization.yaml")
	for _, agentOwned := range []string{"storage_v1alpha1_nvmedevice.yaml", "storage_v1alpha1_rdmastoragenode.yaml"} {
		if regexp.MustCompile(`(?m)^\s*-\s*` + regexp.QuoteMeta(agentOwned) + `\s*$`).MatchString(kustomization) {
			t.Errorf("default sample kustomization applies agent-owned resource %s", agentOwned)
		}
	}
}

func TestChartUsesSeparateServiceAccounts(t *testing.T) {
	rbac := readRepositoryFile(t, "deploy/charts/distort/templates/rbac.yaml")
	componentRange := regexp.MustCompile(`range \$component := list "manager" "agent" "csiController" "csiNode"`)
	if !componentRange.MatchString(rbac) {
		t.Fatal("chart must create service accounts for manager, agent, CSI controller, and CSI node")
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

func TestCRDsEnforceDeviceAndClaimIdentitySafety(t *testing.T) {
	claimCRD := readRepositoryFile(t, "config/crd/bases/storage.distort.io_nvmedeviceclaims.yaml")
	for _, requiredText := range []string{
		"minLength: 1",
		"rule: self == oldSelf",
		"message: serialNumber is immutable",
	} {
		if !strings.Contains(claimCRD, requiredText) {
			t.Errorf("NVMeDeviceClaim CRD is missing %q", requiredText)
		}
	}

	deviceCRD := readRepositoryFile(t, "config/crd/bases/storage.distort.io_nvmedevices.yaml")
	for _, requiredText := range []string{
		"pattern: ^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\\.[0-7]$",
		"rule: quantity(string(self)).isGreaterThan(quantity('0'))",
		"message: totalCapacity must be positive",
	} {
		if !strings.Contains(deviceCRD, requiredText) {
			t.Errorf("NVMeDevice CRD is missing %q", requiredText)
		}
	}
}

func TestRDMAStorageNodeCRDAdvertisesOnlyImplementedTransports(t *testing.T) {
	generated := readRepositoryFile(t, "config/crd/bases/storage.distort.io_rdmastoragenodes.yaml")
	chart := readRepositoryFile(t, "deploy/charts/distort/crds/storage.distort.io_rdmastoragenodes.yaml")
	if generated != chart {
		t.Fatal("generated and Helm RDMAStorageNode CRDs differ")
	}
	if strings.Contains(generated, "\n                - TCP\n") {
		t.Fatal("RDMAStorageNode CRD advertises unsupported TCP transport")
	}
	for _, transport := range []string{"RoCEv2", "InfiniBand"} {
		if !strings.Contains(generated, "\n                - "+transport+"\n") {
			t.Errorf("RDMAStorageNode CRD is missing %s transport", transport)
		}
	}
}

func TestDocumentationMatchesGoDirective(t *testing.T) {
	goMod := readRepositoryFile(t, "go.mod")
	match := regexp.MustCompile(`(?m)^go\s+([0-9]+\.[0-9]+)`).FindStringSubmatch(goMod)
	if len(match) != 2 {
		t.Fatal("go.mod has no parseable go directive")
	}
	want := regexp.MustCompile(`Go(?:\s+version)?\s+` + regexp.QuoteMeta(match[1]))
	for _, document := range []string{"docs/content/architecture.md", "docs/content/contributing.md"} {
		text := readRepositoryFile(t, document)
		if !want.MatchString(text) {
			t.Errorf("%s does not mention the module Go version %s", document, match[1])
		}
	}
}
