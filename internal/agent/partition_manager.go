package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/agent/plugins"
	attachmentidentity "distort/internal/attachment"
	"distort/internal/capacity"
	"distort/internal/rdmahealth"
	"distort/internal/storageoptions"
	"distort/internal/volumeidentity"
)

const partitionFinalizer = "storage.distort.io/partition-cleanup"

const claimAuthorizationCondition = "ClaimAuthorized"

const partitionProvisioningCondition = "ProvisioningReady"

const spdkTargetBackend = "spdk"

const exportedHealthInterval = 15 * time.Second

func partitionHasTerminalFailure(partition *storagev1alpha1.NVMePartition) bool {
	condition := meta.FindStatusCondition(partition.Status.Conditions, partitionProvisioningCondition)
	return condition != nil && condition.Status == metav1.ConditionFalse &&
		condition.ObservedGeneration == partition.Generation && strings.HasPrefix(condition.Reason, "Terminal")
}

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

func (p *PartitionManager) attachmentForPartition(ctx context.Context, partition *storagev1alpha1.NVMePartition) (*storagev1alpha1.NVMeVolumeAttachment, error) {
	var attachment storagev1alpha1.NVMeVolumeAttachment
	key := types.NamespacedName{Namespace: partition.Namespace, Name: attachmentidentity.Name(partition.UID)}
	if err := p.Get(ctx, key, &attachment); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &attachment, nil
}

func (p *PartitionManager) setAttachmentCondition(ctx context.Context, key types.NamespacedName, attachmentID string,
	conditionStatus metav1.ConditionStatus, reason, message string,
) error {
	var latest storagev1alpha1.NVMeVolumeAttachment
	if err := p.Get(ctx, key, &latest); err != nil {
		return client.IgnoreNotFound(err)
	}
	base := latest.DeepCopy()
	if conditionStatus == metav1.ConditionTrue {
		latest.Status.ObservedAttachmentID = attachmentID
	} else {
		latest.Status.ObservedAttachmentID = ""
	}
	meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
		Type:               attachmentidentity.AccessReadyCondition,
		Status:             conditionStatus,
		ObservedGeneration: latest.Generation,
		Reason:             reason,
		Message:            message,
	})
	return p.Status().Patch(ctx, &latest, client.MergeFrom(base))
}

func (p *PartitionManager) reconcileAttachmentAccess(ctx context.Context, partition *storagev1alpha1.NVMePartition, backend plugins.TargetBackend) error {
	attachment, err := p.attachmentForPartition(ctx, partition)
	if err != nil {
		return err
	}
	desiredHost := ""
	validAttachment := attachment != nil && attachment.DeletionTimestamp.IsZero() &&
		controllerutil.ContainsFinalizer(attachment, attachmentidentity.Finalizer) &&
		attachment.Spec.VolumeRef.Name == partition.Name && attachment.Spec.VolumeRef.UID == string(partition.UID) &&
		attachment.Spec.HostNQN == attachmentidentity.HostNQN(attachment.Spec.NodeID)
	if validAttachment {
		desiredHost = attachment.Spec.HostNQN
	}
	if err := backend.ReconcileHostAccess(ctx, partition.Status.NQN, desiredHost); err != nil {
		if attachment != nil && attachment.DeletionTimestamp.IsZero() {
			_ = p.setAttachmentCondition(ctx, client.ObjectKeyFromObject(attachment), attachment.Spec.AttachmentID,
				metav1.ConditionFalse, "TargetAccessFailed", err.Error())
		}
		return err
	}
	if attachment == nil {
		return nil
	}
	if !attachment.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(attachment, attachmentidentity.Finalizer) {
			base := attachment.DeepCopy()
			controllerutil.RemoveFinalizer(attachment, attachmentidentity.Finalizer)
			return p.Patch(ctx, attachment, client.MergeFrom(base))
		}
		return nil
	}
	if !validAttachment {
		return p.setAttachmentCondition(ctx, client.ObjectKeyFromObject(attachment), attachment.Spec.AttachmentID,
			metav1.ConditionFalse, "AttachmentIdentityInvalid", "Attachment volume or host identity does not match the partition")
	}
	return p.setAttachmentCondition(ctx, client.ObjectKeyFromObject(attachment), attachment.Spec.AttachmentID,
		metav1.ConditionTrue, "TargetAccessAuthorized", "Provider target exclusively authorizes the attached node host NQN")
}

