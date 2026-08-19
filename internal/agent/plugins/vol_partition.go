package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"distort/internal/capacity"
)

const partitionAlignmentBytes int64 = 1024 * 1024

var (
	partitionDeviceLocks sync.Map
	partitionPathStat    = os.Stat
	partitionPollPeriod  = time.Second
	partitionPollCount   = 15
	executeParted        = func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "parted", args...).CombinedOutput()
	}
	executeWipefs = func(ctx context.Context, devicePath string) ([]byte, error) {
		return exec.CommandContext(ctx, "wipefs", "-a", devicePath).CombinedOutput()
	}
)

type partedPartition struct {
	number int
	start  int64
	end    int64
	name   string
}

type partedExtent struct {
	start int64
	end   int64
}

type PartedVolumeManager struct{}

func init() {
	RegisterVolumeManager(&PartedVolumeManager{})
}

func (pv *PartedVolumeManager) Name() string {
	return "parted"
}

func devicePartitionLock(devicePath string) *sync.Mutex {
	lock, _ := partitionDeviceLocks.LoadOrStore(devicePath, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func runParted(ctx context.Context, args ...string) ([]byte, error) {
	out, err := executeParted(ctx, args...)
	if err != nil {
		return out, fmt.Errorf("parted %s failed: %w; output: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func parseByteField(value string) (int64, error) {
	value = strings.TrimSuffix(value, "B")
	return strconv.ParseInt(value, 10, 64)
}

func parsePartedTable(output []byte) ([]partedPartition, []partedExtent, error) {
	var partitions []partedPartition
	var freeExtents []partedExtent
	for line := range strings.SplitSeq(string(output), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, ";"))
		if line == "" || line == "BYT" || strings.HasPrefix(line, "/") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) == 5 && fields[4] == "free" {
			start, startErr := parseByteField(fields[1])
			end, endErr := parseByteField(fields[2])
			if startErr != nil || endErr != nil {
				return nil, nil, fmt.Errorf("parse free extent %q", line)
			}
			freeExtents = append(freeExtents, partedExtent{start: start, end: end})
			continue
		}
		if len(fields) < 7 {
			return nil, nil, fmt.Errorf("parse partition table row %q", line)
		}
		number, numberErr := strconv.Atoi(fields[0])
		start, startErr := parseByteField(fields[1])
		end, endErr := parseByteField(fields[2])
		if numberErr != nil || startErr != nil || endErr != nil || number < 1 {
			return nil, nil, fmt.Errorf("parse partition table row %q", line)
		}
		partitions = append(partitions, partedPartition{
			number: number,
			start:  start,
			end:    end,
			name:   fields[5],
		})
	}
	return partitions, freeExtents, nil
}

func readPartedTable(ctx context.Context, devicePath string, includeFree bool) ([]partedPartition, []partedExtent, error) {
	args := []string{"-m", "-s", devicePath, "unit", "B", "print"}
	if includeFree {
		args = append(args, "free")
	}
	out, err := runParted(ctx, args...)
	if err != nil {
		return nil, nil, err
	}
	return parsePartedTable(out)
}

func alignUp(value, alignment int64) int64 {
	return ((value + alignment - 1) / alignment) * alignment
}

func selectFreeExtent(extents []partedExtent, sizeBytes int64) (int64, int64, error) {
	if sizeBytes <= 0 {
		return 0, 0, fmt.Errorf("partition size must be positive, got %d", sizeBytes)
	}
	for _, extent := range extents {
		start := alignUp(extent.start, partitionAlignmentBytes)
		if start <= extent.end && sizeBytes-1 <= extent.end-start {
			return start, start + sizeBytes - 1, nil
		}
	}
	return 0, 0, fmt.Errorf("no free extent can satisfy a %d-byte partition", sizeBytes)
}

func lowestAvailablePartitionNumber(partitions []partedPartition) int {
	used := make(map[int]struct{}, len(partitions))
	for _, partition := range partitions {
		used[partition.number] = struct{}{}
	}
	for number := 1; ; number++ {
		if _, exists := used[number]; !exists {
			return number
		}
	}
}

func partitionsNamed(partitions []partedPartition, name string) []partedPartition {
	var matches []partedPartition
	for _, partition := range partitions {
		if partition.name == name {
			matches = append(matches, partition)
		}
	}
	return matches
}

func partitionPath(devicePath string, number int) string {
	return fmt.Sprintf("%sp%d", devicePath, number)
}

func partitionNumberFromPath(devicePath, backendVolumeID string) (int, error) {
	prefix := devicePath + "p"
	if !strings.HasPrefix(backendVolumeID, prefix) {
		return 0, fmt.Errorf("backend volume %q does not belong to device %q", backendVolumeID, devicePath)
	}
	number, err := strconv.Atoi(strings.TrimPrefix(backendVolumeID, prefix))
	if err != nil || number < 1 || filepath.Clean(backendVolumeID) != backendVolumeID {
		return 0, fmt.Errorf("backend volume %q has no valid partition number", backendVolumeID)
	}
	return number, nil
}

func waitForPartition(ctx context.Context, path string) error {
	for attempt := 0; attempt < partitionPollCount; attempt++ {
		if _, err := partitionPathStat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(partitionPollPeriod):
		}
	}
	return fmt.Errorf("partition device %s did not appear after %d attempts", path, partitionPollCount)
}

