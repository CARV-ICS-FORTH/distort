package plugins

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
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
var inspectSPDKProcess = inspectRunningSPDKProcess
var spdkManagedExit chan error

func init() {
	RegisterTargetBackend(&SPDKBackend{})
}

func (s *SPDKBackend) Name() string {
	return "spdk"
}

type spdkProcessState struct {
	running  bool
	coreMask string
}

func canonicalSPDKCoreMask(coreMask string) (string, error) {
	if coreMask == "" {
		coreMask = "0x1"
	}
	if err := storageoptions.ValidateSPDKCoreMask(coreMask); err != nil {
		return "", err
	}
	value, ok := new(big.Int).SetString(coreMask[2:], 16)
	if !ok {
		return "", fmt.Errorf("parse %s %q", storageoptions.SPDKCoreMaskOption, coreMask)
	}
	return "0x" + value.Text(16), nil
}

func inspectRunningSPDKProcess() (spdkProcessState, error) {
	output, err := exec.Command("pidof", "nvmf_tgt").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return spdkProcessState{}, nil
		}
		return spdkProcessState{}, fmt.Errorf("inspect running nvmf_tgt process: %w", err)
	}
	pids := strings.Fields(string(output))
	if len(pids) != 1 {
		return spdkProcessState{}, fmt.Errorf("expected one running nvmf_tgt process, found %d", len(pids))
	}
	commandLine, err := os.ReadFile(filepath.Join("/proc", pids[0], "cmdline"))
	if err != nil {
		return spdkProcessState{}, fmt.Errorf("read nvmf_tgt process %s command line: %w", pids[0], err)
	}
	args := strings.Split(strings.TrimRight(string(commandLine), "\x00"), "\x00")
	for index, arg := range args {
		var mask string
		switch {
		case arg == "-m" || arg == "--cpumask":
			if index+1 >= len(args) {
				return spdkProcessState{}, fmt.Errorf("nvmf_tgt process %s has %s without a value", pids[0], arg)
			}
			mask = args[index+1]
		case strings.HasPrefix(arg, "--cpumask="):
			mask = strings.TrimPrefix(arg, "--cpumask=")
		default:
			continue
		}
		canonical, err := canonicalSPDKCoreMask(mask)
		if err != nil {
			return spdkProcessState{}, fmt.Errorf("nvmf_tgt process %s has invalid core mask: %w", pids[0], err)
		}
		return spdkProcessState{running: true, coreMask: canonical}, nil
	}
	return spdkProcessState{}, fmt.Errorf("running nvmf_tgt process %s has no verifiable core mask", pids[0])
}

func stopManagedSPDKProcess(cmd *exec.Cmd, exit <-chan error) error {
	var failures []error
	if cmd.Process != nil {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			failures = append(failures, fmt.Errorf("kill nvmf_tgt after initialization failure: %w", err))
		}
	}
	select {
	case <-exit:
	case <-time.After(5 * time.Second):
		failures = append(failures, errors.New("timed out reaping nvmf_tgt after initialization failure"))
	}
	if spdkManagedExit == exit {
		spdkManagedExit = nil
	}
	return errors.Join(failures...)
}

