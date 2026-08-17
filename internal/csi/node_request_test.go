package csi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"distort/test/knownfailure"
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

func TestNodeStageValidatesEverythingBeforeConnecting(t *testing.T) {
	knownfailure.Require(t, "F11")
	server := &NodeServer{}
	requests := []*csipb.NodeStageVolumeRequest{
		{VolumeId: "volume", VolumeContext: map[string]string{}},
		{
			VolumeId:          "volume",
			StagingTargetPath: t.TempDir(),
			VolumeContext: map[string]string{
				"nqn": "", "portalIP": "", "portalPort": "",
			},
			VolumeCapability: mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
		},
	}
	for i, req := range requests {
		_, err := server.NodeStageVolume(context.Background(), req)
		if code := statusCode(err); code != codes.InvalidArgument {
			t.Fatalf("request %d returned %s (%v), want InvalidArgument before nvme connect", i, code, err)
		}
	}
}

func TestNodePublishRejectsMissingFieldsAndHonorsReadOnly(t *testing.T) {
	knownfailure.Require(t, "F10")
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
	knownfailure.Require(t, "F10")
	fakeBin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "mount-arguments")
	script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > \"$MOUNT_LOG\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "mount"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOUNT_LOG", logPath)

	server := &NodeServer{}
	_, err := server.NodePublishVolume(context.Background(), &csipb.NodePublishVolumeRequest{
		VolumeId:          "read-only-volume",
		StagingTargetPath: t.TempDir(),
		TargetPath:        filepath.Join(t.TempDir(), "target"),
		Readonly:          true,
	})
	if err != nil {
		t.Fatalf("NodePublishVolume returned error: %v", err)
	}
	arguments, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(arguments), "ro") {
		t.Fatalf("read-only request produced a writable bind mount:\n%s", arguments)
	}
}

func TestNodeStageRollsBackConnectionWhenDeviceDiscoveryFails(t *testing.T) {
	knownfailure.Require(t, "F11")
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

	server := &NodeServer{}
	_, err := server.NodeStageVolume(context.Background(), &csipb.NodeStageVolumeRequest{
		VolumeId:          "rollback-volume",
		StagingTargetPath: t.TempDir(),
		VolumeContext: map[string]string{
			"nqn": "nqn.test:rollback", "portalIP": "192.0.2.10", "portalPort": "4420",
		},
		VolumeCapability: mountCapability(csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
	})
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

func TestExistingMountMustMatchExpectedSource(t *testing.T) {
	knownfailure.Require(t, "F12")
	if err := formatAndMount("/dev/definitely-not-root", "/", "ext4"); err == nil {
		t.Fatal("formatAndMount accepted an existing mount with the wrong source")
	}
	if err := bindMount("/definitely-not-root", "/"); err == nil {
		t.Fatal("bindMount accepted an existing bind target with the wrong source")
	}
}

func statusCode(err error) codes.Code {
	return status.Code(err)
}
