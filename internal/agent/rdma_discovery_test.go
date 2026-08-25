package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	storagev1alpha1 "distort/api/v1alpha1"
)

func setupRDMAFixture(t *testing.T, state, linkLayer string) {
	t.Helper()
	oldRoot, oldLookup := sysClassInfiniBand, lookupInterfaceAddress
	sysClassInfiniBand = t.TempDir()
	lookupInterfaceAddress = func(name string) (string, error) {
		if name != "rdma0" {
			return "", errors.New("unexpected interface")
		}
		return "192.0.2.10", nil
	}
	t.Cleanup(func() {
		sysClassInfiniBand = oldRoot
		lookupInterfaceAddress = oldLookup
	})
	port := filepath.Join(sysClassInfiniBand, "rxe0", "ports", "1")
	writeDiscoveryFile(t, filepath.Join(port, "state"), state)
	writeDiscoveryFile(t, filepath.Join(port, "link_layer"), linkLayer)
	writeDiscoveryFile(t, filepath.Join(port, "rate"), "100 Gb/sec\n")
	writeDiscoveryFile(t, filepath.Join(port, "gid_attrs", "ndevs", "0"), "rdma0\n")
}

func TestDiscoverRDMAEndpointFindsActiveRoCEInterface(t *testing.T) {
	setupRDMAFixture(t, "4: ACTIVE\n", "Ethernet\n")
	endpoint, err := DiscoverRDMAEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Interface != "rdma0" || endpoint.IP != "192.0.2.10" ||
		endpoint.Transport != storagev1alpha1.RDMATransportRoCEv2 || endpoint.LinkSpeed != "100 Gb/sec" {
		t.Fatalf("unexpected RDMA endpoint: %#v", endpoint)
	}
}

func TestDiscoverRDMAEndpointRejectsDownLink(t *testing.T) {
	setupRDMAFixture(t, "1: DOWN\n", "Ethernet\n")
	if _, err := DiscoverRDMAEndpoint(); err == nil {
		t.Fatal("down RDMA link was reported ready")
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
