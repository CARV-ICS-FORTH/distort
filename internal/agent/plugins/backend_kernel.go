package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/klog/v2"

	"distort/internal/volumeidentity"

	"distort/internal/storageoptions"
)

type KernelBackend struct{}

func init() {
	RegisterTargetBackend(&KernelBackend{})
}

func (k *KernelBackend) Name() string {
	return "kernel"
}

func (k *KernelBackend) SetupDevice(ctx context.Context, pciAddress string, deviceName string, options map[string]string) error {
	if err := storageoptions.Validate(k.Name(), options); err != nil {
		return err
	}
	klog.Infof("Ensuring device %s (%s) is bound to kernel nvme driver", deviceName, pciAddress)
	if err := ResetSPDKDevice(pciAddress); err != nil {
		klog.Warningf("spdk_setup.sh reset failed or warned: %v", err)
	}
	return nil
}

const nvmetPath = "/sys/kernel/config/nvmet"

func isMountPoint(target string) bool {
	target = filepath.Clean(target)
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		data, err = os.ReadFile("/proc/mounts")
		if err != nil {
			return false
		}
	}
	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 5 {
			if filepath.Clean(fields[4]) == target {
				return true
			}
		}
	}
	return false
}

func (k *KernelBackend) ExportVolume(ctx context.Context, volumeName string, blockPath string, portalIP string, portalPort int, options map[string]string) (string, error) {
	nqn := volumeidentity.NQN(volumeName)
	subsysPath := filepath.Join(nvmetPath, "subsystems", nqn)
	if _, err := os.Stat(subsysPath); err == nil {
		klog.Infof("NVMe-oF target %s already exported via Kernel ConfigFS", nqn)
		return nqn, nil
	}

	klog.Infof("Exporting %s as NVMe-oF target %s on %s:%d via Kernel ConfigFS", blockPath, nqn, portalIP, portalPort)

	// Ensure configfs is mounted (nvmet module loaded)
	if _, err := os.Stat(nvmetPath); err != nil {
		klog.Info("Loading nvmet and nvmet-rdma modules...")
		_ = exec.Command("modprobe", "nvmet").Run()
		_ = exec.Command("modprobe", "nvmet-rdma").Run()
		if !isMountPoint("/sys/kernel/config") {
			klog.Info("Mounting configfs on /sys/kernel/config...")
			_ = exec.Command("mount", "-t", "configfs", "none", "/sys/kernel/config").Run()
		}
	}

	// 1. Create Subsystem
	subsysPath = filepath.Join(nvmetPath, "subsystems", nqn)
	if err := os.Mkdir(subsysPath, 0755); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("failed to create subsystem %s: %w", subsysPath, err)
	}

	if err := os.WriteFile(filepath.Join(subsysPath, "attr_allow_any_host"), []byte("1"), 0644); err != nil {
		return "", fmt.Errorf("failed to allow any host: %w", err)
	}

	// 2. Create Namespace (NSID 1)
	nsPath := filepath.Join(subsysPath, "namespaces", "1")
	if err := os.Mkdir(nsPath, 0755); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("failed to create namespace: %w", err)
	}

	if err := os.WriteFile(filepath.Join(nsPath, "device_path"), []byte(blockPath), 0644); err != nil {
		return "", fmt.Errorf("failed to set device_path: %w", err)
	}

	if err := os.WriteFile(filepath.Join(nsPath, "enable"), []byte("1"), 0644); err != nil {
		return "", fmt.Errorf("failed to enable namespace: %w", err)
	}

	// 3. Create Port
	portID := 1
	portPath := filepath.Join(nvmetPath, "ports", strconv.Itoa(portID))
	if err := os.Mkdir(portPath, 0755); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("failed to create port: %w", err)
	}

	if err := os.WriteFile(filepath.Join(portPath, "addr_adrfam"), []byte("ipv4"), 0644); err != nil {
		return "", fmt.Errorf("failed to set addr_adrfam: %w", err)
	}
	if err := os.WriteFile(filepath.Join(portPath, "addr_trtype"), []byte("rdma"), 0644); err != nil {
		return "", fmt.Errorf("failed to set addr_trtype: %w", err)
	}
	if err := os.WriteFile(filepath.Join(portPath, "addr_trsvcid"), []byte(strconv.Itoa(portalPort)), 0644); err != nil {
		return "", fmt.Errorf("failed to set addr_trsvcid: %w", err)
	}
	if err := os.WriteFile(filepath.Join(portPath, "addr_traddr"), []byte(portalIP), 0644); err != nil {
		return "", fmt.Errorf("failed to set addr_traddr: %w", err)
	}

	// 4. Link Subsystem to Port
	linkPath := filepath.Join(portPath, "subsystems", nqn)
	err := os.Symlink(subsysPath, linkPath)
	if err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("failed to link subsystem to port: %w", err)
	}

	return nqn, nil
}

func (k *KernelBackend) UnexportVolume(ctx context.Context, nqn string) error {
	klog.Infof("Unexporting Kernel NVMe-oF target %s", nqn)

	portID := 1
	portPath := filepath.Join(nvmetPath, "ports", strconv.Itoa(portID))
	linkPath := filepath.Join(portPath, "subsystems", nqn)

	// Remove link
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove port symlink: %w", err)
	}

	subsysPath := filepath.Join(nvmetPath, "subsystems", nqn)
	nsPath := filepath.Join(subsysPath, "namespaces", "1")

	// Disable and remove namespace
	if err := os.WriteFile(filepath.Join(nsPath, "enable"), []byte("0"), 0644); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to disable namespace: %w", err)
	}

	if err := os.Remove(nsPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove namespace: %w", err)
	}

	// Remove subsystem
	if err := os.Remove(subsysPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove subsystem: %w", err)
	}

	return nil
}
