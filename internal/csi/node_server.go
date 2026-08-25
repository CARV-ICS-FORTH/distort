package csi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	nodeID                 string
	connectRDMA            func(context.Context, string, string, string, string) (bool, error)
	disconnectRDMA         func(context.Context, string) error
	getDeviceByNQN         func(context.Context, string) (string, error)
	pathStat               func(string) (os.FileInfo, error)
	mkdirAll               func(string, os.FileMode) error
	removePath             func(string) error
	stageMount             func(context.Context, string, string, string) (bool, error)
	unstageMount           func(context.Context, string) error
	devicePollInterval     time.Duration
	deviceDiscoveryTimeout time.Duration
}

const (
	publishContextNodeID       = "attachedNodeID"
	publishContextHostNQN      = "hostNQN"
	publishContextAttachmentID = "attachmentID"
)

type validatedNodeStageRequest struct {
	volumeID          string
	stagingTargetPath string
	nqn               string
	portalIP          string
	portalPort        string
	hostNQN           string
	filesystem        string
}

func (ns *NodeServer) validateNodeStageRequest(req *csi.NodeStageVolumeRequest) (validatedNodeStageRequest, error) {
	if req.GetVolumeId() == "" {
		return validatedNodeStageRequest{}, status.Error(codes.InvalidArgument, "Volume ID must be provided")
	}
	stagingTargetPath := req.GetStagingTargetPath()
	if stagingTargetPath == "" || !filepath.IsAbs(stagingTargetPath) || filepath.Clean(stagingTargetPath) == "/" {
		return validatedNodeStageRequest{}, status.Error(codes.InvalidArgument, "Staging target path must be a non-root absolute path")
	}
	capability, err := validateVolumeCapabilities(req.GetVolumeContext(), []*csi.VolumeCapability{req.GetVolumeCapability()})
	if err != nil {
		return validatedNodeStageRequest{}, status.Errorf(codes.InvalidArgument, "Invalid volume capability: %v", err)
	}
	volumeContext := req.GetVolumeContext()
	nqn := strings.TrimSpace(volumeContext["nqn"])
	portalIP := strings.TrimSpace(volumeContext["portalIP"])
	portalPort := strings.TrimSpace(volumeContext["portalPort"])
	if nqn == "" {
		return validatedNodeStageRequest{}, status.Error(codes.InvalidArgument, "Volume context NQN must be provided")
	}
	parsedIP := net.ParseIP(portalIP)
	if parsedIP == nil || parsedIP.IsLoopback() || parsedIP.IsUnspecified() || parsedIP.IsMulticast() {
		return validatedNodeStageRequest{}, status.Errorf(codes.InvalidArgument, "Volume context portalIP %q is not a usable remote IP", portalIP)
	}
	port, err := strconv.Atoi(portalPort)
	if err != nil || port < 1 || port > 65535 {
		return validatedNodeStageRequest{}, status.Errorf(codes.InvalidArgument, "Volume context portalPort %q is invalid", portalPort)
	}
	publishContext := req.GetPublishContext()
	attachedNodeID := strings.TrimSpace(publishContext[publishContextNodeID])
	hostNQN := strings.TrimSpace(publishContext[publishContextHostNQN])
	attachmentID := strings.TrimSpace(publishContext[publishContextAttachmentID])
	if ns.nodeID == "" || attachedNodeID != ns.nodeID {
		return validatedNodeStageRequest{}, status.Errorf(codes.FailedPrecondition,
			"Volume attachment belongs to node %q, not this node %q", attachedNodeID, ns.nodeID)
	}
	if attachmentID == "" || hostNQN != hostNQNForNode(ns.nodeID) {
		return validatedNodeStageRequest{}, status.Error(codes.FailedPrecondition, "Volume attachment authorization is missing or invalid")
	}
	return validatedNodeStageRequest{
		volumeID:          req.GetVolumeId(),
		stagingTargetPath: filepath.Clean(stagingTargetPath),
		nqn:               nqn,
		portalIP:          portalIP,
		portalPort:        strconv.Itoa(port),
		hostNQN:           hostNQN,
		filesystem:        capability.Filesystem,
	}, nil
}

func nodeOperationError(ctx context.Context, message string, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	return status.Errorf(codes.Internal, "%s: %v", message, err)
}

