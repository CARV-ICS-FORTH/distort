package plugins

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

type SPDKLvolManager struct{}

type spdkLvstore struct {
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	BaseBdev string `json:"base_bdev"`
}

type spdkLvolDetails struct {
	LvolStoreUUID string `json:"lvol_store_uuid"`
}

type spdkBdev struct {
	Name           string   `json:"name"`
	UUID           string   `json:"uuid"`
	Aliases        []string `json:"aliases"`
	DriverSpecific struct {
		Lvol *spdkLvolDetails `json:"lvol,omitempty"`
	} `json:"driver_specific"`
}

func init() {
	RegisterVolumeManager(&SPDKLvolManager{})
}

func (s *SPDKLvolManager) Name() string {
	return "spdk-lvol"
}

func listSPDKLvstores() ([]spdkLvstore, error) {
	var stores []spdkLvstore
	if err := CallSPDKRPC("bdev_lvol_get_lvstores", &stores); err != nil {
		return nil, fmt.Errorf("failed to list SPDK logical volume stores: %w", err)
	}
	return stores, nil
}

func listSPDKBdevs() ([]spdkBdev, error) {
	var bdevs []spdkBdev
	if err := CallSPDKRPC("bdev_get_bdevs", &bdevs); err != nil {
		return nil, fmt.Errorf("failed to list SPDK bdevs: %w", err)
	}
	return bdevs, nil
}

func lvstoreForBaseBdev(stores []spdkLvstore, baseBdev string) (spdkLvstore, error) {
	var matches []spdkLvstore
	for _, store := range stores {
		if store.BaseBdev == baseBdev {
			matches = append(matches, store)
		}
	}
	if len(matches) != 1 {
		return spdkLvstore{}, fmt.Errorf("found %d SPDK logical volume stores for base bdev %q, want exactly one", len(matches), baseBdev)
	}
	return matches[0], nil
}

func GetLvstoreName(deviceName string) (string, error) {
	stores, err := listSPDKLvstores()
	if err != nil {
		return "", err
	}
	store, err := lvstoreForBaseBdev(stores, deviceName)
	if err != nil {
		return "", err
	}
	return store.Name, nil
}

func (s *SPDKLvolManager) SetupStorage(ctx context.Context, devicePath string, deviceName string) error {
	stores, err := listSPDKLvstores()
	if err != nil {
		return err
	}
	for _, store := range stores {
		if store.BaseBdev == deviceName {
			return nil
		}
	}
	storeName := "lvs_" + deviceName
	return CallSPDKRPC("bdev_lvol_create_lvstore", nil, deviceName, storeName)
}

func matchingBdevs(bdevs []spdkBdev, match func(spdkBdev) bool) []int {
	var matches []int
	for index, bdev := range bdevs {
		if match(bdev) {
			matches = append(matches, index)
		}
	}
	return matches
}

func lvolIdentity(bdev spdkBdev, store spdkLvstore, volumeName string) VolumeIdentity {
	alias := store.Name + "/" + volumeName
	uuid := bdev.UUID
	if uuid == "" {
		uuid = bdev.Name
	}
	return VolumeIdentity{
		BackendVolumeID: alias,
		BaseBdev:        store.BaseBdev,
		VolumeStoreName: store.Name,
		VolumeStoreUUID: store.UUID,
		VolumeName:      volumeName,
		VolumeUUID:      uuid,
	}
}

func findCreatedLvol(bdevs []spdkBdev, alias, uuid string) (spdkBdev, error) {
	matches := matchingBdevs(bdevs, func(bdev spdkBdev) bool {
		matchesUUID := uuid != "" && (bdev.Name == uuid || bdev.UUID == uuid)
		return matchesUUID || bdev.Name == alias || slices.Contains(bdev.Aliases, alias)
	})
	if len(matches) != 1 {
		return spdkBdev{}, fmt.Errorf("found %d SPDK lvol bdevs for alias %q and UUID %q, want exactly one", len(matches), alias, uuid)
	}
	return bdevs[matches[0]], nil
}