// EnsureSPDKRunning starts nvmf_tgt when needed and waits until its JSON-RPC
// service is usable. The requested core mask is node-global and must match the
// command line of an already running process.
func EnsureSPDKRunning(ctx context.Context, coreMask string) error {
	spdkStartMu.Lock()
	defer spdkStartMu.Unlock()
	requestedMask, err := canonicalSPDKCoreMask(coreMask)
	if err != nil {
		return err
	}
	if spdkManagedExit != nil {
		select {
		case err := <-spdkManagedExit:
			if err != nil {
				klog.ErrorS(err, "Managed nvmf_tgt process exited")
			}
			spdkManagedExit = nil
		default:
		}
	}

	process, err := inspectSPDKProcess()
	if err != nil {
		return err
	}
	if process.running {
		if process.coreMask != requestedMask {
			return fmt.Errorf("running nvmf_tgt uses core mask %s, requested %s", process.coreMask, requestedMask)
		}
		return waitForSPDKRPC(ctx)
	}

	{
		klog.InfoS("Starting SPDK NVMe-oF target daemon")
		iobufArgs, configureIobufPools, err := spdkIobufPoolArgs()
		if err != nil {
			return err
		}
		args := []string{"-m", requestedMask}
		if configureIobufPools {
			args = append(args, "--wait-for-rpc")
		}
		if err := prepareSPDKProcess(); err != nil {
			return fmt.Errorf("prepare nvmf_tgt process limits: %w", err)
		}
		cmd := exec.Command(spdkTargetExecutable, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start nvmf_tgt: %v", err)
		}
		spdkManagedExit = make(chan error, 1)
		exit := spdkManagedExit
		go func(result chan<- error) { result <- cmd.Wait() }(exit)
		fail := func(initializationErr error) error {
			cleanupErr := stopManagedSPDKProcess(cmd, exit)
			return errors.Join(initializationErr, cleanupErr)
		}

		if configureIobufPools {
			if err := waitForSPDKRPC(ctx); err != nil {
				return fail(err)
			}
			if err := CallSPDKRPCContext(ctx, "iobuf_set_options", nil, iobufArgs...); err != nil {
				return fail(fmt.Errorf("failed to configure SPDK iobuf pools: %w", err))
			}
			if err := CallSPDKRPCContext(ctx, "framework_start_init", nil); err != nil {
				return fail(fmt.Errorf("failed to start SPDK framework initialization: %w", err))
			}
			if err := CallSPDKRPCContext(ctx, "framework_wait_init", nil); err != nil {
				return fail(fmt.Errorf("SPDK framework initialization failed: %w", err))
			}
		}
		if err := waitForSPDKRPC(ctx); err != nil {
			return fail(err)
		}
	}
	return nil
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
		if err := CallSPDKRPCContext(ctx, "rpc_get_methods", &methods); err == nil {
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
	attached, err := isNVMeControllerAttached(ctx, deviceName)
	if err == nil && attached {
		return nil
	}

	// setup.sh changes host-wide PCI driver state. Serialize it with reset and
	// retry because udev/sysfs may briefly report the device while its driver is
	// still transitioning.
	deviceSetupMu.Lock()
	defer deviceSetupMu.Unlock()

	attached, err = isNVMeControllerAttached(ctx, deviceName)
	if err == nil && attached {
		return nil
	}

	klog.InfoS("Binding device to SPDK user-space driver", "device", deviceName, "pciAddress", pciAddress)
	_ = exec.CommandContext(ctx, "modprobe", "uio_pci_generic").Run()
	if err := runSPDKSetup(ctx, pciAddress); err != nil {
		return err
	}

	klog.InfoS("Attaching physical NVMe controller to SPDK", "device", deviceName, "pciAddress", pciAddress)
	if err := CallSPDKRPCContext(ctx, "bdev_nvme_attach_controller", nil, "-b", deviceName, "-t", "PCIe", "-a", pciAddress); err != nil {
		// An earlier timed-out RPC may have completed on the server.
		if attached, checkErr := isNVMeControllerAttached(ctx, deviceName); checkErr == nil && attached {
			return nil
		}
		return fmt.Errorf("failed to attach NVMe %s to SPDK: %w", pciAddress, err)
	}

	return nil
}

func isNVMeControllerAttached(ctx context.Context, deviceName string) (bool, error) {
	var controllers []struct {
		Name string `json:"name"`
	}
	if err := CallSPDKRPCContext(ctx, "bdev_nvme_get_controllers", &controllers); err != nil {
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
		klog.ErrorS(err, "SPDK device setup attempt failed", "attempt", attempt, "maxAttempts", 5, "pciAddress", pciAddress, "output", lastOutput)

		select {
		case <-ctx.Done():
			return fmt.Errorf("spdk_setup.sh interrupted: %w", ctx.Err())
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
	return fmt.Errorf("spdk_setup.sh failed after 5 attempts: %v, output: %s", lastErr, lastOutput)
}

func EnsureNVMeTransport(ctx context.Context) error {
	spdkTransportMu.Lock()
	defer spdkTransportMu.Unlock()

	args, err := spdkNVMfTransportArgs()
	if err != nil {
		return err
	}

	var transports []struct {
		Trtype string `json:"trtype"`
	}
	if err := CallSPDKRPCContext(ctx, "nvmf_get_transports", &transports); err != nil {
		return fmt.Errorf("failed to list SPDK NVMe-oF transports: %w", err)
	}
	for _, transport := range transports {
		if strings.EqualFold(transport.Trtype, "RDMA") {
			return nil
		}
	}

	if err := CallSPDKRPCContext(ctx, "nvmf_create_transport", nil, args...); err != nil {
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

type spdkSubsystem struct {
	NQN        string `json:"nqn"`
	Namespaces []struct {
		Name     string `json:"name"`
		BdevName string `json:"bdev_name"`
	} `json:"namespaces"`
	ListenAddresses []struct {
		Trtype  string `json:"trtype"`
		Traddr  string `json:"traddr"`
		Trsvcid string `json:"trsvcid"`
	} `json:"listen_addresses"`
}

func spdkBdevIdentities(ctx context.Context, blockPath string) (map[string]struct{}, error) {
	var bdevs []struct {
		Name    string   `json:"name"`
		UUID    string   `json:"uuid"`
		Aliases []string `json:"aliases"`
	}
	if err := CallSPDKRPCContext(ctx, "bdev_get_bdevs", &bdevs, "-b", blockPath); err != nil {
		return nil, fmt.Errorf("resolve SPDK backing bdev %s: %w", blockPath, err)
	}
	if len(bdevs) == 0 {
		return nil, fmt.Errorf("SPDK backing bdev %s is missing", blockPath)
	}
	identities := map[string]struct{}{blockPath: {}}
	for _, bdev := range bdevs {
		identities[bdev.Name] = struct{}{}
		identities[bdev.UUID] = struct{}{}
		for _, alias := range bdev.Aliases {
			identities[alias] = struct{}{}
		}
	}
	delete(identities, "")
	return identities, nil
}

func (s *SPDKBackend) findSubsystem(ctx context.Context, nqn string) (*spdkSubsystem, error) {
	var subsystems []spdkSubsystem
	if err := CallSPDKRPCContext(ctx, "nvmf_get_subsystems", &subsystems); err != nil {
		return nil, err
	}
	for i := range subsystems {
		if subsystems[i].NQN == nqn {
			return &subsystems[i], nil
		}
	}
	return nil, nil
}

// CheckExport restores the supervised target if necessary and verifies the
// subsystem namespace, listener address, port, and backing bdev.
func (s *SPDKBackend) CheckExport(ctx context.Context, nqn, blockPath, portalIP string, portalPort int, options map[string]string) error {
	if err := EnsureSPDKRunning(ctx, options[storageoptions.SPDKCoreMaskOption]); err != nil {
		return err
	}
	subsystem, err := s.findSubsystem(ctx, nqn)
	if err != nil {
		return fmt.Errorf("list SPDK subsystems: %w", err)
	}
	if subsystem == nil {
		return fmt.Errorf("SPDK subsystem %s is missing", nqn)
	}
	bdevIdentities, err := spdkBdevIdentities(ctx, blockPath)
	if err != nil {
		return err
	}
	namespaceMatches := false
	for _, namespace := range subsystem.Namespaces {
		_, nameMatches := bdevIdentities[namespace.Name]
		_, bdevNameMatches := bdevIdentities[namespace.BdevName]
		if nameMatches || bdevNameMatches {
			namespaceMatches = true
			break
		}
	}
	if !namespaceMatches {
		return fmt.Errorf("SPDK subsystem %s does not use backing bdev %s", nqn, blockPath)
	}
	listenerMatches := false
	for _, listener := range subsystem.ListenAddresses {
		if strings.EqualFold(listener.Trtype, "RDMA") && listener.Traddr == portalIP && listener.Trsvcid == strconv.Itoa(portalPort) {
			listenerMatches = true
			break
		}
	}
	if !listenerMatches {
		return fmt.Errorf("SPDK subsystem %s has no RDMA listener on %s:%d", nqn, portalIP, portalPort)
	}
	return nil
}

func (s *SPDKBackend) ExportVolume(ctx context.Context, volumeName string, blockPath string, portalIP string, portalPort int, options map[string]string) (string, error) {
	nqn := volumeidentity.NQN(volumeName)
	klog.InfoS("Exporting SPDK NVMe-oF target", "blockPath", blockPath, "nqn", nqn, "portalIP", portalIP, "portalPort", portalPort)

	if err := s.CheckExport(ctx, nqn, blockPath, portalIP, portalPort, options); err == nil {
		return nqn, nil
	}
	if existing, err := s.findSubsystem(ctx, nqn); err != nil {
		return "", fmt.Errorf("inspect existing SPDK subsystem %s: %w", nqn, err)
	} else if existing != nil {
		if err := CallSPDKRPCContext(ctx, "nvmf_delete_subsystem", nil, nqn); err != nil {
			return "", fmt.Errorf("replace unhealthy SPDK subsystem %s: %w", nqn, err)
		}
	}

	if err := EnsureNVMeTransport(ctx); err != nil {
		return "", err
	}

	// 1. Create Subsystem
	err := CallSPDKRPCContext(ctx, "nvmf_create_subsystem", nil, nqn, "-s", "distort")
	if err != nil {
		return "", fmt.Errorf("failed to create SPDK subsystem %s: %w", nqn, err)
	}

	// 2. Add Namespace
	err = CallSPDKRPCContext(ctx, "nvmf_subsystem_add_ns", nil, nqn, blockPath)
	if err != nil {
		_ = CallSPDKRPCContext(ctx, "nvmf_delete_subsystem", nil, nqn)
		return "", fmt.Errorf("failed to add namespace %s to subsystem: %w", blockPath, err)
	}

	// 3. Add Listener
	err = CallSPDKRPCContext(ctx, "nvmf_subsystem_add_listener", nil, nqn, "-t", "RDMA", "-a", portalIP, "-s", fmt.Sprintf("%d", portalPort))
	if err != nil {
		_ = CallSPDKRPCContext(ctx, "nvmf_delete_subsystem", nil, nqn)
		return "", fmt.Errorf("failed to add RDMA listener to subsystem: %w", err)
	}

	return nqn, nil
}

func (s *SPDKBackend) ReconcileHostAccess(ctx context.Context, nqn, hostNQN string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	type subsystemRecord struct {
		NQN          string `json:"nqn"`
		AllowAnyHost bool   `json:"allow_any_host"`
		Hosts        []struct {
			NQN string `json:"nqn"`
		} `json:"hosts"`
	}
	var subsystems []subsystemRecord
	if err := CallSPDKRPCContext(ctx, "nvmf_get_subsystems", &subsystems); err != nil {
		return fmt.Errorf("list SPDK subsystems before reconciling host access: %w", err)
	}
	var current *subsystemRecord
	for index := range subsystems {
		if subsystems[index].NQN == nqn {
			current = &subsystems[index]
			break
		}
	}
	if current == nil {
		return fmt.Errorf("SPDK subsystem %s does not exist", nqn)
	}
	exact := !current.AllowAnyHost && len(current.Hosts) <= 1
	if hostNQN == "" {
		exact = exact && len(current.Hosts) == 0
	} else {
		exact = exact && len(current.Hosts) == 1 && current.Hosts[0].NQN == hostNQN
	}
	if exact {
		return nil
	}
	if current.AllowAnyHost {
		if err := CallSPDKRPCContext(ctx, "nvmf_subsystem_allow_any_host", nil, nqn, "-d"); err != nil {
			return fmt.Errorf("disable unrestricted host access for %s: %w", nqn, err)
		}
	}
	for _, host := range current.Hosts {
		if err := ctx.Err(); err != nil {
			return err
		}
		// SPDK v26.01 folds controller disconnection into remove_host. The RPC
		// returns only after matching connections are gone or this timeout expires.
		if err := CallSPDKRPCContext(ctx, "nvmf_subsystem_remove_host", nil, nqn, host.NQN); err != nil {
			return fmt.Errorf("disconnect and remove stale host %s from %s: %w", host.NQN, nqn, err)
		}
	}
	if hostNQN != "" {
		if err := CallSPDKRPCContext(ctx, "nvmf_subsystem_add_host", nil, nqn, hostNQN); err != nil {
			return fmt.Errorf("authorize host %s for %s: %w", hostNQN, nqn, err)
		}
	}
	return nil
}

func (s *SPDKBackend) UnexportVolume(ctx context.Context, nqn string) error {
	klog.InfoS("Unexporting SPDK NVMe-oF target", "nqn", nqn)
	subsystemExists := func() (bool, error) {
		var subsystems []struct {
			NQN string `json:"nqn"`
		}
		if err := CallSPDKRPCContext(ctx, "nvmf_get_subsystems", &subsystems); err != nil {
			return false, fmt.Errorf("failed to list SPDK NVMe-oF subsystems: %w", err)
		}
		for _, subsystem := range subsystems {
			if subsystem.NQN == nqn {
				return true, nil
			}
		}
		return false, nil
	}
	found, err := subsystemExists()
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	deleteErr := CallSPDKRPCContext(ctx, "nvmf_delete_subsystem", nil, nqn)
	found, verifyErr := subsystemExists()
	if verifyErr != nil {
		return fmt.Errorf("verify SPDK subsystem %s absence: %w", nqn, verifyErr)
	}
	if found {
		if deleteErr != nil {
			return fmt.Errorf("failed to delete SPDK subsystem %s: %w", nqn, deleteErr)
		}
		return fmt.Errorf("SPDK subsystem %s still exists after deletion", nqn)
	}
	return nil
}

// ResetSPDKDevice unbinds the SPDK driver from the PCI device and binds it back to the kernel.
func ResetSPDKDevice(pciAddress string) error {
	deviceSetupMu.Lock()
	defer deviceSetupMu.Unlock()

	klog.InfoS("Resetting device to kernel NVMe driver", "pciAddress", pciAddress)
	setupCmd := exec.Command("/opt/spdk/scripts/setup.sh", "reset")
	setupCmd.Env = append(setupCmd.Environ(), "FORCE=1", "PCI_ALLOWED="+pciAddress)
	if out, err := setupCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("spdk_setup.sh reset failed: %v, output: %s", err, string(out))
	}
	return nil
}
