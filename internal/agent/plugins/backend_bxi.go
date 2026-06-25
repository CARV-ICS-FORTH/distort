package plugins

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"k8s.io/klog/v2"
)

type BXIBackend struct{}

func init() {
	RegisterTargetBackend(&BXIBackend{})
}

func (b *BXIBackend) Name() string {
	return "bxi"
}

// EnsureBXISPDKRunning verifies nvmf_tgt is running in the background with Portals/BXI environment variables.
func EnsureBXISPDKRunning(coreMask string, portalsPID string) error {
	if err := exec.Command("pidof", "nvmf_tgt").Run(); err != nil {
		klog.Info("Starting nvmf_tgt daemon for BXI/Portals in the background...")
		if coreMask == "" {
			coreMask = "0x1"
		}
		if portalsPID == "" {
			portalsPID = "11"
		}

		// Inject BXI specific environment variables (ROLE and PORTALS_PID) required by the custom DPDK build
		cmdStr := fmt.Sprintf("ROLE=target PORTALS_PID=%s ulimit -l unlimited && /spdk/build/bin/nvmf_tgt -m %s", portalsPID, coreMask)
		cmd := exec.Command("bash", "-c", cmdStr)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start BXI nvmf_tgt: %v", err)
		}
		go func() {
			if err := cmd.Wait(); err != nil {
				klog.Errorf("BXI nvmf_tgt exited with error: %v", err)
			}
		}()

		// Give SPDK/DPDK extra time to initialize the Portals EAL and start JSON-RPC
		time.Sleep(4 * time.Second)
	}
	return nil
}

func (b *BXIBackend) SetupDevice(ctx context.Context, pciAddress string, deviceName string, options map[string]string) error {
	coreMask := options["spdk-core-mask"]
	portalsPID := options["portals-pid"]

	if err := EnsureBXISPDKRunning(coreMask, portalsPID); err != nil {
		return err
	}

	// Unbind the specific NVMe drive from kernel and bind to user-space
	klog.Infof("Running spdk_setup.sh to bind device %s (%s) to user-space for BXI", deviceName, pciAddress)
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

func EnsureBXITransport() error {
	// Create the RDMA transport. Safe if it already exists.
	err := CallSPDKRPC("nvmf_create_transport", nil, "-t", "RDMA", "-u", "8192", "-m", "4", "-c", "0")
	if err != nil {
		klog.V(4).Infof("nvmf_create_transport RDMA returned: %v (might already exist)", err)
	}
	return nil
}

func (b *BXIBackend) ExportVolume(ctx context.Context, volumeName string, blockPath string, portalIP string, portalPort int, options map[string]string) (string, error) {
	nqn := "nqn.2026-02.io.distort:volume-" + volumeName

	// Determine BXI-specific routing from the options map
	bxiNID := options["bxi-nid"]
	if bxiNID == "" {
		bxiNID = portalIP // Fallback to standard IP if no explicit BXI NID is provided via K8s config
	}

	portalsPID := options["portals-pid"]
	if portalsPID == "" {
		portalsPID = "11" // Hard fallback for BXI default env vars if needed
	}

	rdmaPort := fmt.Sprintf("%d", portalPort)
	if rdmaPort == "0" {
		rdmaPort = "4420"
	}

	klog.Infof("Exporting %s as NVMe-oF target %s on IP %s (RDMA Port %s, BXI PID %s) via SPDK", blockPath, nqn, bxiNID, rdmaPort, portalsPID)

	// Check if already exported (subsystem exists)
	var subsystems []struct {
		NQN string `json:"nqn"`
	}
	if err := CallSPDKRPC("nvmf_get_subsystems", &subsystems); err == nil {
		for _, sub := range subsystems {
			if sub.NQN == nqn {
				return nqn, nil // Subsystem already configured
			}
		}
	}

	if err := EnsureBXITransport(); err != nil {
		return "", err
	}

	// 1. Create Subsystem
	err := CallSPDKRPC("nvmf_create_subsystem", nil, nqn, "-a", "-s", "distort", "-d", "SPDK_Controller1")
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
	// Map the transport address (-a) to the BXI NID and use RDMA transport
	err = CallSPDKRPC("nvmf_subsystem_add_listener", nil, nqn, "-t", "rdma", "-a", bxiNID, "-s", rdmaPort)
	if err != nil {
		_ = CallSPDKRPC("nvmf_delete_subsystem", nil, nqn)
		return "", fmt.Errorf("failed to add RDMA listener to subsystem: %w", err)
	}

	return nqn, nil
}

func (b *BXIBackend) UnexportVolume(ctx context.Context, nqn string) error {
	klog.Infof("Unexporting BXI SPDK NVMe-oF target %s", nqn)
	if err := CallSPDKRPC("nvmf_delete_subsystem", nil, nqn); err != nil {
		return fmt.Errorf("failed to delete BXI SPDK subsystem %s: %w", nqn, err)
	}
	return nil
}