func (s *SPDKLvolManager) CreateVolume(ctx context.Context, devicePath string, deviceName string, volumeName string, sizeBytes int64) (VolumeIdentity, error) {
	stores, err := listSPDKLvstores()
	if err != nil {
		return VolumeIdentity{}, err
	}
	store, err := lvstoreForBaseBdev(stores, deviceName)
	if err != nil {
		return VolumeIdentity{}, err
	}
	alias := store.Name + "/" + volumeName

	bdevs, err := listSPDKBdevs()
	if err != nil {
		return VolumeIdentity{}, fmt.Errorf("list SPDK bdevs before creating %q: %w", alias, err)
	}
	existing, err := findCreatedLvol(bdevs, alias, "")
	if err == nil {
		return lvolIdentity(existing, store, volumeName), nil
	}
	if matches := matchingBdevs(bdevs, func(bdev spdkBdev) bool {
		return bdev.Name == alias || slices.Contains(bdev.Aliases, alias)
	}); len(matches) > 1 {
		return VolumeIdentity{}, fmt.Errorf("multiple SPDK lvol bdevs use alias %q", alias)
	}

	if sizeBytes < 1024*1024 {
		return VolumeIdentity{}, fmt.Errorf("requested lvol size %d bytes is smaller than SPDK's 1 MiB RPC unit", sizeBytes)
	}
	sizeMB := (sizeBytes + 1024*1024 - 1) / (1024 * 1024)
	var uuid string
	if err := CallSPDKRPC("bdev_lvol_create", &uuid, "-l", store.Name, volumeName, fmt.Sprintf("%d", sizeMB)); err != nil {
		return VolumeIdentity{}, err
	}

	bdevs, err = listSPDKBdevs()
	if err != nil {
		return VolumeIdentity{}, fmt.Errorf("verify SPDK lvol %q after creation: %w", alias, err)
	}
	created, err := findCreatedLvol(bdevs, alias, uuid)
	if err != nil {
		return VolumeIdentity{}, err
	}
	identity := lvolIdentity(created, store, volumeName)
	if uuid != "" {
		identity.VolumeUUID = uuid
	}
	return identity, nil
}

type lvolSelector struct {
	name    string
	matches []int
}

func resolveLvolForDeletion(bdevs []spdkBdev, volumeName string, identity VolumeIdentity) (*spdkBdev, error) {
	var selectors []lvolSelector
	if identity.VolumeUUID != "" {
		selectors = append(selectors, lvolSelector{"lvol UUID", matchingBdevs(bdevs, func(bdev spdkBdev) bool {
			return bdev.Name == identity.VolumeUUID || bdev.UUID == identity.VolumeUUID
		})})
	}
	if identity.BackendVolumeID != "" {
		selectors = append(selectors, lvolSelector{"backend volume ID", matchingBdevs(bdevs, func(bdev spdkBdev) bool {
			return bdev.Name == identity.BackendVolumeID || slices.Contains(bdev.Aliases, identity.BackendVolumeID)
		})})
	}
	if identity.VolumeStoreName != "" && identity.VolumeName != "" {
		alias := identity.VolumeStoreName + "/" + identity.VolumeName
		if alias != identity.BackendVolumeID {
			selectors = append(selectors, lvolSelector{"lvstore/name alias", matchingBdevs(bdevs, func(bdev spdkBdev) bool {
				return bdev.Name == alias || slices.Contains(bdev.Aliases, alias)
			})})
		}
	}
	// A legacy object can lack every backend identity if its status update was
	// lost. Use the globally unique external name only as a last-resort selector;
	// when an exact persisted selector exists, a broad suffix match could make an
	// otherwise safe legacy deletion ambiguous because of another old same-named
	// volume in a different lvstore.
	if len(selectors) == 0 && volumeName != "" {
		selectors = append(selectors, lvolSelector{"logical volume name", matchingBdevs(bdevs, func(bdev spdkBdev) bool {
			if bdev.Name == volumeName {
				return true
			}
			for _, alias := range bdev.Aliases {
				if alias == volumeName || strings.HasSuffix(alias, "/"+volumeName) {
					return true
				}
			}
			return false
		})})
	}
	if len(selectors) == 0 {
		return nil, fmt.Errorf("SPDK lvol cleanup has no stable identifier")
	}

	foundIndex := -1
	missing := false
	for _, selector := range selectors {
		if len(selector.matches) > 1 {
			return nil, fmt.Errorf("%s resolves to %d SPDK lvols", selector.name, len(selector.matches))
		}
		if len(selector.matches) == 0 {
			missing = true
			continue
		}
		if foundIndex >= 0 && foundIndex != selector.matches[0] {
			return nil, fmt.Errorf("persisted SPDK identifiers resolve to different lvols")
		}
		foundIndex = selector.matches[0]
	}
	if foundIndex < 0 {
		return nil, nil
	}
	if missing {
		return nil, fmt.Errorf("only some persisted SPDK identifiers resolve; refusing unsafe lvol deletion")
	}
	return &bdevs[foundIndex], nil
}

