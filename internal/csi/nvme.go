/*
Copyright 2026, FORTH-ICS.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package csi

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"k8s.io/klog/v2"
)

// NVMeDevice represents a block device retrieved from `nvme list-subsys -o json`
type NVMeDevice struct {
	Name string
}

type NVMESubsystem struct {
	NQN   string `json:"NQN"`
	Paths []struct {
		Name      string `json:"Name"`
		Transport string `json:"Transport"`
		Address   string `json:"Address"`
		State     string `json:"State"`
	} `json:"Paths"`
}

type NVMEHostList []struct {
	Subsystems []NVMESubsystem `json:"Subsystems"`
}

// ConnectRDMA connects to an NVMe-oF RDMA target and reports whether this call
// created the connection. An already-connected target is left owned by the
// earlier staging operation and must not be disconnected during rollback.
func ConnectRDMA(ctx context.Context, nqn, portalIP, portalPort, hostNQN string) (bool, error) {
	klog.InfoS("Connecting NVMe target", "transport", "rdma", "portalIP", portalIP, "portalPort", portalPort, "nqn", nqn)

	// NodeStageVolume is idempotent. A rapid Pod recreation can stage the same
	// volume while its node-level NVMe controller is still live, and some
	// nvme-cli/kernel combinations report a duplicate connect as EINVAL instead
	// of the more recognizable "already connected" message. Observe the durable
	// kernel state before issuing another connect rather than relying on CLI
	// error text.
	if devicePath, err := GetDeviceByNQN(ctx, nqn); err == nil {
		klog.InfoS("NVMe target is already connected", "nqn", nqn, "devicePath", devicePath)
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("inspect existing NVMe connection: %w", err)
	}

	cmd := exec.CommandContext(ctx, "nvme", "connect", "-t", "rdma", "-a", portalIP, "-s", portalPort, "-n", nqn, "--hostnqn", hostNQN)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			// The command may have reached the target before cancellation. Report an
			// uncertain new connection so the staging transaction revokes it.
			return true, fmt.Errorf("nvme connect interrupted: %w", ctx.Err())
		}
		if strings.Contains(string(out), "already connected") {
			klog.InfoS("NVMe target is already connected", "nqn", nqn)
			return false, nil
		}
		// A concurrent stage may have connected the controller after the
		// observation above. Prefer the resulting live kernel state over
		// version-specific nvme-cli error wording and leave that connection owned
		// by the successful staging operation.
		if devicePath, observationErr := GetDeviceByNQN(ctx, nqn); observationErr == nil {
			klog.InfoS("NVMe target became connected concurrently", "nqn", nqn, "devicePath", devicePath)
			return false, nil
		}
		return true, fmt.Errorf("nvme connect failed: %v, output: %s", err, string(out))
	}
	return true, nil
}

// DisconnectRDMA disconnects from an NVMe-oF target by NQN.
func DisconnectRDMA(ctx context.Context, nqn string) error {
	klog.InfoS("Disconnecting NVMe target", "nqn", nqn)

	cmd := exec.CommandContext(ctx, "nvme", "disconnect", "-n", nqn)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("nvme disconnect interrupted: %w", ctx.Err())
		}
		if strings.Contains(string(out), "no controllers found") {
			klog.InfoS("NVMe target is already disconnected", "nqn", nqn)
			return nil
		}
		return fmt.Errorf("nvme disconnect failed: %v, output: %s", err, string(out))
	}
	return nil
}

// GetDeviceByNQN finds the block device (e.g., /dev/nvmeXn1) associated with an NQN.
func GetDeviceByNQN(ctx context.Context, nqn string) (string, error) {
	cmd := exec.CommandContext(ctx, "nvme", "list-subsys", "-o", "json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("nvme list-subsys interrupted: %w", ctx.Err())
		}
		return "", fmt.Errorf("nvme list-subsys failed: %v, output: %s", err, string(out))
	}

	return deviceByNQN(out, nqn)
}

func deviceByNQN(data []byte, nqn string) (string, error) {
	var list NVMEHostList
	if err := json.Unmarshal(data, &list); err != nil {
		return "", fmt.Errorf("failed to parse nvme list-subsys JSON: %v", err)
	}

	for _, host := range list {
		for _, sys := range host.Subsystems {
			if sys.NQN == nqn {
				for _, p := range sys.Paths {
					if p.State == "live" && p.Name != "" {
						// We need to return the block device, not the controller.
						// e.g., if Name is "nvme1", the block device is likely "nvme1n1"
						// We assume namespace 1.
						return "/dev/" + p.Name + "n1", nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("device for NQN %s not found", nqn)
}