func (ns *NodeServer) connect(ctx context.Context, nqn, ip, port, hostNQN string) (bool, error) {
	if ns.connectRDMA != nil {
		return ns.connectRDMA(ctx, nqn, ip, port, hostNQN)
	}
	return ConnectRDMA(ctx, nqn, ip, port, hostNQN)
}

func (ns *NodeServer) disconnect(ctx context.Context, nqn string) error {
	if ns.disconnectRDMA != nil {
		return ns.disconnectRDMA(ctx, nqn)
	}
	return DisconnectRDMA(ctx, nqn)
}

func (ns *NodeServer) findDevice(ctx context.Context, nqn string) (string, error) {
	if ns.getDeviceByNQN != nil {
		return ns.getDeviceByNQN(ctx, nqn)
	}
	return GetDeviceByNQN(ctx, nqn)
}

func (ns *NodeServer) statPath(path string) (os.FileInfo, error) {
	if ns.pathStat != nil {
		return ns.pathStat(path)
	}
	return os.Stat(path)
}

func (ns *NodeServer) createDirectory(path string, mode os.FileMode) error {
	if ns.mkdirAll != nil {
		return ns.mkdirAll(path, mode)
	}
	return os.MkdirAll(path, mode)
}

func (ns *NodeServer) remove(path string) error {
	if ns.removePath != nil {
		return ns.removePath(path)
	}
	return os.Remove(path)
}

func (ns *NodeServer) mountStage(ctx context.Context, source, target, filesystem string) (bool, error) {
	if ns.stageMount != nil {
		return ns.stageMount(ctx, source, target, filesystem)
	}
	return formatAndMount(ctx, source, target, filesystem)
}

func (ns *NodeServer) unmount(ctx context.Context, target string) error {
	if ns.unstageMount != nil {
		return ns.unstageMount(ctx, target)
	}
	return unmount(ctx, target)
}

