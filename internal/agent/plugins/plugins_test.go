package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"distort/internal/volumeidentity"
)

func writeTestExecutable(t *testing.T, directory, name, body string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nset -eu\n"+body+"\n"), 0755); err != nil {
		t.Fatalf("writing test executable %s: %v", name, err)
	}
	return path
}

func TestBuiltInPluginsAreRegistered(t *testing.T) {
	for _, name := range []string{"spdk", "kernel"} {
		backend, err := GetTargetBackend(name)
		if err != nil || backend.Name() != name {
			t.Fatalf("backend %q is not registered correctly: backend=%v err=%v", name, backend, err)
		}
	}
	for _, name := range []string{"parted", "spdk-lvol"} {
		manager, err := GetVolumeManager(name)
		if err != nil || manager.Name() != name {
			t.Fatalf("volume manager %q is not registered correctly: manager=%v err=%v", name, manager, err)
		}
	}
	if _, err := GetTargetBackend("missing"); err == nil {
		t.Fatal("GetTargetBackend accepted an unregistered backend")
	}
	if _, err := GetVolumeManager("lvm"); err == nil {
		t.Fatal("GetVolumeManager accepted the unimplemented lvm manager")
	}
}

func TestKernelHostAccessRevokesOldHostBeforeAuthorizingReplacement(t *testing.T) {
	oldNVMetPath := nvmetPath
	nvmetPath = t.TempDir()
	t.Cleanup(func() { nvmetPath = oldNVMetPath })
	nqn := "nqn.test:kernel-fencing"
	subsystemPath := filepath.Join(nvmetPath, "subsystems", nqn)
	allowedHostsPath := filepath.Join(subsystemPath, "allowed_hosts")
	portSubsystemsPath := filepath.Join(nvmetPath, "ports", "1", "subsystems")
	for _, path := range []string{allowedHostsPath, portSubsystemsPath, filepath.Join(nvmetPath, "hosts")} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(subsystemPath, "attr_allow_any_host"), []byte("1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(subsystemPath, filepath.Join(portSubsystemsPath, nqn)); err != nil {
		t.Fatal(err)
	}

	backend := &KernelBackend{}
	firstHost := "nqn.2026-01.io.distort:host-11111111111111111111111111111111"
	secondHost := "nqn.2026-01.io.distort:host-22222222222222222222222222222222"
	for _, host := range []string{firstHost, secondHost, ""} {
		if err := backend.ReconcileHostAccess(context.Background(), nqn, host); err != nil {
			t.Fatalf("reconciling host %q: %v", host, err)
		}
		entries, err := os.ReadDir(allowedHostsPath)
		if err != nil {
			t.Fatal(err)
		}
		if host == "" && len(entries) != 0 {
			t.Fatalf("revocation retained allowed hosts: %#v", entries)
		}
		if host != "" && (len(entries) != 1 || entries[0].Name() != host) {
			t.Fatalf("allowed hosts after reconciling %q: %#v", host, entries)
		}
		if _, err := os.Lstat(filepath.Join(portSubsystemsPath, nqn)); err != nil {
			t.Fatalf("subsystem port link was not restored: %v", err)
		}
	}
	allowAny, err := os.ReadFile(filepath.Join(subsystemPath, "attr_allow_any_host"))
	if err != nil || strings.TrimSpace(string(allowAny)) != "0" {
		t.Fatalf("allow-any-host remains enabled: value=%q err=%v", allowAny, err)
	}
	linkPath := filepath.Join(portSubsystemsPath, nqn)
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := backend.ReconcileHostAccess(context.Background(), nqn, ""); err != nil {
		t.Fatalf("reconciling already exact access after link loss: %v", err)
	}
	if !kernelLinkMatches(linkPath, subsystemPath) {
		t.Fatal("exact host access reconciliation did not restore the missing port link")
	}
}

