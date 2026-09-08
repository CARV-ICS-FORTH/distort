package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/klog/v2"

	"distort/internal/storageoptions"
	"distort/internal/volumeidentity"
)

// BXIBackend exports SPDK logical volumes through the Portals/BXI RDMA
// provider built into the BXI container image.
type BXIBackend struct{}

var inspectBXIPortalPID = runningSPDKPortalPID

func init() {
	RegisterTargetBackend(&BXIBackend{})
}

func (b *BXIBackend) Name() string { return "bxi" }

func runningSPDKPortalPID() (string, bool, error) {
	output, err := exec.Command("pidof", "nvmf_tgt").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect running BXI nvmf_tgt process: %w", err)
	}
	pids := strings.Fields(string(output))
	if len(pids) != 1 {
		return "", false, fmt.Errorf("expected one running nvmf_tgt process, found %d", len(pids))
	}
	environment, err := os.ReadFile(filepath.Join("/proc", pids[0], "environ"))
	if err != nil {
		return "", false, fmt.Errorf("read nvmf_tgt process %s environment: %w", pids[0], err)
	}
	for entry := range strings.SplitSeq(string(environment), "\x00") {
		if value, found := strings.CutPrefix(entry, "PORTALS_PID="); found {
			return value, true, nil
		}
	}
	return "", false, nil
}

