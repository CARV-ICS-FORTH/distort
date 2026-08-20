package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"k8s.io/klog/v2"
)

var spdkRPCExecutable = "/opt/spdk/scripts/rpc.py"

const spdkRPCTimeout = 15 * time.Second

// CallSPDKRPC executes spdk_rpc.py with the given method and arguments.
// It parses the JSON output into the provided result object.
func CallSPDKRPC(method string, result any, args ...string) error {
	return CallSPDKRPCContext(context.Background(), method, result, args...)
}

// CallSPDKRPCContext executes one bounded SPDK JSON-RPC command and terminates
// the helper process when the caller is cancelled or the operation times out.
func CallSPDKRPCContext(ctx context.Context, method string, result any, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, spdkRPCTimeout)
	defer cancel()
	cmdArgs := append([]string{method}, args...)
	cmd := exec.CommandContext(ctx, spdkRPCExecutable, cmdArgs...)
	cmd.WaitDelay = 250 * time.Millisecond
	klog.V(4).Infof("Executing SPDK RPC: %v", cmdArgs)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("spdk_rpc.py %s interrupted: %w", method, ctx.Err())
		}
		return fmt.Errorf("spdk_rpc.py %s failed: %v\nStdout: %s\nStderr: %s",
			method, err, stdout.String(), stderr.String())
	}

	if result != nil {
		outBytes := bytes.TrimSpace(stdout.Bytes())
		if len(outBytes) > 0 && outBytes[0] != '{' && outBytes[0] != '[' && outBytes[0] != '"' {
			// Some SPDK RPC methods (like bdev_lvol_create) return unquoted naked UUID strings!
			outBytes = fmt.Appendf(nil, "%q", string(outBytes))
		}
		if err := json.Unmarshal(outBytes, result); err != nil {
			return fmt.Errorf("failed to parse SPDK RPC response: %v\nOutput: %s", err, stdout.String())
		}
	}
	return nil
}