func (p *PartitionManager) releaseAttachmentAfterUnexport(ctx context.Context, partition *storagev1alpha1.NVMePartition) error {
	attachment, err := p.attachmentForPartition(ctx, partition)
	if err != nil || attachment == nil {
		return err
	}
	if attachment.DeletionTimestamp.IsZero() {
		if err := p.Delete(ctx, attachment); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err := p.Get(ctx, client.ObjectKeyFromObject(attachment), attachment); err != nil {
			return client.IgnoreNotFound(err)
		}
	}
	if controllerutil.ContainsFinalizer(attachment, attachmentidentity.Finalizer) {
		base := attachment.DeepCopy()
		controllerutil.RemoveFinalizer(attachment, attachmentidentity.Finalizer)
		return p.Patch(ctx, attachment, client.MergeFrom(base))
	}
	return nil
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

func (p *PartitionManager) rdmaEndpoint(ctx context.Context) (string, error) {
	var node storagev1alpha1.RDMAStorageNode
	if err := p.Get(ctx, types.NamespacedName{Name: p.NodeName}, &node); err != nil {
		return "", fmt.Errorf("resolve RDMAStorageNode %s: %w", p.NodeName, err)
	}
	if err := rdmahealth.Validate(&node, time.Now()); err != nil {
		return "", err
	}
	return node.Spec.RDMAIP, nil
}

func claimReferencesEqual(a, b *storagev1alpha1.NVMeDeviceClaimReference) bool {
	return a != nil && b != nil &&
		a.Namespace == b.Namespace && a.Name == b.Name && a.UID == b.UID
}

func (p *PartitionManager) verifyProvisioningAuthorization(ctx context.Context, partition *storagev1alpha1.NVMePartition) (*storagev1alpha1.NVMeDevice, error) {
	recoveringExistingAllocation := partition.Status.ExternalID != "" || partition.Status.BackendVolumeID != ""
	claimRef := partition.Spec.ClaimRef
	if claimRef == nil || claimRef.Namespace == "" || claimRef.Name == "" || claimRef.UID == "" {
		return nil, fmt.Errorf("NVMePartition %s has no complete owning claim reference", partition.Name)
	}
	if partition.Spec.ParentDeviceSerialNumber == "" {
		return nil, fmt.Errorf("NVMePartition %s is missing ParentDeviceSerialNumber", partition.Name)
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
	if claim.Status.MatchedDevice == "" || claim.Status.NodeName != partition.Spec.NodeName ||
		!strings.EqualFold(claim.Spec.SerialNumber, partition.Spec.ParentDeviceSerialNumber) {
		return nil, fmt.Errorf("NVMeDeviceClaim %s does not match the assigned device identity", claimKey)
	}
	if !claim.Status.Active && !recoveringExistingAllocation {
		return nil, fmt.Errorf("NVMeDeviceClaim %s is not actively bound to NVMeDevice %s", claimKey, claim.Status.MatchedDevice)
	}

	var device storagev1alpha1.NVMeDevice
	if err := p.Get(ctx, types.NamespacedName{Name: claim.Status.MatchedDevice}, &device); err != nil {
		return nil, fmt.Errorf("resolve parent NVMeDevice %s: %w", claim.Status.MatchedDevice, err)
	}
	if device.Spec.NodeName != partition.Spec.NodeName ||
		!strings.EqualFold(device.Spec.SerialNumber, partition.Spec.ParentDeviceSerialNumber) {
		return nil, fmt.Errorf("NVMeDevice %s does not match the assigned node and serial", device.Name)
	}
	if device.Status.State != storagev1alpha1.NVMeDeviceStateClaimed && !recoveringExistingAllocation {
		return nil, fmt.Errorf("NVMeDevice %s is not claimed", device.Name)
	}
	if !claimReferencesEqual(device.Status.ClaimRef, claimRef) {
		return nil, fmt.Errorf("NVMeDevice %s is owned by a different claim", device.Name)
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

func (p *PartitionManager) recordProvisioningFailure(
	ctx context.Context,
	partition *storagev1alpha1.NVMePartition,
	reason string,
	err error,
	terminal bool,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	if terminal {
		reason = "Terminal" + reason
	} else {
		reason = "Retryable" + reason
	}
	statusErr := p.updatePartitionStatus(ctx, client.ObjectKeyFromObject(partition), func(status *storagev1alpha1.NVMePartitionStatus) {
		status.State = storagev1alpha1.NVMePartitionStateFailed
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type: partitionProvisioningCondition, Status: metav1.ConditionFalse,
			ObservedGeneration: partition.Generation, Reason: reason, Message: err.Error(),
		})
	})
	if statusErr != nil {
		logger.Error(statusErr, "Failed to record NVMePartition provisioning failure", "partition", partition.Name)
		return ctrl.Result{}, statusErr
	}
	if terminal {
		logger.Error(err, "NVMePartition has a terminal provisioning failure", "partition", partition.Name, "reason", reason)
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, err
}

func (p *PartitionManager) terminalProvisioningFailure(ctx context.Context, partition *storagev1alpha1.NVMePartition, reason string, err error) (ctrl.Result, error) {
	return p.recordProvisioningFailure(ctx, partition, reason, err, true)
}

func (p *PartitionManager) retryableProvisioningFailure(ctx context.Context, partition *storagev1alpha1.NVMePartition, reason string, err error) (ctrl.Result, error) {
	return p.recordProvisioningFailure(ctx, partition, reason, err, false)
}

type resolvedPartitionPlugins struct {
	targetBackendName string
	volumeManagerName string
	targetBackend     plugins.TargetBackend
	volumeManager     plugins.VolumeManager
}

func resolvePartitionPlugins(partition *storagev1alpha1.NVMePartition) (resolvedPartitionPlugins, string, error) {
	targetBackendName := partition.Spec.TargetBackend
	if targetBackendName == "" {
		targetBackendName = spdkTargetBackend
	}
	targetBackend, err := plugins.GetTargetBackend(targetBackendName)
	if err != nil {
		return resolvedPartitionPlugins{}, "InvalidBackend", err
	}

	volumeManagerName := partition.Spec.VolumeManager
	if volumeManagerName == "" || volumeManagerName == "partition" {
		if targetBackendName == spdkTargetBackend {
			volumeManagerName = "spdk-lvol"
		} else {
			volumeManagerName = "parted"
		}
	}
	volumeManager, err := plugins.GetVolumeManager(volumeManagerName)
	if err != nil {
		return resolvedPartitionPlugins{}, "InvalidVolumeManager", err
	}
	return resolvedPartitionPlugins{
		targetBackendName: targetBackendName,
		volumeManagerName: volumeManagerName,
		targetBackend:     targetBackend,
		volumeManager:     volumeManager,
	}, "", nil
}

func (p *PartitionManager) assignedDeviceName(ctx context.Context, partition *storagev1alpha1.NVMePartition) (string, error) {
	claimRef := partition.Spec.ClaimRef
	if claimRef != nil && claimRef.Namespace != "" && claimRef.Name != "" && claimRef.UID != "" {
		var claim storagev1alpha1.NVMeDeviceClaim
		claimKey := types.NamespacedName{Namespace: claimRef.Namespace, Name: claimRef.Name}
		if err := p.Get(ctx, claimKey, &claim); err == nil {
			if claim.UID != claimRef.UID {
				return "", fmt.Errorf("NVMeDeviceClaim %s UID does not match the allocation", claimKey)
			}
			if claim.Status.MatchedDevice != "" && claim.Status.NodeName == partition.Spec.NodeName &&
				strings.EqualFold(claim.Spec.SerialNumber, partition.Spec.ParentDeviceSerialNumber) {
				return claim.Status.MatchedDevice, nil
			}
		} else if !apierrors.IsNotFound(err) {
			return "", fmt.Errorf("resolve owning NVMeDeviceClaim %s during cleanup: %w", claimKey, err)
		}
	}

	var devices storagev1alpha1.NVMeDeviceList
	if err := p.List(ctx, &devices); err != nil {
		return "", fmt.Errorf("list NVMeDevices during cleanup: %w", err)
	}
	var matches []string
	for _, device := range devices.Items {
		if device.Spec.NodeName == partition.Spec.NodeName &&
			strings.EqualFold(device.Spec.SerialNumber, partition.Spec.ParentDeviceSerialNumber) {
			matches = append(matches, device.Name)
		}
	}
	if len(matches) == 0 {
		// Preserve cleanup of pre-hash allocations whose claim and device objects
		// are already gone. New SPDK allocations persist their exact base bdev.
		return p.NodeName + "-" + strings.ToLower(partition.Spec.ParentDeviceSerialNumber), nil
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("found %d NVMeDevices for node %s and serial %s during cleanup, want exactly one",
			len(matches), partition.Spec.NodeName, partition.Spec.ParentDeviceSerialNumber)
	}
	return matches[0], nil
}

func (p *PartitionManager) resolveDeletionDevice(ctx context.Context, partition *storagev1alpha1.NVMePartition, targetBackendName string) (string, string, error) {
	if targetBackendName == spdkTargetBackend {
		if partition.Status.SPDKBaseBdev != "" {
			return partition.Status.SPDKBaseBdev, partition.Status.SPDKBaseBdev, nil
		}
		name, err := p.assignedDeviceName(ctx, partition)
		if err != nil {
			return "", "", err
		}
		return name, name, nil
	}
	devices, err := DiscoverNVMe()
	if err != nil {
		return "", "", fmt.Errorf("discover NVMe devices during teardown: %w", err)
	}
	for _, device := range devices {
		if strings.EqualFold(device.SerialNumber, partition.Spec.ParentDeviceSerialNumber) {
			return "/dev/" + device.Name + "n1", device.Name, nil
		}
	}
	return "", "", fmt.Errorf("NVMe device with serial %s was not found during teardown", partition.Spec.ParentDeviceSerialNumber)
}

func (p *PartitionManager) clearInactiveBackend(ctx context.Context, partition *storagev1alpha1.NVMePartition, targetBackendName string) error {
	if targetBackendName == spdkTargetBackend {
		return nil
	}
	var partitions storagev1alpha1.NVMePartitionList
	if err := p.List(ctx, &partitions); err != nil {
		return fmt.Errorf("list NVMePartitions during teardown: %w", err)
	}
	for _, candidate := range partitions.Items {
		if candidate.Spec.ParentDeviceSerialNumber == partition.Spec.ParentDeviceSerialNumber &&
			(candidate.Namespace != partition.Namespace || candidate.Name != partition.Name) &&
			candidate.DeletionTimestamp.IsZero() {
			return nil
		}
	}
	deviceName, err := p.assignedDeviceName(ctx, partition)
	if err != nil {
		return err
	}
	return p.updateDeviceStatus(ctx, types.NamespacedName{Name: deviceName}, func(status *storagev1alpha1.NVMeDeviceStatus) {
		status.ActiveBackend = ""
	})
}

func (p *PartitionManager) reconcilePartitionDeletion(
	ctx context.Context,
	key types.NamespacedName,
	partition *storagev1alpha1.NVMePartition,
	resolved resolvedPartitionPlugins,
	externalID string,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(partition, partitionFinalizer) {
		return ctrl.Result{}, nil
	}
	logger := log.FromContext(ctx)
	logger.Info("Cleaning up NVMePartition", "partition", partition.Name)

	nqn := partition.Status.NQN
	if nqn == "" {
		nqn = volumeidentity.NQN(externalID)
	}
	if err := resolved.targetBackend.UnexportVolume(ctx, nqn); err != nil {
		logger.Error(err, "Failed to unexport NVMe target", "nqn", nqn)
		return ctrl.Result{}, err
	}
	if err := p.releaseAttachmentAfterUnexport(ctx, partition); err != nil {
		logger.Error(err, "Failed to release NVMeVolumeAttachment after target removal")
		return ctrl.Result{}, err
	}

	if partition.Spec.ParentDeviceSerialNumber != "" {
		devicePath, deviceName, err := p.resolveDeletionDevice(ctx, partition, resolved.targetBackendName)
		if err != nil {
			logger.Error(err, "Failed to resolve NVMe device during teardown")
			return ctrl.Result{}, err
		}
		identity := backendVolumeIdentity(partition.Status)
		if err := resolved.volumeManager.DeleteVolume(ctx, devicePath, deviceName, externalID, identity); err != nil {
			logger.Error(err, "Failed to delete volume from backend", "volume", externalID)
			return ctrl.Result{}, err
		}
		if err := p.clearInactiveBackend(ctx, partition, resolved.targetBackendName); err != nil {
			logger.Error(err, "Failed to clear NVMeDevice active backend")
			return ctrl.Result{}, err
		}
	}

	var latest storagev1alpha1.NVMePartition
	if err := p.Get(ctx, key, &latest); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	base := latest.DeepCopy()
	controllerutil.RemoveFinalizer(&latest, partitionFinalizer)
	return ctrl.Result{}, p.Patch(ctx, &latest, client.MergeFrom(base))
}

func (p *PartitionManager) ensurePartitionIdentityAndFinalizer(
	ctx context.Context,
	key types.NamespacedName,
	partition *storagev1alpha1.NVMePartition,
	externalID string,
	volumeID string,
) error {
	if partition.Status.ExternalID != externalID || partition.Status.VolumeID != volumeID {
		if err := p.updatePartitionStatus(ctx, key, func(status *storagev1alpha1.NVMePartitionStatus) {
			status.ExternalID = externalID
			status.VolumeID = volumeID
		}); err != nil {
			return err
		}
		partition.Status.ExternalID = externalID
		partition.Status.VolumeID = volumeID
	}
	if controllerutil.ContainsFinalizer(partition, partitionFinalizer) {
		return nil
	}
	base := partition.DeepCopy()
	controllerutil.AddFinalizer(partition, partitionFinalizer)
	return p.Patch(ctx, partition, client.MergeFrom(base))
}

func (p *PartitionManager) reconcileExportedPartition(
	ctx context.Context,
	partition *storagev1alpha1.NVMePartition,
	backend plugins.TargetBackend,
) (bool, ctrl.Result, error) {
	if partition.Status.State != storagev1alpha1.NVMePartitionStateExported {
		return false, ctrl.Result{}, nil
	}
	portalIP, err := p.rdmaEndpoint(ctx)
	if err != nil {
		return true, ctrl.Result{}, err
	}
	if checker, ok := backend.(plugins.ExportHealthChecker); ok {
		err := checker.CheckExport(ctx, partition.Status.NQN, partition.Status.BackendVolumeID,
			portalIP, partition.Status.PortalPort, partition.Spec.TargetOptions)
		if err != nil {
			log.FromContext(ctx).Error(err, "Export health check failed; re-provisioning", "nqn", partition.Status.NQN)
			return false, ctrl.Result{}, nil
		}
	}
	if err := p.reconcileAttachmentAccess(ctx, partition, backend); err != nil {
		log.FromContext(ctx).Error(err, "Failed to reconcile exclusive NVMe host access", "nqn", partition.Status.NQN)
		return true, ctrl.Result{}, err
	}
	return true, ctrl.Result{RequeueAfter: exportedHealthInterval}, nil
}

func findProvisioningDevice(partition *storagev1alpha1.NVMePartition, targetBackendName string) (string, string, string, error) {
	devices, err := DiscoverNVMe()
	if err != nil {
		return "", "", "DiscoveryFailed", fmt.Errorf("discover NVMe devices: %w", err)
	}
	for _, device := range devices {
		if strings.EqualFold(device.SerialNumber, partition.Spec.ParentDeviceSerialNumber) {
			path := "/dev/" + device.Name + "n1"
			if targetBackendName == spdkTargetBackend {
				path = device.Name
			}
			return path, device.Name, "", nil
		}
	}
	return "", "", "DeviceNotFound", fmt.Errorf("NVMe device with serial %s not found on host", partition.Spec.ParentDeviceSerialNumber)
}

func (p *PartitionManager) persistBackendVolumeIdentity(
	ctx context.Context,
	key types.NamespacedName,
	partition *storagev1alpha1.NVMePartition,
	created plugins.VolumeIdentity,
) error {
	if backendVolumeIdentity(partition.Status) == created {
		return nil
	}
	if err := p.updatePartitionStatus(ctx, key, func(status *storagev1alpha1.NVMePartitionStatus) {
		status.BackendVolumeID = created.BackendVolumeID
		status.AllocatedCapacity = *resource.NewQuantity(created.CapacityBytes, resource.BinarySI)
		status.SPDKBaseBdev = created.BaseBdev
		status.SPDKLvstoreName = created.VolumeStoreName
		status.SPDKLvstoreUUID = created.VolumeStoreUUID
		status.SPDKLvolName = created.VolumeName
		status.SPDKLvolUUID = created.VolumeUUID
	}); err != nil {
		return err
	}
	partition.Status.BackendVolumeID = created.BackendVolumeID
	partition.Status.AllocatedCapacity = *resource.NewQuantity(created.CapacityBytes, resource.BinarySI)
	partition.Status.SPDKBaseBdev = created.BaseBdev
	partition.Status.SPDKLvstoreName = created.VolumeStoreName
	partition.Status.SPDKLvstoreUUID = created.VolumeStoreUUID
	partition.Status.SPDKLvolName = created.VolumeName
	partition.Status.SPDKLvolUUID = created.VolumeUUID
	return nil
}

func (p *PartitionManager) provisionPartition(
	ctx context.Context,
	key types.NamespacedName,
	partition *storagev1alpha1.NVMePartition,
	resolved resolvedPartitionPlugins,
	externalID string,
	requestedAllocation int64,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Provisioning NVMePartition", "partition", partition.Name, "size", partition.Spec.Size.String())

	device, err := p.verifyProvisioningAuthorization(ctx, partition)
	if err != nil {
		return p.rejectUnauthorizedProvisioning(ctx, partition, err)
	}
	if _, err := p.rdmaEndpoint(ctx); err != nil {
		return p.retryableProvisioningFailure(ctx, partition, "RDMAEndpointUnavailable", err)
	}
	if device.Status.ActiveBackend != "" && device.Status.ActiveBackend != resolved.targetBackendName {
		err := fmt.Errorf("device is currently locked to backend %s, requested %s", device.Status.ActiveBackend, resolved.targetBackendName)
		logger.Error(err, "Mismatched backend for target device")
		return p.retryableProvisioningFailure(ctx, partition, "BackendBusy", err)
	}
	if err := resolved.targetBackend.SetupDevice(ctx, device.Spec.PCIAddress, device.Name, partition.Spec.TargetOptions); err != nil {
		logger.Error(err, "Failed to setup physical device driver for backend")
		return p.retryableProvisioningFailure(ctx, partition, "DeviceSetupFailed", err)
	}
	if device.Status.ActiveBackend == "" {
		err := p.updateDeviceStatus(ctx, types.NamespacedName{Name: device.Name}, func(status *storagev1alpha1.NVMeDeviceStatus) {
			status.ActiveBackend = resolved.targetBackendName
		})
		if err != nil {
			logger.Error(err, "Failed to update NVMeDevice active backend status")
			return p.retryableProvisioningFailure(ctx, partition, "DeviceStatusUpdateFailed", err)
		}
	}

	devicePath, deviceName, failureReason, err := findProvisioningDevice(partition, resolved.targetBackendName)
	if err != nil {
		logger.Error(err, "Failed to locate parent NVMe device")
		return p.retryableProvisioningFailure(ctx, partition, failureReason, err)
	}
	if _, err := p.verifyProvisioningAuthorization(ctx, partition); err != nil {
		return p.rejectUnauthorizedProvisioning(ctx, partition, err)
	}
	if err := resolved.volumeManager.SetupStorage(ctx, devicePath, deviceName); err != nil {
		logger.Error(err, "Failed to configure storage slicing")
		return p.retryableProvisioningFailure(ctx, partition, "StorageSetupFailed", err)
	}
	if _, err := p.verifyProvisioningAuthorization(ctx, partition); err != nil {
		return p.rejectUnauthorizedProvisioning(ctx, partition, err)
	}
	created, err := resolved.volumeManager.CreateVolume(ctx, devicePath, deviceName, externalID, partition.Spec.Size.Value())
	if err != nil {
		logger.Error(err, "Failed to carve volume from device")
		return p.retryableProvisioningFailure(ctx, partition, "VolumeCreationFailed", err)
	}
	if created.BackendVolumeID == "" {
		err := fmt.Errorf("volume manager %s returned an empty backend volume ID", resolved.volumeManagerName)
		return p.terminalProvisioningFailure(ctx, partition, "InvalidVolumeIdentity", err)
	}
	if created.CapacityBytes < requestedAllocation {
		err := fmt.Errorf("volume manager %s allocated %d bytes, below required %d", resolved.volumeManagerName, created.CapacityBytes, requestedAllocation)
		return p.terminalProvisioningFailure(ctx, partition, "InsufficientAllocation", err)
	}
	if err := p.persistBackendVolumeIdentity(ctx, key, partition, created); err != nil {
		return ctrl.Result{}, err
	}

	portalIP, err := p.rdmaEndpoint(ctx)
	if err != nil {
		return p.retryableProvisioningFailure(ctx, partition, "RDMAEndpointUnavailable", err)
	}
	if _, err := p.verifyProvisioningAuthorization(ctx, partition); err != nil {
		return p.rejectUnauthorizedProvisioning(ctx, partition, err)
	}
	nqn, err := resolved.targetBackend.ExportVolume(ctx, externalID, created.BackendVolumeID, portalIP, 4420, partition.Spec.TargetOptions)
	if err != nil {
		logger.Error(err, "Failed to export volume as target")
		return p.retryableProvisioningFailure(ctx, partition, "ExportFailed", err)
	}
	err = p.updatePartitionStatus(ctx, key, func(status *storagev1alpha1.NVMePartitionStatus) {
		status.State = storagev1alpha1.NVMePartitionStateExported
		status.NQN = nqn
		status.PortalIP = portalIP
		status.PortalPort = 4420
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type: partitionProvisioningCondition, Status: metav1.ConditionTrue,
			ObservedGeneration: partition.Generation, Reason: "Provisioned",
			Message: "The NVMe partition is provisioned and exported",
		})
	})
	if err != nil {
		logger.Error(err, "Failed to update NVMePartition status")
		return ctrl.Result{}, err
	}
	logger.Info("Provisioned and exported NVMePartition", "partition", partition.Name)
	return ctrl.Result{RequeueAfter: exportedHealthInterval}, nil
}

// Reconcile handles partition configuration when an admin/controller assigns them to this node.
func (p *PartitionManager) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	var partition storagev1alpha1.NVMePartition
	if err := p.Get(ctx, req.NamespacedName, &partition); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if partition.Spec.NodeName != p.NodeName {
		return ctrl.Result{}, nil
	}
	deleting := partition.GetDeletionTimestamp() != nil
	if !deleting && partitionHasTerminalFailure(&partition) {
		return ctrl.Result{}, nil
	}
	resolved, failureReason, err := resolvePartitionPlugins(&partition)
	if err != nil {
		logger.Error(err, "Failed to resolve NVMePartition plugins", "reason", failureReason)
		if !deleting {
			return p.terminalProvisioningFailure(ctx, &partition, failureReason, err)
		}
		return ctrl.Result{}, err
	}
	externalID, volumeID := identitiesForPartition(&partition)
	if deleting {
		return p.reconcilePartitionDeletion(ctx, req.NamespacedName, &partition, resolved, externalID)
	}
	if err := storageoptions.Validate(resolved.targetBackendName, partition.Spec.TargetOptions); err != nil {
		return p.terminalProvisioningFailure(ctx, &partition, "InvalidOptions", err)
	}
	requestedAllocation, err := capacity.RoundUp(partition.Spec.Size.Value())
	if err != nil {
		return p.terminalProvisioningFailure(ctx, &partition, "InvalidCapacity", fmt.Errorf("invalid partition capacity %q: %w", partition.Spec.Size.String(), err))
	}
	if _, err := p.verifyProvisioningAuthorization(ctx, &partition); err != nil {
		return p.rejectUnauthorizedProvisioning(ctx, &partition, err)
	}
	if err := p.setClaimAuthorizationCondition(ctx, &partition, metav1.ConditionTrue, "ClaimOwnershipVerified", "The live owning claim authorizes this allocation"); err != nil {
		return ctrl.Result{}, err
	}
	if err := p.ensurePartitionIdentityAndFinalizer(ctx, req.NamespacedName, &partition, externalID, volumeID); err != nil {
		return ctrl.Result{}, err
	}
	handled, result, err := p.reconcileExportedPartition(ctx, &partition, resolved.targetBackend)
	if handled || err != nil {
		return result, err
	}
	if partition.Status.State == storagev1alpha1.NVMePartitionStateFailed {
		logger.Info("Retrying failed NVMePartition provisioning")
	}
	return p.provisionPartition(ctx, req.NamespacedName, &partition, resolved, externalID, requestedAllocation)
}

// SetupWithManager sets up the controller with the Manager.
func (p *PartitionManager) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.NVMePartition{}).
		Watches(&storagev1alpha1.NVMeVolumeAttachment{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, object client.Object) []reconcile.Request {
				attachment, ok := object.(*storagev1alpha1.NVMeVolumeAttachment)
				if !ok || attachment.Spec.VolumeRef.Name == "" {
					return nil
				}
				return []reconcile.Request{{NamespacedName: types.NamespacedName{
					Namespace: attachment.Namespace,
					Name:      attachment.Spec.VolumeRef.Name,
				}}}
			},
		)).
		Named("agent-partition-manager").
		Complete(p)
}
