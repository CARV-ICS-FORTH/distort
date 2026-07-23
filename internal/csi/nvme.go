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

// ConnectRDMA connects to an NVMe-oF RDMA target.
func ConnectRDMA(nqn, portalIP, portalPort string) error {
	klog.Infof("Executing nvme connect -t rdma -a %s -s %s -n %s", portalIP, portalPort, nqn)

	cmd := exec.Command("nvme", "connect", "-t", "rdma", "-a", portalIP, "-s", portalPort, "-n", nqn)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "already connected") {
			klog.Infof("NVMe target %s is already connected", nqn)
			return nil
		}
		return fmt.Errorf("nvme connect failed: %v, output: %s", err, string(out))
	}
	return nil
}

// DisconnectRDMA disconnects from an NVMe-oF target by NQN.
func DisconnectRDMA(ctx context.Context, nqn string) error {
	klog.Infof("Executing nvme disconnect -n %s", nqn)

	cmd := exec.CommandContext(ctx, "nvme", "disconnect", "-n", nqn)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("nvme disconnect interrupted: %w", ctx.Err())
		}
		if strings.Contains(string(out), "no controllers found") {
			klog.Infof("NVMe target %s already disconnected", nqn)
			return nil
		}
		return fmt.Errorf("nvme disconnect failed: %v, output: %s", err, string(out))
	}
	return nil
}

// GetDeviceByNQN finds the block device (e.g., /dev/nvmeXn1) associated with an NQN.
func GetDeviceByNQN(nqn string) (string, error) {
	cmd := exec.Command("nvme", "list-subsys", "-o", "json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("nvme list-subsys failed: %v, output: %s", err, string(out))
	}

	var list NVMEHostList
	if err := json.Unmarshal(out, &list); err != nil {
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
