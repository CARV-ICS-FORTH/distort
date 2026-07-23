package plugins

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

type SPDKBackend struct{}

var deviceSetupMu sync.Mutex

func init() {
	RegisterTargetBackend(&SPDKBackend{})
}

func (s *SPDKBackend) Name() string {
	return "spdk"
}

// EnsureSPDKRunning starts nvmf_tgt when needed and waits until its JSON-RPC
// service is usable. A running process is not necessarily ready to accept RPCs.
func EnsureSPDKRunning(ctx context.Context, coreMask string) error {
	if err := exec.Command("pidof", "nvmf_tgt").Run(); err != nil {
		klog.Info("Starting nvmf_tgt daemon in the background...")
		if coreMask == "" {
			coreMask = "0x1"
		}
		// Start nvmf_tgt with specified core mask
		cmd := exec.Command("bash", "-c", fmt.Sprintf("ulimit -l unlimited && nvmf_tgt -m %s", coreMask))
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start nvmf_tgt: %v", err)
		}
		go func() {
			if err := cmd.Wait(); err != nil {
				klog.Errorf("nvmf_tgt exited with error: %v", err)
			}
		}()
	}

	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		var methods []string
		if err := CallSPDKRPC("rpc_get_methods", &methods); err == nil {
			return nil
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for nvmf_tgt JSON-RPC readiness: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("nvmf_tgt JSON-RPC was not ready after 30 seconds: %w", lastErr)
}

func (s *SPDKBackend) SetupDevice(ctx context.Context, pciAddress string, deviceName string, options map[string]string) error {
	coreMask := options["spdk-core-mask"]
	if err := EnsureSPDKRunning(ctx, coreMask); err != nil {
		return err
	}

	// Verify if already attached to avoid re-attaching
	attached, err := isNVMeControllerAttached(deviceName)
	if err == nil && attached {
		return nil
	}

	// setup.sh changes host-wide PCI driver state. Serialize it with reset and
	// retry because udev/sysfs may briefly report the device while its driver is
	// still transitioning.
	deviceSetupMu.Lock()
	defer deviceSetupMu.Unlock()

	attached, err = isNVMeControllerAttached(deviceName)
	if err == nil && attached {
		return nil
	}

	klog.Infof("Running spdk_setup.sh to bind device %s (%s) to user-space", deviceName, pciAddress)
	_ = exec.CommandContext(ctx, "modprobe", "uio_pci_generic").Run()
	if err := runSPDKSetup(ctx, pciAddress); err != nil {
		return err
	}

	klog.Infof("Attaching Physical NVMe controller %s at %s to SPDK", deviceName, pciAddress)
	if err := CallSPDKRPC("bdev_nvme_attach_controller", nil, "-b", deviceName, "-t", "PCIe", "-a", pciAddress); err != nil {
		// An earlier timed-out RPC may have completed on the server.
		if attached, checkErr := isNVMeControllerAttached(deviceName); checkErr == nil && attached {
			return nil
		}
		return fmt.Errorf("failed to attach NVMe %s to SPDK: %w", pciAddress, err)
	}

	return nil
}

func isNVMeControllerAttached(deviceName string) (bool, error) {
	var controllers []struct {
		Name string `json:"name"`
	}
	if err := CallSPDKRPC("bdev_nvme_get_controllers", &controllers); err != nil {
		return false, err
	}
	for _, controller := range controllers {
		if controller.Name == deviceName {
			return true, nil
		}
	}
	return false, nil
}

