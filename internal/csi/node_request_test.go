package csi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"distort/internal/volumeidentity"
)

func TestNodeIdentityAndCapabilities(t *testing.T) {
	server := &NodeServer{nodeID: "distort-worker-1"}
	info, err := server.NodeGetInfo(context.Background(), &csipb.NodeGetInfoRequest{})
	if err != nil {
		t.Fatalf("NodeGetInfo returned error: %v", err)
	}
	if info.NodeId != "distort-worker-1" {
		t.Fatalf("NodeId = %q", info.NodeId)
	}
	caps, err := server.NodeGetCapabilities(context.Background(), &csipb.NodeGetCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("NodeGetCapabilities returned error: %v", err)
	}
	if len(caps.Capabilities) != 1 || caps.Capabilities[0].GetRpc().GetType() != csipb.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME {
		t.Fatalf("unexpected node capabilities: %#v", caps.Capabilities)
	}
}

func TestNodeUnstageAndUnpublishValidateRequiredFields(t *testing.T) {
	server := &NodeServer{}
	tests := []struct {
		name string
		call func() error
	}{
		{name: "unstage volume ID", call: func() error {
			_, err := server.NodeUnstageVolume(context.Background(), &csipb.NodeUnstageVolumeRequest{StagingTargetPath: t.TempDir()})
			return err
		}},
		{name: "unstage path", call: func() error {
			_, err := server.NodeUnstageVolume(context.Background(), &csipb.NodeUnstageVolumeRequest{VolumeId: "volume"})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireCode(t, test.call(), codes.InvalidArgument)
		})
	}
}

func TestNodeOperationsRejectUnsafePathsBeforeSideEffects(t *testing.T) {
	invalidPaths := []struct {
		name string
		path string
	}{
		{name: "missing", path: ""},
		{name: "relative", path: "var/lib/kubelet/volume"},
		{name: "root", path: "/"},
		{name: "parent traversal", path: "/var/lib/kubelet/pods/../plugins/volume"},
		{name: "trailing separator", path: "/var/lib/kubelet/volume/"},
		{name: "null byte", path: "/var/lib/kubelet/volume\x00suffix"},
	}
	for _, invalid := range invalidPaths {
		t.Run(invalid.name, func(t *testing.T) {
			operations := 0
			server := &NodeServer{
				publishMount: func(context.Context, string, string, bool) error {
					operations++
					return nil
				},
				unstageMount: func(context.Context, string) error {
					operations++
					return nil
				},
				disconnectRDMA: func(context.Context, string) error {
					operations++
					return nil
				},
			}
			validPath := filepath.Join(t.TempDir(), "volume")
			calls := []struct {
				name string
				call func() error
			}{
				{name: "unstage", call: func() error {
					_, err := server.NodeUnstageVolume(context.Background(), &csipb.NodeUnstageVolumeRequest{
						VolumeId: "volume", StagingTargetPath: invalid.path,
					})
					return err
				}},
				{name: "publish source", call: func() error {
					_, err := server.NodePublishVolume(context.Background(), &csipb.NodePublishVolumeRequest{
						VolumeId: "volume", StagingTargetPath: invalid.path, TargetPath: validPath,
						VolumeCapability: mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					})
					return err
				}},
				{name: "publish target", call: func() error {
					_, err := server.NodePublishVolume(context.Background(), &csipb.NodePublishVolumeRequest{
						VolumeId: "volume", StagingTargetPath: validPath, TargetPath: invalid.path,
						VolumeCapability: mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					})
					return err
				}},
				{name: "unpublish", call: func() error {
					_, err := server.NodeUnpublishVolume(context.Background(), &csipb.NodeUnpublishVolumeRequest{
						VolumeId: "volume", TargetPath: invalid.path,
					})
					return err
				}},
			}
			for _, call := range calls {
				t.Run(call.name, func(t *testing.T) {
					requireCode(t, call.call(), codes.InvalidArgument)
				})
			}
			if operations != 0 {
				t.Fatalf("invalid path reached %d mount or NVMe operations", operations)
			}
		})
	}
}