func validateLvolOwnership(candidate spdkBdev, stores []spdkLvstore, identity VolumeIdentity) error {
	var store *spdkLvstore
	for index := range stores {
		matchesUUID := identity.VolumeStoreUUID != "" && stores[index].UUID == identity.VolumeStoreUUID
		matchesName := identity.VolumeStoreName != "" && stores[index].Name == identity.VolumeStoreName
		if matchesUUID || matchesName {
			if store != nil && store.UUID != stores[index].UUID {
				return fmt.Errorf("persisted lvstore identifiers resolve to different stores")
			}
			store = &stores[index]
		}
	}
	if (identity.VolumeStoreUUID != "" || identity.VolumeStoreName != "") && store == nil {
		return fmt.Errorf("persisted SPDK lvstore cannot be resolved while its lvol still exists")
	}
	if store != nil {
		if identity.VolumeStoreUUID != "" && store.UUID != identity.VolumeStoreUUID {
			return fmt.Errorf("SPDK lvstore UUID does not match persisted identity")
		}
		if identity.VolumeStoreName != "" && store.Name != identity.VolumeStoreName {
			return fmt.Errorf("SPDK lvstore name does not match persisted identity")
		}
		if identity.BaseBdev != "" && store.BaseBdev != identity.BaseBdev {
			return fmt.Errorf("SPDK lvstore base bdev %q does not match persisted %q", store.BaseBdev, identity.BaseBdev)
		}
		if candidate.DriverSpecific.Lvol != nil && candidate.DriverSpecific.Lvol.LvolStoreUUID != "" &&
			store.UUID != candidate.DriverSpecific.Lvol.LvolStoreUUID {
			return fmt.Errorf("SPDK lvol belongs to lvstore UUID %q, not %q", candidate.DriverSpecific.Lvol.LvolStoreUUID, store.UUID)
		}
	}
	return nil
}

func (s *SPDKLvolManager) DeleteVolume(ctx context.Context, devicePath string, deviceName string, volumeName string, identity VolumeIdentity) error {
	stores, err := listSPDKLvstores()
	if err != nil {
		return err
	}
	bdevs, err := listSPDKBdevs()
	if err != nil {
		return fmt.Errorf("list SPDK bdevs before deleting %q: %w", volumeName, err)
	}
	candidate, err := resolveLvolForDeletion(bdevs, volumeName, identity)
	if err != nil {
		return err
	}
	if candidate == nil {
		return nil
	}
	if err := validateLvolOwnership(*candidate, stores, identity); err != nil {
		return err
	}

	deleteErr := CallSPDKRPC("bdev_lvol_delete", nil, candidate.Name)
	bdevs, verifyErr := listSPDKBdevs()
	if verifyErr != nil {
		return fmt.Errorf("verify SPDK lvol absence after deletion: %w", verifyErr)
	}
	remaining, resolveErr := resolveLvolForDeletion(bdevs, volumeName, identity)
	if resolveErr != nil {
		return resolveErr
	}
	if remaining != nil {
		if deleteErr != nil {
			return fmt.Errorf("delete SPDK lvol %q: %w", candidate.Name, deleteErr)
		}
		return fmt.Errorf("SPDK lvol %q still exists after deletion", candidate.Name)
	}
	return nil
}
