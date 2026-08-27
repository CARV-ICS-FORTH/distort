package csi

import (
	"context"
	"time"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"

	storagev1alpha1 "distort/api/v1alpha1"
	attachmentidentity "distort/internal/attachment"
	"distort/internal/volumeidentity"
)

func (cs *ControllerServer) ControllerPublishVolume(ctx context.Context, req *csipb.ControllerPublishVolumeRequest) (*csipb.ControllerPublishVolumeResponse, error) {
	if req.GetVolumeId() == "" || req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID and node ID must be provided")
	}
	if _, err := validateVolumeCapabilities(req.GetVolumeContext(), []*csipb.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid volume capability: %v", err)
	}
	partition, err := cs.partitionForVolumeHandle(ctx, req.GetVolumeId())
	if err != nil {
		return nil, err
	}
	if partition.Status.State != storagev1alpha1.NVMePartitionStateExported {
		return nil, status.Errorf(codes.FailedPrecondition, "Volume %s is not exported", req.GetVolumeId())
	}

	key := types.NamespacedName{Namespace: partition.Namespace, Name: attachmentidentity.Name(partition.UID)}
	for {
		var existing storagev1alpha1.NVMeVolumeAttachment
		err := cs.k8sClient.Get(ctx, key, &existing)
		if apierrors.IsNotFound(err) {
			controller := true
			blockOwnerDeletion := true
			desired := &storagev1alpha1.NVMeVolumeAttachment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       key.Name,
					Namespace:  key.Namespace,
					Finalizers: []string{attachmentidentity.Finalizer},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion:         storagev1alpha1.GroupVersion.String(),
						Kind:               "NVMePartition",
						Name:               partition.Name,
						UID:                partition.UID,
						Controller:         &controller,
						BlockOwnerDeletion: &blockOwnerDeletion,
					}},
				},
				Spec: storagev1alpha1.NVMeVolumeAttachmentSpec{
					VolumeRef:    storagev1alpha1.NVMeVolumeReference{Name: partition.Name, UID: string(partition.UID)},
					NodeID:       req.GetNodeId(),
					HostNQN:      hostNQNForNode(req.GetNodeId()),
					AttachmentID: string(uuid.NewUUID()),
				},
			}
			if err := cs.k8sClient.Create(ctx, desired); err != nil {
				if apierrors.IsAlreadyExists(err) {
					continue
				}
				return nil, status.Errorf(codes.Internal, "Failed to create volume attachment: %v", err)
			}
			existing = *desired
		} else if err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to get volume attachment: %v", err)
		}

		if existing.Spec.VolumeRef.Name != partition.Name || existing.Spec.VolumeRef.UID != string(partition.UID) {
			return nil, status.Error(codes.FailedPrecondition, "Attachment identity does not match the requested volume")
		}
		if !existing.DeletionTimestamp.IsZero() {
			if err := cs.waitForAttachmentDeleted(ctx, key); err != nil {
				return nil, err
			}
			continue
		}
		if existing.Spec.NodeID != req.GetNodeId() {
			if existing.Annotations[attachmentidentity.ForceDetachAnnotation] != existing.Spec.NodeID {
				return nil, status.Errorf(codes.FailedPrecondition,
					"Volume is attached to node %q; annotate %s with %s=%q only after confirming the old node is fenced",
					existing.Spec.NodeID, key.String(), attachmentidentity.ForceDetachAnnotation, existing.Spec.NodeID)
			}
			if err := cs.k8sClient.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
				return nil, status.Errorf(codes.Internal, "Failed to begin forced attachment revocation: %v", err)
			}
			if err := cs.waitForAttachmentDeleted(ctx, key); err != nil {
				return nil, err
			}
			continue
		}
		if err := cs.waitForAttachmentReady(ctx, key, existing.Spec.AttachmentID); err != nil {
			return nil, err
		}
		return &csipb.ControllerPublishVolumeResponse{PublishContext: map[string]string{
			publishContextNodeID:       existing.Spec.NodeID,
			publishContextHostNQN:      existing.Spec.HostNQN,
			publishContextAttachmentID: existing.Spec.AttachmentID,
		}}, nil
	}
}

