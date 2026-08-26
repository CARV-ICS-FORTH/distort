package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/klog/v2"

	"distort/internal/volumeidentity"

	"distort/internal/rdmahealth"
	"distort/internal/storageoptions"
)

type KernelBackend struct{}

func init() {
	RegisterTargetBackend(&KernelBackend{})
}

func (k *KernelBackend) Name() string {
	return "kernel"
}

func (k *KernelBackend) SetupDevice(ctx context.Context, pciAddress string, deviceName string, options map[string]string) error {
	if err := storageoptions.Validate(k.Name(), options); err != nil {
		return err
	}
	klog.InfoS("Ensuring device is bound to kernel NVMe driver", "device", deviceName, "pciAddress", pciAddress)
	if err := ResetSPDKDevice(pciAddress); err != nil {
		klog.ErrorS(err, "SPDK device reset returned a warning")
	}
	return nil
}

var nvmetPath = "/sys/kernel/config/nvmet"

func isMountPoint(target string) bool {
	target = filepath.Clean(target)
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		data, err = os.ReadFile("/proc/mounts")
		if err != nil {
			return false
		}
	}
	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 5 {
			if filepath.Clean(fields[4]) == target {
				return true
			}
		}
	}
	return false
}

const kernelPortID = "1"

func kernelAddressFamily(portalIP string) (string, error) {
	ip, err := rdmahealth.ParseUsableIP(portalIP)
	if err != nil {
		return "", fmt.Errorf("invalid kernel NVMe-oF portal IP: %w", err)
	}
	if ip.To4() != nil {
		return "ipv4", nil
	}
	return "ipv6", nil
}

func configValue(path string) (string, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(value)), nil
}

func configValueMatches(path, expected string) bool {
	actual, err := configValue(path)
	return err == nil && actual == expected
}

func kernelLinkMatches(linkPath, subsystemPath string) bool {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(linkPath), target)
	}
	return filepath.Clean(target) == filepath.Clean(subsystemPath)
}

func ensureKernelSubsystemLink(linkPath, subsystemPath string) error {
	if kernelLinkMatches(linkPath, subsystemPath) {
		return nil
	}
	if info, err := os.Lstat(linkPath); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("kernel target link %s exists but is not a symlink", linkPath)
		}
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("remove incorrect kernel target link %s: %w", linkPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect kernel target link %s: %w", linkPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0755); err != nil {
		return fmt.Errorf("create kernel target link directory: %w", err)
	}
	if err := os.Symlink(subsystemPath, linkPath); err != nil {
		return fmt.Errorf("link subsystem to kernel target port: %w", err)
	}
	return nil
}

type kernelPortLink struct {
	path   string
	target string
}

func restoreKernelPortLinks(links []kernelPortLink) error {
	for _, link := range links {
		if err := os.Symlink(link.target, link.path); err != nil && !os.IsExist(err) {
			return fmt.Errorf("restore kernel target link %s: %w", link.path, err)
		}
	}
	return nil
}

