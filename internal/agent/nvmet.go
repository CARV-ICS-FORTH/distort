package agent

import (
	"fmt"

	"k8s.io/klog/v2"
)

// EnsureNVMeTransport creates the RDMA transport if it doesn't exist
func EnsureNVMeTransport() error {
	// Try to create RDMA transport. It's safe if it already exists, so we just log the warning.
	err := CallSPDKRPC("nvmf_create_transport", nil, "-t", "RDMA", "-u", "8192", "-i", "131072", "-c", "8192")
	if err != nil {
		klog.V(4).Infof("nvmf_create_transport RDMA returned: %v (might already exist)", err)
	}
	return nil
}

// ExportNVMeTarget sets up the SPDK NVMe-oF target for the given bdev.
func ExportNVMeTarget(nqn, blockDevice, portalIP string, portID, portalPort int) error {
	klog.Infof("Exporting %s as NVMe-oF target %s on %s:%d via SPDK", blockDevice, nqn, portalIP, portalPort)

	if err := EnsureNVMeTransport(); err != nil {
		return err
	}

	// 1. Create Subsystem
	err := CallSPDKRPC("nvmf_create_subsystem", nil, nqn, "-a", "-s", "distort")
	if err != nil {
		return fmt.Errorf("failed to create SPDK subsystem %s: %w", nqn, err)
	}

	// 2. Add Namespace (the blockDevice is the LVOL bdev name, e.g. "lvs_Nvme0/part1")
	err = CallSPDKRPC("nvmf_subsystem_add_ns", nil, nqn, blockDevice)
	if err != nil {
		return fmt.Errorf("failed to add namespace %s to subsystem: %w", blockDevice, err)
	}

	// 3. Add Listener
	err = CallSPDKRPC("nvmf_subsystem_add_listener", nil, nqn, "-t", "RDMA", "-a", portalIP, "-s", fmt.Sprintf("%d", portalPort))
	if err != nil {
		return fmt.Errorf("failed to add RDMA listener to subsystem: %w", err)
	}

	return nil
}

// UnexportNVMeTarget tears down the SPDK NVMe-oF subsystem.
func UnexportNVMeTarget(nqn string, portID int) error {
	klog.Infof("Unexporting SPDK NVMe-oF target %s", nqn)

	err := CallSPDKRPC("nvmf_delete_subsystem", nil, nqn)
	if err != nil {
		return fmt.Errorf("failed to delete SPDK subsystem %s: %w", nqn, err)
	}

	return nil
}
