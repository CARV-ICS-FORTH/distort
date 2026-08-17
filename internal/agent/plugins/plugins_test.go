package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"distort/test/knownfailure"
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

func TestPartedSetupStorageIsIdempotentWhenPartitionExists(t *testing.T) {
	device := filepath.Join(t.TempDir(), "nvme0n1")
	if err := os.WriteFile(device+"p1", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := (&PartedVolumeManager{}).SetupStorage(context.Background(), device, "nvme0"); err != nil {
		t.Fatalf("SetupStorage returned error for existing partition: %v", err)
	}
}

func TestPartedVolumesDoNotAliasPartitionOne(t *testing.T) {
	knownfailure.Require(t, "F3")
	device := filepath.Join(t.TempDir(), "nvme0n1")
	if err := os.WriteFile(device+"p1", nil, 0600); err != nil {
		t.Fatal(err)
	}
	manager := &PartedVolumeManager{}
	first, err := manager.CreateVolume(context.Background(), device, "nvme0", "first", 128*1024*1024)
	if err != nil {
		t.Fatalf("creating first volume: %v", err)
	}
	second, err := manager.CreateVolume(context.Background(), device, "nvme0", "second", 128*1024*1024)
	if err == nil && first == second {
		t.Fatalf("two independent volumes alias the same block path %q", first)
	}
}

func TestPartedCreateReturnsCommandFailure(t *testing.T) {
	knownfailure.Require(t, "F8")
	fakeBin := t.TempDir()
	writeTestExecutable(t, fakeBin, "parted", "touch \"${4}p1\"\nexit 17")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	device := filepath.Join(t.TempDir(), "nvme0n1")
	_, err := (&PartedVolumeManager{}).CreateVolume(context.Background(), device, "nvme0", "broken", 64*1024*1024)
	if err == nil {
		t.Fatal("CreateVolume returned success after parted exited with status 17")
	}
}

func TestPartedRoundsCapacityUp(t *testing.T) {
	knownfailure.Require(t, "F7")
	fakeBin := t.TempDir()
	capture := filepath.Join(t.TempDir(), "arguments")
	body := fmt.Sprintf("printf '%%s\\n' \"$@\" > %q\ntouch \"${4}p1\"", capture)
	writeTestExecutable(t, fakeBin, "parted", body)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	device := filepath.Join(t.TempDir(), "nvme0n1")
	_, err := (&PartedVolumeManager{}).CreateVolume(context.Background(), device, "nvme0", "rounded", 1024*1024+1)
	if err != nil {
		t.Fatalf("CreateVolume returned error: %v", err)
	}
	arguments, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("reading captured parted arguments: %v", err)
	}
	if !strings.Contains(string(arguments), "3MB") {
		t.Fatalf("parted arguments did not round the end upward to 3MB:\n%s", arguments)
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

func TestSPDKLvolRoundsCapacityUp(t *testing.T) {
	knownfailure.Require(t, "F7")
	fakeBin := t.TempDir()
	capture := filepath.Join(t.TempDir(), "lvol-create-arguments")
	rpcBody := fmt.Sprintf(`
case "$1" in
  bdev_lvol_get_lvstores) printf '[{"name":"lvs_device","base_bdev":"device"}]\n' ;;
  bdev_get_bdevs) printf '[]\n' ;;
  bdev_lvol_create) printf '%%s\n' "$@" > %q; printf 'uuid-1\n' ;;
  *) printf 'unexpected method %%s\n' "$1" >&2; exit 8 ;;
esac`, capture)
	rpc := writeTestExecutable(t, fakeBin, "rpc.py", rpcBody)
	oldExecutable := spdkRPCExecutable
	spdkRPCExecutable = rpc
	t.Cleanup(func() { spdkRPCExecutable = oldExecutable })

	_, err := (&SPDKLvolManager{}).CreateVolume(context.Background(), "device", "device", "volume", 1024*1024+1)
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
}
