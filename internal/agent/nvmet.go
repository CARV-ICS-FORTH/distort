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

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"k8s.io/klog/v2"
)

const nvmetPath = "/sys/kernel/config/nvmet"

// ExportNVMeTarget sets up the configfs NVMe-oF target for the given block device.
func ExportNVMeTarget(nqn, blockDevice, portalIP string, portID, portalPort int) error {
	klog.Infof("Exporting %s as NVMe-oF target %s on %s:%d", blockDevice, nqn, portalIP, portalPort)

	// 1. Create Subsystem
	subsysPath := filepath.Join(nvmetPath, "subsystems", nqn)
	if err := os.MkdirAll(subsysPath, 0755); err != nil {
		return fmt.Errorf("failed to create subsystem: %w", err)
	}

	if err := os.WriteFile(filepath.Join(subsysPath, "attr_allow_any_host"), []byte("1"), 0644); err != nil {
		return fmt.Errorf("failed to allow any host: %w", err)
	}

	// 2. Create Namespace (NSID 1)
	nsPath := filepath.Join(subsysPath, "namespaces", "1")
	if err := os.MkdirAll(nsPath, 0755); err != nil {
		return fmt.Errorf("failed to create namespace: %w", err)
	}

	if err := os.WriteFile(filepath.Join(nsPath, "device_path"), []byte(blockDevice), 0644); err != nil {
		return fmt.Errorf("failed to set device_path: %w", err)
	}

	if err := os.WriteFile(filepath.Join(nsPath, "enable"), []byte("1"), 0644); err != nil {
		return fmt.Errorf("failed to enable namespace: %w", err)
	}

	// 3. Create Port
	portPath := filepath.Join(nvmetPath, "ports", strconv.Itoa(portID))
	if err := os.MkdirAll(portPath, 0755); err != nil {
		return fmt.Errorf("failed to create port: %w", err)
	}

	if err := os.WriteFile(filepath.Join(portPath, "addr_adrfam"), []byte("ipv4"), 0644); err != nil {
		return fmt.Errorf("failed to set addr_adrfam: %w", err)
	}
	if err := os.WriteFile(filepath.Join(portPath, "addr_trtype"), []byte("rdma"), 0644); err != nil {
		return fmt.Errorf("failed to set addr_trtype: %w", err)
	}
	if err := os.WriteFile(filepath.Join(portPath, "addr_trsvcid"), []byte(strconv.Itoa(portalPort)), 0644); err != nil {
		return fmt.Errorf("failed to set addr_trsvcid: %w", err)
	}
	if err := os.WriteFile(filepath.Join(portPath, "addr_traddr"), []byte(portalIP), 0644); err != nil {
		return fmt.Errorf("failed to set addr_traddr: %w", err)
	}

	// 4. Link Subsystem to Port
	linkPath := filepath.Join(portPath, "subsystems", nqn)
	err := os.Symlink(subsysPath, linkPath)
	if err != nil && !os.IsExist(err) {
		return fmt.Errorf("failed to link subsystem to port: %w", err)
	}

	return nil
}

// UnexportNVMeTarget tears down the NVMe-oF configfs structures.
func UnexportNVMeTarget(nqn string, portID int) error {
	klog.Infof("Unexporting NVMe-oF target %s", nqn)

	portPath := filepath.Join(nvmetPath, "ports", strconv.Itoa(portID))
	linkPath := filepath.Join(portPath, "subsystems", nqn)

	// Remove link
	os.Remove(linkPath)

	subsysPath := filepath.Join(nvmetPath, "subsystems", nqn)
	nsPath := filepath.Join(subsysPath, "namespaces", "1")

	// Disable and remove namespace
	os.WriteFile(filepath.Join(nsPath, "enable"), []byte("0"), 0644)
	os.Remove(nsPath)

	// Remove subsystem
	os.Remove(subsysPath)

	return nil
}