func TestNodeOperationsAcceptCanonicalPaths(t *testing.T) {
	var publishedSource, publishedTarget, disconnectedNQN string
	var unmountedTargets []string
	server := &NodeServer{
		publishMount: func(_ context.Context, source, target string, _ bool) error {
			publishedSource, publishedTarget = source, target
			return nil
		},
		unstageMount: func(_ context.Context, target string) error {
			unmountedTargets = append(unmountedTargets, target)
			return nil
		},
		disconnectRDMA: func(_ context.Context, nqn string) error {
			disconnectedNQN = nqn
			return nil
		},
	}
	stagingPath := filepath.Join(t.TempDir(), "staging")
	targetPath := filepath.Join(t.TempDir(), "published")
	if _, err := server.NodePublishVolume(context.Background(), &csipb.NodePublishVolumeRequest{
		VolumeId: "volume", StagingTargetPath: stagingPath, TargetPath: targetPath,
		VolumeCapability: mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
	}); err != nil {
		t.Fatalf("NodePublishVolume: %v", err)
	}
	if _, err := server.NodeUnpublishVolume(context.Background(), &csipb.NodeUnpublishVolumeRequest{
		VolumeId: "volume", TargetPath: targetPath,
	}); err != nil {
		t.Fatalf("NodeUnpublishVolume: %v", err)
	}
	if _, err := server.NodeUnstageVolume(context.Background(), &csipb.NodeUnstageVolumeRequest{
		VolumeId: "volume", StagingTargetPath: stagingPath,
	}); err != nil {
		t.Fatalf("NodeUnstageVolume: %v", err)
	}
	if publishedSource != stagingPath || publishedTarget != targetPath || len(unmountedTargets) != 2 ||
		unmountedTargets[0] != targetPath || unmountedTargets[1] != stagingPath {
		t.Fatalf("unexpected validated paths: publish=%q->%q unmounted=%q",
			publishedSource, publishedTarget, unmountedTargets)
	}
	if disconnectedNQN != volumeidentity.NQN("volume") {
		t.Fatalf("disconnected NQN = %q", disconnectedNQN)
	}
}

func TestNodeStageValidatesEverythingBeforeConnecting(t *testing.T) {
	connectCalls := 0
	server := &NodeServer{
		nodeID: "distort-worker-1",
		connectRDMA: func(context.Context, string, string, string, string) (bool, error) {
			connectCalls++
			return true, nil
		},
	}
	requests := []*csipb.NodeStageVolumeRequest{
		{VolumeId: "volume", VolumeContext: map[string]string{}},
		func() *csipb.NodeStageVolumeRequest {
			req := validNodeStageRequest(t, server.nodeID)
			req.StagingTargetPath = "relative/path"
			return req
		}(),
		func() *csipb.NodeStageVolumeRequest {
			req := validNodeStageRequest(t, server.nodeID)
			req.StagingTargetPath = "/"
			return req
		}(),
		func() *csipb.NodeStageVolumeRequest {
			req := validNodeStageRequest(t, server.nodeID)
			req.StagingTargetPath = "/var/lib/kubelet/pods/../plugins/volume"
			return req
		}(),
		{
			VolumeId:          "volume",
			StagingTargetPath: t.TempDir(),
			VolumeContext: map[string]string{
				"nqn": "", "portalIP": "", "portalPort": "",
			},
			VolumeCapability: mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
		},
		func() *csipb.NodeStageVolumeRequest {
			req := validNodeStageRequest(t, server.nodeID)
			req.VolumeContext["portalIP"] = "127.0.0.1"
			return req
		}(),
		func() *csipb.NodeStageVolumeRequest {
			req := validNodeStageRequest(t, server.nodeID)
			req.VolumeContext["portalIP"] = "169.254.1.10"
			return req
		}(),
		func() *csipb.NodeStageVolumeRequest {
			req := validNodeStageRequest(t, server.nodeID)
			req.VolumeContext["portalIP"] = "fe80::1"
			return req
		}(),
		func() *csipb.NodeStageVolumeRequest {
			req := validNodeStageRequest(t, server.nodeID)
			req.VolumeContext["portalIP"] = "224.0.0.1"
			return req
		}(),
		func() *csipb.NodeStageVolumeRequest {
			req := validNodeStageRequest(t, server.nodeID)
			req.VolumeContext["portalPort"] = "70000"
			return req
		}(),
		func() *csipb.NodeStageVolumeRequest {
			req := validNodeStageRequest(t, server.nodeID)
			req.PublishContext[publishContextHostNQN] = hostNQNForNode("other-node")
			return req
		}(),
	}
	for i, req := range requests {
		_, err := server.NodeStageVolume(context.Background(), req)
		if code := statusCode(err); code != codes.InvalidArgument && code != codes.FailedPrecondition {
			t.Fatalf("request %d returned %s (%v), want validation failure before nvme connect", i, code, err)
		}
	}
	if connectCalls != 0 {
		t.Fatalf("invalid requests reached nvme connect %d times", connectCalls)
	}
}

