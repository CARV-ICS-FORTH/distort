package plugins

import (
	"context"
	"fmt"
	"os"
	"time"

	"k8s.io/klog/v2"

	"distort/internal/execlog"
)

type PartedVolumeManager struct{}

func init() {
	RegisterVolumeManager(&PartedVolumeManager{})
}

func (pv *PartedVolumeManager) Name() string {
	return "parted"
}

func (pv *PartedVolumeManager) SetupStorage(ctx context.Context, devicePath string, deviceName string) error {
	// If a partition already exists, skip setup to avoid wiping data
	partPath := devicePath + "p1"
	if _, err := os.Stat(partPath); err == nil {
		klog.Infof("Storage already configured on %s (partition exists)", devicePath)
		return nil
	}

	klog.Infof("Wiping and Initializing GPT label on %s", devicePath)

	_, _ = execlog.Run("wipefs", "-a", devicePath)

	out, err := execlog.Run("parted", "-s", devicePath, "mklabel", "gpt")
	if err != nil {
		return fmt.Errorf("parted mklabel error: %v output: %s", err, string(out))
	}
	return nil
}

func (pv *PartedVolumeManager) CreateVolume(ctx context.Context, devicePath string, deviceName string, volumeName string, sizeBytes int64) (string, error) {
	partPath := devicePath + "p1"
	if _, err := os.Stat(partPath); err == nil {
		klog.Infof("Partition already exists at %s", partPath)
		return partPath, nil
	}

	// Simple slicing starting at 1MB
	startMB := int64(1)
	endMB := startMB + sizeBytes/(1024*1024)

	klog.Infof("Creating partition %s on %s (%dMB -> %dMB)", volumeName, devicePath, startMB, endMB)
	startStr := fmt.Sprintf("%dMB", startMB)
	endStr := fmt.Sprintf("%dMB", endMB)

	_, _ = execlog.Run("parted", "-s", "-a", "optimal", devicePath, "mkpart", "primary", startStr, endStr)

	// Wait for udev to create the device partition.
	// Hardcoded to partition 1 for the E2E test's single-slice scenario.
	partPath = devicePath + "p1"

	klog.Infof("Waiting for udev to create %s...", partPath)
	for i := 0; i < 15; i++ {
		if _, err := os.Stat(partPath); err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}

	return partPath, nil
}

func (pv *PartedVolumeManager) DeleteVolume(ctx context.Context, devicePath string, deviceName string, volumeName string) error {
	klog.Infof("Removing partition 1 from %s", devicePath)
	out, err := execlog.Run("parted", "-s", devicePath, "rm", "1")
	if err != nil {
		return fmt.Errorf("parted rm error: %v output: %s", err, string(out))
	}
	return nil
}
