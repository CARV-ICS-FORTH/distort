package csi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagev1alpha1 "distort/api/v1alpha1"
)

// ControllerServer implements the CSI Controller interface.
// It translates CreateVolume calls into NVMePartition CRDs.
type ControllerServer struct {
	csi.UnimplementedControllerServer
	k8sClient client.Client
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
		if targetBackend == "spdk" || targetBackend == "bxi" {
			volumeManager = "spdk"
		} else {
			volumeManager = "partition"
		}
	}
	targetOptions := make(map[string]string)
	for k, v := range req.GetParameters() {
		if strings.HasPrefix(k, "spdk-") || strings.HasPrefix(k, "bxi-") || strings.HasPrefix(k, "portals-") || strings.HasPrefix(k, "rdma-") || k == "spdk-core-mask" || k == "port" {
			targetOptions[k] = v
		}
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

	err := cs.k8sClient.Create(ctx, partition)
	if err != nil {
		if client.IgnoreAlreadyExists(err) != nil {
			klog.Errorf("Failed to create NVMePartition CRD: %v", err)
			return nil, status.Errorf(codes.Internal, "failed to create partition: %v", err)
		}
		// If it already exists, just retrieve it
		err = cs.k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, partition)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get existing partition: %v", err)
		}
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

	// Construct context for the Node Stage/Publish calls
	volCtx := map[string]string{
		"nqn":            partition.Status.NQN,
		"portalIP":       partition.Status.PortalIP,
		"portalPort":     fmt.Sprintf("%d", partition.Status.PortalPort),
		"target-backend": targetBackend,
	}

	// Propagate StorageClass parameters to the VolumeContext for the Node plugin
	for k, v := range req.GetParameters() {
		if _, exists := volCtx[k]; !exists {
			volCtx[k] = v
		}
	}

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      name,
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

	var pList storagev1alpha1.NVMePartitionList
	if err := cs.k8sClient.List(ctx, &pList); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list partitions: %v", err)
	}

	for _, p := range pList.Items {
		if p.Name == volID {
			if err := cs.k8sClient.Delete(ctx, &p); err != nil && client.IgnoreNotFound(err) != nil {
				klog.Errorf("Failed to delete NVMePartition %s: %v", volID, err)
				return nil, status.Errorf(codes.Internal, "failed to delete partition: %v", err)
			}
			return &csi.DeleteVolumeResponse{}, nil
		}
	}

	// Note: We might want to wait for actual deletion if we use finalizers in the Agent
	return &csi.DeleteVolumeResponse{}, nil
}

func (cs *ControllerServer) waitForPartitionReady(ctx context.Context, name, namespace string) error {
	// Simple polling for now
	timeout := time.After(2 * time.Minute)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
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
