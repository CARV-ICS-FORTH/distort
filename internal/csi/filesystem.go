package csi

import (
	"fmt"
	"strings"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
)

const (
	defaultFilesystem      = "ext4"
	xfsFilesystem          = "xfs"
	filesystemParameter    = "fsType"
	csiFilesystemParameter = "csi.storage.k8s.io/fstype"
)

var supportedFilesystems = map[string]struct{}{
	"ext4": {},
	"xfs":  {},
}

// resolveFilesystem selects the filesystem requested by the StorageClass. An
// explicit parameter takes precedence over VolumeCapability because the CSI
// provisioner may populate the latter with its own ext4 default.
func resolveFilesystem(parameters map[string]string, capabilities []*csipb.VolumeCapability) (string, error) {
	explicit := make([]string, 0, 2)
	for _, key := range []string{filesystemParameter, csiFilesystemParameter} {
		if value, ok := parameters[key]; ok && strings.TrimSpace(value) != "" {
			explicit = append(explicit, normalizeFilesystem(value))
		}
	}

	if len(explicit) > 0 {
		for _, filesystem := range explicit[1:] {
			if filesystem != explicit[0] {
				return "", fmt.Errorf("conflicting filesystem parameters: %s=%q and %s=%q",
					filesystemParameter, parameters[filesystemParameter],
					csiFilesystemParameter, parameters[csiFilesystemParameter])
			}
		}
		return validateFilesystem(explicit[0])
	}

	requested := ""
	for _, capability := range capabilities {
		mount := capability.GetMount()
		if mount == nil || strings.TrimSpace(mount.GetFsType()) == "" {
			continue
		}
		filesystem := normalizeFilesystem(mount.GetFsType())
		if requested != "" && filesystem != requested {
			return "", fmt.Errorf("conflicting filesystem types in volume capabilities: %q and %q", requested, filesystem)
		}
		requested = filesystem
	}

	if requested == "" {
		requested = defaultFilesystem
	}
	return validateFilesystem(requested)
}

func normalizeFilesystem(filesystem string) string {
	return strings.ToLower(strings.TrimSpace(filesystem))
}

func validateFilesystem(filesystem string) (string, error) {
	if _, ok := supportedFilesystems[filesystem]; !ok {
		return "", fmt.Errorf("unsupported filesystem %q: supported filesystems are ext4 and xfs", filesystem)
	}
	return filesystem, nil
}
