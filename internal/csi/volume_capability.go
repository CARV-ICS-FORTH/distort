package csi

import (
	"fmt"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
)

const supportedAccessMode = csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER

type canonicalVolumeCapability struct {
	AccessMode string `json:"accessMode"`
	Filesystem string `json:"filesystem"`
}

func validateVolumeCapabilities(parameters map[string]string, capabilities []*csipb.VolumeCapability) (canonicalVolumeCapability, error) {
	if len(capabilities) == 0 {
		return canonicalVolumeCapability{}, fmt.Errorf("volume capabilities cannot be empty")
	}

	capabilityFilesystem := ""
	for index, capability := range capabilities {
		if capability == nil {
			return canonicalVolumeCapability{}, fmt.Errorf("volume capability %d is missing", index)
		}
		mount := capability.GetMount()
		if mount == nil {
			return canonicalVolumeCapability{}, fmt.Errorf("raw block volumes are not supported")
		}
		if len(mount.GetMountFlags()) != 0 {
			return canonicalVolumeCapability{}, fmt.Errorf("mount flags are not supported")
		}
		if mount.GetFsType() != "" {
			currentFilesystem := normalizeFilesystem(mount.GetFsType())
			if capabilityFilesystem != "" && currentFilesystem != capabilityFilesystem {
				return canonicalVolumeCapability{}, fmt.Errorf("conflicting filesystem types in volume capabilities: %q and %q",
					capabilityFilesystem, currentFilesystem)
			}
			capabilityFilesystem = currentFilesystem
		}
		if capability.GetAccessMode() == nil || capability.GetAccessMode().GetMode() != supportedAccessMode {
			return canonicalVolumeCapability{}, fmt.Errorf("access mode %s is not supported; only %s is supported",
				capability.GetAccessMode().GetMode(), supportedAccessMode)
		}
	}
	filesystem, err := resolveFilesystem(parameters, capabilities)
	if err != nil {
		return canonicalVolumeCapability{}, err
	}

	return canonicalVolumeCapability{
		AccessMode: supportedAccessMode.String(),
		Filesystem: filesystem,
	}, nil
}
