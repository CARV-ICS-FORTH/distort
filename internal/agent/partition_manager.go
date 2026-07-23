package agent

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/agent/plugins"
)

const partitionFinalizer = "storage.distort.io/partition-cleanup"

// PartitionManager watches for NVMePartitions assigned to this node and acts on them.
type PartitionManager struct {
	client.Client
	NodeName string
}

func (p *PartitionManager) updatePartitionStatus(ctx context.Context, key types.NamespacedName, updateFn func(status *storagev1alpha1.NVMePartitionStatus)) error {
	var latest storagev1alpha1.NVMePartition
	if err := p.Get(ctx, key, &latest); err != nil {
		return err
	}
	base := latest.DeepCopy()
	updateFn(&latest.Status)
	return p.Status().Patch(ctx, &latest, client.MergeFrom(base))
}

func (p *PartitionManager) updateDeviceStatus(ctx context.Context, key types.NamespacedName, updateFn func(status *storagev1alpha1.NVMeDeviceStatus)) error {
	var latest storagev1alpha1.NVMeDevice
	if err := p.Get(ctx, key, &latest); err != nil {
		return err
	}
	base := latest.DeepCopy()
	updateFn(&latest.Status)
	return p.Status().Patch(ctx, &latest, client.MergeFrom(base))
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

	// Resolve the target backend and volume manager plugins
	targetBackendName := partition.Spec.TargetBackend
	logger.Info("Resolving target backend plugin", "backend", targetBackendName)
	if targetBackendName == "" {
		targetBackendName = "spdk" // default fallback
	}
	backend, err := plugins.GetTargetBackend(targetBackendName)
	if err != nil {
		logger.Error(err, "Failed to resolve target backend plugin", "backend", targetBackendName)
		return ctrl.Result{}, err
	}

	vmName := partition.Spec.VolumeManager
	logger.Info("Resolving volume manager plugin", "vm", vmName)
	if vmName == "" || vmName == "partition" {
		if targetBackendName == "spdk" {
			vmName = "spdk-lvol"
		} else {
			vmName = "parted"
		}
	}
	volManager, err := plugins.GetVolumeManager(vmName)
	if err != nil {
		logger.Error(err, "Failed to resolve volume manager plugin", "manager", vmName)
		return ctrl.Result{}, err
	}

	isMarkedToBeDeleted := partition.GetDeletionTimestamp() != nil
	if isMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(&partition, partitionFinalizer) {
			logger.Info("Cleaning up NVMePartition", "partition", partition.Name)

			// 1. Unexport the NVMe-oF target
			if partition.Status.NQN != "" {
				if err := backend.UnexportVolume(ctx, partition.Status.NQN); err != nil {
					logger.Error(err, "Failed to unexport NVMe target")
				}
			}

			// 2. Remove the partition from the block device
			if partition.Spec.ParentDeviceSerialNumber != "" {
				devices, err := DiscoverNVMe()
				if err == nil {
					var devPath, devBaseName string
					for _, d := range devices {
						if strings.EqualFold(d.SerialNumber, partition.Spec.ParentDeviceSerialNumber) {
							devBaseName = d.Name
							devPath = "/dev/" + d.Name + "n1"
							if targetBackendName == "spdk" {
								devPath = d.Name
							}
							break
						}
					}
					if devBaseName != "" {
						if err := volManager.DeleteVolume(ctx, devPath, devBaseName, partition.Name); err != nil {
							logger.Error(err, "Failed to delete volume from backend")
						}
					}
				} else {
					logger.Error(err, "Failed to discover NVMe devices during teardown")
				}

				// Reset the device's operational mode status if this was the last partition
				var partList storagev1alpha1.NVMePartitionList
				if err := p.List(ctx, &partList); err == nil {
					activeCount := 0
					for _, pt := range partList.Items {
						if pt.Spec.ParentDeviceSerialNumber == partition.Spec.ParentDeviceSerialNumber &&
							pt.Name != partition.Name &&
							pt.DeletionTimestamp.IsZero() {
							activeCount++
						}
					}
					if activeCount == 0 {
						deviceName := p.NodeName + "-" + strings.ToLower(partition.Spec.ParentDeviceSerialNumber)
						err := p.updateDeviceStatus(ctx, types.NamespacedName{Name: deviceName}, func(status *storagev1alpha1.NVMeDeviceStatus) {
							status.ActiveBackend = ""
						})
						if err != nil {
							logger.Error(err, "Failed to clear NVMeDevice active backend")
						} else if targetBackendName == "spdk" {
							// Also release driver back to kernel
							var dev storagev1alpha1.NVMeDevice
							if err := p.Get(ctx, types.NamespacedName{Name: deviceName}, &dev); err == nil {
								_ = plugins.ResetSPDKDevice(dev.Spec.PCIAddress)
							}
						}
					}
				}
			}

			var latest storagev1alpha1.NVMePartition
			if err := p.Get(ctx, req.NamespacedName, &latest); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			base := latest.DeepCopy()
			controllerutil.RemoveFinalizer(&latest, partitionFinalizer)
			err := p.Patch(ctx, &latest, client.MergeFrom(base))
			if err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present
	if !controllerutil.ContainsFinalizer(&partition, partitionFinalizer) {
		base := partition.DeepCopy()
		controllerutil.AddFinalizer(&partition, partitionFinalizer)
		if err := p.Patch(ctx, &partition, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
	}

	// If it's already exported or failed, verify its existence or return
	if partition.Status.State == storagev1alpha1.NVMePartitionStateExported {
		if targetBackendName == "spdk" {
			nqn := partition.Status.NQN
			var subsystems []struct {
				NQN string `json:"nqn"`
			}
			exists := false
			if err := plugins.CallSPDKRPC("nvmf_get_subsystems", &subsystems); err == nil {
				for _, sub := range subsystems {
					if sub.NQN == nqn {
						exists = true
						break
					}
				}
			}
			if exists {
				return ctrl.Result{}, nil
			}
			logger.Info("Partition is marked Exported but subsystem is missing from SPDK. Re-provisioning...", "nqn", nqn)
			// Fall through to re-provision
		} else {
			return ctrl.Result{}, nil
		}
	} else if partition.Status.State == storagev1alpha1.NVMePartitionStateFailed {
		return ctrl.Result{}, nil
	}

	logger.Info("Provisioning partition on local node", "partition", partition.Name, "size", partition.Spec.Size.String())

	if partition.Spec.ParentDeviceSerialNumber == "" {
		err := fmt.Errorf("NVMePartition %s is missing ParentDeviceSerialNumber", partition.Name)
		logger.Error(err, "Cannot provision partition")
		_ = p.updatePartitionStatus(ctx, req.NamespacedName, func(status *storagev1alpha1.NVMePartitionStatus) {
			status.State = storagev1alpha1.NVMePartitionStateFailed
		})
		return ctrl.Result{}, err
	}

	// Fetch parent device details and verify active backend matches
	deviceName := p.NodeName + "-" + strings.ToLower(partition.Spec.ParentDeviceSerialNumber)
	var dev storagev1alpha1.NVMeDevice
	if err := p.Get(ctx, types.NamespacedName{Name: deviceName}, &dev); err != nil {
		logger.Error(err, "Failed to resolve parent NVMeDevice CRD", "deviceName", deviceName)
		_ = p.updatePartitionStatus(ctx, req.NamespacedName, func(status *storagev1alpha1.NVMePartitionStatus) {
			status.State = storagev1alpha1.NVMePartitionStateFailed
		})
		return ctrl.Result{}, err
	}

	if dev.Status.ActiveBackend != "" && dev.Status.ActiveBackend != targetBackendName {
		err := fmt.Errorf("device is currently locked to backend %s, requested %s", dev.Status.ActiveBackend, targetBackendName)
		logger.Error(err, "Mismatched backend for target device")
		_ = p.updatePartitionStatus(ctx, req.NamespacedName, func(status *storagev1alpha1.NVMePartitionStatus) {
			status.State = storagev1alpha1.NVMePartitionStateFailed
		})
		return ctrl.Result{}, err
	}

	// Setup the hardware device driver for the chosen backend
	if err := backend.SetupDevice(ctx, dev.Spec.PCIAddress, dev.Name, partition.Spec.TargetOptions); err != nil {
		logger.Error(err, "Failed to setup physical device driver for backend")
		// Driver binding and backend startup can fail transiently while sysfs,
		// udev, or the SPDK RPC service is still becoming ready. Returning the
		// error lets controller-runtime retry with backoff; marking the partition
		// Failed here would make the early-return above suppress every retry.
		return ctrl.Result{}, err
	}

	// Lock the active backend state on the device
	if dev.Status.ActiveBackend == "" {
		err := p.updateDeviceStatus(ctx, types.NamespacedName{Name: deviceName}, func(status *storagev1alpha1.NVMeDeviceStatus) {
			status.ActiveBackend = targetBackendName
		})
		if err != nil {
			logger.Error(err, "Failed to update NVMeDevice active backend status")
			_ = p.updatePartitionStatus(ctx, req.NamespacedName, func(status *storagev1alpha1.NVMePartitionStatus) {
				status.State = storagev1alpha1.NVMePartitionStateFailed
			})
			return ctrl.Result{}, err
		}
	}

	// Resolve the block device paths
	devices, err := DiscoverNVMe()
	if err != nil {
		logger.Error(err, "Failed to discover NVMe devices")
		return ctrl.Result{}, err
	}

	var devPath, devBaseName string
	for _, d := range devices {
		if strings.EqualFold(d.SerialNumber, partition.Spec.ParentDeviceSerialNumber) {
			devBaseName = d.Name
			devPath = "/dev/" + d.Name + "n1"
			if targetBackendName == "spdk" {
				devPath = d.Name
			}
			break
		}
	}

	if devBaseName == "" {
		err := fmt.Errorf("NVMe device with serial %s not found on host", partition.Spec.ParentDeviceSerialNumber)
		logger.Error(err, "Cannot locate parent device")
		_ = p.updatePartitionStatus(ctx, req.NamespacedName, func(status *storagev1alpha1.NVMePartitionStatus) {
			status.State = storagev1alpha1.NVMePartitionStateFailed
		})
		return ctrl.Result{}, err
	}

	// Setup and slice volume storage
	if err := volManager.SetupStorage(ctx, devPath, devBaseName); err != nil {
		logger.Error(err, "Failed to configure storage slicing")
		_ = p.updatePartitionStatus(ctx, req.NamespacedName, func(status *storagev1alpha1.NVMePartitionStatus) {
			status.State = storagev1alpha1.NVMePartitionStateFailed
		})
		return ctrl.Result{}, err
	}

	blockPath, err := volManager.CreateVolume(ctx, devPath, devBaseName, partition.Name, partition.Spec.Size.Value())
	if err != nil {
		logger.Error(err, "Failed to carve volume from device")
		_ = p.updatePartitionStatus(ctx, req.NamespacedName, func(status *storagev1alpha1.NVMePartitionStatus) {
			status.State = storagev1alpha1.NVMePartitionStateFailed
		})
		return ctrl.Result{}, err
	}

	// Fetch K8s Node IP for the export portal
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
		logger.Error(err, "Failed to fetch node IP for portal")
	}

	portalPort := 4420
	nqn, err := backend.ExportVolume(ctx, partition.Name, blockPath, portalIP, portalPort, partition.Spec.TargetOptions)
	if err != nil {
		logger.Error(err, "Failed to export volume as target")
		_ = p.updatePartitionStatus(ctx, req.NamespacedName, func(status *storagev1alpha1.NVMePartitionStatus) {
			status.State = storagev1alpha1.NVMePartitionStateFailed
		})
		return ctrl.Result{}, err
	}

	// Update partition status
	err = p.updatePartitionStatus(ctx, req.NamespacedName, func(status *storagev1alpha1.NVMePartitionStatus) {
		status.State = storagev1alpha1.NVMePartitionStateExported
		status.NQN = nqn
		status.PortalIP = portalIP
		status.PortalPort = portalPort
	})
	if err != nil {
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
