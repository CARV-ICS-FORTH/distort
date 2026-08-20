package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
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
	remove := strings.Index(text, "nvmf_subsystem_remove_host nqn.test:spdk-fencing nqn.test:old-host --timeout-ms 10000")
	add := strings.Index(text, "nvmf_subsystem_add_host nqn.test:spdk-fencing "+newHost)
	if disable < 0 || remove < disable || add < remove || strings.Contains(text, "nvmf_subsystem_disconnect_host") {
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
		return nil, nil
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
			if !d.initialized {
				return nil, fmt.Errorf("unrecognized disk label")
			}
			return []byte(d.table(index+1 < len(args) && args[index+1] == "free")), nil
		case "mklabel":
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
  nvmf_get_subsystems) printf '[{"nqn":"nqn.test","namespaces":[{"bdev_name":"lvs/volume"}],"listen_addresses":[{"trtype":"RDMA","traddr":"192.0.2.10","trsvcid":"4420"}]}]\n' ;;
  *) printf 'unexpected method %s\n' "$1" >&2; exit 8 ;;
esac`)
	oldExecutable := spdkRPCExecutable
	spdkRPCExecutable = rpc
	t.Cleanup(func() { spdkRPCExecutable = oldExecutable })

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

func TestSPDKLvolPersistsAndDeletesExactCreatedIdentity(t *testing.T) {
	state := filepath.Join(t.TempDir(), "lvol-exists")
	deleted := filepath.Join(t.TempDir(), "deleted-uuid")
	rpcBody := fmt.Sprintf(`
case "$1" in
  bdev_lvol_get_lvstores) printf '[{"uuid":"store-uuid","name":"lvs_base-n1","base_bdev":"base-n1"}]\n' ;;
  bdev_get_bdevs)
    if [ -f %q ]; then
      printf '[{"name":"lvol-uuid","uuid":"lvol-uuid","aliases":["lvs_base-n1/volume-id"],"driver_specific":{"lvol":{"lvol_store_uuid":"store-uuid"}}}]\n'
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
      printf '[{"name":"uuid-1","uuid":"uuid-1","aliases":["lvs_device/volume"],"driver_specific":{"lvol":{"lvol_store_uuid":"store-uuid"}}}]\n'
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