func TestNodeStageValidationAcceptsRoutableIPv6(t *testing.T) {
	server := &NodeServer{nodeID: "distort-worker-1"}
	request := validNodeStageRequest(t, server.nodeID)
	request.VolumeContext["portalIP"] = "2001:db8::10"
	validated, err := server.validateNodeStageRequest(request)
	if err != nil {
		t.Fatalf("routable IPv6 portal was rejected: %v", err)
	}
	if validated.portalIP != "2001:db8::10" {
		t.Fatalf("validated portal IP = %q", validated.portalIP)
	}
}

func TestNodePublishRejectsMissingFieldsAndHonorsReadOnly(t *testing.T) {
	server := &NodeServer{}
	_, err := server.NodePublishVolume(context.Background(), &csipb.NodePublishVolumeRequest{
		VolumeId:          "",
		StagingTargetPath: filepath.Join(t.TempDir(), "missing-source"),
		TargetPath:        filepath.Join(t.TempDir(), "target"),
		Readonly:          true,
	})
	requireCode(t, err, codes.InvalidArgument)

	_, err = server.NodeUnpublishVolume(context.Background(), &csipb.NodeUnpublishVolumeRequest{
		VolumeId:   "",
		TargetPath: t.TempDir(),
	})
	requireCode(t, err, codes.InvalidArgument)
}

func TestNodePublishUsesAReadOnlyBindMount(t *testing.T) {
	fakeBin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "mount-arguments")
	script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$MOUNT_LOG\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "mount"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOUNT_LOG", logPath)
	oldReadMountInfo := readMountInfo
	oldStatMountPath := statMountPath
	oldSameMountFile := sameMountFile
	readMountInfo = func() ([]byte, error) {
		if arguments, err := os.ReadFile(logPath); err == nil && strings.Contains(string(arguments), "remount,bind,ro") {
			return []byte("1 0 0:1 / " + filepath.Join(filepath.Dir(logPath), "target-placeholder") + " ro - none bind ro\n"), nil
		}
		return nil, nil
	}
	sameMountFile = func(os.FileInfo, os.FileInfo) bool { return true }
	t.Cleanup(func() {
		readMountInfo = oldReadMountInfo
		statMountPath = oldStatMountPath
		sameMountFile = oldSameMountFile
	})

	server := &NodeServer{}
	targetPath := filepath.Join(filepath.Dir(logPath), "target-placeholder")
	_, err := server.NodePublishVolume(context.Background(), &csipb.NodePublishVolumeRequest{
		VolumeId:          "read-only-volume",
		StagingTargetPath: t.TempDir(),
		TargetPath:        targetPath,
		Readonly:          true,
		VolumeCapability:  mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
	})
	if err != nil {
		t.Fatalf("NodePublishVolume returned error: %v", err)
	}
	arguments, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(arguments), "--bind") || !strings.Contains(string(arguments), "remount,bind,ro") {
		t.Fatalf("read-only request did not bind then remount read-only:\n%s", arguments)
	}
}

func TestNodeStageRollsBackConnectionWhenDeviceDiscoveryFails(t *testing.T) {
	fakeBin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "nvme-calls")
	script := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$NVME_LOG"
case "${1:-}" in
  connect) exit 0 ;;
  list-subsys) printf '[{"Subsystems":[]}]\n'; exit 0 ;;
  disconnect) exit 0 ;;
