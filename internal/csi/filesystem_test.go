package csi

import (
	"errors"
	"reflect"
	"testing"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
)

func TestResolveFilesystem(t *testing.T) {
	const xfsFilesystem = "xfs"

	mountCapability := func(fsType string) []*csipb.VolumeCapability {
		return []*csipb.VolumeCapability{{
			AccessType: &csipb.VolumeCapability_Mount{
				Mount: &csipb.VolumeCapability_MountVolume{FsType: fsType},
			},
		}}
	}

	tests := []struct {
		name       string
		parameters map[string]string
		caps       []*csipb.VolumeCapability
		want       string
		wantErr    bool
	}{
		{name: "defaults to ext4", want: "ext4"},
		{name: "Mayastor-compatible parameter selects xfs", parameters: map[string]string{filesystemParameter: xfsFilesystem}, want: xfsFilesystem},
		{name: "CSI parameter selects xfs", parameters: map[string]string{csiFilesystemParameter: xfsFilesystem}, want: xfsFilesystem},
		{name: "matching aliases are accepted", parameters: map[string]string{filesystemParameter: "XFS", csiFilesystemParameter: xfsFilesystem}, want: xfsFilesystem},
		{
			name:       "explicit parameter overrides provisioner capability default",
			parameters: map[string]string{filesystemParameter: xfsFilesystem},
			caps:       mountCapability("ext4"),
			want:       xfsFilesystem,
		},
		{name: "volume capability selects xfs", caps: mountCapability(xfsFilesystem), want: xfsFilesystem},
		{
			name:       "conflicting aliases are rejected",
			parameters: map[string]string{filesystemParameter: xfsFilesystem, csiFilesystemParameter: "ext4"},
			wantErr:    true,
		},
		{name: "unsupported filesystem is rejected", parameters: map[string]string{filesystemParameter: "btrfs"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveFilesystem(tt.parameters, tt.caps)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveFilesystem() expected an error, got filesystem %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveFilesystem() returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveFilesystem() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnsureFilesystem(t *testing.T) {
	const xfsFilesystem = "xfs"

	t.Run("formats a blank device", func(t *testing.T) {
		formatted := false
		err := ensureFilesystem("/dev/test", xfsFilesystem,
			func(string) (string, error) { return "", nil },
			func(source, fsType string) error {
				formatted = source == "/dev/test" && fsType == xfsFilesystem
				return nil
			})
		if err != nil {
			t.Fatalf("ensureFilesystem() returned error: %v", err)
		}
		if !formatted {
			t.Fatal("ensureFilesystem() did not format the blank device as xfs")
		}
	})

	t.Run("preserves a matching filesystem", func(t *testing.T) {
		formatCalled := false
		err := ensureFilesystem("/dev/test", xfsFilesystem,
			func(string) (string, error) { return xfsFilesystem, nil },
			func(string, string) error { formatCalled = true; return nil })
		if err != nil {
			t.Fatalf("ensureFilesystem() returned error: %v", err)
		}
		if formatCalled {
			t.Fatal("ensureFilesystem() formatted a device with a matching filesystem")
		}
	})

	t.Run("rejects a filesystem mismatch", func(t *testing.T) {
		formatCalled := false
		err := ensureFilesystem("/dev/test", xfsFilesystem,
			func(string) (string, error) { return "ext4", nil },
			func(string, string) error { formatCalled = true; return nil })
		var mismatch *filesystemMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("ensureFilesystem() error = %v, want filesystemMismatchError", err)
		}
		if formatCalled {
			t.Fatal("ensureFilesystem() formatted a device with a mismatching filesystem")
		}
	})
}

func TestFilesystemFormatCommand(t *testing.T) {
	tests := []struct {
		fsType      string
		wantCommand string
		wantArgs    []string
	}{
		{fsType: "ext4", wantCommand: "mkfs.ext4", wantArgs: []string{"-F", "/dev/test"}},
		{fsType: "xfs", wantCommand: "mkfs.xfs", wantArgs: []string{"-f", "/dev/test"}},
	}

	for _, tt := range tests {
		t.Run(tt.fsType, func(t *testing.T) {
			command, args, err := filesystemFormatCommand("/dev/test", tt.fsType)
			if err != nil {
				t.Fatalf("filesystemFormatCommand() returned error: %v", err)
			}
			if command != tt.wantCommand || !reflect.DeepEqual(args, tt.wantArgs) {
				t.Fatalf("filesystemFormatCommand() = %q %v, want %q %v", command, args, tt.wantCommand, tt.wantArgs)
			}
		})
	}
}
