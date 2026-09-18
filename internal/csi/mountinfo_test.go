package csi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mountInfoTestRow(device, root, target, filesystem, source, mode string) string {
	return fmt.Sprintf("42 1 %s %s %s %s - %s %s %s\n", device, root, target, mode, filesystem, source, mode)
}

func installMountInfoTestState(t *testing.T, data string) {
	t.Helper()
	oldRead := readMountInfo
	oldDeviceNumber := deviceNumber
	oldStat := statMountPath
	oldSame := sameMountFile
	readMountInfo = func() ([]byte, error) { return []byte(data), nil }
	deviceNumber = func(string) (string, error) { return "259:7", nil }
	statMountPath = os.Stat
	sameMountFile = os.SameFile
	t.Cleanup(func() {
		readMountInfo = oldRead
		deviceNumber = oldDeviceNumber
		statMountPath = oldStat
		sameMountFile = oldSame
	})
}

func TestStagingMountVerificationChecksDeviceFilesystemAndFlags(t *testing.T) {
	target := "/var/lib/kubelet/plugins/kubernetes.io/csi/stage"
	installMountInfoTestState(t, mountInfoTestRow("259:7", "/", target, "ext4", "/dev/nvme1n1", "rw"))
	mounted, err := verifyStagingMount("/dev/nvme1n1", target, "ext4")
	if err != nil || !mounted {
		t.Fatalf("correct staging mount rejected: mounted=%t err=%v", mounted, err)
	}

	deviceNumber = func(string) (string, error) { return "259:8", nil }
	if _, err := verifyStagingMount("/dev/nvme2n1", target, "ext4"); err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("wrong staging device error = %v", err)
	}
	deviceNumber = func(string) (string, error) { return "259:7", nil }
	if _, err := verifyStagingMount("/dev/nvme1n1", target, "xfs"); err == nil || !strings.Contains(err.Error(), "filesystem") {
		t.Fatalf("wrong staging filesystem error = %v", err)
	}
	readMountInfo = func() ([]byte, error) {
		return []byte(mountInfoTestRow("259:7", "/", target, "ext4", "/dev/nvme1n1", "ro")), nil
	}
	if _, err := verifyStagingMount("/dev/nvme1n1", target, "ext4"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only staging mount error = %v", err)
	}
}

func TestPublishedMountVerificationChecksBindSourceAndReadOnlyState(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	installMountInfoTestState(t, mountInfoTestRow("259:7", "/stage", target, "ext4", "/dev/nvme1n1", "ro"))
	sameMountFile = func(os.FileInfo, os.FileInfo) bool { return true }
	mounted, err := verifyPublishedMount(source, target, true)
	if err != nil || !mounted {
		t.Fatalf("correct read-only bind rejected: mounted=%t err=%v", mounted, err)
	}

	sameMountFile = func(os.FileInfo, os.FileInfo) bool { return false }
	if _, err := verifyPublishedMount(source, target, true); err == nil || !strings.Contains(err.Error(), "not a bind mount") {
		t.Fatalf("wrong bind source error = %v", err)
	}
	sameMountFile = func(os.FileInfo, os.FileInfo) bool { return true }
	if _, err := verifyPublishedMount(source, target, false); err == nil || !strings.Contains(err.Error(), "read-only state") {
		t.Fatalf("wrong publish flags error = %v", err)
	}
}

func TestMountInfoParserHandlesEscapedPaths(t *testing.T) {
	records, err := parseMountInfo([]byte("1 0 8:1 /dir\\040one /target\\040one rw - ext4 /dev/test rw\n"))
	if err != nil || len(records) != 1 {
		t.Fatalf("parseMountInfo records=%#v err=%v", records, err)
	}
	if records[0].root != "/dir one" || records[0].target != "/target one" {
		t.Fatalf("escaped paths were not decoded: %#v", records[0])
	}
}
