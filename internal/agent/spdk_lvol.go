package agent

import (
	"fmt"
)

// EnsureLvstore creates an lvol store on a bdev if it doesn't exist
func EnsureLvstore(bdevName string, storeName string) error {
	var lvsList []struct {
		Name string `json:"name"`
	}
	if err := CallSPDKRPC("bdev_lvol_get_lvstores", &lvsList); err == nil {
		for _, lvs := range lvsList {
			if lvs.Name == storeName {
				return nil // exists
			}
		}
	}

	// If it doesn't exist, create it
	return CallSPDKRPC("bdev_lvol_create_lvstore", nil, bdevName, storeName)
}

// CreateLvol creates a logical volume
func CreateLvol(storeName string, lvolName string, sizeMB int64) (string, error) {
	var uuid string
	// Syntax: bdev_lvol_create -l <store_name> <lvol_name> <size_mb>
	err := CallSPDKRPC("bdev_lvol_create", &uuid, "-l", storeName, lvolName, fmt.Sprintf("%d", sizeMB))
	// The SPDK LVOL Bdev name takes the format: "storeName/lvolName"
	// and sometimes includes the UUID, but it's simpler to just return UUID if we want to use it
	return fmt.Sprintf("%s/%s", storeName, lvolName), err
}

// DeleteLvol deletes a logical volume
func DeleteLvol(lvolBdevName string) error {
	return CallSPDKRPC("bdev_lvol_delete", nil, lvolBdevName)
}