func TestKernelExportRepairsAndVerifiesCompleteConfigFSState(t *testing.T) {
	oldNVMetPath := nvmetPath
	nvmetPath = t.TempDir()
	t.Cleanup(func() { nvmetPath = oldNVMetPath })
	backend := &KernelBackend{}
	volumeName := "kernel-repair"
	blockPath := "/dev/nvme0n1p2"
	nqn, err := backend.ExportVolume(context.Background(), volumeName, blockPath, "192.0.2.10", 4420, nil)
	if err != nil {
		t.Fatalf("initial kernel export: %v", err)
	}
	if err := backend.CheckExport(context.Background(), nqn, blockPath, "192.0.2.10", 4420, nil); err != nil {
		t.Fatalf("healthy kernel export rejected: %v", err)
	}

	subsystemPath := filepath.Join(nvmetPath, "subsystems", nqn)
	portPath := filepath.Join(nvmetPath, "ports", kernelPortID)
	corruptions := []struct {
		name  string
		path  string
		value string
	}{
		{name: "namespace device", path: filepath.Join(subsystemPath, "namespaces", "1", "device_path"), value: "/dev/wrong"},
		{name: "namespace enable", path: filepath.Join(subsystemPath, "namespaces", "1", "enable"), value: "0"},
		{name: "address family", path: filepath.Join(portPath, "addr_adrfam"), value: "ipv6"},
		{name: "transport", path: filepath.Join(portPath, "addr_trtype"), value: "tcp"},
		{name: "service", path: filepath.Join(portPath, "addr_trsvcid"), value: "4421"},
		{name: "address", path: filepath.Join(portPath, "addr_traddr"), value: "192.0.2.11"},
	}
	for _, corruption := range corruptions {
		t.Run(corruption.name, func(t *testing.T) {
			if err := os.WriteFile(corruption.path, []byte(corruption.value), 0600); err != nil {
				t.Fatal(err)
			}
			if err := backend.CheckExport(context.Background(), nqn, blockPath, "192.0.2.10", 4420, nil); err == nil {
				t.Fatal("corrupted kernel export passed its health check")
			}
			if _, err := backend.ExportVolume(context.Background(), volumeName, blockPath, "192.0.2.10", 4420, nil); err != nil {
				t.Fatalf("repairing kernel export: %v", err)
			}
		})
	}

	linkPath := filepath.Join(portPath, "subsystems", nqn)
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := backend.CheckExport(context.Background(), nqn, blockPath, "192.0.2.10", 4420, nil); err == nil {
		t.Fatal("kernel export without a port link passed its health check")
	}
	if _, err := backend.ExportVolume(context.Background(), volumeName, blockPath, "192.0.2.10", 4420, nil); err != nil {
		t.Fatalf("repairing missing kernel port link: %v", err)
	}

	namespacePath := filepath.Join(subsystemPath, "namespaces", "1")
	for _, name := range []string{"device_path", "enable"} {
		if err := os.Remove(filepath.Join(namespacePath, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(namespacePath); err != nil {
		t.Fatal(err)
	}
	if err := backend.CheckExport(context.Background(), nqn, blockPath, "192.0.2.10", 4420, nil); err == nil {
		t.Fatal("kernel export without namespace 1 passed its health check")
	}
	if _, err := backend.ExportVolume(context.Background(), volumeName, blockPath, "192.0.2.10", 4420, nil); err != nil {
		t.Fatalf("repairing missing kernel namespace: %v", err)
	}

	if err := os.Remove(filepath.Join(portPath, "addr_traddr")); err != nil {
		t.Fatal(err)
	}
	if err := backend.CheckExport(context.Background(), nqn, blockPath, "192.0.2.10", 4420, nil); err == nil {
		t.Fatal("kernel export without listener address passed its health check")
	}
	if _, err := backend.ExportVolume(context.Background(), volumeName, blockPath, "192.0.2.10", 4420, nil); err != nil {
		t.Fatalf("repairing missing kernel listener address: %v", err)
	}

	extraNamespace := filepath.Join(subsystemPath, "namespaces", "2")
	if err := os.Mkdir(extraNamespace, 0755); err != nil {
		t.Fatal(err)
	}
	if err := backend.CheckExport(context.Background(), nqn, blockPath, "192.0.2.10", 4420, nil); err == nil {
		t.Fatal("kernel export with an extra namespace passed its health check")
	}
	if _, err := backend.ExportVolume(context.Background(), volumeName, blockPath, "192.0.2.10", 4420, nil); err != nil {
		t.Fatalf("removing stale kernel namespace: %v", err)
	}
}

func TestKernelExportUsesIPv6AddressFamily(t *testing.T) {
	oldNVMetPath := nvmetPath
	nvmetPath = t.TempDir()
	t.Cleanup(func() { nvmetPath = oldNVMetPath })
	backend := &KernelBackend{}
	nqn, err := backend.ExportVolume(context.Background(), "kernel-ipv6", "/dev/nvme0n1p3", "2001:db8::10", 4420, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.CheckExport(context.Background(), nqn, "/dev/nvme0n1p3", "2001:db8::10", 4420, nil); err != nil {
		t.Fatalf("IPv6 kernel export rejected: %v", err)
	}
	family, err := configValue(filepath.Join(nvmetPath, "ports", kernelPortID, "addr_adrfam"))
	if err != nil || family != "ipv6" {
		t.Fatalf("kernel address family = %q, err=%v; want ipv6", family, err)
	}
}

func TestKernelExportRetriesAfterPartialLinkFailure(t *testing.T) {
	oldNVMetPath := nvmetPath
	nvmetPath = t.TempDir()
	t.Cleanup(func() { nvmetPath = oldNVMetPath })
	volumeName := "kernel-partial"
	nqn := volumeidentity.NQN(volumeName)
	linkPath := filepath.Join(nvmetPath, "ports", kernelPortID, "subsystems", nqn)
	if err := os.MkdirAll(linkPath, 0755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(linkPath, "not-a-symlink")
	if err := os.WriteFile(blocker, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	backend := &KernelBackend{}
	if _, err := backend.ExportVolume(context.Background(), volumeName, "/dev/nvme0n1p4", "192.0.2.10", 4420, nil); err == nil {
		t.Fatal("non-symlink port entry did not fail kernel export")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.ExportVolume(context.Background(), volumeName, "/dev/nvme0n1p4", "192.0.2.10", 4420, nil); err != nil {
		t.Fatalf("kernel export did not recover after partial link failure: %v", err)
	}
}

func TestSPDKHostAccessRevokesOldHostBeforeAuthorizingReplacement(t *testing.T) {
	fakeBin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "rpc-calls")
	script := `printf '%s\n' "$*" >> "$RPC_LOG"
if [ "$1" = nvmf_get_subsystems ]; then
  printf '[{"nqn":"nqn.test:spdk-fencing","allow_any_host":true,"hosts":[{"nqn":"nqn.test:old-host"}]}]\n'
else
  printf 'true\n'
fi`
	executable := writeTestExecutable(t, fakeBin, "rpc.py", script)
	oldExecutable := spdkRPCExecutable
	spdkRPCExecutable = executable
	t.Setenv("RPC_LOG", logPath)
	t.Cleanup(func() { spdkRPCExecutable = oldExecutable })

	newHost := "nqn.2026-01.io.distort:host-33333333333333333333333333333333"
	if err := (&SPDKBackend{}).ReconcileHostAccess(context.Background(), "nqn.test:spdk-fencing", newHost); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(calls)
	disable := strings.Index(text, "nvmf_subsystem_allow_any_host nqn.test:spdk-fencing -d")
	remove := strings.Index(text, "nvmf_subsystem_remove_host nqn.test:spdk-fencing nqn.test:old-host")
	add := strings.Index(text, "nvmf_subsystem_add_host nqn.test:spdk-fencing "+newHost)
	if disable < 0 || remove < disable || add < remove ||
		strings.Contains(text, "nvmf_subsystem_disconnect_host") || strings.Contains(text, "--timeout-ms") {
		t.Fatalf("unsafe SPDK host transition order:\n%s", text)
	}
}

func TestPartedSetupStorageIsIdempotentWhenPartitionExists(t *testing.T) {
	disk := installFakePartedDisk(t)
	disk.initialized = true
	disk.partitions = []partedPartition{{number: 2, start: 1024 * 1024, end: 2*1024*1024 - 1, name: "existing"}}
	if err := (&PartedVolumeManager{}).SetupStorage(context.Background(), disk.devicePath, "nvme0"); err != nil {
		t.Fatalf("SetupStorage returned error for existing partition: %v", err)
	}
	if disk.wipeCalls != 0 {
		t.Fatalf("SetupStorage wiped a device with an existing partition table")
	}
}

func TestPartedVolumesDoNotAliasPartitionOne(t *testing.T) {
	disk := installFakePartedDisk(t)
	manager := &PartedVolumeManager{}
	if err := manager.SetupStorage(context.Background(), disk.devicePath, "nvme0"); err != nil {
		t.Fatal(err)
	}
	first, err := manager.CreateVolume(context.Background(), disk.devicePath, "nvme0", "first", 128*1024*1024)
	if err != nil {
		t.Fatalf("creating first volume: %v", err)
	}
	second, err := manager.CreateVolume(context.Background(), disk.devicePath, "nvme0", "second", 128*1024*1024)
	if err != nil {
		t.Fatalf("creating second volume: %v", err)
	}
	if first.BackendVolumeID != disk.devicePath+"p1" || second.BackendVolumeID != disk.devicePath+"p2" {
		t.Fatalf("volume paths = %q and %q, want distinct p1 and p2", first, second)
	}

	// A fresh manager must recover the durable mapping from the GPT label.
	recovered, err := (&PartedVolumeManager{}).CreateVolume(context.Background(), disk.devicePath, "nvme0", "second", 128*1024*1024)
	if err != nil || recovered != second {
		t.Fatalf("recovering second volume after restart: path=%q err=%v", recovered, err)
	}

	if err := manager.DeleteVolume(context.Background(), disk.devicePath, "nvme0", "first", first); err != nil {
		t.Fatalf("deleting first volume: %v", err)
	}
	third, err := manager.CreateVolume(context.Background(), disk.devicePath, "nvme0", "third", 64*1024*1024)
	if err != nil {
		t.Fatalf("creating volume in reusable partition slot: %v", err)
	}
	if third.BackendVolumeID != disk.devicePath+"p1" {
		t.Fatalf("reusable partition path = %q, want %sp1", third, disk.devicePath)
	}
	if matches := partitionsNamed(disk.partitions, "second"); len(matches) != 1 || matches[0].number != 2 {
		t.Fatalf("deleting p1 damaged surviving volume: %#v", disk.partitions)
	}
	if err := manager.DeleteVolume(context.Background(), disk.devicePath, "nvme0", "first", third); err == nil {
		t.Fatal("deletion accepted a reused partition now owned by another volume")
	}
	if err := manager.DeleteVolume(context.Background(), disk.devicePath, "nvme0", "second", second); err != nil {
		t.Fatalf("deleting second volume: %v", err)
	}
	if matches := partitionsNamed(disk.partitions, "third"); len(matches) != 1 || matches[0].number != 1 {
		t.Fatalf("deleting p2 damaged surviving volume: %#v", disk.partitions)
	}
	fourth, err := manager.CreateVolume(context.Background(), disk.devicePath, "nvme0", "fourth", 64*1024*1024)
	if err != nil {
		t.Fatalf("reusing second partition slot: %v", err)
	}
	if fourth.BackendVolumeID != disk.devicePath+"p2" {
		t.Fatalf("second reusable partition path = %q, want %sp2", fourth, disk.devicePath)
	}
}

type fakePartedDisk struct {
	devicePath             string
	size                   int64
	initialized            bool
	partitions             []partedPartition
	wipeCalls              int
	mklabelCalls           int
	printErr               error
	printOutput            []byte
	wipeErr                error
	createdStartAdjustment int64
	mkpartErr              error
}

func installFakePartedDisk(t *testing.T) *fakePartedDisk {
	t.Helper()
	disk := &fakePartedDisk{devicePath: "/dev/nvme-testn1", size: 1024 * 1024 * 1024}
	oldParted := executeParted
	oldWipefs := executeWipefs
	oldStat := partitionPathStat
	oldPollPeriod := partitionPollPeriod
	oldPollCount := partitionPollCount
	executeParted = disk.runParted
	executeWipefs = func(context.Context, string) ([]byte, error) {
		disk.wipeCalls++
		return []byte("simulated wipefs output"), disk.wipeErr
	}
	partitionPathStat = func(string) (os.FileInfo, error) { return nil, nil }
	partitionPollCount = 1
	t.Cleanup(func() {
		executeParted = oldParted
		executeWipefs = oldWipefs
		partitionPathStat = oldStat
		partitionPollPeriod = oldPollPeriod
		partitionPollCount = oldPollCount
		partitionDeviceLocks.Delete(disk.devicePath)
	})
	return disk
}

func (d *fakePartedDisk) runParted(_ context.Context, args ...string) ([]byte, error) {
	for index, arg := range args {
		switch arg {
		case "print":
			if d.printErr != nil {
				return d.printOutput, d.printErr
			}
			if !d.initialized {
				return []byte("Error: unrecognised disk label"), &exec.ExitError{}
			}
			return []byte(d.table(index+1 < len(args) && args[index+1] == "free")), nil
		case "mklabel":
			d.mklabelCalls++
			d.initialized = true
			d.partitions = nil
			return nil, nil
		case "mkpart":
			if d.mkpartErr != nil {
				return []byte("simulated mkpart failure"), d.mkpartErr
			}
			name := args[index+1]
			start, err := parseByteField(args[index+2])
			if err != nil {
				return nil, err
			}
			end, err := parseByteField(args[index+3])
			if err != nil {
				return nil, err
			}
			start += d.createdStartAdjustment
			number := lowestAvailablePartitionNumber(d.partitions)
			d.partitions = append(d.partitions, partedPartition{number: number, start: start, end: end, name: name})
			return nil, nil
		case "rm":
			number, err := strconv.Atoi(args[index+1])
			if err != nil {
				return nil, err
			}
			for partitionIndex := range d.partitions {
				if d.partitions[partitionIndex].number == number {
					d.partitions = append(d.partitions[:partitionIndex], d.partitions[partitionIndex+1:]...)
					return nil, nil
				}
			}
			return nil, fmt.Errorf("partition %d does not exist", number)
		}
	}
	return nil, fmt.Errorf("unsupported parted arguments: %v", args)
}

func TestPartedSetupStorageFailsClosedOnInspectionAndWipeErrors(t *testing.T) {
	t.Run("inspection error", func(t *testing.T) {
		disk := installFakePartedDisk(t)
		disk.initialized = true
		disk.printErr = syscall.EIO
		disk.printOutput = []byte("input/output error")

		err := (&PartedVolumeManager{}).SetupStorage(context.Background(), disk.devicePath, "nvme0")
		if err == nil || !strings.Contains(err.Error(), "input/output error") {
			t.Fatalf("SetupStorage error = %v, want inspection failure", err)
		}
		if disk.wipeCalls != 0 || disk.mklabelCalls != 0 {
			t.Fatalf("inspection failure caused mutation: wipe=%d mklabel=%d", disk.wipeCalls, disk.mklabelCalls)
		}
	})

	t.Run("wipe error", func(t *testing.T) {
		disk := installFakePartedDisk(t)
		disk.wipeErr = syscall.EACCES

		err := (&PartedVolumeManager{}).SetupStorage(context.Background(), disk.devicePath, "nvme0")
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("SetupStorage error = %v, want wipe failure", err)
		}
		if disk.wipeCalls != 1 || disk.mklabelCalls != 0 {
			t.Fatalf("wipe failure caused unexpected mutation: wipe=%d mklabel=%d", disk.wipeCalls, disk.mklabelCalls)
		}
	})

	t.Run("unlabelled device", func(t *testing.T) {
		disk := installFakePartedDisk(t)
		if err := (&PartedVolumeManager{}).SetupStorage(context.Background(), disk.devicePath, "nvme0"); err != nil {
			t.Fatal(err)
		}
		if disk.wipeCalls != 1 || disk.mklabelCalls != 1 || !disk.initialized {
			t.Fatalf("initialization calls: wipe=%d mklabel=%d initialized=%t", disk.wipeCalls, disk.mklabelCalls, disk.initialized)
		}
	})
}

func (d *fakePartedDisk) table(includeFree bool) string {
	partitions := append([]partedPartition(nil), d.partitions...)
	sort.Slice(partitions, func(i, j int) bool { return partitions[i].start < partitions[j].start })
	var output strings.Builder
	fmt.Fprintf(&output, "BYT;\n%s:%dB:file:512:512:gpt::;\n", d.devicePath, d.size)
	cursor := int64(0)
	for _, partition := range partitions {
		if includeFree && cursor < partition.start {
			fmt.Fprintf(&output, "1:%dB:%dB:%dB:free;\n", cursor, partition.start-1, partition.start-cursor)
		}
		fmt.Fprintf(&output, "%d:%dB:%dB:%dB::%s:;\n", partition.number, partition.start, partition.end, partition.end-partition.start+1, partition.name)
		cursor = partition.end + 1
	}
	if includeFree && cursor < d.size {
		fmt.Fprintf(&output, "1:%dB:%dB:%dB:free;\n", cursor, d.size-1, d.size-cursor)
	}
	return output.String()
}

func TestPartedCreateReturnsCommandFailure(t *testing.T) {
	disk := installFakePartedDisk(t)
	disk.initialized = true
	disk.mkpartErr = &exec.ExitError{}

	_, err := (&PartedVolumeManager{}).CreateVolume(context.Background(), disk.devicePath, "nvme0", "broken", 64*1024*1024)
	if err == nil || !strings.Contains(err.Error(), "simulated mkpart failure") {
		t.Fatalf("CreateVolume error = %v, want mkpart command failure", err)
	}
}

func TestPartedCreateReturnsMissingExecutableFailure(t *testing.T) {
	disk := installFakePartedDisk(t)
	disk.initialized = true
	executeParted = func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "distort-parted-does-not-exist", args...).CombinedOutput()
	}

	_, err := (&PartedVolumeManager{}).CreateVolume(context.Background(), disk.devicePath, "nvme0", "missing", 64*1024*1024)
	if err == nil || !strings.Contains(err.Error(), "executable file not found") {
		t.Fatalf("CreateVolume error = %v, want missing executable failure", err)
	}
}

func TestPartedCreateRejectsInsufficientCapacity(t *testing.T) {
	disk := installFakePartedDisk(t)
	disk.initialized = true

	_, err := (&PartedVolumeManager{}).CreateVolume(context.Background(), disk.devicePath, "nvme0", "too-large", 2*disk.size)
	if err == nil || !strings.Contains(err.Error(), "no free extent") {
		t.Fatalf("CreateVolume error = %v, want insufficient capacity failure", err)
	}
}

func TestPartedCreateReturnsUdevTimeout(t *testing.T) {
	disk := installFakePartedDisk(t)
	disk.initialized = true
	partitionPathStat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	partitionPollPeriod = time.Millisecond
	partitionPollCount = 2

	_, err := (&PartedVolumeManager{}).CreateVolume(context.Background(), disk.devicePath, "nvme0", "missing-node", 64*1024*1024)
	if err == nil || !strings.Contains(err.Error(), "did not appear") {
		t.Fatalf("CreateVolume error = %v, want udev timeout", err)
	}
}

func TestPartedCreateHonorsContextCancellationDuringUdevWait(t *testing.T) {
	disk := installFakePartedDisk(t)
	disk.initialized = true
	partitionPathStat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	partitionPollPeriod = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (&PartedVolumeManager{}).CreateVolume(ctx, disk.devicePath, "nvme0", "cancelled", 64*1024*1024)
	if err != context.Canceled {
		t.Fatalf("CreateVolume error = %v, want context.Canceled", err)
	}
}

func TestPartedCreateVerifiesCreatedBoundaries(t *testing.T) {
	disk := installFakePartedDisk(t)
	disk.initialized = true
	disk.createdStartAdjustment = partitionAlignmentBytes

	_, err := (&PartedVolumeManager{}).CreateVolume(context.Background(), disk.devicePath, "nvme0", "shifted", 64*1024*1024)
	if err == nil || !strings.Contains(err.Error(), "boundaries") {
		t.Fatalf("CreateVolume error = %v, want boundary verification failure", err)
	}
}

func TestPartedRoundsCapacityUp(t *testing.T) {
	disk := installFakePartedDisk(t)
	manager := &PartedVolumeManager{}
	if err := manager.SetupStorage(context.Background(), disk.devicePath, "nvme0"); err != nil {
		t.Fatal(err)
	}
	identity, err := manager.CreateVolume(context.Background(), disk.devicePath, "nvme0", "rounded", 1024*1024+1)
	if err != nil {
		t.Fatalf("CreateVolume returned error: %v", err)
	}
	if identity.CapacityBytes != 2*1024*1024 {
		t.Fatalf("allocated capacity = %d, want 2 MiB", identity.CapacityBytes)
	}
}

func TestSPDKCoreMaskCannotExecuteShellSyntax(t *testing.T) {
	fakeBin := t.TempDir()
	writeTestExecutable(t, fakeBin, "pidof", "exit 1")
	writeTestExecutable(t, fakeBin, "nvmf_tgt", "exit 0")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	proof := filepath.Join(t.TempDir(), "shell-injection-proof")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := EnsureSPDKRunning(ctx, "0x1; touch "+proof); err == nil {
		t.Fatal("malicious core mask was accepted")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(proof); err == nil {
			t.Fatalf("core mask was interpreted by a shell and created %s", proof)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSPDKStartupUsesExactExecutableAndArguments(t *testing.T) {
	fakeBin := t.TempDir()
	writeTestExecutable(t, fakeBin, "pidof", "exit 1")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SPDK_IOBUF_SMALL_POOL_COUNT", "")
	t.Setenv("SPDK_IOBUF_LARGE_POOL_COUNT", "")

	capture := filepath.Join(t.TempDir(), "nvmf-target-arguments")
	target := writeTestExecutable(t, fakeBin, "nvmf_tgt", fmt.Sprintf("printf '%%s\\n' \"$0\" \"$@\" > %q", capture))
	rpc := writeTestExecutable(t, fakeBin, "rpc.py", `
case "$1" in
  rpc_get_methods) printf '[]\n' ;;
  *) exit 0 ;;
esac`)

	oldTargetExecutable := spdkTargetExecutable
	oldRPCExecutable := spdkRPCExecutable
	oldPrepareSPDKProcess := prepareSPDKProcess
	spdkTargetExecutable = target
	spdkRPCExecutable = rpc
	prepareSPDKProcess = func() error { return nil }
	t.Cleanup(func() {
		spdkTargetExecutable = oldTargetExecutable
		spdkRPCExecutable = oldRPCExecutable
		prepareSPDKProcess = oldPrepareSPDKProcess
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := EnsureSPDKRunning(ctx, "0x3"); err != nil {
		t.Fatalf("EnsureSPDKRunning returned error: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		arguments, err := os.ReadFile(capture)
		if err == nil {
			if got := strings.Fields(string(arguments)); len(got) != 3 || got[0] != target || got[1] != "-m" || got[2] != "0x3" {
				t.Fatalf("nvmf_tgt invocation = %q, want [%q -m 0x3]", got, target)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nvmf_tgt did not capture its arguments: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSPDKCoreMaskMustMatchRunningNodeProcess(t *testing.T) {
	rpc := writeTestExecutable(t, t.TempDir(), "rpc.py", `
case "$1" in
  rpc_get_methods) printf '[]\n' ;;
  *) exit 0 ;;
esac`)
	oldInspect := inspectSPDKProcess
	oldRPC := spdkRPCExecutable
	oldManagedExit := spdkManagedExit
	inspectSPDKProcess = func() (spdkProcessState, error) {
		return spdkProcessState{running: true, coreMask: "0x1"}, nil
	}
	spdkRPCExecutable = rpc
	spdkManagedExit = nil
	t.Cleanup(func() {
		inspectSPDKProcess = oldInspect
		spdkRPCExecutable = oldRPC
		spdkManagedExit = oldManagedExit
	})

	if err := EnsureSPDKRunning(context.Background(), "0x0001"); err != nil {
		t.Fatalf("equivalent running core mask was rejected: %v", err)
	}
	if err := EnsureSPDKRunning(context.Background(), "0x3"); err == nil || !strings.Contains(err.Error(), "running nvmf_tgt uses core mask") {
		t.Fatalf("conflicting node-global core mask error = %v", err)
	}
}

func TestSPDKInitializationFailuresTerminateAndPermitCleanRetry(t *testing.T) {
	for _, failedMethod := range []string{"iobuf_set_options", "framework_start_init", "framework_wait_init"} {
		t.Run(failedMethod, func(t *testing.T) {
			fakeBin := t.TempDir()
			writeTestExecutable(t, fakeBin, "pidof", "exit 1")
			t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("SPDK_IOBUF_SMALL_POOL_COUNT", "4096")
			t.Setenv("SPDK_IOBUF_LARGE_POOL_COUNT", "256")
			t.Setenv("FAIL_METHOD", failedMethod)
			pidPath := filepath.Join(t.TempDir(), "nvmf-target-pid")
			t.Setenv("SPDK_TEST_PID", pidPath)
			target := writeTestExecutable(t, fakeBin, "nvmf_tgt", `printf '%s' "$$" > "$SPDK_TEST_PID"
exec sleep 30`)
			rpc := writeTestExecutable(t, fakeBin, "rpc.py", `
if [ "$1" = rpc_get_methods ]; then
  printf '[]\n'
elif [ "$1" = "${FAIL_METHOD:-}" ]; then
  printf 'injected failure for %s\n' "$1" >&2
  exit 9
else
  printf 'true\n'
fi`)

			oldTarget := spdkTargetExecutable
			oldRPC := spdkRPCExecutable
			oldPrepare := prepareSPDKProcess
			oldManagedExit := spdkManagedExit
			spdkTargetExecutable = target
			spdkRPCExecutable = rpc
			prepareSPDKProcess = func() error { return nil }
			spdkManagedExit = nil
			t.Cleanup(func() {
				spdkTargetExecutable = oldTarget
				spdkRPCExecutable = oldRPC
				prepareSPDKProcess = oldPrepare
				spdkManagedExit = oldManagedExit
			})

			if err := EnsureSPDKRunning(context.Background(), "0x1"); err == nil || !strings.Contains(err.Error(), failedMethod) {
				t.Fatalf("initialization failure = %v, want %s", err, failedMethod)
			}
			if spdkManagedExit != nil {
				t.Fatal("failed initialization retained managed process state")
			}
			pidBytes, err := os.ReadFile(pidPath)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(pidBytes))
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("failed initialization left process %d alive: %v", pid, err)
			}

			t.Setenv("FAIL_METHOD", "")
			if err := EnsureSPDKRunning(context.Background(), "0x1"); err != nil {
				t.Fatalf("clean retry failed: %v", err)
			}
			pidBytes, err = os.ReadFile(pidPath)
			if err != nil {
				t.Fatal(err)
			}
			pid, err = strconv.Atoi(string(pidBytes))
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			select {
			case <-spdkManagedExit:
				spdkManagedExit = nil
			case <-time.After(time.Second):
				t.Fatal("timed out reaping successful test nvmf_tgt process")
			}
		})
	}
}

func TestSPDKIobufPoolArgs(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		t.Setenv("SPDK_IOBUF_SMALL_POOL_COUNT", "")
		t.Setenv("SPDK_IOBUF_LARGE_POOL_COUNT", "")
		args, enabled, err := spdkIobufPoolArgs()
		if err != nil || enabled || args != nil {
			t.Fatalf("unexpected default options: args=%v enabled=%v err=%v", args, enabled, err)
		}
	})

	t.Run("returns configured pools", func(t *testing.T) {
		t.Setenv("SPDK_IOBUF_SMALL_POOL_COUNT", "4096")
		t.Setenv("SPDK_IOBUF_LARGE_POOL_COUNT", "256")
		args, enabled, err := spdkIobufPoolArgs()
		if err != nil || !enabled {
			t.Fatalf("configured options were rejected: enabled=%v err=%v", enabled, err)
		}
		if got := strings.Join(args, " "); got != "--small-pool-count 4096 --large-pool-count 256" {
			t.Fatalf("unexpected RPC arguments: %s", got)
		}
	})

	for _, tc := range []struct {
		name  string
		small string
		large string
	}{
		{name: "missing large pool", small: "4096"},
		{name: "zero small pool", small: "0", large: "256"},
		{name: "malformed large pool", small: "4096", large: "many"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPDK_IOBUF_SMALL_POOL_COUNT", tc.small)
			t.Setenv("SPDK_IOBUF_LARGE_POOL_COUNT", tc.large)
			if _, _, err := spdkIobufPoolArgs(); err == nil {
				t.Fatal("invalid pool configuration was accepted")
			}
		})
	}
}

func TestSPDKNVMfTransportArgs(t *testing.T) {
	t.Run("uses SPDK SRQ default", func(t *testing.T) {
		t.Setenv("SPDK_NVMF_MAX_SRQ_DEPTH", "")
		args, err := spdkNVMfTransportArgs()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(args, " "); got != "-t RDMA -u 8192 -i 131072 -c 8192" {
			t.Fatalf("unexpected default transport arguments: %s", got)
		}
	})

	t.Run("sets bounded SRQ depth", func(t *testing.T) {
		t.Setenv("SPDK_NVMF_MAX_SRQ_DEPTH", "128")
		args, err := spdkNVMfTransportArgs()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(args, " "); got != "-t RDMA -u 8192 -i 131072 -c 8192 -s 128" {
			t.Fatalf("unexpected configured transport arguments: %s", got)
		}
	})

	for _, value := range []string{"0", "many"} {
		t.Run("rejects "+value, func(t *testing.T) {
			t.Setenv("SPDK_NVMF_MAX_SRQ_DEPTH", value)
			if _, err := spdkNVMfTransportArgs(); err == nil {
				t.Fatalf("invalid SRQ depth %q was accepted", value)
			}
		})
	}
}

func TestCallSPDKRPCParsesJSONAndNakedStrings(t *testing.T) {
	fakeBin := t.TempDir()
	rpc := writeTestExecutable(t, fakeBin, "rpc.py", `
case "$1" in
  list) printf '[{"name":"one"}]\n' ;;
  create) printf '550e8400-e29b-41d4-a716-446655440000\n' ;;
  *) printf 'unsupported\n' >&2; exit 7 ;;
esac`)
	oldExecutable := spdkRPCExecutable
	spdkRPCExecutable = rpc
	t.Cleanup(func() { spdkRPCExecutable = oldExecutable })

	var list []struct {
		Name string `json:"name"`
	}
	if err := CallSPDKRPC("list", &list); err != nil || len(list) != 1 || list[0].Name != "one" {
		t.Fatalf("JSON response was not parsed: list=%#v err=%v", list, err)
	}
	var uuid string
	if err := CallSPDKRPC("create", &uuid); err != nil || uuid != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("naked string response was not parsed: uuid=%q err=%v", uuid, err)
	}
	if err := CallSPDKRPC("failure", nil); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("RPC failure did not include stderr: %v", err)
	}
}

func TestCallSPDKRPCContextTerminatesHungCommand(t *testing.T) {
	rpc := writeTestExecutable(t, t.TempDir(), "rpc.py", `sleep 10`)
	oldExecutable := spdkRPCExecutable
	spdkRPCExecutable = rpc
	t.Cleanup(func() { spdkRPCExecutable = oldExecutable })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := CallSPDKRPCContext(ctx, "hung", nil)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("hung RPC error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("hung RPC took %s to terminate", elapsed)
	}
}

func TestSPDKExportHealthChecksBackingBdevAndListener(t *testing.T) {
	fakeBin := t.TempDir()
	writeTestExecutable(t, fakeBin, "pidof", "exit 0")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	rpc := writeTestExecutable(t, fakeBin, "rpc.py", `
case "$1" in
  rpc_get_methods) printf '[]\n' ;;
  bdev_get_bdevs)
    if [ "$3" = "lvs/volume" ]; then
      printf '[{"name":"lvol-uuid","uuid":"lvol-uuid","aliases":["lvs/volume"]}]\n'
    else
      printf '[]\n'
    fi ;;
  nvmf_get_subsystems) printf '[{"nqn":"nqn.test","namespaces":[{"bdev_name":"lvol-uuid"}],"listen_addresses":[{"trtype":"RDMA","traddr":"192.0.2.10","trsvcid":"4420"}]}]\n' ;;
  *) printf 'unexpected method %s\n' "$1" >&2; exit 8 ;;
esac`)
	oldExecutable := spdkRPCExecutable
	oldInspect := inspectSPDKProcess
	spdkRPCExecutable = rpc
	inspectSPDKProcess = func() (spdkProcessState, error) {
		return spdkProcessState{running: true, coreMask: "0x1"}, nil
	}
	t.Cleanup(func() {
		spdkRPCExecutable = oldExecutable
		inspectSPDKProcess = oldInspect
	})

	backend := &SPDKBackend{}
	if err := backend.CheckExport(context.Background(), "nqn.test", "lvs/volume", "192.0.2.10", 4420, nil); err != nil {
		t.Fatalf("healthy export rejected: %v", err)
	}
	if err := backend.CheckExport(context.Background(), "nqn.test", "lvs/wrong", "192.0.2.10", 4420, nil); err == nil {
		t.Fatal("wrong backing bdev was accepted")
	}
	if err := backend.CheckExport(context.Background(), "nqn.test", "lvs/volume", "192.0.2.11", 4420, nil); err == nil {
		t.Fatal("wrong listener address was accepted")
	}
}

func TestSPDKExportObservationFailureDoesNotReplaceLiveSubsystem(t *testing.T) {
	fakeBin := t.TempDir()
	writeTestExecutable(t, fakeBin, "pidof", "exit 0")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	firstInspection := filepath.Join(t.TempDir(), "first-inspection")
	mutations := filepath.Join(t.TempDir(), "mutations")
	volumeName := "observation-safe"
	nqn := volumeidentity.NQN(volumeName)
	rpcBody := fmt.Sprintf(`
case "$1" in
  rpc_get_methods) printf '[]\n' ;;
  nvmf_get_subsystems) printf '[{"nqn":%q,"namespaces":[{"bdev_name":"lvol-uuid"}],"listen_addresses":[{"trtype":"RDMA","traddr":"192.0.2.10","trsvcid":"4420"}]}]\n' ;;
  bdev_get_bdevs)
    if [ ! -f %q ]; then
      touch %q
      printf 'temporary inspection failure\n' >&2
      exit 9
    fi
    printf '[{"name":"lvol-uuid","uuid":"lvol-uuid","aliases":["lvs/volume"]}]\n' ;;
  nvmf_delete_subsystem|nvmf_create_subsystem|nvmf_subsystem_add_ns|nvmf_subsystem_add_listener)
    printf '%%s\n' "$*" >> %q
    printf 'true\n' ;;
  *) printf 'unexpected method %%s\n' "$1" >&2; exit 8 ;;
esac`, nqn, firstInspection, firstInspection, mutations)
	rpc := writeTestExecutable(t, fakeBin, "rpc.py", rpcBody)
	oldExecutable := spdkRPCExecutable
	oldInspect := inspectSPDKProcess
	spdkRPCExecutable = rpc
	inspectSPDKProcess = func() (spdkProcessState, error) {
		return spdkProcessState{running: true, coreMask: "0x1"}, nil
	}
	t.Cleanup(func() {
		spdkRPCExecutable = oldExecutable
		inspectSPDKProcess = oldInspect
	})

	backend := &SPDKBackend{}
	if _, err := backend.ExportVolume(context.Background(), volumeName, "lvs/volume", "192.0.2.10", 4420, nil); !IsExportObservationError(err) {
		t.Fatalf("first ExportVolume error = %v, want observation error", err)
	}
	if _, err := os.Stat(mutations); !os.IsNotExist(err) {
		t.Fatalf("observation failure mutated live subsystem; stat error=%v", err)
	}
	if got, err := backend.ExportVolume(context.Background(), volumeName, "lvs/volume", "192.0.2.10", 4420, nil); err != nil || got != nqn {
		t.Fatalf("healthy retry returned nqn=%q err=%v", got, err)
	}
	if _, err := os.Stat(mutations); !os.IsNotExist(err) {
		t.Fatalf("healthy retry mutated exact subsystem; stat error=%v", err)
	}
}

func TestSPDKExportRepairsConfirmedMismatch(t *testing.T) {
	fakeBin := t.TempDir()
	writeTestExecutable(t, fakeBin, "pidof", "exit 0")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	callsPath := filepath.Join(t.TempDir(), "mutation-calls")
	volumeName := "confirmed-mismatch"
	nqn := volumeidentity.NQN(volumeName)
	rpcBody := fmt.Sprintf(`
case "$1" in
  rpc_get_methods) printf '[]\n' ;;
  nvmf_get_subsystems) printf '[{"nqn":%q,"namespaces":[{"bdev_name":"lvol-uuid"}],"listen_addresses":[{"trtype":"RDMA","traddr":"192.0.2.99","trsvcid":"4420"}]}]\n' ;;
  bdev_get_bdevs) printf '[{"name":"lvol-uuid","aliases":["lvs/volume"]}]\n' ;;
  nvmf_get_transports) printf '[{"trtype":"RDMA"}]\n' ;;
  nvmf_delete_subsystem|nvmf_create_subsystem|nvmf_subsystem_add_ns|nvmf_subsystem_add_listener)
    printf '%%s\n' "$*" >> %q
    printf 'true\n' ;;
  *) printf 'unexpected method %%s\n' "$1" >&2; exit 8 ;;
esac`, nqn, callsPath)
	rpc := writeTestExecutable(t, fakeBin, "rpc.py", rpcBody)
	oldExecutable := spdkRPCExecutable
	oldInspect := inspectSPDKProcess
	spdkRPCExecutable = rpc
	inspectSPDKProcess = func() (spdkProcessState, error) {
		return spdkProcessState{running: true, coreMask: "0x1"}, nil
	}
	t.Cleanup(func() {
		spdkRPCExecutable = oldExecutable
		inspectSPDKProcess = oldInspect
	})

	got, err := (&SPDKBackend{}).ExportVolume(context.Background(), volumeName, "lvs/volume", "192.0.2.10", 4420, nil)
	if err != nil || got != nqn {
		t.Fatalf("confirmed mismatch repair returned nqn=%q err=%v", got, err)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"nvmf_delete_subsystem " + nqn,
		"nvmf_create_subsystem " + nqn + " -s distort",
		"nvmf_subsystem_add_ns " + nqn + " lvs/volume",
		"nvmf_subsystem_add_listener " + nqn + " -t RDMA -a 192.0.2.10 -s 4420",
	}
	if gotCalls := strings.FieldsFunc(strings.TrimSpace(string(calls)), func(r rune) bool { return r == '\n' }); !slices.Equal(gotCalls, want) {
		t.Fatalf("repair calls = %#v, want %#v", gotCalls, want)
	}
}

func TestKernelSetupDeviceReturnsSPDKResetFailure(t *testing.T) {
	oldReset := resetSPDKDevice
	resetSPDKDevice = func(context.Context, string) error { return syscall.EIO }
	t.Cleanup(func() { resetSPDKDevice = oldReset })

	err := (&KernelBackend{}).SetupDevice(context.Background(), "0000:01:00.0", "nvme0", nil)
	if err == nil || !strings.Contains(err.Error(), "input/output error") {
		t.Fatalf("SetupDevice error = %v, want reset failure", err)
	}
}

func TestSPDKLvolPersistsAndDeletesExactCreatedIdentity(t *testing.T) {
	state := filepath.Join(t.TempDir(), "lvol-exists")
	deleted := filepath.Join(t.TempDir(), "deleted-uuid")
	rpcBody := fmt.Sprintf(`
case "$1" in
  bdev_lvol_get_lvstores) printf '[{"uuid":"store-uuid","name":"lvs_base-n1","base_bdev":"base-n1"}]\n' ;;
  bdev_get_bdevs)
    if [ -f %q ]; then
      printf '[{"name":"lvol-uuid","uuid":"lvol-uuid","aliases":["lvs_base-n1/volume-id"],"block_size":4096,"num_blocks":16384,"driver_specific":{"lvol":{"lvol_store_uuid":"store-uuid"}}}]\n'
    else
      printf '[]\n'
    fi ;;
  bdev_lvol_create) touch %q; printf 'lvol-uuid\n' ;;
  bdev_lvol_delete) printf '%%s' "$2" > %q; rm -f %q; printf 'true\n' ;;
  *) printf 'unexpected method %%s\n' "$1" >&2; exit 8 ;;
esac`, state, state, deleted, state)
	rpc := writeTestExecutable(t, t.TempDir(), "rpc.py", rpcBody)
	oldExecutable := spdkRPCExecutable
	spdkRPCExecutable = rpc
	t.Cleanup(func() { spdkRPCExecutable = oldExecutable })

	manager := &SPDKLvolManager{}
	identity, err := manager.CreateVolume(context.Background(), "base-n1", "base-n1", "volume-id", 64*1024*1024)
	if err != nil {
		t.Fatalf("CreateVolume returned error: %v", err)
	}
	want := (VolumeIdentity{
		BackendVolumeID: "lvs_base-n1/volume-id",
		CapacityBytes:   64 * 1024 * 1024,
		BaseBdev:        "base-n1",
		VolumeStoreName: "lvs_base-n1",
		VolumeStoreUUID: "store-uuid",
		VolumeName:      "volume-id",
		VolumeUUID:      "lvol-uuid",
	})
	if identity != want {
		t.Fatalf("created identity = %#v, want %#v", identity, want)
	}
	if err := manager.DeleteVolume(context.Background(), "wrong-guessed-controller", "wrong-guessed-controller", "volume-id", identity); err != nil {
		t.Fatalf("DeleteVolume returned error: %v", err)
	}
	deletedUUID, err := os.ReadFile(deleted)
	if err != nil {
		t.Fatal(err)
	}
	if string(deletedUUID) != "lvol-uuid" {
		t.Fatalf("deleted bdev = %q, want exact lvol UUID", deletedUUID)
	}
	if err := manager.DeleteVolume(context.Background(), "wrong-guessed-controller", "wrong-guessed-controller", "volume-id", identity); err != nil {
		t.Fatalf("idempotent DeleteVolume returned error: %v", err)
	}
}

func TestSPDKLvolLegacyIdentityRecoversExactBdevAfterPartialDelete(t *testing.T) {
	state := filepath.Join(t.TempDir(), "lvol-exists")
	if err := os.WriteFile(state, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rpcBody := fmt.Sprintf(`
case "$1" in
  bdev_lvol_get_lvstores) printf '[{"uuid":"store-uuid","name":"actual-store","base_bdev":"actual-controller-n1"}]\n' ;;
  bdev_get_bdevs)
    if [ -f %q ]; then
      printf '[{"name":"legacy-uuid","aliases":["actual-store/legacy-volume"]},{"name":"other-uuid","aliases":["other-store/legacy-volume"]}]\n'
    else
      printf '[{"name":"other-uuid","aliases":["other-store/legacy-volume"]}]\n'
    fi ;;
  bdev_lvol_delete) rm -f %q; printf 'response lost\n' >&2; exit 9 ;;
  *) printf 'unexpected method %%s\n' "$1" >&2; exit 8 ;;
esac`, state, state)
	rpc := writeTestExecutable(t, t.TempDir(), "rpc.py", rpcBody)
	oldExecutable := spdkRPCExecutable
	spdkRPCExecutable = rpc
	t.Cleanup(func() { spdkRPCExecutable = oldExecutable })

	legacy := VolumeIdentity{BackendVolumeID: "actual-store/legacy-volume"}
	if err := (&SPDKLvolManager{}).DeleteVolume(context.Background(), "guessed-controller", "guessed-controller", "legacy-volume", legacy); err != nil {
		t.Fatalf("legacy cleanup did not verify partial deletion success: %v", err)
	}
}

func TestSPDKLvolRefusesAliasReusedByDifferentUUID(t *testing.T) {
	rpcBody := `
case "$1" in
  bdev_lvol_get_lvstores) printf '[{"uuid":"store-uuid","name":"store","base_bdev":"base-n1"}]\n' ;;
  bdev_get_bdevs) printf '[{"name":"replacement-uuid","aliases":["store/volume-id"]}]\n' ;;
  bdev_lvol_delete) printf 'unsafe delete called\n' >&2; exit 99 ;;
  *) printf 'unexpected method %s\n' "$1" >&2; exit 8 ;;
esac`
	rpc := writeTestExecutable(t, t.TempDir(), "rpc.py", rpcBody)
	oldExecutable := spdkRPCExecutable
	spdkRPCExecutable = rpc
	t.Cleanup(func() { spdkRPCExecutable = oldExecutable })

	identity := VolumeIdentity{
		BackendVolumeID: "store/volume-id",
		VolumeStoreName: "store",
		VolumeStoreUUID: "store-uuid",
		VolumeName:      "volume-id",
		VolumeUUID:      "deleted-original-uuid",
	}
	err := (&SPDKLvolManager{}).DeleteVolume(context.Background(), "base-n1", "base-n1", "volume-id", identity)
	if err == nil || !strings.Contains(err.Error(), "only some persisted SPDK identifiers resolve") {
		t.Fatalf("reused alias was not rejected safely: %v", err)
	}
}

func TestSPDKLvolRetryRequiresExactBackendCapacity(t *testing.T) {
	rpcBody := `
case "$1" in
  bdev_lvol_get_lvstores) printf '[{"uuid":"store-uuid","name":"store","base_bdev":"base-n1"}]\n' ;;
  bdev_get_bdevs) printf '[{"name":"lvol-uuid","uuid":"lvol-uuid","aliases":["store/volume-id"],"block_size":4096,"num_blocks":16384}]\n' ;;
  bdev_lvol_create) printf 'unsafe create called\n' >&2; exit 99 ;;
  *) printf 'unexpected method %s\n' "$1" >&2; exit 8 ;;
esac`
	rpc := writeTestExecutable(t, t.TempDir(), "rpc.py", rpcBody)
	oldExecutable := spdkRPCExecutable
	spdkRPCExecutable = rpc
	t.Cleanup(func() { spdkRPCExecutable = oldExecutable })

	manager := &SPDKLvolManager{}
	identity, err := manager.CreateVolume(context.Background(), "base-n1", "base-n1", "volume-id", 64*1024*1024)
	if err != nil || identity.CapacityBytes != 64*1024*1024 {
		t.Fatalf("exact retry returned identity=%#v err=%v", identity, err)
	}
	if _, err := manager.CreateVolume(context.Background(), "base-n1", "base-n1", "volume-id", 128*1024*1024); err == nil || !strings.Contains(err.Error(), "has capacity") {
		t.Fatalf("mismatched retry error = %v, want exact-capacity rejection", err)
	}
}

func TestSPDKLvolOperationsHonorCanceledContext(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "rpc-calls")
	rpc := writeTestExecutable(t, t.TempDir(), "rpc.py", `printf '%s\n' "$*" >> "$RPC_LOG"; printf '[]\n'`)
	oldExecutable := spdkRPCExecutable
	spdkRPCExecutable = rpc
	t.Setenv("RPC_LOG", logPath)
	t.Cleanup(func() { spdkRPCExecutable = oldExecutable })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := &SPDKLvolManager{}

	tests := []struct {
		name string
		run  func() error
	}{
		{name: "setup", run: func() error { return manager.SetupStorage(ctx, "base-n1", "base-n1") }},
		{name: "create", run: func() error {
			_, err := manager.CreateVolume(ctx, "base-n1", "base-n1", "volume-id", 64*1024*1024)
			return err
		}},
		{name: "delete", run: func() error {
			return manager.DeleteVolume(ctx, "base-n1", "base-n1", "volume-id", VolumeIdentity{BackendVolumeID: "store/volume-id"})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, context.Canceled) {
				t.Fatalf("operation error = %v, want context.Canceled", err)
			}
		})
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("canceled operations executed SPDK RPC; stat error=%v", err)
	}
}

func TestSPDKUnexportVerifiesSubsystemAbsentAfterLostDeleteResponse(t *testing.T) {
	state := filepath.Join(t.TempDir(), "subsystem-exists")
	if err := os.WriteFile(state, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rpcBody := fmt.Sprintf(`
case "$1" in
  nvmf_get_subsystems)
    if [ -f %q ]; then
      printf '[{"nqn":"nqn.test:volume"}]\n'
    else
      printf '[]\n'
    fi ;;
  nvmf_delete_subsystem) rm -f %q; printf 'response lost\n' >&2; exit 9 ;;
  *) printf 'unexpected method %%s\n' "$1" >&2; exit 8 ;;
esac`, state, state)
	rpc := writeTestExecutable(t, t.TempDir(), "rpc.py", rpcBody)
	oldExecutable := spdkRPCExecutable
	spdkRPCExecutable = rpc
	t.Cleanup(func() { spdkRPCExecutable = oldExecutable })

	backend := &SPDKBackend{}
	if err := backend.UnexportVolume(context.Background(), "nqn.test:volume"); err != nil {
		t.Fatalf("UnexportVolume did not accept verified partial success: %v", err)
	}
	if err := backend.UnexportVolume(context.Background(), "nqn.test:volume"); err != nil {
		t.Fatalf("idempotent UnexportVolume returned error: %v", err)
	}
}

func TestSPDKLvolRoundsCapacityUp(t *testing.T) {
	fakeBin := t.TempDir()
	capture := filepath.Join(t.TempDir(), "lvol-create-arguments")
	rpcBody := fmt.Sprintf(`
case "$1" in
  bdev_lvol_get_lvstores) printf '[{"uuid":"store-uuid","name":"lvs_device","base_bdev":"device"}]\n' ;;
  bdev_get_bdevs)
    if [ -f %q ]; then
      printf '[{"name":"uuid-1","uuid":"uuid-1","aliases":["lvs_device/volume"],"block_size":4096,"num_blocks":512,"driver_specific":{"lvol":{"lvol_store_uuid":"store-uuid"}}}]\n'
    else
      printf '[]\n'
    fi ;;
  bdev_lvol_create) printf '%%s\n' "$@" > %q; printf 'uuid-1\n' ;;
  *) printf 'unexpected method %%s\n' "$1" >&2; exit 8 ;;
esac`, capture, capture)
	rpc := writeTestExecutable(t, fakeBin, "rpc.py", rpcBody)
	oldExecutable := spdkRPCExecutable
	spdkRPCExecutable = rpc
	t.Cleanup(func() { spdkRPCExecutable = oldExecutable })

	identity, err := (&SPDKLvolManager{}).CreateVolume(context.Background(), "device", "device", "volume", 1024*1024+1)
	if err != nil {
		t.Fatalf("CreateVolume returned error: %v", err)
	}
	arguments, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(arguments))
	if len(lines) == 0 || lines[len(lines)-1] != "2" {
		t.Fatalf("SPDK lvol size was not rounded upward to 2 MiB:\n%s", arguments)
	}
	if identity.CapacityBytes != 2*1024*1024 {
		t.Fatalf("allocated capacity = %d, want 2 MiB", identity.CapacityBytes)
	}
}