func ensureKernelPort(portalIP string, portalPort int) (retErr error) {
	addressFamily, err := kernelAddressFamily(portalIP)
	if err != nil {
		return err
	}
	portPath := filepath.Join(nvmetPath, "ports", kernelPortID)
	linksPath := filepath.Join(portPath, "subsystems")
	if err := os.MkdirAll(linksPath, 0755); err != nil {
		return fmt.Errorf("create kernel target port: %w", err)
	}
	desired := map[string]string{
		"addr_adrfam":  addressFamily,
		"addr_trtype":  "rdma",
		"addr_trsvcid": strconv.Itoa(portalPort),
		"addr_traddr":  portalIP,
	}
	configured := true
	for name, value := range desired {
		if !configValueMatches(filepath.Join(portPath, name), value) {
			configured = false
			break
		}
	}
	if configured {
		return nil
	}

	entries, err := os.ReadDir(linksPath)
	if err != nil {
		return fmt.Errorf("list kernel target port links: %w", err)
	}
	links := make([]kernelPortLink, 0, len(entries))
	for _, entry := range entries {
		path := filepath.Join(linksPath, entry.Name())
		target, err := os.Readlink(path)
		if err != nil {
			return fmt.Errorf("read kernel target port link %s: %w", path, err)
		}
		links = append(links, kernelPortLink{path: path, target: target})
	}
	for _, link := range links {
		if err := os.Remove(link.path); err != nil {
			return fmt.Errorf("disconnect kernel target port link %s: %w", link.path, err)
		}
	}
	defer func() {
		if err := restoreKernelPortLinks(links); retErr == nil && err != nil {
			retErr = err
		}
	}()
	for _, name := range []string{"addr_adrfam", "addr_trtype", "addr_trsvcid", "addr_traddr"} {
		if err := os.WriteFile(filepath.Join(portPath, name), []byte(desired[name]), 0644); err != nil {
			return fmt.Errorf("configure kernel target port %s: %w", name, err)
		}
	}
	return nil
}

