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
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

const sysClassNVMe = "/sys/class/nvme"
const sysClassBlock = "/sys/class/block"

// HardwareNVMe represents a discovered physical NVMe controller.
type HardwareNVMe struct {
	Name         string // e.g. nvme0
	PCIAddress   string // e.g. 0000:01:00.0
	SerialNumber string
	Model        string
	TotalBytes   int64
	NUMANode     int
}

// DiscoverNVMe scans the sysfs tree to find local NVMe controllers and their capacities.
func DiscoverNVMe() ([]HardwareNVMe, error) {
	var devices []HardwareNVMe

	entries, err := os.ReadDir(sysClassNVMe)
	if err != nil {
		if os.IsNotExist(err) {
			klog.V(4).Infof("No NVMe subsystem found (directory %s does not exist)", sysClassNVMe)
			return devices, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "nvme") {
			continue
		}

		devName := entry.Name()
		devPath := filepath.Join(sysClassNVMe, devName)

		// Read Transport and filter out non-PCIe devices (like nvme-of network targets)
		transport := "pcie" // default
		if b, err := os.ReadFile(filepath.Join(devPath, "transport")); err == nil {
			transport = strings.TrimSpace(string(b))
		}

		if transport != "pcie" {
			klog.V(4).Infof("Skipping non-PCIe NVMe device %s (transport: %s)", devName, transport)
			continue
		}

		hwDev := HardwareNVMe{
			Name: devName,
		}

		// Read Model
		if b, err := os.ReadFile(filepath.Join(devPath, "model")); err == nil {
			hwDev.Model = strings.TrimSpace(string(b))
		}

		// Read Serial Number
		if b, err := os.ReadFile(filepath.Join(devPath, "serial")); err == nil {
			hwDev.SerialNumber = strings.TrimSpace(string(b))
		}

		// Read NUMA Node (often -1 if not assigned)
		if b, err := os.ReadFile(filepath.Join(devPath, "numa_node")); err == nil {
			if parsed, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				hwDev.NUMANode = parsed
			}
		}

		// Determine PCI Address by resolving the device symlink
		link, err := os.Readlink(filepath.Join(devPath, "device"))
		if err == nil {
			hwDev.PCIAddress = filepath.Base(link)
		}

		// Calculate total bytes by finding the namespaces/block devices
		hwDev.TotalBytes = calculateTotalBytes(devName)

		devices = append(devices, hwDev)
	}

	return devices, nil
}

func calculateTotalBytes(nvmeName string) int64 {
	var total int64

	// Look for block devices matching this NVMe name (e.g., nvme0n1, nvme0n2 inside /sys/class/block)
	entries, err := os.ReadDir(sysClassBlock)
	if err != nil {
		return 0
	}

	for _, entry := range entries {
		bName := entry.Name()
		if strings.HasPrefix(bName, nvmeName+"n") {
			// Read size (in 512-byte blocks)
			sizePath := filepath.Join(sysClassBlock, bName, "size")
			if b, err := os.ReadFile(sizePath); err == nil {
				if blocks, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
					// Also optionally check hw_sector_size, but linux 'size' is generally in 512b sectors
					total += blocks * 512
				}
			}
		}
	}

	return total
}