esac
exit 1
`
	if err := os.WriteFile(filepath.Join(fakeBin, "nvme"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NVME_LOG", logPath)

	server := &NodeServer{nodeID: "distort-worker-1"}
	request := validNodeStageRequest(t, server.nodeID)
	_, err := server.NodeStageVolume(context.Background(), request)
	if err == nil {
		t.Fatal("NodeStageVolume unexpectedly succeeded without a discovered device")
	}
	calls, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(calls), "disconnect") {
		t.Fatalf("connection was leaked after staging failed:\n%s", calls)
	}
}

func TestNodeStageRollsBackMountConnectionAndCreatedDirectory(t *testing.T) {
	stagingPath := filepath.Join(t.TempDir(), "created-stage")
	devicePath := filepath.Join(t.TempDir(), "nvme-test")
	if err := os.WriteFile(devicePath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	disconnects := 0
	unmounts := 0
	server := &NodeServer{
		nodeID: "distort-worker-1",
		connectRDMA: func(context.Context, string, string, string, string) (bool, error) {
			return true, nil
		},
		disconnectRDMA: func(context.Context, string) error {
			disconnects++
			return nil
		},
		getDeviceByNQN: func(context.Context, string) (string, error) { return devicePath, nil },
		stageMount: func(context.Context, string, string, string) (bool, error) {
			return true, errors.New("simulated failure after mount")
		},
		unstageMount: func(context.Context, string) error {
			unmounts++
			return nil
		},
	}
	request := validNodeStageRequest(t, server.nodeID)
	request.StagingTargetPath = stagingPath
	_, err := server.NodeStageVolume(context.Background(), request)
	if err == nil {
		t.Fatal("NodeStageVolume unexpectedly succeeded")
	}
	if disconnects != 1 || unmounts != 1 {
		t.Fatalf("rollback calls: disconnect=%d unmount=%d, want one each", disconnects, unmounts)
	}
	if _, err := os.Stat(stagingPath); !os.IsNotExist(err) {
		t.Fatalf("created staging directory remains after rollback: %v", err)
	}
}

func TestNodeStageCancellationDuringUdevWaitUsesIndependentRollbackContext(t *testing.T) {
	disconnects := 0
	server := &NodeServer{
		nodeID: "distort-worker-1",
		connectRDMA: func(context.Context, string, string, string, string) (bool, error) {
			return true, nil
		},
		disconnectRDMA: func(ctx context.Context, _ string) error {
			if ctx.Err() != nil {
				t.Fatal("rollback inherited the canceled request context")
			}
			disconnects++
			return nil
		},
		getDeviceByNQN:         func(context.Context, string) (string, error) { return "/dev/missing-test-device", nil },
		devicePollInterval:     10 * time.Millisecond,
		deviceDiscoveryTimeout: time.Second,
	}
	request := validNodeStageRequest(t, server.nodeID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := server.NodeStageVolume(ctx, request)
	requireCode(t, err, codes.Canceled)
	if disconnects != 1 {
		t.Fatalf("disconnect calls = %d, want 1", disconnects)
	}
}

func TestNVMeConnectHonorsContextCancellation(t *testing.T) {
	fakeBin := t.TempDir()
	script := `#!/usr/bin/env bash
if [ "$1" = "list-subsys" ]; then
  printf '%s\n' '[{"Subsystems":[]}]'
  exit 0
fi
exec sleep 10
`
	if err := os.WriteFile(filepath.Join(fakeBin, "nvme"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	created, err := ConnectRDMA(ctx, "nqn.test:cancel", "192.0.2.10", "4420", hostNQNForNode("node-a"))
	if err == nil || !created || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ConnectRDMA created=%t err=%v, want uncertain created connection and deadline", created, err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("canceled nvme command took %s", time.Since(started))
	}
}

func TestExistingMountMustMatchExpectedSource(t *testing.T) {
	if _, err := formatAndMount(context.Background(), "/dev/definitely-not-root", "/", "ext4"); err == nil {
		t.Fatal("formatAndMount accepted an existing mount with the wrong source")
	}
	if err := bindMount("/definitely-not-root", "/"); err == nil {
		t.Fatal("bindMount accepted an existing bind target with the wrong source")
	}
}

func validNodeStageRequest(t *testing.T, nodeID string) *csipb.NodeStageVolumeRequest {
	t.Helper()
	return &csipb.NodeStageVolumeRequest{
		VolumeId:          "volume",
		StagingTargetPath: t.TempDir(),
		VolumeContext: map[string]string{
			"nqn": "nqn.2026-01.io.distort:test", "portalIP": "192.0.2.10", "portalPort": "4420",
		},
		PublishContext: map[string]string{
			publishContextNodeID:       nodeID,
			publishContextHostNQN:      hostNQNForNode(nodeID),
			publishContextAttachmentID: "attachment-id",
		},
		VolumeCapability: mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
	}
}

func statusCode(err error) codes.Code {
	return status.Code(err)
}
