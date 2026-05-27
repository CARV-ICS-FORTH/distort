package agent

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// HardwareNVMe represents a discovered physical NVMe namespace mapped into SPDK.
type HardwareNVMe struct {
	Name         string // e.g. Nvme0n1
	PCIAddress   string // e.g. 0000:01:00.0
	SerialNumber string
	Model        string
	TotalBytes   int64
	NUMANode     int
}

// SpdkBdev represents an SPDK Block Device from bdev_get_bdevs
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

// SpdkNVMeController represents a Controller from bdev_nvme_get_controllers
type SpdkNVMeController struct {
	Name  string `json:"name"`
	Ctrlr struct {
		Model        string `json:"model"`
		SerialNumber string `json:"serial_number"`
	} `json:"ctrlr"`
}

// EnsureSPDK verifies `nvmf_tgt` is running and the physical PCIe NVMe drives are attached to it.
func EnsureSPDK() error {
	// Start nvmf_tgt if not running
	if err := exec.Command("pidof", "nvmf_tgt").Run(); err != nil {
		klog.Info("Starting nvmf_tgt daemon in the background...")
		// Use core 0 with -m 0x1
		cmd := exec.Command("bash", "-c", "ulimit -l unlimited && nvmf_tgt -m 0x1")
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start nvmf_tgt: %v", err)
		}
		go func() {
			if err := cmd.Wait(); err != nil {
				klog.Errorf("nvmf_tgt exited with error: %v", err)
			}
		}()
		// Give SPDK time to initialize DPDK EAL and start the JSON-RPC listener
		time.Sleep(3 * time.Second)
	}

	// Unbind NVMe drives from linux kernel and bind to vfio-pci
	klog.V(4).Info("Running spdk_setup.sh to bind NVMe devices to user-space...")
	exec.Command("modprobe", "uio_pci_generic").Run()
	setupCmd := exec.Command("bash", "-c", "FORCE=1 /opt/spdk/scripts/setup.sh")
	if out, err := setupCmd.CombinedOutput(); err != nil {
		klog.Warningf("spdk_setup.sh might have failed: %v, output: %s", err, string(out))
	}

	return attachLocalNVMe()
}

func attachLocalNVMe() error {
	// Find NVMe PCI addresses using lspci
	cmd := exec.Command("bash", "-c", "lspci -D | grep 'Non-Volatile memory controller' | awk '{print $1}'")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to run lspci: %v", err)
	}

	addrs := strings.Split(strings.TrimSpace(string(out)), "\n")

	// Get currently attached controllers to avoid re-attaching
	var controllers []SpdkNVMeController
	if err := CallSPDKRPC("bdev_nvme_get_controllers", &controllers); err != nil {
		return err
	}

	attached := make(map[string]bool)
	for _, c := range controllers {
		attached[c.Name] = true
	}

	for i, pciAddr := range addrs {
		if pciAddr == "" {
			continue
		}
		name := fmt.Sprintf("Nvme%d", i)
		if attached[name] {
			continue
		}

		klog.Infof("Attaching Physical NVMe controller %s at %s to SPDK", name, pciAddr)
		if err := CallSPDKRPC("bdev_nvme_attach_controller", nil, "-b", name, "-t", "PCIe", "-a", pciAddr); err != nil {
			klog.Warningf("Failed to attach NVMe %s: %v", pciAddr, err)
		}
	}
	return nil
}

// DiscoverNVMe asks SPDK for local NVMe bdevs via JSON-RPC
func DiscoverNVMe() ([]HardwareNVMe, error) {
	if err := EnsureSPDK(); err != nil {
		return nil, err
	}

	var controllers []SpdkNVMeController
	if err := CallSPDKRPC("bdev_nvme_get_controllers", &controllers); err != nil {
		return nil, err
	}

	ctrlrMap := make(map[string]SpdkNVMeController)
	for _, c := range controllers {
		ctrlrMap[c.Name] = c
	}

	var bdevs []SpdkBdev
	if err := CallSPDKRPC("bdev_get_bdevs", &bdevs); err != nil {
		return nil, err
	}

	var devices []HardwareNVMe
	for _, bdev := range bdevs {
		// We only care about physical NVMe bdevs, not logical volumes (lvols)
		if bdev.DriverSpecific == nil || len(bdev.DriverSpecific.NVMe) == 0 {
			continue
		}

		// Match the base controller name (e.g. Nvme0n1 -> Nvme0)
		baseName := strings.Split(bdev.Name, "n")[0]
		_, idx := ctrlrMap[baseName]
		if !idx {
			continue
		}

		hwDev := HardwareNVMe{
			Name:         bdev.Name,
			PCIAddress:   bdev.DriverSpecific.NVMe[0].PCIAddress,
			SerialNumber: strings.TrimSpace(bdev.DriverSpecific.NVMe[0].CtrlrData.SerialNumber),
			Model:        strings.TrimSpace(bdev.DriverSpecific.NVMe[0].CtrlrData.ModelNumber),
			TotalBytes:   bdev.NumBlocks * bdev.BlockSize,
			NUMANode:     -1, // NUMA info typically requires more complex DPDK topology queries
		}
		devices = append(devices, hwDev)
	}

	return devices, nil
}
