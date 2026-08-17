package csi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/storageoptions"
	"distort/internal/volumeidentity"
)

// ControllerServer implements the CSI Controller interface.
// It translates CreateVolume calls into NVMePartition CRDs.
type ControllerServer struct {
	csi.UnimplementedControllerServer
	k8sClient                  client.Client
	partitionReadyPollInterval time.Duration
	partitionReadyTimeout      time.Duration
}

func (cs *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "Name cannot be empty")
	}

	caps := req.GetVolumeCapabilities()
	if len(caps) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume Capabilities cannot be empty")
	}
	fsType, err := resolveFilesystem(req.GetParameters(), caps)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid filesystem configuration: %v", err)
	}

	requiredBytes := req.GetCapacityRange().GetRequiredBytes()
	if requiredBytes == 0 {
		// Default to 1Gi if not specified, though usually the provisioner provides it
		requiredBytes = 1024 * 1024 * 1024
	}

	klog.Infof("CreateVolume: Name=%s, SizeBytes=%d", name, requiredBytes)

	ns := req.GetParameters()["csi.storage.k8s.io/pvc/namespace"]
	if ns == "" {
		ns = "default"
	}

	targetBackend := req.GetParameters()["target-backend"]
	if targetBackend == "" {
		targetBackend = "spdk"
	}
	volumeManager := req.GetParameters()["volume-manager"]
	if volumeManager == "" {
		volumeManager = "partition"
	}
	targetOptions := make(map[string]string)
	// csi.storage.k8s.io/* keys are injected by the provisioner sidecar and are not for backends.
	for k, v := range req.GetParameters() {
		if !strings.HasPrefix(k, "csi.storage.k8s.io/") && k != "target-backend" && k != "volume-manager" && k != filesystemParameter {
			targetOptions[k] = v
		}
	}
	if err := storageoptions.Validate(targetBackend, targetOptions); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid backend options: %v", err)
	}

	// Create NVMePartition CRD
	partition := &storagev1alpha1.NVMePartition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: storagev1alpha1.NVMePartitionSpec{
			Size:          *resource.NewQuantity(requiredBytes, resource.BinarySI),
			TargetBackend: targetBackend,
			VolumeManager: volumeManager,
			TargetOptions: targetOptions,
			// NodeName is intentionally omitted here; the mutating scheduler (Mgmt-Controller) handles it.
		},
	}

	err = cs.k8sClient.Create(ctx, partition)
	if err != nil {
		if client.IgnoreAlreadyExists(err) != nil {
			klog.Errorf("Failed to create NVMePartition CRD: %v", err)
			return nil, status.Errorf(codes.Internal, "failed to create partition: %v", err)
		}
		// Partition already exists — retrieve it and verify it matches the requested backend.
		// This prevents silently reusing a stale partition created with a different backend.
		existing := &storagev1alpha1.NVMePartition{}
		if err = cs.k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, existing); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get existing partition: %v", err)
		}
		if existing.Spec.TargetBackend != targetBackend {
			klog.Errorf("Existing NVMePartition %s has backend %q but %q was requested", name, existing.Spec.TargetBackend, targetBackend)
			return nil, status.Errorf(codes.AlreadyExists,
				"partition %s already exists with backend %q; delete the existing partition or PVC first",
				name, existing.Spec.TargetBackend)
		}
		partition = existing
	}

	// Wait for the partition to be Exported
	// The Mgmt-Controller schedules it -> The Agent slices and exports it.
	klog.Infof("Waiting for NVMePartition %s in namespace %s to be Exported...", name, ns)
	err = cs.waitForPartitionReady(ctx, name, ns)
	if err != nil {
		return nil, status.Errorf(codes.DeadlineExceeded, "partition failed to become ready: %v", err)
	}

	// Fetch the updated partition with NQN and Portal IPs
	err = cs.k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, partition)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get final partition status: %v", err)
	}
	volumeID := partition.Status.VolumeID
	if volumeID == "" {
		identity, identityErr := volumeidentity.New(partition.Namespace, partition.Name, partition.UID)
		if identityErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to derive partition identity: %v", identityErr)
		}
		volumeID = identity.VolumeHandle
	}

	// Construct context for the Node Stage/Publish calls
	volCtx := map[string]string{
		"nqn":               partition.Status.NQN,
		"portalIP":          partition.Status.PortalIP,
		"portalPort":        fmt.Sprintf("%d", partition.Status.PortalPort),
		filesystemParameter: fsType,
	}

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: requiredBytes,
			VolumeContext: volCtx,
		},
	}, nil
}

