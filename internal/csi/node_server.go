package csi

import (
	"context"
	"errors"
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

	"distort/internal/volumeidentity"
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

	fsType, err := resolveFilesystem(volCtx, []*csi.VolumeCapability{req.GetVolumeCapability()})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid filesystem configuration: %v", err)
	}

	if err := formatAndMount(devPath, stagingTargetPath, fsType); err != nil {
		var mismatch *filesystemMismatchError
		if errors.As(err, &mismatch) {
			return nil, status.Error(codes.FailedPrecondition, mismatch.Error())
		}
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

	// 2. Execute `nvme disconnect -n <NQN>`. New handles carry the immutable
	// partition UID; retain the legacy fallback for already-published volumes.
	nqn := volumeidentity.NQN(volID)
	if reference, err := volumeidentity.ParseVolumeHandle(volID); err == nil {
		identity, identityErr := volumeidentity.New(reference.Namespace, reference.Name, reference.UID)
		if identityErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "Invalid volume identity: %v", identityErr)
		}
		nqn = volumeidentity.NQN(identity.ExternalID)
	}

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

type filesystemMismatchError struct {
	source    string
	requested string
	detected  string
}

func (e *filesystemMismatchError) Error() string {
	return fmt.Sprintf("Block device %s contains %s, but %s was requested; refusing to format or mount it", e.source, e.detected, e.requested)
}

func formatAndMount(source, target, fsType string) error {
	_ = os.MkdirAll(target, 0750)

	mounted, err := isMountPoint(target)
	if err == nil && mounted {
		klog.Infof("Target %s is already mounted", target)
		return nil
	}

	if err := ensureFilesystem(source, fsType, probeFilesystem, formatFilesystem); err != nil {
		return err
	}

	klog.Infof("Mounting %s as %s to %s", source, fsType, target)
	mountCmd := exec.Command("mount", "-t", fsType, source, target)
	if out, mountErr := mountCmd.CombinedOutput(); mountErr != nil {
		if strings.Contains(string(out), "already mounted") {
			return nil
		}
		return fmt.Errorf("mount failed: %v, output: %s", mountErr, string(out))
	}
	return nil
}

func ensureFilesystem(source, requested string, probe func(string) (string, error), format func(string, string) error) error {
	detected, err := probe(source)
	if err != nil {
		return fmt.Errorf("probing filesystem on %s: %w", source, err)
	}
	if detected == "" {
		return format(source, requested)
	}
	if normalizeFilesystem(detected) != requested {
		return &filesystemMismatchError{source: source, requested: requested, detected: detected}
	}
	klog.Infof("Block device %s already contains %s", source, detected)
	return nil
}

func probeFilesystem(source string) (string, error) {
	cmd := exec.Command("blkid", "-p", "-s", "TYPE", "-o", "value", source)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return normalizeFilesystem(string(out)), nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
		return "", nil
	}
	return "", fmt.Errorf("blkid failed: %w, output: %s", err, strings.TrimSpace(string(out)))
}

func formatFilesystem(source, fsType string) error {
	command, args, err := filesystemFormatCommand(source, fsType)
	if err != nil {
		return err
	}

	klog.Infof("Formatting %s as %s", source, fsType)
	if out, err := exec.Command(command, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("formatting %s as %s failed: %w, output: %s", source, fsType, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func filesystemFormatCommand(source, fsType string) (string, []string, error) {
	switch fsType {
	case "ext4":
		return "mkfs.ext4", []string{"-F", source}, nil
	case "xfs":
		return "mkfs.xfs", []string{"-f", source}, nil
	default:
		return "", nil, fmt.Errorf("unsupported filesystem %q", fsType)
	}
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
