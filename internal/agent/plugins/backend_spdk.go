package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"

	"distort/internal/volumeidentity"

	"distort/internal/storageoptions"
)

type SPDKBackend struct{}

var deviceSetupMu sync.Mutex
var spdkStartMu sync.Mutex
var spdkTransportMu sync.Mutex
var spdkTargetExecutable = "nvmf_tgt"
var prepareSPDKProcess = raiseMemlockLimit

func init() {
	RegisterTargetBackend(&SPDKBackend{})
}

func (s *SPDKBackend) Name() string {
	return "spdk"
}

// EnsureSPDKRunning starts nvmf_tgt when needed and waits until its JSON-RPC
// service is usable. A running process is not necessarily ready to accept RPCs.
func EnsureSPDKRunning(ctx context.Context, coreMask string) error {
	spdkStartMu.Lock()
	defer spdkStartMu.Unlock()
	if coreMask == "" {
		coreMask = "0x1"
	}
	if err := storageoptions.ValidateSPDKCoreMask(coreMask); err != nil {
		return err
	}

	if err := exec.Command("pidof", "nvmf_tgt").Run(); err != nil {
		klog.Info("Starting nvmf_tgt daemon in the background...")
		iobufArgs, configureIobufPools, err := spdkIobufPoolArgs()
		if err != nil {
			return err
		}
		args := []string{"-m", coreMask}
		if configureIobufPools {
			args = append(args, "--wait-for-rpc")
		}
		if err := prepareSPDKProcess(); err != nil {
			return fmt.Errorf("prepare nvmf_tgt process limits: %w", err)
		}
		cmd := exec.CommandContext(ctx, spdkTargetExecutable, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start nvmf_tgt: %v", err)
		}
		go func() {
			if err := cmd.Wait(); err != nil {
				klog.Errorf("nvmf_tgt exited with error: %v", err)
			}
		}()

		if configureIobufPools {
			if err := waitForSPDKRPC(ctx); err != nil {
				return err
			}
			if err := CallSPDKRPC("iobuf_set_options", nil, iobufArgs...); err != nil {
				return fmt.Errorf("failed to configure SPDK iobuf pools: %w", err)
			}
			if err := CallSPDKRPC("framework_start_init", nil); err != nil {
				return fmt.Errorf("failed to start SPDK framework initialization: %w", err)
			}
			if err := CallSPDKRPC("framework_wait_init", nil); err != nil {
				return fmt.Errorf("SPDK framework initialization failed: %w", err)
			}
		}
	}

	return waitForSPDKRPC(ctx)
}

func raiseMemlockLimit() error {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &limit); err != nil {
		return err
	}
	if limit.Cur == limit.Max {
		return nil
	}
	limit.Cur = limit.Max
	return unix.Setrlimit(unix.RLIMIT_MEMLOCK, &limit)
}

