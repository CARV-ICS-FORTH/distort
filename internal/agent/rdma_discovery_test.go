package agent

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	storagev1alpha1 "distort/api/v1alpha1"
)

const testRDMAIPv4 = "192.0.2.10"

func setupRDMAFixture(t *testing.T, state, linkLayer string) string {
	t.Helper()
	oldRoot, oldNetRoot, oldLookup := sysClassInfiniBand, sysClassNet, lookupInterfaceAddress
	fixtureRoot := t.TempDir()
	sysClassInfiniBand = filepath.Join(fixtureRoot, "infiniband")
	sysClassNet = filepath.Join(fixtureRoot, "net")
	if err := os.MkdirAll(sysClassNet, 0o755); err != nil {
		t.Fatal(err)
	}
	lookupInterfaceAddress = func(name string) (string, error) {
		if name != "rdma0" {
			return "", errors.New("unexpected interface")
		}
		return testRDMAIPv4, nil
	}
	t.Cleanup(func() {
		sysClassInfiniBand = oldRoot
		sysClassNet = oldNetRoot
		lookupInterfaceAddress = oldLookup
	})
	port := filepath.Join(sysClassInfiniBand, "rxe0", "ports", "1")
	writeDiscoveryFile(t, filepath.Join(port, "state"), state)
	writeDiscoveryFile(t, filepath.Join(port, "link_layer"), linkLayer)
	writeDiscoveryFile(t, filepath.Join(port, "rate"), "100 Gb/sec\n")
	writeDiscoveryFile(t, filepath.Join(port, "gid_attrs", "ndevs", "0"), "rdma0\n")
	return port
}

func TestDiscoverRDMAEndpointFindsActiveRoCEInterface(t *testing.T) {
	setupRDMAFixture(t, "4: ACTIVE\n", "Ethernet\n")
	endpoint, err := DiscoverRDMAEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Interface != "rdma0" || endpoint.IP != testRDMAIPv4 ||
		endpoint.Transport != storagev1alpha1.RDMATransportRoCEv2 || endpoint.LinkSpeed != "100 Gb/sec" {
		t.Fatalf("unexpected RDMA endpoint: %#v", endpoint)
	}
}

