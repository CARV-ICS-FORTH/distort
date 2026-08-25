package agent

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	storagev1alpha1 "distort/api/v1alpha1"
)

var sysClassInfiniBand = "/sys/class/infiniband"
var lookupInterfaceAddress = interfaceAddress

type RDMAEndpoint struct {
	Interface string
	IP        string
	Transport storagev1alpha1.RDMATransportType
	LinkSpeed string
}

func DiscoverRDMAEndpoint() (RDMAEndpoint, error) {
	devices, err := os.ReadDir(sysClassInfiniBand)
	if err != nil {
		return RDMAEndpoint{}, fmt.Errorf("read RDMA devices: %w", err)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name() < devices[j].Name() })
	var failures []string
	for _, device := range devices {
		portsRoot := filepath.Join(sysClassInfiniBand, device.Name(), "ports")
		ports, err := os.ReadDir(portsRoot)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", device.Name(), err))
			continue
		}
		for _, port := range ports {
			portRoot := filepath.Join(portsRoot, port.Name())
			state, err := os.ReadFile(filepath.Join(portRoot, "state"))
			if err != nil || !strings.Contains(strings.ToUpper(string(state)), "ACTIVE") {
				continue
			}
			linkLayer, err := os.ReadFile(filepath.Join(portRoot, "link_layer"))
			if err != nil {
				continue
			}
			transport := storagev1alpha1.RDMATransportInfiniBand
			if strings.EqualFold(strings.TrimSpace(string(linkLayer)), "Ethernet") {
				transport = storagev1alpha1.RDMATransportRoCEv2
			}
			interfaceName := rdmaPortInterface(portRoot)
			if interfaceName == "" {
				continue
			}
			ip, err := lookupInterfaceAddress(interfaceName)
			if err != nil {
				failures = append(failures, err.Error())
				continue
			}
			rate, _ := os.ReadFile(filepath.Join(portRoot, "rate"))
			return RDMAEndpoint{Interface: interfaceName, IP: ip, Transport: transport, LinkSpeed: strings.TrimSpace(string(rate))}, nil
		}
	}
	if len(failures) > 0 {
		return RDMAEndpoint{}, fmt.Errorf("no usable active RDMA endpoint: %s", strings.Join(failures, "; "))
	}
	return RDMAEndpoint{}, fmt.Errorf("no active RDMA port with a non-loopback IP address")
}

func rdmaPortInterface(portRoot string) string {
	entries, err := os.ReadDir(filepath.Join(portRoot, "gid_attrs", "ndevs"))
	if err != nil {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		value, err := os.ReadFile(filepath.Join(portRoot, "gid_attrs", "ndevs", entry.Name()))
		if err == nil && strings.TrimSpace(string(value)) != "" {
			return strings.TrimSpace(string(value))
		}
	}
	return ""
}

func interfaceAddress(name string) (string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", fmt.Errorf("resolve RDMA interface %s: %w", name, err)
	}
	if iface.Flags&net.FlagUp == 0 {
		return "", fmt.Errorf("RDMA interface %s is down", name)
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return "", fmt.Errorf("list RDMA interface %s addresses: %w", name, err)
	}
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && !ip.IsLoopback() && !ip.IsUnspecified() {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("RDMA interface %s has no usable IP address", name)
}