func (cs *ControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	volID := req.GetVolumeId()
	if volID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	klog.Infof("DeleteVolume: ID=%s", volID)

	reference, err := volumeidentity.ParseVolumeHandle(volID)
	if err != nil {
		return cs.deleteLegacyVolume(ctx, volID)
	}

	partition := &storagev1alpha1.NVMePartition{}
	key := types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}
	if err := cs.k8sClient.Get(ctx, key, partition); err != nil {
		if apierrors.IsNotFound(err) {
			return &csi.DeleteVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "failed to get partition: %v", err)
	}
	if partition.UID != reference.UID {
		// The volume represented by this handle is already gone. Never delete a
		// replacement object which happens to reuse its namespace and name.
		return &csi.DeleteVolumeResponse{}, nil
	}
	if err := cs.k8sClient.Delete(ctx, partition); err != nil && client.IgnoreNotFound(err) != nil {
		klog.Errorf("Failed to delete NVMePartition %s: %v", key, err)
		return nil, status.Errorf(codes.Internal, "failed to delete partition: %v", err)
	}

	// Note: We might want to wait for actual deletion if we use finalizers in the Agent
	return &csi.DeleteVolumeResponse{}, nil
}

func (cs *ControllerServer) deleteLegacyVolume(ctx context.Context, volumeID string) (*csi.DeleteVolumeResponse, error) {
	// Releases before globally unique handles stored only metadata.name in PVs.
	// Preserve upgrade cleanup without repeating the old unsafe first-match
	// behavior: delete only a single object whose persisted backend identity is
	// also the legacy name, and refuse an ambiguous request.
	var partitions storagev1alpha1.NVMePartitionList
	if err := cs.k8sClient.List(ctx, &partitions); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resolve legacy volume ID: %v", err)
	}
	matches := make([]*storagev1alpha1.NVMePartition, 0, 1)
	for index := range partitions.Items {
		partition := &partitions.Items[index]
		if partition.Name != volumeID {
			continue
		}
		externalID := partition.Status.ExternalID
		if externalID == "" {
			externalID, _ = volumeidentity.ExternalIDFromNQN(partition.Status.NQN)
		}
		if externalID == "" || externalID == volumeID {
			matches = append(matches, partition)
		}
	}
	if len(matches) == 0 {
		return &csi.DeleteVolumeResponse{}, nil
	}
	if len(matches) > 1 {
		return nil, status.Errorf(codes.FailedPrecondition,
			"legacy volume ID %q matches multiple partitions; refusing ambiguous deletion", volumeID)
	}
	if err := cs.k8sClient.Delete(ctx, matches[0]); err != nil && client.IgnoreNotFound(err) != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete legacy partition: %v", err)
	}
	return &csi.DeleteVolumeResponse{}, nil
}

func (cs *ControllerServer) waitForPartitionReady(ctx context.Context, name, namespace string) error {
	pollInterval := cs.partitionReadyPollInterval
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}
	readyTimeout := cs.partitionReadyTimeout
	if readyTimeout <= 0 {
		readyTimeout = 2 * time.Minute
	}

	timeout := time.NewTimer(readyTimeout)
	defer timeout.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-timeout.C:
			return fmt.Errorf("timeout waiting for NVMePartition %s", name)
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			var p storagev1alpha1.NVMePartition
			if err := cs.k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &p); err == nil {
				if p.Status.State == storagev1alpha1.NVMePartitionStateExported {
					return nil // Ready
				}
				if p.Status.State == storagev1alpha1.NVMePartitionStateFailed {
					return fmt.Errorf("partition creation failed")
				}
			}
		}
	}
}

// ControllerGetCapabilities declares our supported capabilities (Create/Delete volume)
func (cs *ControllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: []*csi.ControllerServiceCapability{
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					},
				},
			},
		},
	}, nil
}
