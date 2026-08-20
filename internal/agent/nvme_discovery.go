package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

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

var sysClassNVMe = "/sys/class/nvme"
var sysClassBlock = "/sys/class/block"

const unsafeMountInspectionEnv = "NVME_ALLOW_UNSAFE_MOUNT_INSPECTION"

var pciAddressPattern = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

type discoveryPolicy struct {
	allowed               map[string]struct{}
	excluded              map[string]struct{}
	unsafeMountInspection bool
}

func parsePCIAddressSet(name, value string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if strings.TrimSpace(value) == "" {
		return result, nil
	}
	for raw := range strings.SplitSeq(value, ",") {
		address := strings.ToLower(strings.TrimSpace(raw))
		if address == "" || !pciAddressPattern.MatchString(address) {
			return nil, fmt.Errorf("%s contains invalid PCI address %q", name, raw)
		}
		result[address] = struct{}{}
	}
	return result, nil
}

func currentDiscoveryPolicy() (discoveryPolicy, error) {
	allowed, err := parsePCIAddressSet("NVME_ALLOWED_DEVICES", os.Getenv("NVME_ALLOWED_DEVICES"))
	if err != nil {
		return discoveryPolicy{}, err
	}
	excluded, err := parsePCIAddressSet("NVME_EXCLUDE_DEVICES", os.Getenv("NVME_EXCLUDE_DEVICES"))
	if err != nil {
		return discoveryPolicy{}, err
	}
	unsafe := false
	if value := os.Getenv(unsafeMountInspectionEnv); value != "" {
		unsafe, err = strconv.ParseBool(value)
		if err != nil {
			return discoveryPolicy{}, fmt.Errorf("%s must be a boolean: %w", unsafeMountInspectionEnv, err)
		}
	}
	return discoveryPolicy{allowed: allowed, excluded: excluded, unsafeMountInspection: unsafe}, nil
}

func (p discoveryPolicy) permits(pciAddress string) bool {
	address := strings.ToLower(strings.TrimSpace(pciAddress))
	if _, excluded := p.excluded[address]; excluded {
		return false
	}
	if len(p.allowed) == 0 {
		return true
	}
	_, allowed := p.allowed[address]
	return allowed
}

// DiscoverNVMe scans both the Linux sysfs tree (for kernel-bound NVMe devices)
// and SPDK JSON-RPC (for SPDK-bound NVMe devices), returning a unified list.
func DiscoverNVMe() ([]HardwareNVMe, error) {
	policy, err := currentDiscoveryPolicy()
	if err != nil {
		return nil, err
	}
	var devices []HardwareNVMe
	seenSerials := make(map[string]bool)
	var discoveryErrors []error

	// 1. Scan kernel-bound devices from sysfs
	kernelDevs, err := discoverKernelNVMeWithPolicy(policy)
	if err == nil {
		for _, d := range kernelDevs {
			serial := strings.ToLower(strings.TrimSpace(d.SerialNumber))
			if serial != "" && !seenSerials[serial] {
				devices = append(devices, d)
				seenSerials[serial] = true
			}
		}
	} else {
		discoveryErrors = append(discoveryErrors, fmt.Errorf("kernel NVMe discovery: %w", err))
	}

	// 2. Scan SPDK-bound devices if SPDK is running
	spdkDevs, err := discoverSPDKNVMeWithPolicy(policy)
	if err == nil {
		for _, d := range spdkDevs {
			serial := strings.ToLower(strings.TrimSpace(d.SerialNumber))
			if serial != "" && !seenSerials[serial] {
				devices = append(devices, d)
				seenSerials[serial] = true
			}
		}
	} else {
		discoveryErrors = append(discoveryErrors, fmt.Errorf("SPDK NVMe discovery: %w", err))
	}

	return devices, errors.Join(discoveryErrors...)
}

func discoverKernelNVMe() ([]HardwareNVMe, error) {
	policy, err := currentDiscoveryPolicy()
	if err != nil {
		return nil, err
	}
	return discoverKernelNVMeWithPolicy(policy)
}

func discoverKernelNVMeWithPolicy(policy discoveryPolicy) ([]HardwareNVMe, error) {
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

		if !policy.permits(hwDev.PCIAddress) {
			continue
		}

		// Check for mounted filesystems using lsblk
		mounted, err := inspectDeviceMounts(devName)
		if err != nil {
			if !policy.unsafeMountInspection {
				return nil, fmt.Errorf("inspect mount state for %s: %w", devName, err)
			}
		} else if mounted {
			continue
		}

		// Calculate total bytes from matching namespace blocks
		hwDev.TotalBytes, err = calculateTotalBytes(devName)
		if err != nil {
			return nil, fmt.Errorf("calculate capacity for %s: %w", devName, err)
		}

		devices = append(devices, hwDev)
	}

	return devices, nil
}

func inspectDeviceMounts(devName string) (bool, error) {
	// devName is an NVMe controller (e.g. "nvme1"), which is a character device.
	// lsblk only works on block devices, so we must check the namespaces
	// (e.g. nvme1n1, nvme1n2) found under /sys/class/block.
	entries, err := os.ReadDir(sysClassBlock)
	if err != nil {
		return false, err
	}

	for _, entry := range entries {
		bName := entry.Name()
		if !strings.HasPrefix(bName, devName+"n") {
			continue
		}
		out, err := exec.Command("lsblk", "/dev/"+bName, "-n", "-o", "MOUNTPOINT").Output()
		if err != nil {
			return false, fmt.Errorf("lsblk namespace %s: %w", bName, err)
		}
		lines := strings.SplitSeq(string(out), "\n")
		for line := range lines {
			if strings.TrimSpace(line) != "" {
				return true, nil
			}
		}
	}
	return false, nil
}

func calculateTotalBytes(nvmeName string) (int64, error) {
	var total int64
	entries, err := os.ReadDir(sysClassBlock)
	if err != nil {
		return 0, err
	}

	for _, entry := range entries {
		bName := entry.Name()
		if strings.HasPrefix(bName, nvmeName+"n") {
			sizePath := filepath.Join(sysClassBlock, bName, "size")
			b, err := os.ReadFile(sizePath)
			if err != nil {
				return 0, err
			}
			blocks, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
			if err != nil {
				return 0, err
			}
			total += blocks * 512
		}
	}
	return total, nil
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

func discoverSPDKNVMeWithPolicy(policy discoveryPolicy) ([]HardwareNVMe, error) {
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
		if policy.permits(hwDev.PCIAddress) {
			devices = append(devices, hwDev)
		}
	}

	return devices, nil
}
