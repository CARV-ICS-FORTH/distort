package csi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

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

	// Wait for udev to create the block device node
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(devPath); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if _, err := os.Stat(devPath); err != nil {
		return nil, status.Errorf(codes.Internal, "Block device %s did not appear in time: %v", devPath, err)
	}

	klog.Infof("Mapped NQN %s to local block device %s", nqn, devPath)

	// 3. Mount the device to req.GetStagingTargetPath()
	stagingTargetPath := req.GetStagingTargetPath()
	if stagingTargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path must be provided")
	}

	klog.Infof("Staging volume %s (device %s) to %s", volID, devPath, stagingTargetPath)

	if err := formatAndMount(devPath, stagingTargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to format and mount: %v", err)
	}

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

	// 1. Unmount the staging path
	if err := unmount(stagingTargetPath); err != nil {
		klog.Warningf("Failed to unmount staging path: %v", err)
	}

	// 2. Execute `nvme disconnect -n <NQN>`
	nqn := "nqn.2026-02.io.distort:volume-" + volID

	if err := DisconnectRDMA(nqn); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to disconnect NVMe target: %v", err)
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *NodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volID := req.GetVolumeId()
	source := req.GetStagingTargetPath()
	target := req.GetTargetPath()

	klog.Infof("NodePublishVolume: ID=%s Source=%s Target=%s", volID, source, target)

	if err := bindMount(source, target); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to bind mount: %v", err)
	}

	klog.Infof("Bind mounted %s to %s", source, target)
	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *NodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	volID := req.GetVolumeId()
	target := req.GetTargetPath()

	klog.Infof("NodeUnpublishVolume: ID=%s Target=%s", volID, target)

	if err := unmount(target); err != nil {
		klog.Warningf("Failed to unmount target path: %v", err)
	}

	klog.Infof("Unmounted %s", target)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func formatAndMount(source, target string) error {
	os.MkdirAll(target, 0750)

	// Check if it's already mounted by trying to mount it.
	klog.Infof("Trying to mount %s to %s", source, target)
	cmd := exec.Command("mount", source, target)
	if out, err := cmd.CombinedOutput(); err == nil {
		return nil // Successfully mounted, was already formatted
	} else if strings.Contains(string(out), "already mounted") {
		return nil // Already mounted
	}

	// Formatting
	klog.Infof("Formatting %s as ext4", source)
	cmd = exec.Command("mkfs.ext4", "-F", source)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("formatting failed: %v, output: %s", err, string(out))
	}

	// Mount again
	cmd = exec.Command("mount", source, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount failed: %v, output: %s", err, string(out))
	}
	return nil
}

func bindMount(source, target string) error {
	os.MkdirAll(target, 0750)
	cmd := exec.Command("mount", "--bind", source, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "already mounted") {
			return nil
		}
		return fmt.Errorf("bind mount failed: %v, output: %s", err, string(out))
	}
	return nil
}

func unmount(target string) error {
	cmd := exec.Command("umount", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "not mounted") {
			return nil
		}
		return fmt.Errorf("unmount failed: %v, output: %s", err, string(out))
	}
	return nil
}