func (pv *PartedVolumeManager) SetupStorage(ctx context.Context, devicePath string, deviceName string) error {
	lock := devicePartitionLock(devicePath)
	lock.Lock()
	defer lock.Unlock()

	if _, _, err := readPartedTable(ctx, devicePath, false); err == nil {
		klog.Infof("Storage already configured on %s (partition table exists)", devicePath)
		return nil
	}

	klog.Infof("Wiping and initializing GPT label on %s", devicePath)
	if out, err := executeWipefs(ctx, devicePath); err != nil {
		klog.Warningf("wipefs error (ignoring): %v output: %s", err, string(out))
	}
	if _, err := runParted(ctx, "-s", devicePath, "mklabel", "gpt"); err != nil {
		return err
	}
	return nil
}

func (pv *PartedVolumeManager) CreateVolume(ctx context.Context, devicePath string, deviceName string, volumeName string, sizeBytes int64) (VolumeIdentity, error) {
	lock := devicePartitionLock(devicePath)
	lock.Lock()
	defer lock.Unlock()

	allocatedBytes, err := capacity.RoundUp(sizeBytes)
	if err != nil {
		return VolumeIdentity{}, err
	}
	partitions, freeExtents, err := readPartedTable(ctx, devicePath, true)
	if err != nil {
		return VolumeIdentity{}, err
	}
	existing := partitionsNamed(partitions, volumeName)
	if len(existing) > 1 {
		return VolumeIdentity{}, fmt.Errorf("multiple physical partitions claim ownership by volume %q", volumeName)
	}
	if len(existing) == 1 {
		path := partitionPath(devicePath, existing[0].number)
		if err := waitForPartition(ctx, path); err != nil {
			return VolumeIdentity{}, err
		}
		return VolumeIdentity{
			BackendVolumeID: path,
			CapacityBytes:   existing[0].end - existing[0].start + 1,
			VolumeName:      volumeName,
		}, nil
	}

	partitionNumber := lowestAvailablePartitionNumber(partitions)
	start, end, err := selectFreeExtent(freeExtents, allocatedBytes)
	if err != nil {
		return VolumeIdentity{}, err
	}
	klog.Infof("Creating partition %d for %s on %s (%dB -> %dB)", partitionNumber, volumeName, devicePath, start, end)
	if _, err := runParted(ctx, "-s", "-a", "optimal", devicePath, "mkpart", volumeName,
		fmt.Sprintf("%dB", start), fmt.Sprintf("%dB", end)); err != nil {
		return VolumeIdentity{}, err
	}

	partitions, _, err = readPartedTable(ctx, devicePath, false)
	if err != nil {
		return VolumeIdentity{}, err
	}
	created := partitionsNamed(partitions, volumeName)
	if len(created) != 1 {
		return VolumeIdentity{}, fmt.Errorf("partition table contains %d partitions owned by volume %q after creation", len(created), volumeName)
	}
	if created[0].number != partitionNumber {
		return VolumeIdentity{}, fmt.Errorf("partition-number allocation raced: created p%d for %q, expected reusable p%d", created[0].number, volumeName, partitionNumber)
	}
	path := partitionPath(devicePath, partitionNumber)
	if err := waitForPartition(ctx, path); err != nil {
		return VolumeIdentity{}, err
	}
	return VolumeIdentity{
		BackendVolumeID: path,
		CapacityBytes:   created[0].end - created[0].start + 1,
		VolumeName:      volumeName,
	}, nil
}

func (pv *PartedVolumeManager) DeleteVolume(ctx context.Context, devicePath string, deviceName string, volumeName string, identity VolumeIdentity) error {
	lock := devicePartitionLock(devicePath)
	lock.Lock()
	defer lock.Unlock()

	partitions, _, err := readPartedTable(ctx, devicePath, false)
	if err != nil {
		return err
	}

	var target *partedPartition
	if identity.BackendVolumeID != "" {
		number, err := partitionNumberFromPath(devicePath, identity.BackendVolumeID)
		if err != nil {
			return err
		}
		for index := range partitions {
			if partitions[index].number == number {
				target = &partitions[index]
				break
			}
		}
		if target == nil {
			return nil
		}
		if target.name != volumeName {
			return fmt.Errorf("refusing to delete %s: partition p%d is owned by %q, not %q", identity.BackendVolumeID, number, target.name, volumeName)
		}
	} else {
		matches := partitionsNamed(partitions, volumeName)
		if len(matches) == 0 {
			return nil
		}
		if len(matches) > 1 {
			return fmt.Errorf("refusing ambiguous deletion: %d partitions are owned by %q", len(matches), volumeName)
		}
		target = &matches[0]
	}

	klog.Infof("Removing partition %d owned by %s from %s", target.number, volumeName, devicePath)
	_, err = runParted(ctx, "-s", devicePath, "rm", strconv.Itoa(target.number))
	return err
}
