package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDiscoveryFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func setupDiscoveryFixture(t *testing.T) string {
	t.Helper()
	oldNVMe, oldBlock := sysClassNVMe, sysClassBlock
	root := t.TempDir()
	sysClassNVMe = filepath.Join(root, "nvme")
	sysClassBlock = filepath.Join(root, "block")
	t.Cleanup(func() {
		sysClassNVMe = oldNVMe
		sysClassBlock = oldBlock
	})

	controller := filepath.Join(sysClassNVMe, "nvme0")
	writeDiscoveryFile(t, filepath.Join(controller, "transport"), "pcie\n")
	writeDiscoveryFile(t, filepath.Join(controller, "model"), "Virtual NVMe\n")
	writeDiscoveryFile(t, filepath.Join(controller, "serial"), "SERIAL-1\n")
	writeDiscoveryFile(t, filepath.Join(controller, "numa_node"), "2\n")
	if err := os.Symlink("../../../devices/pci0000:00/0000:01:00.0", filepath.Join(controller, "device")); err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFile(t, filepath.Join(sysClassBlock, "nvme0n1", "size"), "4096\n")

	fakeBin := t.TempDir()
	lsblk := filepath.Join(fakeBin, "lsblk")
	script := "#!/usr/bin/env bash\nif [[ ${LSBLK_FAIL:-0} == 1 ]]; then exit 9; fi\nprintf '%s\\n' \"${LSBLK_OUTPUT:-}\"\n"
	if err := os.WriteFile(lsblk, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NVME_ALLOWED_DEVICES", "")
	t.Setenv("NVME_EXCLUDE_DEVICES", "")
	t.Setenv("LSBLK_FAIL", "0")
	t.Setenv("LSBLK_OUTPUT", "")
	t.Setenv(unsafeMountInspectionEnv, "false")
	return "0000:01:00.0"
}

func TestDiscoverKernelNVMeReadsHardwareAndCapacity(t *testing.T) {
	pciAddress := setupDiscoveryFixture(t)
	devices, err := discoverKernelNVMe()
	if err != nil {
		t.Fatalf("discoverKernelNVMe returned error: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("discovered %d devices, want 1: %#v", len(devices), devices)
	}
	device := devices[0]
	if device.PCIAddress != pciAddress || device.SerialNumber != "SERIAL-1" || device.Model != "Virtual NVMe" || device.NUMANode != 2 {
		t.Fatalf("unexpected discovered metadata: %#v", device)
	}
	if device.TotalBytes != 4096*512 {
		t.Fatalf("TotalBytes = %d, want %d", device.TotalBytes, 4096*512)
	}
}

func TestDiscoverKernelNVMeSkipsMountedNamespaces(t *testing.T) {
	setupDiscoveryFixture(t)
	t.Setenv("LSBLK_OUTPUT", "/var/lib/data")
	devices, err := discoverKernelNVMe()
	if err != nil {
		t.Fatalf("discoverKernelNVMe returned error: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("mounted device was discovered: %#v", devices)
	}
}

func TestDiscoveryFiltersUseExactCommaSeparatedPCIAddresses(t *testing.T) {
	pciAddress := setupDiscoveryFixture(t)
	t.Setenv("NVME_ALLOWED_DEVICES", "prefix-"+pciAddress+"-suffix")
	devices, err := discoverKernelNVMe()
	if err == nil && len(devices) != 0 {
		t.Fatalf("substring allow-list entry incorrectly admitted %s", pciAddress)
	}
}

func TestDiscoveryFailsSafeWhenMountInspectionFails(t *testing.T) {
	setupDiscoveryFixture(t)
	t.Setenv("LSBLK_FAIL", "1")
	devices, err := discoverKernelNVMe()
	if err == nil && len(devices) != 0 {
		t.Fatalf("device was admitted after lsblk failed: %#v", devices)
	}
}

func TestDiscoveryNormalizesAndDeduplicatesPCIAddressLists(t *testing.T) {
	pciAddress := setupDiscoveryFixture(t)
	t.Setenv("NVME_ALLOWED_DEVICES", " 0000:01:00.0,0000:01:00.0 ")
	devices, err := discoverKernelNVMe()
	if err != nil || len(devices) != 1 || devices[0].PCIAddress != pciAddress {
		t.Fatalf("normalized allow list returned devices=%#v err=%v", devices, err)
	}
	t.Setenv("NVME_EXCLUDE_DEVICES", " 0000:01:00.0 ")
	devices, err = discoverKernelNVMe()
	if err != nil || len(devices) != 0 {
		t.Fatalf("exact exclusion returned devices=%#v err=%v", devices, err)
	}
}

func TestDiscoveryRejectsMalformedPCIAddressLists(t *testing.T) {
	setupDiscoveryFixture(t)
	for _, value := range []string{"0000:01:00.0,", "not-a-pci-address", "0000:01:00.8"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("NVME_ALLOWED_DEVICES", value)
			if _, err := discoverKernelNVMe(); err == nil {
				t.Fatalf("malformed allow list %q was accepted", value)
			}
		})
	}
}

func TestUnsafeMountInspectionOverrideIsExplicit(t *testing.T) {
	setupDiscoveryFixture(t)
	t.Setenv("LSBLK_FAIL", "1")
	t.Setenv(unsafeMountInspectionEnv, "true")
	devices, err := discoverKernelNVMe()
	if err != nil || len(devices) != 1 {
		t.Fatalf("unsafe override returned devices=%#v err=%v", devices, err)
	}
}
