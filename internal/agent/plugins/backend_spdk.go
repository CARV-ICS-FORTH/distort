package plugins

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"k8s.io/klog/v2"
)

type SPDKBackend struct{}

func init() {
	RegisterTargetBackend(&SPDKBackend{})
}

func (s *SPDKBackend) Name() string {
	return "spdk"
}

// EnsureSPDKRunning verifies nvmf_tgt is running in the background.
func EnsureSPDKRunning(coreMask string) error {
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
		// Give SPDK time to initialize DPDK EAL and start JSON-RPC
		time.Sleep(3 * time.Second)
	}
	return nil
}

func (s *SPDKBackend) SetupDevice(ctx context.Context, pciAddress string, deviceName string, options map[string]string) error {
	coreMask := options["spdk-core-mask"]
	if err := EnsureSPDKRunning(coreMask); err != nil {
		return err
	}

	// Unbind the specific NVMe drive from kernel and bind to vfio-pci/uio_pci_generic using setup.sh
	klog.Infof("Running spdk_setup.sh to bind device %s (%s) to user-space", deviceName, pciAddress)
	_ = exec.Command("modprobe", "uio_pci_generic").Run()
	setupCmd := exec.Command("bash", "-c", fmt.Sprintf("FORCE=1 PCI_ALLOWED=%s /opt/spdk/scripts/setup.sh", pciAddress))
	if out, err := setupCmd.CombinedOutput(); err != nil {
		klog.Warningf("spdk_setup.sh failed or warned: %v, output: %s", err, string(out))
	}

	// Verify if already attached to avoid re-attaching
	var controllers []struct {
		Name string `json:"name"`
	}
	if err := CallSPDKRPC("bdev_nvme_get_controllers", &controllers); err == nil {
		for _, c := range controllers {
			if c.Name == deviceName {
				return nil // already attached
			}
		}
	}

	klog.Infof("Attaching Physical NVMe controller %s at %s to SPDK", deviceName, pciAddress)
	if err := CallSPDKRPC("bdev_nvme_attach_controller", nil, "-b", deviceName, "-t", "PCIe", "-a", pciAddress); err != nil {
		return fmt.Errorf("failed to attach NVMe %s to SPDK: %w", pciAddress, err)
	}

	return nil
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
	klog.Infof("Resetting device %s back to kernel nvme driver", pciAddress)
	setupCmd := exec.Command("bash", "-c", fmt.Sprintf("FORCE=1 PCI_ALLOWED=%s /opt/spdk/scripts/setup.sh reset", pciAddress))
	if out, err := setupCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("spdk_setup.sh reset failed: %v, output: %s", err, string(out))
	}
	return nil
}
