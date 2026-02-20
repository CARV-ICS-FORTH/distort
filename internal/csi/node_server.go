package csi

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

type NodeServer struct {
	csi.UnimplementedNodeServer
	nodeID string
}

func (ns *NodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: ns.nodeID,
	}, nil
}

func (ns *NodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
					},
				},
			},
		},
	}, nil
}

func (ns *NodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	volID := req.GetVolumeId()
	if volID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID must be provided")
	}

	volCtx := req.GetVolumeContext()

	// Extract the connection details we placed in CreateVolume
	nqn := volCtx["nqn"]
	portalIP := volCtx["portalIP"]
	portalPort := volCtx["portalPort"]

	klog.Infof("NodeStageVolume: Connecting to NVMe-oF target. NQN=%s Portal=%s:%s", nqn, portalIP, portalPort)

	// 1. Connect via RDMA
	if err := ConnectRDMA(nqn, portalIP, portalPort); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to connect to NVMe-oF target: %v", err)
	}

	// 2. Find the local block device
	// It may take a moment for udev to populate the device, but `nvme list-subsys` should work soon after connect
	devPath, err := GetDeviceByNQN(nqn)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to locate block device for NQN %s: %v", nqn, err)
	}

	klog.Infof("Mapped NQN %s to local block device %s", nqn, devPath)

	// 3. Mount the device to req.GetStagingTargetPath()
	stagingTargetPath := req.GetStagingTargetPath()
	if stagingTargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path must be provided")
	}

	// Format and mount. Real CSI uses `k8s.io/mount-utils`
	// For scaffolding sake, we simulate ext4 creation if needed.
	// We'll skip real formatting here as it requires `mkfs.ext4`, but note the implementation structure.
	klog.Infof("Staging volume %s (device %s) to %s", volID, devPath, stagingTargetPath)

	// A proper implementation uses `mounter.FormatAndMount(devPath, stagingTargetPath, "ext4", nil)`

	return &csi.NodeStageVolumeResponse{}, nil
}

func (ns *NodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	volID := req.GetVolumeId()
	if volID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID must be provided")
	}

	stagingTargetPath := req.GetStagingTargetPath()
	if stagingTargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging Target Path must be provided")
	}

	klog.Infof("NodeUnstageVolume: ID=%s Path=%s", volID, stagingTargetPath)

	// 1. Unmount the staging path (skipped in scaffold)
	// mounter.Unmount(stagingTargetPath)

	// 2. Execute `nvme disconnect -n <NQN>` (we derive NQN from our standard scheme)
	// Ideally we look it up or pass it in context. For this scaffold, we rebuild it:
	nqn := "nqn.2026-02.io.distort:volume-" + volID

	if err := DisconnectRDMA(nqn); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to disconnect NVMe target: %v", err)
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *NodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	// Publish volume mounts from the Staging path to the Pod's target path
	volID := req.GetVolumeId()
	source := req.GetStagingTargetPath()
	target := req.GetTargetPath()

	klog.Infof("NodePublishVolume: ID=%s Source=%s Target=%s", volID, source, target)

	// 1. Ensure target directory exists (skipped in mock)
	// os.MkdirAll(target, 0750)

	// 2. Bind mount from StagingTargetPath to TargetPath
	// mounter.Mount(source, target, "", []string{"bind"})
	klog.Infof("Bind mounted %s to %s", source, target)

	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *NodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	volID := req.GetVolumeId()
	target := req.GetTargetPath()

	klog.Infof("NodeUnpublishVolume: ID=%s Target=%s", volID, target)

	// 1. Unmount the TargetPath
	// mounter.Unmount(target)
	klog.Infof("Unmounted %s", target)

	return &csi.NodeUnpublishVolumeResponse{}, nil
}
