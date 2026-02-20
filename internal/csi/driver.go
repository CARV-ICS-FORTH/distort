package csi

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
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
	klog.Infof("Starting CSI grpc server endpoint: %s", d.endpoint)

	// TODO: Start GRPC server at endpoint and register CSI interfaces
	// For example:
	// s := NonBlockingGRPCServer{}
	// s.Start(d.endpoint, d.ids, d.cs, d.ns)
	// s.Wait()
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
