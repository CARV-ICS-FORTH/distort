package csi

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	DriverName      = "storage.distort.io"
	VendorVersion   = "0.5.0"
	DefaultEndpoint = "unix:///tmp/csi.sock"
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
	klog.InfoS("Creating CSI driver", "name", DriverName, "nodeID", nodeID, "endpoint", endpoint)

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

func (d *Driver) Run() error {
	listener, err := listenEndpoint(d.endpoint)
	if err != nil {
		return err
	}
	defer func() {
		if err := listener.Close(); err != nil {
			klog.ErrorS(err, "Failed to close CSI listener")
		}
	}()

	server := grpc.NewServer()
	csi.RegisterIdentityServer(server, d.ids)
	csi.RegisterControllerServer(server, d.cs)
	csi.RegisterNodeServer(server, d.ns)

	klog.InfoS("Starting CSI GRPC server", "endpoint", d.endpoint)
	if err := server.Serve(listener); err != nil {
		return fmt.Errorf("serve CSI GRPC endpoint: %w", err)
	}
	return nil
}

func listenEndpoint(endpoint string) (net.Listener, error) {
	parts := strings.SplitN(endpoint, "://", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid endpoint format %q", endpoint)
	}
	protocol, addr := parts[0], parts[1]

	if protocol == "unix" {
		if !filepath.IsAbs(addr) {
			return nil, fmt.Errorf("unix endpoint path must be absolute: %q", addr)
		}
		if err := os.MkdirAll(filepath.Dir(addr), 0o750); err != nil {
			return nil, fmt.Errorf("create unix socket directory %s: %w", filepath.Dir(addr), err)
		}
		info, err := os.Lstat(addr)
		switch {
		case os.IsNotExist(err):
		case err != nil:
			return nil, fmt.Errorf("inspect existing socket %s: %w", addr, err)
		case info.Mode()&os.ModeSocket == 0:
			return nil, fmt.Errorf("refusing to remove non-socket path %s", addr)
		default:
			klog.InfoS("Removing stale socket", "path", addr)
			if err := os.Remove(addr); err != nil {
				return nil, fmt.Errorf("remove existing socket %s: %w", addr, err)
			}
		}
	}

	listener, err := net.Listen(protocol, addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	return listener, nil
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
	klog.V(5).InfoS("Using default GetPluginInfo")
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
