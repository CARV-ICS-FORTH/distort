package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"

	"k8s.io/klog/v2"
)

// CallSPDKRPC executes spdk_rpc.py with the given method and arguments.
// It parses the JSON output into the provided result object.
func CallSPDKRPC(method string, result interface{}, args ...string) error {
	cmdArgs := append([]string{method}, args...)
	cmd := exec.Command("/opt/spdk/scripts/rpc.py", cmdArgs...)
	klog.V(4).Infof("Executing SPDK RPC: %v", cmdArgs)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("spdk_rpc.py %s failed: %v\nStderr: %s", method, err, stderr.String())
	}

	if result != nil {
		outBytes := bytes.TrimSpace(stdout.Bytes())
		if len(outBytes) > 0 && outBytes[0] != '{' && outBytes[0] != '[' && outBytes[0] != '"' {
			// Some SPDK RPC methods (like bdev_lvol_create) return unquoted naked UUID strings!
			outBytes = []byte(fmt.Sprintf("%q", string(outBytes)))
		}
		if err := json.Unmarshal(outBytes, result); err != nil {
			return fmt.Errorf("failed to parse SPDK RPC response: %v\nOutput: %s", err, stdout.String())
		}
	}
	return nil
}