func (cs *ControllerServer) ControllerUnpublishVolume(ctx context.Context, req *csipb.ControllerUnpublishVolumeRequest) (*csipb.ControllerUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID must be provided")
	}
	partition, err := cs.partitionForVolumeHandle(ctx, req.GetVolumeId())
	if status.Code(err) == codes.NotFound {
		return &csipb.ControllerUnpublishVolumeResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	key := types.NamespacedName{Namespace: partition.Namespace, Name: attachmentidentity.Name(partition.UID)}
	var existing storagev1alpha1.NVMeVolumeAttachment
	if err := cs.k8sClient.Get(ctx, key, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			return &csipb.ControllerUnpublishVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "Failed to get volume attachment: %v", err)
	}
	if req.GetNodeId() != "" && existing.Spec.NodeID != req.GetNodeId() {
		return &csipb.ControllerUnpublishVolumeResponse{}, nil
	}
	if err := cs.k8sClient.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
		return nil, status.Errorf(codes.Internal, "Failed to delete volume attachment: %v", err)
	}
	if err := cs.waitForAttachmentDeleted(ctx, key); err != nil {
		return nil, err
	}
	return &csipb.ControllerUnpublishVolumeResponse{}, nil
}

func (cs *ControllerServer) partitionForVolumeHandle(ctx context.Context, volumeID string) (*storagev1alpha1.NVMePartition, error) {
	reference, err := volumeidentity.ParseVolumeHandle(volumeID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "Volume handle is invalid or legacy and cannot be attached: %v", err)
	}
	partition := &storagev1alpha1.NVMePartition{}
	key := types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}
	if err := cs.k8sClient.Get(ctx, key, partition); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Error(codes.NotFound, "Volume does not exist")
		}
		return nil, status.Errorf(codes.Internal, "Failed to get volume: %v", err)
	}
	if partition.UID != reference.UID {
		return nil, status.Error(codes.NotFound, "Volume no longer exists")
	}
	return partition, nil
}

func (cs *ControllerServer) attachmentTiming() (time.Duration, time.Duration) {
	poll := cs.attachmentPollInterval
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	timeout := cs.attachmentReadyTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return poll, timeout
}

func (cs *ControllerServer) waitForAttachmentReady(ctx context.Context, key types.NamespacedName, attachmentID string) error {
	return cs.pollAttachment(ctx, key, func(current *storagev1alpha1.NVMeVolumeAttachment) (bool, error) {
		if current == nil || !current.DeletionTimestamp.IsZero() {
			return false, status.Error(codes.Aborted, "Volume attachment was revoked while waiting for target authorization")
		}
		if current.Status.ObservedAttachmentID == attachmentID && meta.IsStatusConditionTrue(current.Status.Conditions, attachmentidentity.AccessReadyCondition) {
			return true, nil
		}
		return false, nil
	}, "Timed out waiting for provider target to authorize attachment")
}

func (cs *ControllerServer) waitForAttachmentDeleted(ctx context.Context, key types.NamespacedName) error {
	return cs.pollAttachment(ctx, key, func(current *storagev1alpha1.NVMeVolumeAttachment) (bool, error) {
		return current == nil, nil
	}, "Timed out waiting for provider target to revoke attachment")
}

func (cs *ControllerServer) pollAttachment(ctx context.Context, key types.NamespacedName,
	done func(*storagev1alpha1.NVMeVolumeAttachment) (bool, error), timeoutMessage string,
) error {
	poll, timeoutDuration := cs.attachmentTiming()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	timer := time.NewTimer(timeoutDuration)
	defer timer.Stop()
	for {
		var current storagev1alpha1.NVMeVolumeAttachment
		err := cs.k8sClient.Get(ctx, key, &current)
		var value *storagev1alpha1.NVMeVolumeAttachment
		if err == nil {
			value = &current
		} else if !apierrors.IsNotFound(err) {
			return status.Errorf(codes.Internal, "Failed to observe volume attachment: %v", err)
		}
		complete, err := done(value)
		if err != nil || complete {
			return err
		}
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case <-timer.C:
			return status.Error(codes.DeadlineExceeded, timeoutMessage)
		case <-ticker.C:
		}
	}
}
