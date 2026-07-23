package plugins

import (
	"context"
	"fmt"
	"slices"
)

type SPDKLvolManager struct{}

func init() {
	RegisterVolumeManager(&SPDKLvolManager{})
}

func (s *SPDKLvolManager) Name() string {
	return "spdk-lvol"
}

func GetLvstoreName(deviceName string) (string, error) {
	var lvsList []struct {
		Name     string `json:"name"`
		BaseBdev string `json:"base_bdev"`
	}
	if err := CallSPDKRPC("bdev_lvol_get_lvstores", &lvsList); err != nil {
		return "", fmt.Errorf("failed to list SPDK logical volume stores: %w", err)
	}
	for _, lvs := range lvsList {
		if lvs.BaseBdev == deviceName {
			return lvs.Name, nil
		}
	}
	return "", fmt.Errorf("SPDK logical volume store for base bdev %q was not found", deviceName)
}

func (s *SPDKLvolManager) SetupStorage(ctx context.Context, devicePath string, deviceName string) error {
	var lvsList []struct {
		Name     string `json:"name"`
		BaseBdev string `json:"base_bdev"`
	}
	if err := CallSPDKRPC("bdev_lvol_get_lvstores", &lvsList); err != nil {
		return fmt.Errorf("failed to list SPDK logical volume stores: %w", err)
	}
	for _, lvs := range lvsList {
		if lvs.BaseBdev == deviceName {
			return nil // already exists on this bdev
		}
	}
	storeName := "lvs_" + deviceName
	return CallSPDKRPC("bdev_lvol_create_lvstore", nil, deviceName, storeName)
}

func (s *SPDKLvolManager) CreateVolume(ctx context.Context, devicePath string, deviceName string, volumeName string, sizeBytes int64) (string, error) {
	storeName, err := GetLvstoreName(deviceName)
	if err != nil {
		return "", err
	}
	lvolBdevName := fmt.Sprintf("%s/%s", storeName, volumeName)

	var bdevs []struct {
		Name    string   `json:"name"`
		Aliases []string `json:"aliases"`
	}
	if err := CallSPDKRPC("bdev_get_bdevs", &bdevs); err != nil {
		return "", fmt.Errorf("failed to list SPDK bdevs before creating %q: %w", lvolBdevName, err)
	}
	for _, bdev := range bdevs {
		if bdev.Name == lvolBdevName {
			return lvolBdevName, nil
		}
		if slices.Contains(bdev.Aliases, lvolBdevName) {
			return lvolBdevName, nil
		}
	}

	if sizeBytes < 1024*1024 {
		return "", fmt.Errorf("requested lvol size %d bytes is smaller than SPDK's 1 MiB RPC unit", sizeBytes)
	}
	sizeMB := sizeBytes / (1024 * 1024)
	var uuid string
	err = CallSPDKRPC("bdev_lvol_create", &uuid, "-l", storeName, volumeName, fmt.Sprintf("%d", sizeMB))
	if err != nil {
		return "", err
	}
	return lvolBdevName, nil
}

func (s *SPDKLvolManager) DeleteVolume(ctx context.Context, devicePath string, deviceName string, volumeName string) error {
	var lvsList []struct {
		Name     string `json:"name"`
		BaseBdev string `json:"base_bdev"`
	}
	if err := CallSPDKRPC("bdev_lvol_get_lvstores", &lvsList); err != nil {
		return fmt.Errorf("failed to list SPDK logical volume stores: %w", err)
	}
	storeName := ""
	for _, lvs := range lvsList {
		if lvs.BaseBdev == deviceName {
			storeName = lvs.Name
			break
		}
	}
	if storeName == "" {
		return nil // lvstore and volume are already absent
	}
	lvolBdevName := storeName + "/" + volumeName

	var bdevs []struct {
		Name    string   `json:"name"`
		Aliases []string `json:"aliases"`
	}
	if err := CallSPDKRPC("bdev_get_bdevs", &bdevs); err != nil {
		return fmt.Errorf("failed to list SPDK bdevs before deleting %q: %w", lvolBdevName, err)
	}

	for _, bdev := range bdevs {
		if bdev.Name == lvolBdevName {
			return CallSPDKRPC("bdev_lvol_delete", nil, bdev.Name)
		}
		if slices.Contains(bdev.Aliases, lvolBdevName) {
			// Delete by UUID/name, which remains stable even though the
			// user-facing lvol path is represented as an alias.
			return CallSPDKRPC("bdev_lvol_delete", nil, bdev.Name)
		}
	}

	return nil // already deleted
}
