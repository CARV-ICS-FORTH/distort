package contracts

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	syaml "sigs.k8s.io/yaml"
)

func hasMinimalEventWriteRule(rules []rbacv1.PolicyRule) bool {
	for _, rule := range rules {
		if len(rule.APIGroups) != 1 || rule.APIGroups[0] != "" ||
			len(rule.Resources) != 1 || rule.Resources[0] != "events" || len(rule.Verbs) != 2 {
			continue
		}
		verbs := map[string]bool{rule.Verbs[0]: true, rule.Verbs[1]: true}
		if verbs["create"] && verbs["patch"] {
			return true
		}
	}
	return false
}

func TestGeneratedManagerRoleAllowsEventWrites(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "config/rbac/role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := syaml.Unmarshal(data, &role); err != nil {
		t.Fatalf("decode generated manager role: %v", err)
	}
	if !hasMinimalEventWriteRule(role.Rules) {
		t.Fatal("generated manager role lacks the minimal core events create/patch rule")
	}
}

func TestRenderedManagerRoleAllowsEventWrites(t *testing.T) {
	chart := filepath.Join(repositoryRoot(t), "deploy/charts/distort")
	cmd := exec.Command("helm", "template", "distort", chart, "--namespace", "distort-system",
		"--set-string", "image.repository=registry.example.com/distort")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("render chart: %v\n%s", err, output)
	}

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(output), 4096)
	for {
		var role rbacv1.ClusterRole
		if err := decoder.Decode(&role); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode rendered chart: %v", err)
		}
		if role.Kind == "ClusterRole" && role.Name == "distort-manager" {
			if !hasMinimalEventWriteRule(role.Rules) {
				t.Fatal("rendered manager role lacks the minimal core events create/patch rule")
			}
			return
		}
	}
	t.Fatal("rendered chart has no distort-manager ClusterRole")
}
