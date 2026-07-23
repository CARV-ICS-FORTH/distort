package csi

import (
	"context"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	DriverName    = "storage.distort.io"
	VendorVersion = "0.1.0"
)

type Driver struct {
	name     string
	nodeID   string
	endpoint string

	k8sClient client.Client

	// CSI services
	ids *IdentityServer
	cs  *ControllerServer
	ns  *NodeServer
}

func NewDriver(nodeID, endpoint string, k8sClient client.Client) *Driver {
	klog.Infof("Creating new CSI driver: name=%s nodeID=%s endpoint=%s", DriverName, nodeID, endpoint)

	d := &Driver{
		name:      DriverName,
		nodeID:    nodeID,
		endpoint:  endpoint,
		k8sClient: k8sClient,
	}

	d.ids = &IdentityServer{name: DriverName, version: VendorVersion}
	d.cs = &ControllerServer{k8sClient: k8sClient}
	d.ns = &NodeServer{nodeID: nodeID}

	return d
}

func (d *Driver) Run() {
	var wg sync.WaitGroup

	wg.Go(func() {

		// Parse endpoint
		var protocol, addr string
		parts := strings.SplitN(d.endpoint, "://", 2)
		if len(parts) == 2 {
			protocol = parts[0]
			addr = parts[1]
		} else {
			klog.Fatalf("Invalid endpoint format: %s", d.endpoint)
		}

		if protocol == "unix" {
			klog.Infof("Removing existing socket: %s", addr)
			if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
				klog.Fatalf("Failed to remove existing socket %s: %v", addr, err)
			}
		}

		listener, err := net.Listen(protocol, addr)
		if err != nil {
			klog.Fatalf("Failed to listen on %s: %v", addr, err)
		}

		server := grpc.NewServer()

		// Register CSI Services
		csi.RegisterIdentityServer(server, d.ids)
		csi.RegisterControllerServer(server, d.cs)
		csi.RegisterNodeServer(server, d.ns)

		klog.Infof("Starting CSI GRPC server on %s", d.endpoint)
		if err := server.Serve(listener); err != nil {
			klog.Fatalf("GRPC server failed: %v", err)
		}
	})

	wg.Wait()
}

// ==========================================
// Identity Server
// ==========================================

type IdentityServer struct {
	csi.UnimplementedIdentityServer
	name    string
	version string
}

func (ids *IdentityServer) GetPluginInfo(ctx context.Context, req *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	klog.V(5).Infof("Using default GetPluginInfo")
	if ids.name == "" {
		return nil, status.Error(codes.Unavailable, "Driver name not configured")
	}

	if ids.version == "" {
		return nil, status.Error(codes.Unavailable, "Driver is missing version")
	}

	return &csi.GetPluginInfoResponse{
		Name:          ids.name,
		VendorVersion: ids.version,
	}, nil
}

func (ids *IdentityServer) Probe(ctx context.Context, req *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{}, nil
}

func (ids *IdentityServer) GetPluginCapabilities(ctx context.Context, req *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{
		Capabilities: []*csi.PluginCapability{
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
					},
				},
			},
		},
	}, nil
}
