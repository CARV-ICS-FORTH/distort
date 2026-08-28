package contracts

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/yaml"
	syaml "sigs.k8s.io/yaml"
)

type renderedWorkload struct {
	Kind string `json:"kind"`
	Spec struct {
		Template struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec corev1.PodSpec `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

func renderChart(t *testing.T, valuesPath string) map[string]corev1.PodSpec {
	t.Helper()
	args := []string{
		"template", "distort", filepath.Join(repositoryRoot(t), "deploy/charts/distort"),
		"--namespace", "distort-system", "--set-string", "image.repository=registry.example.com/distort",
	}
	if valuesPath != "" {
		args = append(args, "--values", valuesPath)
	}
	cmd := exec.Command("helm", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("render chart: %v\n%s", err, output)
	}

	workloads := make(map[string]corev1.PodSpec)
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(output), 4096)
	for {
		var workload renderedWorkload
		if err := decoder.Decode(&workload); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode rendered chart: %v", err)
		}
		if workload.Kind != "Deployment" && workload.Kind != "DaemonSet" {
			continue
		}
		component := workload.Spec.Template.Metadata.Labels["app.kubernetes.io/component"]
		workloads[component] = workload.Spec.Template.Spec
	}
	return workloads
}

func writeValues(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestChartSchedulesComponentsIndependently(t *testing.T) {
	values := writeValues(t, `
manager:
  nodeSelector: {distort.io/role: manager}
agent:
  nodeSelector: {distort.io/role: provider}
csiController:
  nodeSelector: {distort.io/role: csi-controller}
csiNode:
  nodeSelector: {distort.io/role: consumer}
`)
	workloads := renderChart(t, values)
	want := map[string]string{
		"manager": "manager", "agent": "provider",
		"csi-controller": "csi-controller", "csi-node": "consumer",
	}
	for component, role := range want {
		pod, ok := workloads[component]
		if !ok {
			t.Errorf("rendered chart has no %s workload", component)
			continue
		}
		if got := pod.NodeSelector["distort.io/role"]; got != role {
			t.Errorf("%s node selector = %q, want %q", component, got, role)
		}
	}
}

func TestChartRetainsLegacySchedulingFallback(t *testing.T) {
	values := writeValues(t, `
nodeSelector: {distort.io/legacy: shared}
tolerations:
  - key: distort.io/legacy
    operator: Equal
    value: shared
    effect: NoSchedule
affinity:
  nodeAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
        - matchExpressions:
            - key: distort.io/legacy
              operator: In
              values: [shared]
`)
	for component, pod := range renderChart(t, values) {
		if got := pod.NodeSelector["distort.io/legacy"]; got != "shared" {
			t.Errorf("%s does not inherit the legacy node selector", component)
		}
		if len(pod.Tolerations) != 1 || pod.Tolerations[0].Key != "distort.io/legacy" {
			t.Errorf("%s does not inherit the legacy toleration", component)
		}
		if pod.Affinity == nil || pod.Affinity.NodeAffinity == nil {
			t.Errorf("%s does not inherit the legacy affinity", component)
		}
	}
}

func containerCPU(t *testing.T, pod corev1.PodSpec, containerName string) resource.Quantity {
	t.Helper()
	for _, container := range pod.Containers {
		if container.Name == containerName {
			return container.Resources.Requests[corev1.ResourceCPU]
		}
	}
	t.Fatalf("container %s not found", containerName)
	return resource.Quantity{}
}

func TestVagrantLabCPURequestsFitDefaultNodes(t *testing.T) {
	vagrantfile := readRepositoryFile(t, "vagrant/Vagrantfile")
	match := regexp.MustCompile(`VM_CPUS = ENV\.fetch\("DISTORT_VM_CPUS", "([0-9]+)"\)`).FindStringSubmatch(vagrantfile)
	if len(match) != 2 {
		t.Fatal("could not read the default Vagrant CPU count")
	}
	vmCPUs, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		t.Fatal(err)
	}

	labValues := filepath.Join(repositoryRoot(t), "vagrant/helm-values.yaml")
	lab := renderChart(t, labValues)
	agentCPU := containerCPU(t, lab["agent"], "agent")
	csiCPU := containerCPU(t, lab["csi-node"], "csi-driver")
	headroomMilliCPU := vmCPUs*1000 - agentCPU.MilliValue() - csiCPU.MilliValue()
	if headroomMilliCPU < 500 {
		t.Fatalf("lab reserves %dm CPU for agent+CSI on a %d CPU node, leaving only %dm; want at least 500m headroom",
			agentCPU.MilliValue()+csiCPU.MilliValue(), vmCPUs, headroomMilliCPU)
	}

	productionAgent := renderChart(t, "")["agent"]
	productionCPU := containerCPU(t, productionAgent, "agent")
	if productionCPU.MilliValue() != 2000 {
		t.Fatalf("production agent CPU request = %s, want performance default 2", productionCPU.String())
	}
	for _, container := range productionAgent.Containers {
		if cpuLimit, limited := container.Resources.Limits[corev1.ResourceCPU]; container.Name == "agent" && limited {
			t.Fatalf("production agent has CPU limit %s; SPDK must not be CFS-quota limited", cpuLimit.String())
		}
	}

	makefile := readRepositoryFile(t, "Makefile")
	if !regexp.MustCompile(`test-env-deploy[\s\S]*--values vagrant/helm-values\.yaml`).MatchString(makefile) {
		t.Fatal("test-env-deploy does not consume the validated lab Helm values")
	}
}

func TestProjectScopesMatchGeneratedCRDs(t *testing.T) {
	type projectResource struct {
		API struct {
			Namespaced bool `json:"namespaced"`
		} `json:"api"`
		Kind string `json:"kind"`
	}
	var project struct {
		Resources []projectResource `json:"resources"`
	}
	if err := syaml.Unmarshal([]byte(readRepositoryFile(t, "PROJECT")), &project); err != nil {
		t.Fatalf("parse PROJECT: %v", err)
	}

	crdFiles, err := filepath.Glob(filepath.Join(repositoryRoot(t), "config/crd/bases/*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	crdScopes := make(map[string]bool, len(crdFiles))
	for _, path := range crdFiles {
		var crd struct {
			Spec struct {
				Scope string `json:"scope"`
				Names struct {
					Kind string `json:"kind"`
				} `json:"names"`
			} `json:"spec"`
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if unmarshalErr := syaml.Unmarshal(data, &crd); unmarshalErr != nil {
			t.Fatalf("parse %s: %v", filepath.Base(path), unmarshalErr)
		}
		crdScopes[crd.Spec.Names.Kind] = crd.Spec.Scope == "Namespaced"
	}

	if len(project.Resources) != len(crdScopes) {
		t.Fatalf("PROJECT has %d resources and generated CRDs have %d", len(project.Resources), len(crdScopes))
	}
	for _, entry := range project.Resources {
		want, ok := crdScopes[entry.Kind]
		if !ok {
			t.Errorf("PROJECT resource %s has no generated CRD", entry.Kind)
			continue
		}
		if entry.API.Namespaced != want {
			t.Errorf("PROJECT resource %s namespaced=%t, generated CRD namespaced=%t",
				entry.Kind, entry.API.Namespaced, want)
		}
	}
}

func TestKubeconfigFetchValidatesBeforeReplacing(t *testing.T) {
	makefile := readRepositoryFile(t, "Makefile")
	requiredInOrder := []string{
		`tr -d '\r'`,
		`sed -n '/^apiVersion: v1$$/,$$p'`,
		`config view --minify`,
		`mv "$(LOCAL_KUBECONFIG).tmp" "$(LOCAL_KUBECONFIG)"`,
	}
	position := 0
	for _, required := range requiredInOrder {
		offset := strings.Index(makefile[position:], required)
		if offset < 0 {
			t.Fatalf("get-kubeconfig is missing ordered safety step %q", required)
		}
		position += offset + len(required)
	}
}
