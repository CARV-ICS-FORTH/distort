package agent

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"distort/internal/agent/plugins"
	"distort/internal/capacity"
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

// NVMeDiscoveryError preserves health for each independent discovery source
// while still allowing callers to consume safe partial results.
type NVMeDiscoveryError struct {
	Kernel error
	SPDK   error
}

func (e *NVMeDiscoveryError) Error() string {
	var failures []error
	if e.Kernel != nil {
		failures = append(failures, fmt.Errorf("kernel NVMe discovery: %w", e.Kernel))
	}
	if e.SPDK != nil {
		failures = append(failures, fmt.Errorf("SPDK NVMe discovery: %w", e.SPDK))
	}
	if len(failures) == 0 {
		return "NVMe discovery degraded"
	}
	return errors.Join(failures...).Error()
}

func (e *NVMeDiscoveryError) Unwrap() []error {
	failures := make([]error, 0, 2)
	if e.Kernel != nil {
		failures = append(failures, e.Kernel)
	}
	if e.SPDK != nil {
		failures = append(failures, e.SPDK)
	}
	return failures
}

func validateHardwareNVMe(device HardwareNVMe) error {
	if strings.TrimSpace(device.Name) == "" {
		return errors.New("device name is empty")
	}
	if strings.TrimSpace(device.SerialNumber) == "" {
		return errors.New("serial number is empty")
	}
	pciAddress := strings.ToLower(strings.TrimSpace(device.PCIAddress))
	if !pciAddressPattern.MatchString(pciAddress) {
		return fmt.Errorf("PCI address %q is invalid", device.PCIAddress)
	}
	if device.TotalBytes <= 0 {
		return fmt.Errorf("capacity must be positive, got %d", device.TotalBytes)
	}
	return nil
}

var sysClassNVMe = "/sys/class/nvme"
var sysClassBlock = "/sys/class/block"

const (
	unsafeMountInspectionEnv = "NVME_ALLOW_UNSAFE_MOUNT_INSPECTION"
	primaryNamespaceID       = int64(1)
	managedCapacityPercent   = int64(99)
)

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
	// 1. Scan kernel-bound devices from sysfs
	kernelDevs, kernelErr := discoverKernelNVMeWithPolicy(policy)
	for _, d := range kernelDevs {
		serial := strings.ToLower(strings.TrimSpace(d.SerialNumber))
		if serial != "" && !seenSerials[serial] {
			devices = append(devices, d)
			seenSerials[serial] = true
		}
	}

	// 2. Scan SPDK-bound devices if SPDK is running
	spdkDevs, spdkErr := discoverSPDKNVMeWithPolicy(policy)
	for _, d := range spdkDevs {
		serial := strings.ToLower(strings.TrimSpace(d.SerialNumber))
		if serial != "" && !seenSerials[serial] {
			devices = append(devices, d)
			seenSerials[serial] = true
		}
	}

	if kernelErr != nil || spdkErr != nil {
		return devices, &NVMeDiscoveryError{Kernel: kernelErr, SPDK: spdkErr}
	}
	return devices, nil
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
	var discoveryErrors []error

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

		// Serial and PCI identity are mandatory. Skipping either would create an
		// object that cannot be claimed or provisioned safely.
		serial, err := os.ReadFile(filepath.Join(devPath, "serial"))
		if err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("read serial for %s: %w", devName, err))
			continue
		}
		hwDev.SerialNumber = strings.TrimSpace(string(serial))

		// Read NUMA Node
		if b, err := os.ReadFile(filepath.Join(devPath, "numa_node")); err == nil {
			if parsed, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				hwDev.NUMANode = parsed
			}
		}

		// Determine PCI Address by resolving the device symlink
		link, err := os.Readlink(filepath.Join(devPath, "device"))
		if err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("resolve PCI address for %s: %w", devName, err))
			continue
		}
		hwDev.PCIAddress = strings.ToLower(filepath.Base(link))

		if !policy.permits(hwDev.PCIAddress) {
			continue
		}

		// Check for mounted filesystems using lsblk
		mounted, err := inspectDeviceMounts(devName)
		if err != nil {
			if !policy.unsafeMountInspection {
				discoveryErrors = append(discoveryErrors, fmt.Errorf("inspect mount state for %s: %w", devName, err))
				continue
			}
		} else if mounted {
			continue
		}

		// DISTORT manages namespace 1 under both kernel and SPDK backends.
		// Advertising only that namespace keeps capacity stable across rebinding.
		hwDev.TotalBytes, err = calculatePrimaryNamespaceBytes(devName)
		if err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("calculate capacity for %s: %w", devName, err))
			continue
		}
		if err := validateHardwareNVMe(hwDev); err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("validate %s: %w", devName, err))
			continue
		}

		devices = append(devices, hwDev)
	}

	return devices, errors.Join(discoveryErrors...)
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