func TestDiscoverRDMAEndpointFindsNativeInfiniBandIPoIBInterface(t *testing.T) {
	port := setupRDMAFixture(t, "4: ACTIVE\n", "InfiniBand\n")
	if err := os.RemoveAll(filepath.Join(port, "gid_attrs", "ndevs")); err != nil {
		t.Fatal(err)
	}

	devicePath := filepath.Join(t.TempDir(), "devices", "0000:43:00.0")
	if err := os.MkdirAll(devicePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(devicePath, filepath.Join(sysClassInfiniBand, "rxe0", "device")); err != nil {
		t.Fatal(err)
	}

	interfaceRoot := filepath.Join(sysClassNet, "ibs2")
	writeDiscoveryFile(t, filepath.Join(interfaceRoot, "type"), "32\n")
	writeDiscoveryFile(t, filepath.Join(interfaceRoot, "dev_port"), "0\n")
	if err := os.Symlink(devicePath, filepath.Join(interfaceRoot, "device")); err != nil {
		t.Fatal(err)
	}
	lookupInterfaceAddress = func(name string) (string, error) {
		if name != "ibs2" {
			return "", errors.New("unexpected interface")
		}
		return "192.168.5.94", nil
	}

	endpoint, err := DiscoverRDMAEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Interface != "ibs2" || endpoint.IP != "192.168.5.94" ||
		endpoint.Transport != storagev1alpha1.RDMATransportInfiniBand || endpoint.LinkSpeed != "100 Gb/sec" {
		t.Fatalf("unexpected RDMA endpoint: %#v", endpoint)
	}
}

func TestDiscoverRDMAEndpointDoesNotUseIPoIBInterfaceFromAnotherPort(t *testing.T) {
	port := setupRDMAFixture(t, "4: ACTIVE\n", "InfiniBand\n")
	if err := os.Remove(filepath.Join(port, "gid_attrs", "ndevs", "0")); err != nil {
		t.Fatal(err)
	}

	devicePath := filepath.Join(t.TempDir(), "devices", "0000:43:00.0")
	if err := os.MkdirAll(devicePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(devicePath, filepath.Join(sysClassInfiniBand, "rxe0", "device")); err != nil {
		t.Fatal(err)
	}
	interfaceRoot := filepath.Join(sysClassNet, "ibs2")
	writeDiscoveryFile(t, filepath.Join(interfaceRoot, "type"), "32\n")
	writeDiscoveryFile(t, filepath.Join(interfaceRoot, "dev_port"), "1\n")
	if err := os.Symlink(devicePath, filepath.Join(interfaceRoot, "device")); err != nil {
		t.Fatal(err)
	}

	if _, err := DiscoverRDMAEndpoint(); err == nil || !strings.Contains(err.Error(), "no IPoIB interface matches") {
		t.Fatalf("different-port IPoIB discovery error = %v", err)
	}
}

func TestDiscoverRDMAEndpointRejectsDownLink(t *testing.T) {
	setupRDMAFixture(t, "1: DOWN\n", "Ethernet\n")
	if _, err := DiscoverRDMAEndpoint(); err == nil {
		t.Fatal("down RDMA link was reported ready")
	}
}

func TestDiscoverRDMAEndpointRejectsActiveDeferState(t *testing.T) {
	setupRDMAFixture(t, "5: ACTIVE_DEFER\n", "Ethernet\n")
	if _, err := DiscoverRDMAEndpoint(); err == nil {
		t.Fatal("ACTIVE_DEFER RDMA link was reported ready")
	}
}

func TestDiscoverRDMAEndpointRejectsUnknownLinkLayer(t *testing.T) {
	setupRDMAFixture(t, "4: ACTIVE\n", "TCP\n")
	if _, err := DiscoverRDMAEndpoint(); err == nil {
		t.Fatal("unsupported RDMA link layer was reported ready")
	}
}

type testNetworkAddress string

func (a testNetworkAddress) Network() string { return "ip" }
func (a testNetworkAddress) String() string  { return string(a) }

func TestSelectRoutableAddressRejectsUnsafeAddressesAndPrefersIPv4(t *testing.T) {
	addresses := []net.Addr{
		testNetworkAddress("fe80::1/64"),
		testNetworkAddress("ff02::1/64"),
		testNetworkAddress("2001:db8::10/64"),
		testNetworkAddress(testRDMAIPv4 + "/24"),
	}
	address, err := selectRoutableAddress(addresses)
	if err != nil || address != testRDMAIPv4 {
		t.Fatalf("selected address = %q, err=%v; want preferred routable IPv4", address, err)
	}
}

func TestSelectRoutableAddressAcceptsGlobalIPv6Fallback(t *testing.T) {
	addresses := []net.Addr{
		testNetworkAddress("169.254.1.10/16"),
		testNetworkAddress("2001:db8::20/64"),
	}
	address, err := selectRoutableAddress(addresses)
	if err != nil || address != "2001:db8::20" {
		t.Fatalf("selected address = %q, err=%v; want routable IPv6", address, err)
	}
}

func TestSelectRoutableAddressRejectsOnlyLocalAndMulticastAddresses(t *testing.T) {
	addresses := []net.Addr{
		testNetworkAddress("127.0.0.1/8"),
		testNetworkAddress("169.254.1.10/16"),
		testNetworkAddress("fe80::1/64"),
		testNetworkAddress("224.0.0.1/32"),
	}
	if address, err := selectRoutableAddress(addresses); err == nil {
		t.Fatalf("unsafe address set selected %q", address)
	}
}

func TestDiscoverRDMAEndpointSurfacesUnreadableSysfs(t *testing.T) {
	oldRoot := sysClassInfiniBand
	sysClassInfiniBand = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { sysClassInfiniBand = oldRoot })
	if _, err := DiscoverRDMAEndpoint(); err == nil || !os.IsNotExist(errors.Unwrap(err)) {
		t.Fatalf("unreadable RDMA sysfs error = %v", err)
	}
}
