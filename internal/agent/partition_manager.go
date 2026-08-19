package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/agent/plugins"
	"distort/internal/capacity"
	"distort/internal/volumeidentity"
)

const partitionFinalizer = "storage.distort.io/partition-cleanup"

const claimAuthorizationCondition = "ClaimAuthorized"

func identitiesForPartition(partition *storagev1alpha1.NVMePartition) (externalID, volumeID string) {
	derived, deriveErr := volumeidentity.New(partition.Namespace, partition.Name, partition.UID)
	externalID = partition.Status.ExternalID
	if externalID == "" {
		// Preserve the backend name of volumes exported by an older release.
		if legacyID, ok := volumeidentity.ExternalIDFromNQN(partition.Status.NQN); ok {
			externalID = legacyID
		} else if deriveErr == nil {
			externalID = derived.ExternalID
		} else {
			// Kubernetes always assigns a UID. This fallback only supports unit-test
			// fixtures and safe cleanup of malformed legacy objects.
			externalID = partition.Name
		}
	}
	volumeID = partition.Status.VolumeID
	if volumeID == "" && deriveErr == nil {
		volumeID = derived.VolumeHandle
	}
	return externalID, volumeID
}

func backendVolumeIdentity(status storagev1alpha1.NVMePartitionStatus) plugins.VolumeIdentity {
	return plugins.VolumeIdentity{
		BackendVolumeID: status.BackendVolumeID,
		CapacityBytes:   status.AllocatedCapacity.Value(),
		BaseBdev:        status.SPDKBaseBdev,
		VolumeStoreName: status.SPDKLvstoreName,
		VolumeStoreUUID: status.SPDKLvstoreUUID,
		VolumeName:      status.SPDKLvolName,
		VolumeUUID:      status.SPDKLvolUUID,
	}
}

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

func claimReferencesEqual(a, b *storagev1alpha1.NVMeDeviceClaimReference) bool {
	return a != nil && b != nil &&
		a.Namespace == b.Namespace && a.Name == b.Name && a.UID == b.UID
}

func (p *PartitionManager) verifyProvisioningAuthorization(ctx context.Context, partition *storagev1alpha1.NVMePartition) (*storagev1alpha1.NVMeDevice, error) {
	claimRef := partition.Spec.ClaimRef
	if claimRef == nil || claimRef.Namespace == "" || claimRef.Name == "" || claimRef.UID == "" {
		return nil, fmt.Errorf("NVMePartition %s has no complete owning claim reference", partition.Name)
	}
	if partition.Spec.ParentDeviceSerialNumber == "" {
		return nil, fmt.Errorf("NVMePartition %s is missing ParentDeviceSerialNumber", partition.Name)
	}

	deviceName := p.NodeName + "-" + strings.ToLower(partition.Spec.ParentDeviceSerialNumber)
	var device storagev1alpha1.NVMeDevice
	if err := p.Get(ctx, types.NamespacedName{Name: deviceName}, &device); err != nil {
		return nil, fmt.Errorf("resolve parent NVMeDevice %s: %w", deviceName, err)
	}
	if device.Spec.NodeName != partition.Spec.NodeName ||
		!strings.EqualFold(device.Spec.SerialNumber, partition.Spec.ParentDeviceSerialNumber) {
		return nil, fmt.Errorf("NVMeDevice %s does not match the assigned node and serial", device.Name)
	}
	if device.Status.State != storagev1alpha1.NVMeDeviceStateClaimed {
		return nil, fmt.Errorf("NVMeDevice %s is not claimed", device.Name)
	}
	if !claimReferencesEqual(device.Status.ClaimRef, claimRef) {
		return nil, fmt.Errorf("NVMeDevice %s is owned by a different claim", device.Name)
	}

	var claim storagev1alpha1.NVMeDeviceClaim
	claimKey := types.NamespacedName{Namespace: claimRef.Namespace, Name: claimRef.Name}
	if err := p.Get(ctx, claimKey, &claim); err != nil {
		return nil, fmt.Errorf("resolve owning NVMeDeviceClaim %s: %w", claimKey, err)
	}
	if claim.UID != claimRef.UID {
		return nil, fmt.Errorf("NVMeDeviceClaim %s UID does not match the allocation", claimKey)
	}
	if !claim.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("NVMeDeviceClaim %s is being deleted", claimKey)
	}
	if !claim.Status.Active || claim.Status.MatchedDevice != device.Name ||
		claim.Status.NodeName != partition.Spec.NodeName ||
		!strings.EqualFold(claim.Spec.SerialNumber, partition.Spec.ParentDeviceSerialNumber) {
		return nil, fmt.Errorf("NVMeDeviceClaim %s is not actively bound to NVMeDevice %s", claimKey, device.Name)
	}

	return &device, nil
}

