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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/klog/v2"

	"distort/internal/execlog"
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
func ConnectRDMA(nqn, portalIP, portalPort, targetBackend string) error {
	transport := "rdma"
	if targetBackend == "bxi" {
		transport = "portals4"
		// Find valid BXI NID dynamically
		matches, err := filepath.Glob("/sys/devices/*/*/*/bxi3/bxi*/nid")
		if err == nil && len(matches) > 0 {
			for _, match := range matches {
				data, err := os.ReadFile(match)
				if err == nil {
					nidStr := strings.TrimSpace(string(data))
					if _, err := strconv.Atoi(nidStr); err == nil {
						// Assuming BXI NIDs map to 192.168.123.<NID>
						portalIP = fmt.Sprintf("192.168.123.%s", nidStr)
						break
					}
				}
			}
		}
	}

	execlog.LogKernel(6, "nvme connect -t %s -a %s -s %s -n %s", transport, portalIP, portalPort, nqn)
	out, err := execlog.Run("nvme", "connect", "-t", transport, "-a", portalIP, "-s", portalPort, "-n", nqn)
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
func DisconnectRDMA(nqn string) error {
	out, err := execlog.Run("nvme", "disconnect", "-n", nqn)
	if err != nil {
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
	out, err := execlog.Run("nvme", "list-subsys", "-o", "json")
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