func (ns *NodeServer) waitForDevice(ctx context.Context, path string) error {
	pollInterval := ns.devicePollInterval
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
	}
	timeoutDuration := ns.deviceDiscoveryTimeout
	if timeoutDuration <= 0 {
		timeoutDuration = 5 * time.Second
	}
	timer := time.NewTimer(timeoutDuration)
	defer timer.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if _, err := ns.statPath(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("timed out waiting for %s", path)
		case <-ticker.C:
		}
	}
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
	stage, err := ns.validateNodeStageRequest(req)
	if err != nil {
		return nil, err
	}

	klog.InfoS("Connecting to NVMe-oF target for staging", "nqn", stage.nqn, "portalIP", stage.portalIP, "portalPort", stage.portalPort)

	createdDirectory := false
	if _, statErr := ns.statPath(stage.stagingTargetPath); statErr != nil {
		if !os.IsNotExist(statErr) {
			return nil, status.Errorf(codes.Internal, "Failed to inspect staging target path: %v", statErr)
		}
		if err := ns.createDirectory(stage.stagingTargetPath, 0750); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to create staging target path: %v", err)
		}
		createdDirectory = true
	}

	createdConnection, err := ns.connect(ctx, stage.nqn, stage.portalIP, stage.portalPort, stage.hostNQN)
	if err != nil {
		if createdConnection {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_ = ns.disconnect(cleanupCtx, stage.nqn)
			cancel()
		}
		if createdDirectory {
			_ = ns.remove(stage.stagingTargetPath)
		}
		return nil, nodeOperationError(ctx, "Failed to connect to NVMe-oF target", err)
	}
	mountedByCall := false
	succeeded := false
	defer func() {
		if succeeded {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if mountedByCall {
			_ = ns.unmount(cleanupCtx, stage.stagingTargetPath)
		}
		if createdConnection {
			_ = ns.disconnect(cleanupCtx, stage.nqn)
		}
		if createdDirectory {
			_ = ns.remove(stage.stagingTargetPath)
		}
	}()

	devPath, err := ns.findDevice(ctx, stage.nqn)
	if err != nil {
		return nil, nodeOperationError(ctx, fmt.Sprintf("Failed to locate block device for NQN %s", stage.nqn), err)
	}
	if err := ns.waitForDevice(ctx, devPath); err != nil {
		return nil, nodeOperationError(ctx, fmt.Sprintf("Block device %s did not appear", devPath), err)
	}

	klog.InfoS("Mapped NQN to local block device", "nqn", stage.nqn, "devicePath", devPath)
	klog.InfoS("Staging volume", "volumeID", stage.volumeID, "devicePath", devPath, "targetPath", stage.stagingTargetPath)

	mountedByCall, err = ns.mountStage(ctx, devPath, stage.stagingTargetPath, stage.filesystem)
	if err != nil {
		var filesystemMismatch *filesystemMismatchError
		var mountMismatch *mountMismatchError
		if errors.As(err, &filesystemMismatch) || errors.As(err, &mountMismatch) {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		return nil, nodeOperationError(ctx, "Failed to format and mount", err)
	}

	succeeded = true
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

	klog.InfoS("Unstaging volume", "volumeID", volID, "targetPath", stagingTargetPath)

	// 1. Unmount the staging path
	unmountStarted := time.Now()
	if err := unmount(ctx, stagingTargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to unmount staging path: %v", err)
	}
	klog.InfoS("Unmounted staging path", "targetPath", stagingTargetPath, "duration", time.Since(unmountStarted))

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
	klog.InfoS("Disconnected NVMe target", "nqn", nqn, "duration", time.Since(disconnectStarted))
	klog.InfoS("Completed volume unstage", "volumeID", volID, "duration", time.Since(started))

	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *NodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volID := req.GetVolumeId()
	source := req.GetStagingTargetPath()
	target := req.GetTargetPath()
	if volID == "" || source == "" || target == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID, staging target path, and target path must be provided")
	}
	if _, err := validateVolumeCapabilities(req.GetVolumeContext(), []*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid volume capability: %v", err)
	}

	klog.InfoS("Publishing volume", "volumeID", volID, "sourcePath", source, "targetPath", target)

	if err := publishBindMount(ctx, source, target, req.GetReadonly()); err != nil {
		var mismatch *mountMismatchError
		if errors.As(err, &mismatch) {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		return nil, nodeOperationError(ctx, "Failed to bind mount", err)
	}

	klog.InfoS("Bind mounted volume", "sourcePath", source, "targetPath", target)
	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *NodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	started := time.Now()
	volID := req.GetVolumeId()
	target := req.GetTargetPath()
	if volID == "" || target == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID and target path must be provided")
	}

	klog.InfoS("Unpublishing volume", "volumeID", volID, "targetPath", target)

	if err := unmount(ctx, target); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to unmount target path: %v", err)
	}

	klog.InfoS("Unmounted published volume", "targetPath", target, "duration", time.Since(started))
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func isMountPoint(target string) (bool, error) {
	record, err := mountAt(target)
	return record != nil, err
}

type filesystemMismatchError struct {
	source    string
	requested string
	detected  string
}

func (e *filesystemMismatchError) Error() string {
	return fmt.Sprintf("Block device %s contains %s, but %s was requested; refusing to format or mount it", e.source, e.detected, e.requested)
}

type mountMismatchError struct{ message string }

func (e *mountMismatchError) Error() string { return e.message }

func formatAndMount(ctx context.Context, source, target, fsType string) (bool, error) {
	if err := os.MkdirAll(target, 0750); err != nil {
		return false, err
	}

	mounted, err := verifyStagingMount(source, target, fsType)
	if err != nil {
		return false, &mountMismatchError{message: err.Error()}
	}
	if mounted {
		klog.InfoS("Target is already mounted from expected block device", "targetPath", target)
		return false, nil
	}

	if err := ensureFilesystemContext(ctx, source, fsType, probeFilesystem, formatFilesystem); err != nil {
		return false, err
	}

	klog.InfoS("Mounting filesystem", "sourcePath", source, "filesystem", fsType, "targetPath", target)
	mountCmd := exec.CommandContext(ctx, "mount", "-t", fsType, source, target)
	if out, mountErr := mountCmd.CombinedOutput(); mountErr != nil {
		if ctx.Err() != nil {
			return false, fmt.Errorf("mount interrupted: %w", ctx.Err())
		}
		return false, fmt.Errorf("mount failed: %v, output: %s", mountErr, string(out))
	}
	mounted, err = verifyStagingMount(source, target, fsType)
	if err != nil || !mounted {
		_ = unmount(ctx, target)
		if err == nil {
			err = fmt.Errorf("target is not mounted after mount command succeeded")
		}
		return false, &mountMismatchError{message: err.Error()}
	}
	return true, nil
}