func (p *PartitionManager) setClaimAuthorizationCondition(
	ctx context.Context,
	partition *storagev1alpha1.NVMePartition,
	conditionStatus metav1.ConditionStatus,
	reason, message string,
) error {
	return p.updatePartitionStatus(ctx, types.NamespacedName{Name: partition.Name, Namespace: partition.Namespace}, func(status *storagev1alpha1.NVMePartitionStatus) {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               claimAuthorizationCondition,
			Status:             conditionStatus,
			ObservedGeneration: partition.Generation,
			Reason:             reason,
			Message:            message,
		})
	})
}

func (p *PartitionManager) rejectUnauthorizedProvisioning(
	ctx context.Context,
	partition *storagev1alpha1.NVMePartition,
	err error,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Error(err, "Refused to provision NVMePartition without a valid owning claim", "partition", partition.Name)
	if statusErr := p.setClaimAuthorizationCondition(ctx, partition, metav1.ConditionFalse, "ClaimOwnershipInvalid", err.Error()); statusErr != nil {
		logger.Error(statusErr, "Failed to record NVMePartition claim authorization", "partition", partition.Name)
		return ctrl.Result{}, statusErr
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
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
	if !isMarkedToBeDeleted {
		_, err := p.verifyProvisioningAuthorization(ctx, &partition)
		if err != nil {
			return p.rejectUnauthorizedProvisioning(ctx, &partition, err)
		}
		if err := p.setClaimAuthorizationCondition(ctx, &partition, metav1.ConditionTrue, "ClaimOwnershipVerified", "The live owning claim authorizes this allocation"); err != nil {
			return ctrl.Result{}, err
		}
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
	externalID, volumeID := identitiesForPartition(&partition)

	if isMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(&partition, partitionFinalizer) {
			logger.Info("Cleaning up NVMePartition", "partition", partition.Name)

			// 1. Unexport the NVMe-oF target
			nqn := partition.Status.NQN
			if nqn == "" {
				// ExportVolume may have created the subsystem before the status
				// update succeeded. All current backends use this deterministic NQN.
				nqn = volumeidentity.NQN(externalID)
			}
			if err := backend.UnexportVolume(ctx, nqn); err != nil {
				logger.Error(err, "Failed to unexport NVMe target", "nqn", nqn)
				return ctrl.Result{}, err
			}

			// 2. Remove the partition from the block device
			if partition.Spec.ParentDeviceSerialNumber != "" {
				var devPath, devBaseName string
				if targetBackendName == "spdk" {
					// SPDK controller names are deterministic and remain available
					// even when the device is no longer visible in kernel sysfs.
					devBaseName = p.NodeName + "-" + strings.ToLower(partition.Spec.ParentDeviceSerialNumber)
					devPath = devBaseName
				} else {
					devices, err := DiscoverNVMe()
					if err != nil {
						logger.Error(err, "Failed to discover NVMe devices during teardown")
						return ctrl.Result{}, err
					}
					for _, d := range devices {
						if strings.EqualFold(d.SerialNumber, partition.Spec.ParentDeviceSerialNumber) {
							devBaseName = d.Name
							devPath = "/dev/" + d.Name + "n1"
							break
						}
					}
					if devBaseName == "" {
						err := fmt.Errorf("NVMe device with serial %s was not found during teardown", partition.Spec.ParentDeviceSerialNumber)
						logger.Error(err, "Failed to resolve NVMe device during teardown")
						return ctrl.Result{}, err
					}
				}

				if err := volManager.DeleteVolume(ctx, devPath, devBaseName, externalID, backendVolumeIdentity(partition.Status)); err != nil {
					logger.Error(err, "Failed to delete volume from backend", "volume", externalID)
					return ctrl.Result{}, err
				}

				// Clear the operational mode for backends which do not retain
				// ownership of the physical device. SPDK keeps its lvstore and
				// controller attached for reuse; resetting PCI ownership without
				// first deleting the lvstore and detaching the controller is unsafe.
				var partList storagev1alpha1.NVMePartitionList
				if err := p.List(ctx, &partList); err != nil {
					logger.Error(err, "Failed to list NVMePartitions during teardown")
					return ctrl.Result{}, err
				}
				activeCount := 0
				for _, pt := range partList.Items {
					if pt.Spec.ParentDeviceSerialNumber == partition.Spec.ParentDeviceSerialNumber &&
						(pt.Namespace != partition.Namespace || pt.Name != partition.Name) &&
						pt.DeletionTimestamp.IsZero() {
						activeCount++
					}
				}
				if activeCount == 0 && targetBackendName != "spdk" {
					deviceName := p.NodeName + "-" + strings.ToLower(partition.Spec.ParentDeviceSerialNumber)
					err := p.updateDeviceStatus(ctx, types.NamespacedName{Name: deviceName}, func(status *storagev1alpha1.NVMeDeviceStatus) {
						status.ActiveBackend = ""
					})
					if err != nil {
						logger.Error(err, "Failed to clear NVMeDevice active backend")
						return ctrl.Result{}, err
					}
				}
			}

			// Remove the finalizer only after every required external cleanup
			// operation succeeded. Any error above retains it and causes a retry.
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

	if partition.Status.ExternalID != externalID || partition.Status.VolumeID != volumeID {
		if err := p.updatePartitionStatus(ctx, req.NamespacedName, func(status *storagev1alpha1.NVMePartitionStatus) {
			status.ExternalID = externalID
			status.VolumeID = volumeID
		}); err != nil {
			return ctrl.Result{}, err
		}
		partition.Status.ExternalID = externalID
		partition.Status.VolumeID = volumeID
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
		if targetBackendName != "spdk" {
			return ctrl.Result{}, nil
		}
		// SPDK operations may have succeeded before an RPC response or status
		// update failed. Reconcile again so idempotent discovery can recover the
		// existing lvol/subsystem instead of leaving external resources orphaned.
		logger.Info("Retrying failed SPDK partition to recover partial provisioning")
	}

	logger.Info("Provisioning partition on local node", "partition", partition.Name, "size", partition.Spec.Size.String())

	// Re-fetch the ownership chain immediately before the first host operation.
	dev, err := p.verifyProvisioningAuthorization(ctx, &partition)
	if err != nil {
		return p.rejectUnauthorizedProvisioning(ctx, &partition, err)
	}
	deviceName := dev.Name

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
	if _, err := p.verifyProvisioningAuthorization(ctx, &partition); err != nil {
		return p.rejectUnauthorizedProvisioning(ctx, &partition, err)
	}
	if err := volManager.SetupStorage(ctx, devPath, devBaseName); err != nil {
		logger.Error(err, "Failed to configure storage slicing")
		return ctrl.Result{}, err
	}

	if _, err := p.verifyProvisioningAuthorization(ctx, &partition); err != nil {
		return p.rejectUnauthorizedProvisioning(ctx, &partition, err)
	}
	requestedAllocation, err := capacity.RoundUp(partition.Spec.Size.Value())
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid partition capacity %q: %w", partition.Spec.Size.String(), err)
	}
	createdVolume, err := volManager.CreateVolume(ctx, devPath, devBaseName, externalID, partition.Spec.Size.Value())
	if err != nil {
		logger.Error(err, "Failed to carve volume from device")
		return ctrl.Result{}, err
	}
	if createdVolume.BackendVolumeID == "" {
		return ctrl.Result{}, fmt.Errorf("volume manager %s returned an empty backend volume ID", vmName)
	}
	if createdVolume.CapacityBytes < requestedAllocation {
		return ctrl.Result{}, fmt.Errorf("volume manager %s allocated %d bytes, below required %d", vmName, createdVolume.CapacityBytes, requestedAllocation)
	}
	if backendVolumeIdentity(partition.Status) != createdVolume {
		if err := p.updatePartitionStatus(ctx, req.NamespacedName, func(status *storagev1alpha1.NVMePartitionStatus) {
			status.BackendVolumeID = createdVolume.BackendVolumeID
			status.AllocatedCapacity = *resource.NewQuantity(createdVolume.CapacityBytes, resource.BinarySI)
			status.SPDKBaseBdev = createdVolume.BaseBdev
			status.SPDKLvstoreName = createdVolume.VolumeStoreName
			status.SPDKLvstoreUUID = createdVolume.VolumeStoreUUID
			status.SPDKLvolName = createdVolume.VolumeName
			status.SPDKLvolUUID = createdVolume.VolumeUUID
		}); err != nil {
			return ctrl.Result{}, err
		}
		partition.Status.BackendVolumeID = createdVolume.BackendVolumeID
		partition.Status.AllocatedCapacity = *resource.NewQuantity(createdVolume.CapacityBytes, resource.BinarySI)
		partition.Status.SPDKBaseBdev = createdVolume.BaseBdev
		partition.Status.SPDKLvstoreName = createdVolume.VolumeStoreName
		partition.Status.SPDKLvstoreUUID = createdVolume.VolumeStoreUUID
		partition.Status.SPDKLvolName = createdVolume.VolumeName
		partition.Status.SPDKLvolUUID = createdVolume.VolumeUUID
	}
	blockPath := createdVolume.BackendVolumeID

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
	if _, err := p.verifyProvisioningAuthorization(ctx, &partition); err != nil {
		return p.rejectUnauthorizedProvisioning(ctx, &partition, err)
	}
	nqn, err := backend.ExportVolume(ctx, externalID, blockPath, portalIP, portalPort, partition.Spec.TargetOptions)
	if err != nil {
		logger.Error(err, "Failed to export volume as target")
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
