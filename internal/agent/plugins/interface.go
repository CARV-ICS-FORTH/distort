package plugins

import (
	"context"
	"fmt"
	"sync"
)

// TargetBackend defines the interface that all target export plugins must implement.
type TargetBackend interface {
	// Name returns the identifier of the backend (e.g., "spdk", "kernel")
	Name() string

	// SetupDevice prepares the physical device for this backend (e.g. unbinding/binding drivers)
	SetupDevice(ctx context.Context, pciAddress string, deviceName string, options map[string]string) error

	// ExportVolume exports a block device or volume as an NVMe-oF target.
	// Returns NQN, Portal IP, Portal Port, and error
	ExportVolume(ctx context.Context, volumeName string, blockPath string, portalIP string, portalPort int, options map[string]string) (string, error)

	// UnexportVolume removes the NVMe-oF target export for the given volume.
	UnexportVolume(ctx context.Context, nqn string) error
}

// VolumeIdentity contains the stable backend identifiers needed to address one
// carved volume exactly. Managers that do not expose structured identities only
// need to set BackendVolumeID.
type VolumeIdentity struct {
	BackendVolumeID string
	BaseBdev        string
	VolumeStoreName string
	VolumeStoreUUID string
	VolumeName      string
	VolumeUUID      string
}

// VolumeManager defines the interface that all volume carving/management plugins must implement.
type VolumeManager interface {
	// Name returns the identifier of the volume manager (e.g., "partition", "lvm")
	Name() string

	// SetupStorage prepares the physical device for carving (e.g., creating lvstore or LVM VG)
	SetupStorage(ctx context.Context, devicePath string, deviceName string) error

	// CreateVolume carves out a volume of the specified size (in bytes)
	// Returns the stable identity of the resulting block device/volume and error.
	CreateVolume(ctx context.Context, devicePath string, deviceName string, volumeName string, sizeBytes int64) (VolumeIdentity, error)

	// DeleteVolume deletes the exact carved volume returned by CreateVolume.
	DeleteVolume(ctx context.Context, devicePath string, deviceName string, volumeName string, identity VolumeIdentity) error
}

var (
	backendsMu sync.RWMutex
	backends   = make(map[string]TargetBackend)

	volumeManagersMu sync.RWMutex
	volumeManagers   = make(map[string]VolumeManager)
)

// RegisterTargetBackend registers a target backend plugin.
func RegisterTargetBackend(b TargetBackend) {
	backendsMu.Lock()
	defer backendsMu.Unlock()
	if b == nil {
		panic("backend is nil")
	}
	backends[b.Name()] = b
}

// GetTargetBackend retrieves a target backend plugin by name.
func GetTargetBackend(name string) (TargetBackend, error) {
	backendsMu.RLock()
	defer backendsMu.RUnlock()
	b, exists := backends[name]
	if !exists {
		return nil, fmt.Errorf("target backend %q not found", name)
	}
	return b, nil
}

// RegisterVolumeManager registers a volume manager plugin.
func RegisterVolumeManager(vm VolumeManager) {
	volumeManagersMu.Lock()
	defer volumeManagersMu.Unlock()
	if vm == nil {
		panic("volume manager is nil")
	}
	volumeManagers[vm.Name()] = vm
}

// GetVolumeManager retrieves a volume manager plugin by name.
func GetVolumeManager(name string) (VolumeManager, error) {
	volumeManagersMu.RLock()
	defer volumeManagersMu.RUnlock()
	vm, exists := volumeManagers[name]
	if !exists {
		return nil, fmt.Errorf("volume manager %q not found", name)
	}
	return vm, nil
}
