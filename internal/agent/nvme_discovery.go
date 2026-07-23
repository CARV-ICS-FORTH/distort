package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/klog/v2"

	"distort/internal/agent/plugins"
)

// HardwareNVMe represents a discovered physical NVMe namespace mapped into SPDK or Kernel.
type HardwareNVMe struct {
	Name         string // e.g. nvme0 or Nvme0n1
	PCIAddress   string // e.g. 0000:01:00.0
	SerialNumber string
	Model        string
	TotalBytes   int64
	NUMANode     int
}

const sysClassNVMe = "/sys/class/nvme"
const sysClassBlock = "/sys/class/block"

// DiscoverNVMe scans both the Linux sysfs tree (for kernel-bound NVMe devices)
// and SPDK JSON-RPC (for SPDK-bound NVMe devices), returning a unified list.
func DiscoverNVMe() ([]HardwareNVMe, error) {
	var devices []HardwareNVMe
	seenSerials := make(map[string]bool)

	// 1. Scan kernel-bound devices from sysfs
	kernelDevs, err := discoverKernelNVMe()
	if err == nil {
		for _, d := range kernelDevs {
			serial := strings.ToLower(strings.TrimSpace(d.SerialNumber))
			if serial != "" && !seenSerials[serial] {
				devices = append(devices, d)
				seenSerials[serial] = true
			}
		}
	} else {
		klog.Warningf("Failed to discover kernel NVMe devices: %v", err)
	}

	// 2. Scan SPDK-bound devices if SPDK is running
	spdkDevs, err := discoverSPDKNVMe()
	if err == nil {
		for _, d := range spdkDevs {
			serial := strings.ToLower(strings.TrimSpace(d.SerialNumber))
			if serial != "" && !seenSerials[serial] {
				devices = append(devices, d)
				seenSerials[serial] = true
			}
		}
	} else {
		klog.Warningf("Failed to discover SPDK NVMe devices: %v", err)
	}

	return devices, nil
}

func discoverKernelNVMe() ([]HardwareNVMe, error) {
	var devices []HardwareNVMe

	entries, err := os.ReadDir(sysClassNVMe)
	if err != nil {
		if os.IsNotExist(err) {
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

		// Read NUMA Node
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

		// Check environment variables
		if excludeList := os.Getenv("NVME_EXCLUDE_DEVICES"); excludeList != "" {
			if strings.Contains(excludeList, hwDev.PCIAddress) {
				//klog.Infof("Skipping device %s (%s) because it is in NVME_EXCLUDE_DEVICES", devName, hwDev.PCIAddress)

				continue
			}
		}
		if allowList := os.Getenv("NVME_ALLOWED_DEVICES"); allowList != "" {
			if !strings.Contains(allowList, hwDev.PCIAddress) {
				klog.Infof("Skipping device %s (%s) because it is not in NVME_ALLOWED_DEVICES", devName, hwDev.PCIAddress)
				continue
			}
		}

		// Check for mounted filesystems using lsblk
		if isDeviceMounted(devName) {
			klog.Infof("Skipping device %s (%s) because it has mounted filesystems", devName, hwDev.PCIAddress)
			continue
		}

		// Calculate total bytes from matching namespace blocks
		hwDev.TotalBytes = calculateTotalBytes(devName)

		devices = append(devices, hwDev)
	}

	return devices, nil
}

func isDeviceMounted(devName string) bool {
	// devName is an NVMe controller (e.g. "nvme1"), which is a character device.
	// lsblk only works on block devices, so we must check the namespaces
	// (e.g. nvme1n1, nvme1n2) found under /sys/class/block.
	entries, err := os.ReadDir(sysClassBlock)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		bName := entry.Name()
		if !strings.HasPrefix(bName, devName+"n") {
			continue
		}
		out, err := exec.Command("lsblk", "/dev/"+bName, "-n", "-o", "MOUNTPOINT").Output()
		if err != nil {
			klog.Warningf("lsblk failed for namespace %s: %v", bName, err)
			continue
		}
		lines := strings.Split(string(out), "\n")
		klog.Infof("lsblk mountpoints for namespace %s: %v", bName, lines)
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				klog.Infof("Namespace %s is mounted (mountpoint: %q), skipping controller %s", bName, strings.TrimSpace(line), devName)
				return true
			}
		}
	}
	return false
}

func calculateTotalBytes(nvmeName string) int64 {
	var total int64
	entries, err := os.ReadDir(sysClassBlock)
	if err != nil {
		return 0
	}

	for _, entry := range entries {
		bName := entry.Name()
		if strings.HasPrefix(bName, nvmeName+"n") {
			sizePath := filepath.Join(sysClassBlock, bName, "size")
			if b, err := os.ReadFile(sizePath); err == nil {
				if blocks, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
					total += blocks * 512
				}
			}
		}
	}
	return total
}

type SpdkBdev struct {
	Name           string `json:"name"`
	ProductName    string `json:"product_name"`
	BlockSize      int64  `json:"block_size"`
	NumBlocks      int64  `json:"num_blocks"`
	UUID           string `json:"uuid"`
	DriverSpecific *struct {
		NVMe []struct {
			PCIAddress string `json:"pci_address"`
			CtrlrData  struct {
				ModelNumber  string `json:"model_number"`
				SerialNumber string `json:"serial_number"`
			} `json:"ctrlr_data"`
		} `json:"nvme"`
	} `json:"driver_specific"`
}

type SpdkNVMeController struct {
	Name  string `json:"name"`
	Ctrlr struct {
		Model        string `json:"model"`
		SerialNumber string `json:"serial_number"`
	} `json:"ctrlr"`
}

func discoverSPDKNVMe() ([]HardwareNVMe, error) {
	if err := exec.Command("pidof", "nvmf_tgt").Run(); err != nil {
		return nil, nil // SPDK not running
	}

	var bdevs []SpdkBdev
	if err := plugins.CallSPDKRPC("bdev_get_bdevs", &bdevs); err != nil {
		return nil, err
	}

	var devices []HardwareNVMe
	for _, bdev := range bdevs {
		if bdev.DriverSpecific == nil || len(bdev.DriverSpecific.NVMe) == 0 {
			continue
		}

		hwDev := HardwareNVMe{
			Name:         bdev.Name,
			PCIAddress:   bdev.DriverSpecific.NVMe[0].PCIAddress,
			SerialNumber: strings.TrimSpace(bdev.DriverSpecific.NVMe[0].CtrlrData.SerialNumber),
			Model:        strings.TrimSpace(bdev.DriverSpecific.NVMe[0].CtrlrData.ModelNumber),
			TotalBytes:   bdev.NumBlocks * bdev.BlockSize,
			NUMANode:     -1,
		}
		devices = append(devices, hwDev)
	}

	return devices, nil
}