func waitForSPDKRPC(ctx context.Context) error {
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		var methods []string
		if err := CallSPDKRPC("rpc_get_methods", &methods); err == nil {
			return nil
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for nvmf_tgt JSON-RPC readiness: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("nvmf_tgt JSON-RPC was not ready after 30 seconds: %w", lastErr)
}

func spdkIobufPoolArgs() ([]string, bool, error) {
	small := os.Getenv("SPDK_IOBUF_SMALL_POOL_COUNT")
	large := os.Getenv("SPDK_IOBUF_LARGE_POOL_COUNT")
	if small == "" && large == "" {
		return nil, false, nil
	}
	if small == "" || large == "" {
		return nil, false, fmt.Errorf("SPDK_IOBUF_SMALL_POOL_COUNT and SPDK_IOBUF_LARGE_POOL_COUNT must be set together")
	}
	for name, value := range map[string]string{
		"SPDK_IOBUF_SMALL_POOL_COUNT": small,
		"SPDK_IOBUF_LARGE_POOL_COUNT": large,
	} {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || parsed == 0 {
			return nil, false, fmt.Errorf("%s must be a positive integer, got %q", name, value)
		}
	}
	return []string{"--small-pool-count", small, "--large-pool-count", large}, true, nil
}

func (s *SPDKBackend) SetupDevice(ctx context.Context, pciAddress string, deviceName string, options map[string]string) error {
	if err := storageoptions.Validate(s.Name(), options); err != nil {
		return err
	}
	coreMask := options["spdk-core-mask"]
	if err := EnsureSPDKRunning(ctx, coreMask); err != nil {
		return err
	}

	// Verify if already attached to avoid re-attaching
	attached, err := isNVMeControllerAttached(deviceName)
	if err == nil && attached {
		return nil
	}

	// setup.sh changes host-wide PCI driver state. Serialize it with reset and
	// retry because udev/sysfs may briefly report the device while its driver is
	// still transitioning.
	deviceSetupMu.Lock()
	defer deviceSetupMu.Unlock()

	attached, err = isNVMeControllerAttached(deviceName)
	if err == nil && attached {
		return nil
	}

	klog.Infof("Running spdk_setup.sh to bind device %s (%s) to user-space", deviceName, pciAddress)
	_ = exec.CommandContext(ctx, "modprobe", "uio_pci_generic").Run()
	if err := runSPDKSetup(ctx, pciAddress); err != nil {
		return err
	}

	klog.Infof("Attaching Physical NVMe controller %s at %s to SPDK", deviceName, pciAddress)
	if err := CallSPDKRPC("bdev_nvme_attach_controller", nil, "-b", deviceName, "-t", "PCIe", "-a", pciAddress); err != nil {
		// An earlier timed-out RPC may have completed on the server.
		if attached, checkErr := isNVMeControllerAttached(deviceName); checkErr == nil && attached {
			return nil
		}
		return fmt.Errorf("failed to attach NVMe %s to SPDK: %w", pciAddress, err)
	}

	return nil
}

func isNVMeControllerAttached(deviceName string) (bool, error) {
	var controllers []struct {
		Name string `json:"name"`
	}
	if err := CallSPDKRPC("bdev_nvme_get_controllers", &controllers); err != nil {
		return false, err
	}
	for _, controller := range controllers {
		if controller.Name == deviceName {
			return true, nil
		}
	}
	return false, nil
}

func runSPDKSetup(ctx context.Context, pciAddress string) error {
	var lastErr error
	var lastOutput string
	for attempt := 1; attempt <= 5; attempt++ {
		cmd := exec.CommandContext(ctx, "/opt/spdk/scripts/setup.sh")
		cmd.Env = append(cmd.Environ(), "FORCE=1", "PCI_ALLOWED="+pciAddress)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("spdk_setup.sh interrupted: %w", ctx.Err())
		}

		lastErr = err
		lastOutput = strings.TrimSpace(string(out))
		klog.Warningf("spdk_setup.sh attempt %d/5 failed for %s: %v, output: %s", attempt, pciAddress, err, lastOutput)

		select {
		case <-ctx.Done():
			return fmt.Errorf("spdk_setup.sh interrupted: %w", ctx.Err())
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
	return fmt.Errorf("spdk_setup.sh failed after 5 attempts: %v, output: %s", lastErr, lastOutput)
}

func EnsureNVMeTransport() error {
	spdkTransportMu.Lock()
	defer spdkTransportMu.Unlock()

	args, err := spdkNVMfTransportArgs()
	if err != nil {
		return err
	}

	var transports []struct {
		Trtype string `json:"trtype"`
	}
	if err := CallSPDKRPC("nvmf_get_transports", &transports); err != nil {
		return fmt.Errorf("failed to list SPDK NVMe-oF transports: %w", err)
	}
	for _, transport := range transports {
		if strings.EqualFold(transport.Trtype, "RDMA") {
			return nil
		}
	}

	if err := CallSPDKRPC("nvmf_create_transport", nil, args...); err != nil {
		return fmt.Errorf("failed to create SPDK RDMA transport: %w", err)
	}
	return nil
}

func spdkNVMfTransportArgs() ([]string, error) {
	args := []string{"-t", "RDMA", "-u", "8192", "-i", "131072", "-c", "8192"}
	maxSRQDepth := os.Getenv("SPDK_NVMF_MAX_SRQ_DEPTH")
	if maxSRQDepth == "" {
		return args, nil
	}
	parsed, err := strconv.ParseUint(maxSRQDepth, 10, 32)
	if err != nil || parsed == 0 {
		return nil, fmt.Errorf("SPDK_NVMF_MAX_SRQ_DEPTH must be a positive integer, got %q", maxSRQDepth)
	}
	return append(args, "-s", maxSRQDepth), nil
}

func (s *SPDKBackend) ExportVolume(ctx context.Context, volumeName string, blockPath string, portalIP string, portalPort int, options map[string]string) (string, error) {
	nqn := volumeidentity.NQN(volumeName)
	klog.Infof("Exporting %s as NVMe-oF target %s on %s:%d via SPDK", blockPath, nqn, portalIP, portalPort)

	// Check if already exported (subsystem exists)
	var subsystems []struct {
		NQN string `json:"nqn"`
	}
	if err := CallSPDKRPC("nvmf_get_subsystems", &subsystems); err == nil {
		for _, sub := range subsystems {
			if sub.NQN == nqn {
				return nqn, nil
			}
		}
	}

	if err := EnsureNVMeTransport(); err != nil {
		return "", err
	}

	// 1. Create Subsystem
	err := CallSPDKRPC("nvmf_create_subsystem", nil, nqn, "-a", "-s", "distort")
	if err != nil {
		return "", fmt.Errorf("failed to create SPDK subsystem %s: %w", nqn, err)
	}

	// 2. Add Namespace
	err = CallSPDKRPC("nvmf_subsystem_add_ns", nil, nqn, blockPath)
	if err != nil {
		_ = CallSPDKRPC("nvmf_delete_subsystem", nil, nqn)
		return "", fmt.Errorf("failed to add namespace %s to subsystem: %w", blockPath, err)
	}

	// 3. Add Listener
	err = CallSPDKRPC("nvmf_subsystem_add_listener", nil, nqn, "-t", "RDMA", "-a", portalIP, "-s", fmt.Sprintf("%d", portalPort))
	if err != nil {
		_ = CallSPDKRPC("nvmf_delete_subsystem", nil, nqn)
		return "", fmt.Errorf("failed to add RDMA listener to subsystem: %w", err)
	}

	return nqn, nil
}

func (s *SPDKBackend) UnexportVolume(ctx context.Context, nqn string) error {
	klog.Infof("Unexporting SPDK NVMe-oF target %s", nqn)
	var subsystems []struct {
		NQN string `json:"nqn"`
	}
	if err := CallSPDKRPC("nvmf_get_subsystems", &subsystems); err != nil {
		return fmt.Errorf("failed to list SPDK NVMe-oF subsystems: %w", err)
	}
	found := false
	for _, subsystem := range subsystems {
		if subsystem.NQN == nqn {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	if err := CallSPDKRPC("nvmf_delete_subsystem", nil, nqn); err != nil {
		return fmt.Errorf("failed to delete SPDK subsystem %s: %w", nqn, err)
	}
	return nil
}

// ResetSPDKDevice unbinds the SPDK driver from the PCI device and binds it back to the kernel.
func ResetSPDKDevice(pciAddress string) error {
	deviceSetupMu.Lock()
	defer deviceSetupMu.Unlock()

	klog.Infof("Resetting device %s back to kernel nvme driver", pciAddress)
	setupCmd := exec.Command("/opt/spdk/scripts/setup.sh", "reset")
	setupCmd.Env = append(setupCmd.Environ(), "FORCE=1", "PCI_ALLOWED="+pciAddress)
	if out, err := setupCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("spdk_setup.sh reset failed: %v, output: %s", err, string(out))
	}
	return nil
}