// EnsureBXISPDKRunning starts the target with the environment required by the
// Portals provider. PORTALS_PID is node-global and must match an existing
// target process.
func EnsureBXISPDKRunning(ctx context.Context, coreMask string, portalsPID int) error {
	spdkStartMu.Lock()
	defer spdkStartMu.Unlock()

	requestedMask, err := canonicalSPDKCoreMask(coreMask)
	if err != nil {
		return err
	}
	requestedPID := strconv.Itoa(portalsPID)
	if spdkManagedExit != nil {
		select {
		case err := <-spdkManagedExit:
			if err != nil {
				klog.ErrorS(err, "Managed BXI nvmf_tgt process exited")
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
		actualPID, found, err := inspectBXIPortalPID()
		if err != nil {
			return err
		}
		if !found {
			return errors.New("running nvmf_tgt was not started with PORTALS_PID")
		}
		if actualPID != requestedPID {
			return fmt.Errorf("running nvmf_tgt uses PORTALS_PID %s, requested %s", actualPID, requestedPID)
		}
		return waitForSPDKRPC(ctx)
	}

	if err := prepareSPDKProcess(); err != nil {
		return fmt.Errorf("prepare BXI nvmf_tgt process limits: %w", err)
	}
	klog.InfoS("Starting BXI SPDK NVMe-oF target daemon", "portalsPID", requestedPID)
	cmd := exec.Command(spdkTargetExecutable, "-m", requestedMask)
	cmd.Env = append(cmd.Environ(), "ROLE=target", "PORTALS_PID="+requestedPID)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start BXI nvmf_tgt: %w", err)
	}
	spdkManagedExit = make(chan error, 1)
	exit := spdkManagedExit
	go func(result chan<- error) { result <- cmd.Wait() }(exit)
	if err := waitForSPDKRPC(ctx); err != nil {
		return errors.Join(err, stopManagedSPDKProcess(cmd, exit))
	}
	return nil
}

func (b *BXIBackend) SetupDevice(ctx context.Context, pciAddress, deviceName string, options map[string]string) error {
	if err := storageoptions.Validate(b.Name(), options); err != nil {
		return err
	}
	portalsPID, err := storageoptions.BXIPort(options)
	if err != nil {
		return err
	}
	if err := EnsureBXISPDKRunning(ctx, options[storageoptions.SPDKCoreMaskOption], portalsPID); err != nil {
		return err
	}
	attached, err := isNVMeControllerAttached(ctx, deviceName)
	if err == nil && attached {
		return nil
	}

	deviceSetupMu.Lock()
	defer deviceSetupMu.Unlock()
	attached, err = isNVMeControllerAttached(ctx, deviceName)
	if err == nil && attached {
		return nil
	}
	klog.InfoS("Binding device to BXI SPDK user-space driver", "device", deviceName, "pciAddress", pciAddress)
	_ = exec.CommandContext(ctx, "modprobe", "uio_pci_generic").Run()
	if err := runSPDKSetup(ctx, pciAddress); err != nil {
		return err
	}
	if err := CallSPDKRPCContext(ctx, "bdev_nvme_attach_controller", nil, "-b", deviceName, "-t", "PCIe", "-a", pciAddress); err != nil {
		if attached, checkErr := isNVMeControllerAttached(ctx, deviceName); checkErr == nil && attached {
			return nil
		}
		return fmt.Errorf("failed to attach NVMe %s to BXI SPDK: %w", pciAddress, err)
	}
	return nil
}

func ensureBXITransport(ctx context.Context) error {
	spdkTransportMu.Lock()
	defer spdkTransportMu.Unlock()
	var transports []struct {
		Trtype string `json:"trtype"`
	}
	if err := CallSPDKRPCContext(ctx, "nvmf_get_transports", &transports); err != nil {
		return fmt.Errorf("failed to list BXI SPDK NVMe-oF transports: %w", err)
	}
	for _, transport := range transports {
		if strings.EqualFold(transport.Trtype, "RDMA") {
			return nil
		}
	}
	if err := CallSPDKRPCContext(ctx, "nvmf_create_transport", nil, "-t", "RDMA", "-u", "8192", "-m", "4", "-c", "0"); err != nil {
		return fmt.Errorf("failed to create BXI SPDK RDMA transport: %w", err)
	}
	return nil
}

func bxiPortalAddress(portalIP string, options map[string]string) string {
	if address := options[storageoptions.BXINIDOption]; address != "" {
		return address
	}
	return portalIP
}

func (b *BXIBackend) inspectExport(ctx context.Context, nqn, blockPath, portalIP string, portalPort int, options map[string]string) spdkExportInspection {
	portalsPID, err := storageoptions.BXIPort(options)
	if err != nil {
		return spdkExportInspection{state: spdkExportMismatch, detail: err}
	}
	if err := EnsureBXISPDKRunning(ctx, options[storageoptions.SPDKCoreMaskOption], portalsPID); err != nil {
		return spdkExportInspection{state: spdkExportObservationUnavailable, detail: err}
	}
	return (&SPDKBackend{}).inspectExport(ctx, nqn, blockPath, bxiPortalAddress(portalIP, options), portalPort, options)
}

func (b *BXIBackend) CheckExport(ctx context.Context, nqn, blockPath, portalIP string, portalPort int, options map[string]string) error {
	inspection := b.inspectExport(ctx, nqn, blockPath, portalIP, portalPort, options)
	if inspection.state == spdkExportHealthy {
		return nil
	}
	if inspection.state == spdkExportObservationUnavailable {
		return &ExportObservationError{Err: inspection.detail}
	}
	return inspection.detail
}

func (b *BXIBackend) ExportVolume(ctx context.Context, volumeName, blockPath, portalIP string, portalPort int, options map[string]string) (string, error) {
	if err := storageoptions.Validate(b.Name(), options); err != nil {
		return "", err
	}
	nqn := volumeidentity.NQN(volumeName)
	bxiAddress := bxiPortalAddress(portalIP, options)
	klog.InfoS("Exporting BXI NVMe-oF target", "blockPath", blockPath, "nqn", nqn, "bxiAddress", bxiAddress, "portalsPID", portalPort)
	inspection := b.inspectExport(ctx, nqn, blockPath, portalIP, portalPort, options)
	if inspection.state == spdkExportHealthy {
		return nqn, nil
	}
	if inspection.state == spdkExportObservationUnavailable {
		return "", &ExportObservationError{Err: inspection.detail}
	}
	if inspection.state == spdkExportMismatch {
		if err := CallSPDKRPCContext(ctx, "nvmf_delete_subsystem", nil, nqn); err != nil {
			return "", fmt.Errorf("replace unhealthy BXI SPDK subsystem %s: %w", nqn, err)
		}
	}
	if err := ensureBXITransport(ctx); err != nil {
		return "", err
	}
	// Portals/BXI routes connections by NID and its established initiator flow
	// uses the host identity supplied by the local NVMe stack. Keep BXI exports
	// open at the SPDK host-ACL layer as they were before the backend merge;
	// Kubernetes/CSI still controls which node receives and stages the volume.
	if err := CallSPDKRPCContext(ctx, "nvmf_create_subsystem", nil, nqn, "-a", "-s", "distort"); err != nil {
		return "", fmt.Errorf("failed to create BXI SPDK subsystem %s: %w", nqn, err)
	}
	if err := CallSPDKRPCContext(ctx, "nvmf_subsystem_add_ns", nil, nqn, blockPath); err != nil {
		_ = CallSPDKRPCContext(ctx, "nvmf_delete_subsystem", nil, nqn)
		return "", fmt.Errorf("failed to add namespace %s to BXI subsystem: %w", blockPath, err)
	}
	if err := CallSPDKRPCContext(ctx, "nvmf_subsystem_add_listener", nil, nqn, "-t", "RDMA", "-a", bxiAddress, "-s", strconv.Itoa(portalPort)); err != nil {
		_ = CallSPDKRPCContext(ctx, "nvmf_delete_subsystem", nil, nqn)
		return "", fmt.Errorf("failed to add BXI listener to subsystem: %w", err)
	}
	return nqn, nil
}

func (b *BXIBackend) ReconcileHostAccess(ctx context.Context, nqn, hostNQN string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Preserve exact host fencing once CSI has attached the volume. An
	// unattached BXI export remains available to the Portals data-path tooling,
	// which uses the initiator's local NVMe host identity.
	if hostNQN != "" {
		return (&SPDKBackend{}).ReconcileHostAccess(ctx, nqn, hostNQN)
	}
	type subsystemRecord struct {
		NQN          string `json:"nqn"`
		AllowAnyHost bool   `json:"allow_any_host"`
	}
	var subsystems []subsystemRecord
	if err := CallSPDKRPCContext(ctx, "nvmf_get_subsystems", &subsystems); err != nil {
		return fmt.Errorf("list BXI SPDK subsystems before reconciling host access: %w", err)
	}
	for _, subsystem := range subsystems {
		if subsystem.NQN != nqn {
			continue
		}
		if subsystem.AllowAnyHost {
			return nil
		}
		if err := CallSPDKRPCContext(ctx, "nvmf_subsystem_allow_any_host", nil, nqn, "-e"); err != nil {
			return fmt.Errorf("enable BXI host access for %s: %w", nqn, err)
		}
		return nil
	}
	return fmt.Errorf("BXI SPDK subsystem %s does not exist", nqn)
}

func (b *BXIBackend) UnexportVolume(ctx context.Context, nqn string) error {
	return (&SPDKBackend{}).UnexportVolume(ctx, nqn)
}