func ensureFilesystem(source, requested string, probe func(string) (string, error), format func(string, string) error) error {
	return ensureFilesystemContext(context.Background(), source, requested,
		func(_ context.Context, path string) (string, error) { return probe(path) },
		func(_ context.Context, path, filesystem string) error { return format(path, filesystem) })
}

func ensureFilesystemContext(ctx context.Context, source, requested string,
	probe func(context.Context, string) (string, error), format func(context.Context, string, string) error,
) error {
	detected, err := probe(ctx, source)
	if err != nil {
		return fmt.Errorf("probing filesystem on %s: %w", source, err)
	}
	if detected == "" {
		return format(ctx, source, requested)
	}
	if normalizeFilesystem(detected) != requested {
		return &filesystemMismatchError{source: source, requested: requested, detected: detected}
	}
	klog.InfoS("Block device already contains a filesystem", "sourcePath", source, "filesystem", detected)
	return nil
}

func probeFilesystem(ctx context.Context, source string) (string, error) {
	cmd := exec.CommandContext(ctx, "blkid", "-p", "-s", "TYPE", "-o", "value", source)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return normalizeFilesystem(string(out)), nil
	}

	var exitErr *exec.ExitError
	if ctx.Err() != nil {
		return "", fmt.Errorf("blkid interrupted: %w", ctx.Err())
	}
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
		return "", nil
	}
	return "", fmt.Errorf("blkid failed: %w, output: %s", err, strings.TrimSpace(string(out)))
}

func formatFilesystem(ctx context.Context, source, fsType string) error {
	command, args, err := filesystemFormatCommand(source, fsType)
	if err != nil {
		return err
	}

	klog.InfoS("Formatting block device", "sourcePath", source, "filesystem", fsType)
	if out, err := exec.CommandContext(ctx, command, args...).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("formatting interrupted: %w", ctx.Err())
		}
		return fmt.Errorf("formatting %s as %s failed: %w, output: %s", source, fsType, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func filesystemFormatCommand(source, fsType string) (string, []string, error) {
	switch fsType {
	case defaultFilesystem:
		return "mkfs.ext4", []string{"-F", source}, nil
	case xfsFilesystem:
		return "mkfs.xfs", []string{"-f", source}, nil
	default:
		return "", nil, fmt.Errorf("unsupported filesystem %q", fsType)
	}
}

func bindMount(source, target string) error {
	return publishBindMount(context.Background(), source, target, false)
}

func publishBindMount(ctx context.Context, source, target string, readOnly bool) error {
	if err := os.MkdirAll(target, 0750); err != nil {
		return err
	}

	mounted, err := verifyPublishedMount(source, target, readOnly)
	if err != nil {
		return &mountMismatchError{message: err.Error()}
	}
	if mounted {
		klog.InfoS("Target is already bind mounted from expected source", "targetPath", target)
		return nil
	}
	cmd := exec.CommandContext(ctx, "mount", "--bind", source, target)
	if out, mountErr := cmd.CombinedOutput(); mountErr != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("bind mount interrupted: %w", ctx.Err())
		}
		return fmt.Errorf("bind mount failed: %v, output: %s", mountErr, string(out))
	}
	if readOnly {
		cmd = exec.CommandContext(ctx, "mount", "-o", "remount,bind,ro", target)
		if out, remountErr := cmd.CombinedOutput(); remountErr != nil {
			_ = unmount(ctx, target)
			if ctx.Err() != nil {
				return fmt.Errorf("read-only bind remount interrupted: %w", ctx.Err())
			}
			return fmt.Errorf("read-only bind remount failed: %v, output: %s", remountErr, string(out))
		}
	}
	mounted, err = verifyPublishedMount(source, target, readOnly)
	if err != nil || !mounted {
		_ = unmount(ctx, target)
		if err == nil {
			err = fmt.Errorf("target is not mounted after bind command succeeded")
		}
		return &mountMismatchError{message: err.Error()}
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
			klog.InfoS("Target is not mounted", "targetPath", target)
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
