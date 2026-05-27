package agent

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "distort/api/v1alpha1"
)

const partitionFinalizer = "storage.distort.io/partition-cleanup"

// PartitionManager watches for NVMePartitions assigned to this node and acts on them.
type PartitionManager struct {
	client.Client
	NodeName string
}

// Reconcile handles partition configuration when an admin/controller assigns them to this node.
func (p *PartitionManager) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var partition storagev1alpha1.NVMePartition
	if err := p.Get(ctx, req.NamespacedName, &partition); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Double check we only process partitions for our own node
	if partition.Spec.NodeName != p.NodeName {
		return ctrl.Result{}, nil
	}

	isMarkedToBeDeleted := partition.GetDeletionTimestamp() != nil
	if isMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(&partition, partitionFinalizer) {
			logger.Info("Cleaning up NVMePartition", "partition", partition.Name)

			// 1. Unexport the NVMe-oF target
			if partition.Status.NQN != "" {
				if err := UnexportNVMeTarget(partition.Status.NQN, 1); err != nil {
					logger.Error(err, "Failed to unexport NVMe target")
					// In theory we might return err to retry, but let's press on for teardown
				}
			}

			// 2. Remove the SPDK Logical Volume from the Lvstore
			if partition.Spec.ParentDeviceSerialNumber != "" {
				devices, err := DiscoverNVMe()
				if err == nil {
					for _, d := range devices {
						if d.SerialNumber == partition.Spec.ParentDeviceSerialNumber {
							storeName := "lvs_" + d.Name
							lvolBdevName := storeName + "/" + partition.Name
							if err := DeleteLvol(lvolBdevName); err != nil {
								logger.Error(err, "Failed to remove SPDK Lvol")
							}
							break
						}
					}
				} else {
					logger.Error(err, "Failed to discover NVMe devices to remove Lvol")
				}
			}
			controllerutil.RemoveFinalizer(&partition, partitionFinalizer)
			if err := p.Update(ctx, &partition); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present
	if !controllerutil.ContainsFinalizer(&partition, partitionFinalizer) {
		controllerutil.AddFinalizer(&partition, partitionFinalizer)
		if err := p.Update(ctx, &partition); err != nil {
			return ctrl.Result{}, err
		}
	}

	// If it's already exported or failed, do nothing for now (simplified)
	if partition.Status.State == storagev1alpha1.NVMePartitionStateExported || partition.Status.State == storagev1alpha1.NVMePartitionStateFailed {
		return ctrl.Result{}, nil
	}

	logger.Info("Provisioning partition on local node", "partition", partition.Name, "size", partition.Spec.Size.String())

	// ============================================
	// 1. Execute SPDK RPC to create the Logical Volume
	// ============================================
	if partition.Spec.ParentDeviceSerialNumber == "" {
		err := fmt.Errorf("NVMePartition %s is missing ParentDeviceSerialNumber", partition.Name)
		logger.Error(err, "Cannot provision partition")
		partition.Status.State = storagev1alpha1.NVMePartitionStateFailed
		_ = p.Status().Update(ctx, &partition)
		return ctrl.Result{}, err
	}

	devices, err := DiscoverNVMe()
	if err != nil {
		logger.Error(err, "Failed to discover NVMe devices to find parent")
		return ctrl.Result{}, err
	}

	var parentDevice string
	for _, d := range devices {
		if d.SerialNumber == partition.Spec.ParentDeviceSerialNumber {
			parentDevice = d.Name
			break
		}
	}

	if parentDevice == "" {
		err := fmt.Errorf("NVMe device with serial %s not found on node", partition.Spec.ParentDeviceSerialNumber)
		logger.Error(err, "Failed to resolve parent device")
		partition.Status.State = storagev1alpha1.NVMePartitionStateFailed
		_ = p.Status().Update(ctx, &partition)
		return ctrl.Result{}, err
	}

	storeName := "lvs_" + parentDevice
	if err := EnsureLvstore(parentDevice, storeName); err != nil {
		logger.Error(err, "Failed to ensure SPDK Lvstore exists")
		partition.Status.State = storagev1alpha1.NVMePartitionStateFailed
		_ = p.Status().Update(ctx, &partition)
		return ctrl.Result{}, err
	}

	sizeMB := partition.Spec.Size.Value() / (1024 * 1024)
	blockPath, err := CreateLvol(storeName, partition.Name, sizeMB)
	if err != nil {
		logger.Error(err, "Failed to create SPDK Lvol")
		partition.Status.State = storagev1alpha1.NVMePartitionStateFailed
		_ = p.Status().Update(ctx, &partition)
		return ctrl.Result{}, err
	}

	// ============================================
	// 2. Configure `nvmetcli` (configfs) to export via RDMA.
	// ============================================
	nqn := "nqn.2026-02.io.distort:volume-" + partition.Name

	// Fetch real K8s Node IP for the export portal
	portalIP := "127.0.0.1"
	k8sNode := &corev1.Node{}
	if err := p.Get(ctx, types.NamespacedName{Name: p.NodeName}, k8sNode); err == nil {
		for _, addr := range k8sNode.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				portalIP = addr.Address
				break
			}
		}
	} else {
		logger.Error(err, "Failed to fetch node IP for NVMe-oF portal")
	}

	portalPort := 4420
	portID := 1

	if err := ExportNVMeTarget(nqn, blockPath, portalIP, portID, portalPort); err != nil {
		logger.Error(err, "Failed to export NVMe-oF target")
		partition.Status.State = storagev1alpha1.NVMePartitionStateFailed
		_ = p.Status().Update(ctx, &partition)
		return ctrl.Result{}, err
	}

	// ============================================
	// 3. Update the CRD Status
	// ============================================
	partition.Status.State = storagev1alpha1.NVMePartitionStateExported
	partition.Status.NQN = nqn
	partition.Status.PortalIP = portalIP
	partition.Status.PortalPort = portalPort

	if err := p.Status().Update(ctx, &partition); err != nil {
		logger.Error(err, "Failed to update partition status")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully provisioned and exported partition", "partition", partition.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (p *PartitionManager) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.NVMePartition{}).
		Named("agent-partition-manager").
		Complete(p)
}
