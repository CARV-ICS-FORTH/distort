package plugins

import (
	"context"
	"fmt"
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
	if err := CallSPDKRPC("bdev_lvol_get_lvstores", &lvsList); err == nil {
		for _, lvs := range lvsList {
			if lvs.BaseBdev == deviceName {
				return lvs.Name, nil
			}
		}
	}
	return "lvs_" + deviceName, nil
}

func (s *SPDKLvolManager) SetupStorage(ctx context.Context, devicePath string, deviceName string) error {
	var lvsList []struct {
		Name     string `json:"name"`
		BaseBdev string `json:"base_bdev"`
	}
	if err := CallSPDKRPC("bdev_lvol_get_lvstores", &lvsList); err == nil {
		for _, lvs := range lvsList {
			if lvs.BaseBdev == deviceName {
				return nil // already exists on this bdev
			}
		}
	}
	storeName := "lvs_" + deviceName
	return CallSPDKRPC("bdev_lvol_create_lvstore", nil, deviceName, storeName)
}

func (s *SPDKLvolManager) CreateVolume(ctx context.Context, devicePath string, deviceName string, volumeName string, sizeBytes int64) (string, error) {
	storeName, _ := GetLvstoreName(deviceName)
	lvolBdevName := fmt.Sprintf("%s/%s", storeName, volumeName)

	var bdevs []struct {
		Name    string   `json:"name"`
		Aliases []string `json:"aliases"`
	}
	if err := CallSPDKRPC("bdev_get_bdevs", &bdevs); err == nil {
		for _, bdev := range bdevs {
			if bdev.Name == lvolBdevName {
				return lvolBdevName, nil
			}
			for _, alias := range bdev.Aliases {
				if alias == lvolBdevName {
					return lvolBdevName, nil
				}
			}
		}
	}

	sizeMB := sizeBytes / (1024 * 1024)
	var uuid string
	err := CallSPDKRPC("bdev_lvol_create", &uuid, "-l", storeName, volumeName, fmt.Sprintf("%d", sizeMB))
	if err != nil {
		return "", err
	}
	return lvolBdevName, nil
}

func (s *SPDKLvolManager) DeleteVolume(ctx context.Context, devicePath string, deviceName string, volumeName string) error {
	storeName, _ := GetLvstoreName(deviceName)
	lvolBdevName := storeName + "/" + volumeName
	return CallSPDKRPC("bdev_lvol_delete", nil, lvolBdevName)
}