func ensureKernelNamespace(subsystemPath, linkPath, blockPath string) error {
	namespacesPath := filepath.Join(subsystemPath, "namespaces")
	if err := os.MkdirAll(namespacesPath, 0755); err != nil {
		return fmt.Errorf("create kernel target namespaces directory: %w", err)
	}
	entries, err := os.ReadDir(namespacesPath)
	if err != nil {
		return fmt.Errorf("list kernel target namespaces: %w", err)
	}
	namespacePath := filepath.Join(namespacesPath, "1")
	devicePath := filepath.Join(namespacePath, "device_path")
	enablePath := filepath.Join(namespacePath, "enable")
	exact := len(entries) == 1 && entries[0].Name() == "1" &&
		configValueMatches(devicePath, blockPath) && configValueMatches(enablePath, "1")
	if exact {
		return nil
	}
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("disconnect kernel target namespace before repair: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == "1" {
			continue
		}
		stalePath := filepath.Join(namespacesPath, entry.Name())
		enablePath := filepath.Join(stalePath, "enable")
		if _, err := os.Stat(enablePath); err == nil {
			if err := os.WriteFile(enablePath, []byte("0"), 0644); err != nil {
				return fmt.Errorf("disable stale kernel target namespace %s: %w", entry.Name(), err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect stale kernel target namespace %s: %w", entry.Name(), err)
		}
		if err := os.Remove(stalePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale kernel target namespace %s: %w", entry.Name(), err)
		}
	}
	if err := os.MkdirAll(namespacePath, 0755); err != nil {
		return fmt.Errorf("create kernel target namespace: %w", err)
	}
	if err := os.WriteFile(enablePath, []byte("0"), 0644); err != nil {
		return fmt.Errorf("disable kernel target namespace before repair: %w", err)
	}
	if err := os.WriteFile(devicePath, []byte(blockPath), 0644); err != nil {
		return fmt.Errorf("set kernel target namespace device path: %w", err)
	}
	if err := os.WriteFile(enablePath, []byte("1"), 0644); err != nil {
		return fmt.Errorf("enable kernel target namespace: %w", err)
	}
	return nil
}

func ensureKernelConfigFS(ctx context.Context) error {
	if _, err := os.Stat(nvmetPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect kernel NVMe target configfs: %w", err)
	}
	klog.InfoS("Loading kernel NVMe target modules")
	for _, module := range []string{"nvmet", "nvmet-rdma"} {
		if err := exec.CommandContext(ctx, "modprobe", module).Run(); err != nil {
			return fmt.Errorf("load kernel module %s: %w", module, err)
		}
	}
	if !isMountPoint("/sys/kernel/config") {
		klog.InfoS("Mounting configfs", "path", "/sys/kernel/config")
		if err := exec.CommandContext(ctx, "mount", "-t", "configfs", "none", "/sys/kernel/config").Run(); err != nil {
			return fmt.Errorf("mount configfs: %w", err)
		}
	}
	if _, err := os.Stat(nvmetPath); err != nil {
		return fmt.Errorf("kernel NVMe target configfs is unavailable after setup: %w", err)
	}
	return nil
}

func (k *KernelBackend) CheckExport(
	ctx context.Context,
	nqn, blockPath, portalIP string,
	portalPort int,
	options map[string]string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := storageoptions.Validate(k.Name(), options); err != nil {
		return err
	}
	addressFamily, err := kernelAddressFamily(portalIP)
	if err != nil {
		return err
	}
	subsystemPath := filepath.Join(nvmetPath, "subsystems", nqn)
	namespacePath := filepath.Join(subsystemPath, "namespaces", "1")
	portPath := filepath.Join(nvmetPath, "ports", kernelPortID)
	requiredValues := map[string]string{
		filepath.Join(subsystemPath, "attr_allow_any_host"): "0",
		filepath.Join(namespacePath, "device_path"):         blockPath,
		filepath.Join(namespacePath, "enable"):              "1",
		filepath.Join(portPath, "addr_adrfam"):              addressFamily,
		filepath.Join(portPath, "addr_trtype"):              "rdma",
		filepath.Join(portPath, "addr_trsvcid"):             strconv.Itoa(portalPort),
		filepath.Join(portPath, "addr_traddr"):              portalIP,
	}
	for path, expected := range requiredValues {
		actual, err := configValue(path)
		if err != nil {
			return fmt.Errorf("read kernel export state %s: %w", path, err)
		}
		if actual != expected {
			return fmt.Errorf("kernel export state %s is %q, want %q", path, actual, expected)
		}
	}
	namespaces, err := os.ReadDir(filepath.Join(subsystemPath, "namespaces"))
	if err != nil {
		return fmt.Errorf("list kernel export namespaces: %w", err)
	}
	if len(namespaces) != 1 || namespaces[0].Name() != "1" {
		return fmt.Errorf("kernel subsystem %s must expose exactly namespace 1", nqn)
	}
	linkPath := filepath.Join(portPath, "subsystems", nqn)
	if !kernelLinkMatches(linkPath, subsystemPath) {
		return fmt.Errorf("kernel subsystem %s is not linked to port %s", nqn, kernelPortID)
	}
	return nil
}

func (k *KernelBackend) ExportVolume(
	ctx context.Context,
	volumeName, blockPath, portalIP string,
	portalPort int,
	options map[string]string,
) (string, error) {
	nqn := volumeidentity.NQN(volumeName)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := storageoptions.Validate(k.Name(), options); err != nil {
		return "", err
	}
	if _, err := kernelAddressFamily(portalIP); err != nil {
		return "", err
	}
	if portalPort < 1 || portalPort > 65535 {
		return "", fmt.Errorf("kernel NVMe-oF portal port %d is invalid", portalPort)
	}
	if strings.TrimSpace(blockPath) == "" {
		return "", fmt.Errorf("kernel NVMe-oF block path is empty")
	}
	if err := ensureKernelConfigFS(ctx); err != nil {
		return "", err
	}
	klog.InfoS("Ensuring kernel NVMe-oF target export", "blockPath", blockPath, "nqn", nqn, "portalIP", portalIP, "portalPort", portalPort)

	subsystemPath := filepath.Join(nvmetPath, "subsystems", nqn)
	if err := os.MkdirAll(subsystemPath, 0755); err != nil {
		return "", fmt.Errorf("create kernel target subsystem %s: %w", nqn, err)
	}
	if err := os.WriteFile(filepath.Join(subsystemPath, "attr_allow_any_host"), []byte("0"), 0644); err != nil {
		return "", fmt.Errorf("disable unrestricted host access: %w", err)
	}
	linkPath := filepath.Join(nvmetPath, "ports", kernelPortID, "subsystems", nqn)
	if err := ensureKernelNamespace(subsystemPath, linkPath, blockPath); err != nil {
		return "", err
	}
	if err := ensureKernelPort(portalIP, portalPort); err != nil {
		return "", err
	}
	if err := ensureKernelSubsystemLink(linkPath, subsystemPath); err != nil {
		return "", err
	}
	if err := k.CheckExport(ctx, nqn, blockPath, portalIP, portalPort, options); err != nil {
		return "", fmt.Errorf("verify repaired kernel export %s: %w", nqn, err)
	}
	return nqn, nil
}

func (k *KernelBackend) ReconcileHostAccess(ctx context.Context, nqn, hostNQN string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	subsystemPath := filepath.Join(nvmetPath, "subsystems", nqn)
	allowAnyPath := filepath.Join(subsystemPath, "attr_allow_any_host")
	allowAny, err := os.ReadFile(allowAnyPath)
	if err != nil {
		return fmt.Errorf("read unrestricted host policy for %s: %w", nqn, err)
	}
	allowedHostsPath := filepath.Join(subsystemPath, "allowed_hosts")
	entries, err := os.ReadDir(allowedHostsPath)
	if err != nil {
		return fmt.Errorf("list allowed hosts for %s: %w", nqn, err)
	}
	exact := strings.TrimSpace(string(allowAny)) == "0" && len(entries) <= 1
	if hostNQN == "" {
		exact = exact && len(entries) == 0
	} else {
		exact = exact && len(entries) == 1 && entries[0].Name() == hostNQN
	}
	linkPath := filepath.Join(nvmetPath, "ports", kernelPortID, "subsystems", nqn)
	if exact {
		return ensureKernelSubsystemLink(linkPath, subsystemPath)
	}

	// Unlinking the subsystem from its port disconnects existing controllers.
	// Keep it disconnected until every stale host has been removed.
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("disconnect subsystem %s before changing host access: %w", nqn, err)
	}
	if err := os.WriteFile(allowAnyPath, []byte("0"), 0644); err != nil {
		return fmt.Errorf("disable unrestricted host access for %s: %w", nqn, err)
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(allowedHostsPath, entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale host %s from %s: %w", entry.Name(), nqn, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if hostNQN != "" {
		hostPath := filepath.Join(nvmetPath, "hosts", hostNQN)
		if err := os.Mkdir(hostPath, 0755); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create NVMe target host %s: %w", hostNQN, err)
		}
		if err := os.Symlink(hostPath, filepath.Join(allowedHostsPath, hostNQN)); err != nil && !os.IsExist(err) {
			return fmt.Errorf("authorize host %s for %s: %w", hostNQN, nqn, err)
		}
	}
	if err := os.Symlink(subsystemPath, linkPath); err != nil && !os.IsExist(err) {
		return fmt.Errorf("reconnect subsystem %s after changing host access: %w", nqn, err)
	}
	return nil
}

func (k *KernelBackend) UnexportVolume(ctx context.Context, nqn string) error {
	klog.InfoS("Unexporting kernel NVMe-oF target", "nqn", nqn)
	subsysPath := filepath.Join(nvmetPath, "subsystems", nqn)
	if _, err := os.Stat(subsysPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect kernel target subsystem before unexport: %w", err)
	}
	if err := k.ReconcileHostAccess(ctx, nqn, ""); err != nil {
		return fmt.Errorf("revoke kernel target host access before unexport: %w", err)
	}

	portPath := filepath.Join(nvmetPath, "ports", kernelPortID)
	linkPath := filepath.Join(portPath, "subsystems", nqn)

	// Remove link
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove port symlink: %w", err)
	}

	nsPath := filepath.Join(subsysPath, "namespaces", "1")

	// Disable and remove namespace
	if err := os.WriteFile(filepath.Join(nsPath, "enable"), []byte("0"), 0644); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to disable namespace: %w", err)
	}

	if err := os.Remove(nsPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove namespace: %w", err)
	}

	// Remove subsystem
	if err := os.Remove(subsysPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove subsystem: %w", err)
	}

	return nil
}
