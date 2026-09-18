package agent

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/rdmahealth"
)

var sysClassInfiniBand = "/sys/class/infiniband"
var sysClassNet = "/sys/class/net"
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
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s port %s: read state: %v", device.Name(), port.Name(), err))
				continue
			}
			if !rdmaPortIsActive(string(state)) {
				continue
			}
			linkLayer, err := os.ReadFile(filepath.Join(portRoot, "link_layer"))
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s port %s: read link layer: %v", device.Name(), port.Name(), err))
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
			interfaceNames, err := rdmaPortInterfaces(device.Name(), port.Name(), portRoot, transport)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s port %s: %v", device.Name(), port.Name(), err))
				continue
			}
			for _, interfaceName := range interfaceNames {
				ip, err := lookupInterfaceAddress(interfaceName)
				if err != nil {
					failures = append(failures, err.Error())
					continue
				}
				rate, _ := os.ReadFile(filepath.Join(portRoot, "rate"))
				return RDMAEndpoint{Interface: interfaceName, IP: ip, Transport: transport, LinkSpeed: strings.TrimSpace(string(rate))}, nil
			}
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

func rdmaPortInterfaces(deviceName, portName, portRoot string, transport storagev1alpha1.RDMATransportType) ([]string, error) {
	interfaces, gidErr := rdmaGIDNetDevices(portRoot)
	if transport == storagev1alpha1.RDMATransportInfiniBand {
		ipoibInterfaces, ipoibErr := ipoibPortInterfaces(deviceName, portName)
		interfaces = appendUniqueStrings(interfaces, ipoibInterfaces...)
		if len(interfaces) == 0 {
			return nil, fmt.Errorf("no associated network interface found (GID mapping: %v; IPoIB mapping: %v)", gidErr, ipoibErr)
		}
		return interfaces, nil
	}
	if len(interfaces) == 0 {
		return nil, fmt.Errorf("no associated network interface found through GID mapping: %v", gidErr)
	}
	return interfaces, nil
}

func rdmaGIDNetDevices(portRoot string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(portRoot, "gid_attrs", "ndevs"))
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var interfaces []string
	var firstReadErr error
	unreadable := 0
	for _, entry := range entries {
		value, err := os.ReadFile(filepath.Join(portRoot, "gid_attrs", "ndevs", entry.Name()))
		if err != nil {
			unreadable++
			if firstReadErr == nil {
				firstReadErr = err
			}
			continue
		}
		if interfaceName := strings.TrimSpace(string(value)); interfaceName != "" {
			interfaces = appendUniqueStrings(interfaces, interfaceName)
		}
	}
	if len(interfaces) > 0 {
		return interfaces, nil
	}
	if unreadable > 0 {
		return nil, fmt.Errorf("%d mapping entries unreadable: %w", unreadable, firstReadErr)
	}
	return nil, fmt.Errorf("mapping entries are empty")
}

func ipoibPortInterfaces(deviceName, portName string) ([]string, error) {
	rdmaDevicePath, err := filepath.EvalSymlinks(filepath.Join(sysClassInfiniBand, deviceName, "device"))
	if err != nil {
		return nil, fmt.Errorf("resolve RDMA device: %w", err)
	}
	portNumber, err := strconv.ParseUint(portName, 10, 32)
	if err != nil || portNumber == 0 {
		return nil, fmt.Errorf("invalid RDMA port number %q", portName)
	}

	entries, err := os.ReadDir(sysClassNet)
	if err != nil {
		return nil, fmt.Errorf("read network interfaces: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var interfaces []string
	for _, entry := range entries {
		interfaceRoot := filepath.Join(sysClassNet, entry.Name())
		networkType, err := os.ReadFile(filepath.Join(interfaceRoot, "type"))
		if err != nil || strings.TrimSpace(string(networkType)) != "32" {
			continue
		}
		interfaceDevicePath, err := filepath.EvalSymlinks(filepath.Join(interfaceRoot, "device"))
		if err != nil || interfaceDevicePath != rdmaDevicePath {
			continue
		}
		devPort, err := networkDevicePort(interfaceRoot)
		if err != nil || devPort+1 != portNumber {
			continue
		}
		interfaces = appendUniqueStrings(interfaces, entry.Name())
	}
	if len(interfaces) == 0 {
		return nil, fmt.Errorf("no IPoIB interface matches RDMA device %s port %s", deviceName, portName)
	}
	return interfaces, nil
}

func networkDevicePort(interfaceRoot string) (uint64, error) {
	for _, property := range []string{"dev_port", "dev_id"} {
		value, err := os.ReadFile(filepath.Join(interfaceRoot, property))
		if err != nil {
			continue
		}
		port, err := strconv.ParseUint(strings.TrimSpace(string(value)), 0, 32)
		if err == nil {
			return port, nil
		}
	}
	return 0, fmt.Errorf("network interface has no valid dev_port or dev_id")
}

func appendUniqueStrings(values []string, additions ...string) []string {
	for _, addition := range additions {
		if !slices.Contains(values, addition) {
			values = append(values, addition)
		}
	}
	return values
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
