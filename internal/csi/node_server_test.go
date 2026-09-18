package csi

import (
	"os"
	"testing"
)

func TestIsMountPoint(t *testing.T) {
	// Root directory / should always be a mount point
	mounted, err := isMountPoint("/")
	if err != nil {
		t.Fatalf("isMountPoint(/) returned error: %v", err)
	}
	if !mounted {
		t.Errorf("Expected / to be identified as a mount point, got false")
	}

	// Non-existent directory should not be a mount point
	mounted, err = isMountPoint("/nonexistent/directory/path/12345")
	if err != nil {
		t.Fatalf("isMountPoint non-existent path returned error: %v", err)
	}
	if mounted {
		t.Errorf("Expected non-existent path to not be a mount point, got true")
	}

	// /proc or /sys should be mount points on Linux
	if _, err := os.Stat("/proc"); err == nil {
		mounted, err = isMountPoint("/proc")
		if err != nil {
			t.Fatalf("isMountPoint(/proc) returned error: %v", err)
		}
		if !mounted {
			t.Errorf("Expected /proc to be identified as a mount point")
		}
	}
}