func checkedCapacityBytes(name string, numBlocks, blockSize int64) (int64, error) {
	if numBlocks <= 0 || blockSize <= 0 {
		return 0, fmt.Errorf("%s has invalid capacity metadata: %d blocks of %d bytes", name, numBlocks, blockSize)
	}
	if numBlocks > math.MaxInt64/blockSize {
		return 0, fmt.Errorf("%s capacity overflows int64", name)
	}
	return numBlocks * blockSize, nil
}

func allocatableNamespaceBytes(name string, rawBytes int64) (int64, error) {
	if rawBytes <= 0 {
		return 0, fmt.Errorf("%s has invalid raw capacity %d", name, rawBytes)
	}
	// Reserve one percent for the GPT boundaries or SPDK blobstore metadata,
	// then expose only whole allocation units. SPDK's pinned default metadata
	// ratio is below one percent; the explicit margin keeps pre-transition
	// placement conservative if those internals change.
	usableBytes := rawBytes/100*managedCapacityPercent +
		rawBytes%100*managedCapacityPercent/100
	usableBytes = usableBytes / capacity.AllocationUnitBytes * capacity.AllocationUnitBytes
	if usableBytes <= 0 {
		return 0, fmt.Errorf("%s has no allocatable capacity after metadata reservation", name)
	}
	return usableBytes, nil
}

func calculatePrimaryNamespaceBytes(nvmeName string) (int64, error) {
	namespaceName := fmt.Sprintf("%sn%d", nvmeName, primaryNamespaceID)
	b, err := os.ReadFile(filepath.Join(sysClassBlock, namespaceName, "size"))
	if err != nil {
		return 0, err
	}
	blocks, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, err
	}
	rawBytes, err := checkedCapacityBytes(namespaceName, blocks, 512)
	if err != nil {
		return 0, err
	}
	return allocatableNamespaceBytes(namespaceName, rawBytes)
}

type spdkNVMeControllerData struct {
	ModelNumber  string `json:"model_number"`
	SerialNumber string `json:"serial_number"`
}

type spdkNVMeNamespaceData struct {
	ID int64 `json:"id"`
}

type spdkNVMeNamespace struct {
	PCIAddress string                 `json:"pci_address"`
	CtrlrData  spdkNVMeControllerData `json:"ctrlr_data"`
	NSData     spdkNVMeNamespaceData  `json:"ns_data"`
}

type spdkDriverSpecific struct {
	NVMe []spdkNVMeNamespace `json:"nvme"`
}

type SpdkBdev struct {
	Name           string              `json:"name"`
	ProductName    string              `json:"product_name"`
	BlockSize      int64               `json:"block_size"`
	NumBlocks      int64               `json:"num_blocks"`
	UUID           string              `json:"uuid"`
	DriverSpecific *spdkDriverSpecific `json:"driver_specific"`
}

func discoverSPDKNVMeWithPolicy(policy discoveryPolicy) ([]HardwareNVMe, error) {
	if err := exec.Command("pidof", "nvmf_tgt").Run(); err != nil {
		return nil, nil // SPDK not running
	}

	var bdevs []SpdkBdev
	if err := plugins.CallSPDKRPC("bdev_get_bdevs", &bdevs); err != nil {
		return nil, err
	}

	return hardwareFromSPDKBdevs(bdevs, policy)
}

func hardwareFromSPDKBdevs(bdevs []SpdkBdev, policy discoveryPolicy) ([]HardwareNVMe, error) {
	devices := make([]HardwareNVMe, 0, len(bdevs))
	var discoveryErrors []error
	for _, bdev := range bdevs {
		if bdev.DriverSpecific == nil || len(bdev.DriverSpecific.NVMe) == 0 {
			continue
		}
		namespace := bdev.DriverSpecific.NVMe[0]
		if namespace.NSData.ID == 0 {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("SPDK bdev %s is missing its NVMe namespace ID", bdev.Name))
			continue
		}
		if namespace.NSData.ID != primaryNamespaceID {
			continue
		}
		capacityBytes, err := checkedCapacityBytes(bdev.Name, bdev.NumBlocks, bdev.BlockSize)
		if err != nil {
			discoveryErrors = append(discoveryErrors, err)
			continue
		}
		capacityBytes, err = allocatableNamespaceBytes(bdev.Name, capacityBytes)
		if err != nil {
			discoveryErrors = append(discoveryErrors, err)
			continue
		}

		hwDev := HardwareNVMe{
			Name:         bdev.Name,
			PCIAddress:   namespace.PCIAddress,
			SerialNumber: strings.TrimSpace(namespace.CtrlrData.SerialNumber),
			Model:        strings.TrimSpace(namespace.CtrlrData.ModelNumber),
			TotalBytes:   capacityBytes,
			NUMANode:     -1,
		}
		hwDev.PCIAddress = strings.ToLower(strings.TrimSpace(hwDev.PCIAddress))
		if !policy.permits(hwDev.PCIAddress) {
			continue
		}
		if err := validateHardwareNVMe(hwDev); err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("validate SPDK bdev %s: %w", bdev.Name, err))
			continue
		}
		devices = append(devices, hwDev)
	}

	return devices, errors.Join(discoveryErrors...)
}
