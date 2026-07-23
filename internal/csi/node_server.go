package csi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	for range 10 {
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
	started := time.Now()
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
	unmountStarted := time.Now()
	if err := unmount(ctx, stagingTargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to unmount staging path: %v", err)
	}
	klog.Infof("Unmounted staging path %s in %s", stagingTargetPath, time.Since(unmountStarted))

	// 2. Execute `nvme disconnect -n <NQN>`
	nqn := "nqn.2026-02.io.distort:volume-" + volID

	disconnectStarted := time.Now()
	if err := DisconnectRDMA(ctx, nqn); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to disconnect NVMe target: %v", err)
	}
	klog.Infof("Disconnected NVMe target %s in %s", nqn, time.Since(disconnectStarted))
	klog.Infof("NodeUnstageVolume completed for %s in %s", volID, time.Since(started))

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
	started := time.Now()
	volID := req.GetVolumeId()
	target := req.GetTargetPath()

	klog.Infof("NodeUnpublishVolume: ID=%s Target=%s", volID, target)

	if err := unmount(ctx, target); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to unmount target path: %v", err)
	}

	klog.Infof("NodeUnpublishVolume unmounted %s in %s", target, time.Since(started))
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func isMountPoint(target string) (bool, error) {
	target = filepath.Clean(target)
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		data, err = os.ReadFile("/proc/mounts")
		if err != nil {
			return false, err
		}
	}
	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 5 {
			mountPoint := filepath.Clean(fields[4])
			if mountPoint == target {
				return true, nil
			}
		}
	}
	return false, nil
}

func formatAndMount(source, target string) error {
	_ = os.MkdirAll(target, 0750)

	mounted, err := isMountPoint(target)
	if err == nil && mounted {
		klog.Infof("Target %s is already mounted", target)
		return nil
	}

	// Check if source block device is already formatted
	cmd := exec.Command("blkid", source)
	if err := cmd.Run(); err != nil {
		klog.Infof("Formatting %s as ext4", source)
		mkfsCmd := exec.Command("mkfs.ext4", "-F", source)
		if out, mkfsErr := mkfsCmd.CombinedOutput(); mkfsErr != nil {
			return fmt.Errorf("formatting failed: %v, output: %s", mkfsErr, string(out))
		}
	}

	klog.Infof("Mounting %s to %s", source, target)
	mountCmd := exec.Command("mount", source, target)
	if out, mountErr := mountCmd.CombinedOutput(); mountErr != nil {
		if strings.Contains(string(out), "already mounted") {
			return nil
		}
		return fmt.Errorf("mount failed: %v, output: %s", mountErr, string(out))
	}
	return nil
}

func bindMount(source, target string) error {
	_ = os.MkdirAll(target, 0750)

	mounted, err := isMountPoint(target)
	if err == nil && mounted {
		klog.Infof("Target %s is already bind mounted", target)
		return nil
	}

	cmd := exec.Command("mount", "--bind", source, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "already mounted") {
			return nil
		}
		return fmt.Errorf("bind mount failed: %v, output: %s", err, string(out))
	}
	return nil
}

func unmount(ctx context.Context, target string) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("unmount interrupted: %w", err)
		}

		mounted, err := isMountPoint(target)
		if err != nil {
			return fmt.Errorf("checking mount point: %w", err)
		}
		if !mounted {
			klog.Infof("Target %s is not mounted", target)
			return nil
		}

		cmd := exec.CommandContext(ctx, "umount", target)
		if out, err := cmd.CombinedOutput(); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("unmount interrupted: %w", ctx.Err())
			}
			if strings.Contains(string(out), "not mounted") || strings.Contains(string(out), "no mount point specified") {
				return nil
			}
			return fmt.Errorf("unmount failed: %v, output: %s", err, string(out))
		}
	}
}
