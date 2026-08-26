package agent

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/rdmahealth"
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
			if err != nil || !rdmaPortIsActive(string(state)) {
				continue
			}
			linkLayer, err := os.ReadFile(filepath.Join(portRoot, "link_layer"))
			if err != nil {
				continue
			}
			var transport storagev1alpha1.RDMATransportType
			switch {
			case strings.EqualFold(strings.TrimSpace(string(linkLayer)), "Ethernet"):
				transport = storagev1alpha1.RDMATransportRoCEv2
			case strings.EqualFold(strings.TrimSpace(string(linkLayer)), "InfiniBand"):
				transport = storagev1alpha1.RDMATransportInfiniBand
			default:
				failures = append(failures, fmt.Sprintf("%s port %s: unsupported link layer %q",
					device.Name(), port.Name(), strings.TrimSpace(string(linkLayer))))
				continue
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

func rdmaPortIsActive(state string) bool {
	state = strings.TrimSpace(state)
	if _, value, found := strings.Cut(state, ":"); found {
		state = strings.TrimSpace(value)
	}
	return strings.EqualFold(state, "ACTIVE")
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
	if ip, err := selectRoutableAddress(addresses); err == nil {
		return ip, nil
	}
	return "", fmt.Errorf("RDMA interface %s has no usable IP address", name)
}

func selectRoutableAddress(addresses []net.Addr) (string, error) {
	var ipv6 string
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil {
			continue
		}
		if _, err := rdmahealth.ParseUsableIP(ip.String()); err != nil {
			continue
		}
		if ip.To4() != nil {
			return ip.String(), nil
		}
		if ipv6 == "" {
			ipv6 = ip.String()
		}
	}
	if ipv6 != "" {
		return ipv6, nil
	}
	return "", fmt.Errorf("no routable unicast IP address")
}