func runSPDKSetup(ctx context.Context, pciAddress string) error {
	var lastErr error
	var lastOutput string
	for attempt := 1; attempt <= 5; attempt++ {
		cmd := exec.CommandContext(ctx, "/opt/spdk/scripts/setup.sh")
		cmd.Env = append(cmd.Environ(), "FORCE=1", "PCI_ALLOWED="+pciAddress)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("spdk_setup.sh interrupted: %w", ctx.Err())
		}

		lastErr = err
		lastOutput = strings.TrimSpace(string(out))
		klog.Warningf("spdk_setup.sh attempt %d/5 failed for %s: %v, output: %s", attempt, pciAddress, err, lastOutput)

		select {
		case <-ctx.Done():
			return fmt.Errorf("spdk_setup.sh interrupted: %w", ctx.Err())
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
	return fmt.Errorf("spdk_setup.sh failed after 5 attempts: %v, output: %s", lastErr, lastOutput)
}

func EnsureNVMeTransport() error {
	// Create RDMA transport. Safe if it already exists.
	err := CallSPDKRPC("nvmf_create_transport", nil, "-t", "RDMA", "-u", "8192", "-i", "131072", "-c", "8192")
	if err != nil {
		klog.V(4).Infof("nvmf_create_transport RDMA returned: %v (might already exist)", err)
	}
	return nil
}

func (s *SPDKBackend) ExportVolume(ctx context.Context, volumeName string, blockPath string, portalIP string, portalPort int, options map[string]string) (string, error) {
	nqn := "nqn.2026-02.io.distort:volume-" + volumeName
	klog.Infof("Exporting %s as NVMe-oF target %s on %s:%d via SPDK", blockPath, nqn, portalIP, portalPort)

	// Check if already exported (subsystem exists)
	var subsystems []struct {
		NQN string `json:"nqn"`
	}
	if err := CallSPDKRPC("nvmf_get_subsystems", &subsystems); err == nil {
		for _, sub := range subsystems {
			if sub.NQN == nqn {
				return nqn, nil
			}
		}
	}

	if err := EnsureNVMeTransport(); err != nil {
		return "", err
	}

	// 1. Create Subsystem
	err := CallSPDKRPC("nvmf_create_subsystem", nil, nqn, "-a", "-s", "distort")
	if err != nil {
		return "", fmt.Errorf("failed to create SPDK subsystem %s: %w", nqn, err)
	}

	// 2. Add Namespace
	err = CallSPDKRPC("nvmf_subsystem_add_ns", nil, nqn, blockPath)
	if err != nil {
		_ = CallSPDKRPC("nvmf_delete_subsystem", nil, nqn)
		return "", fmt.Errorf("failed to add namespace %s to subsystem: %w", blockPath, err)
	}

	// 3. Add Listener
	err = CallSPDKRPC("nvmf_subsystem_add_listener", nil, nqn, "-t", "RDMA", "-a", portalIP, "-s", fmt.Sprintf("%d", portalPort))
	if err != nil {
		_ = CallSPDKRPC("nvmf_delete_subsystem", nil, nqn)
		return "", fmt.Errorf("failed to add RDMA listener to subsystem: %w", err)
	}

	return nqn, nil
}

func (s *SPDKBackend) UnexportVolume(ctx context.Context, nqn string) error {
	klog.Infof("Unexporting SPDK NVMe-oF target %s", nqn)
	if err := CallSPDKRPC("nvmf_delete_subsystem", nil, nqn); err != nil {
		return fmt.Errorf("failed to delete SPDK subsystem %s: %w", nqn, err)
	}
	return nil
}

// ResetSPDKDevice unbinds the SPDK driver from the PCI device and binds it back to the kernel.
func ResetSPDKDevice(pciAddress string) error {
	deviceSetupMu.Lock()
	defer deviceSetupMu.Unlock()

	klog.Infof("Resetting device %s back to kernel nvme driver", pciAddress)
	setupCmd := exec.Command("/opt/spdk/scripts/setup.sh", "reset")
	setupCmd.Env = append(setupCmd.Environ(), "FORCE=1", "PCI_ALLOWED="+pciAddress)
	if out, err := setupCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("spdk_setup.sh reset failed: %v, output: %s", err, string(out))
	}
	return nil
}
