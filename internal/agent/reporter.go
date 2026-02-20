package agent

import (
	"context"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "distort/api/v1alpha1"
)

// Reporter handles hardware discovery and submitting RDMAStorageNode + NVMeDevice CRs.
type Reporter struct {
	client.Client
	NodeName string
	Interval time.Duration
}

// Start runs the periodic discovery reporting loop.
func (r *Reporter) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("hardware-reporter")
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()

	logger.Info("Starting Hardware Reporter", "node", r.NodeName)

	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping Hardware Reporter")
			return nil
		case <-ticker.C:
			r.report(ctx)
		}
	}
}

func (r *Reporter) report(ctx context.Context) {
	// TODO: Replace mocks with actual sysfs/PCI scanning logic
	r.reportNode(ctx)
	r.reportDevices(ctx)
}

func (r *Reporter) reportNode(ctx context.Context) {
	logger := log.FromContext(ctx)

	nodeCR := &storagev1alpha1.RDMAStorageNode{}
	err := r.Get(ctx, types.NamespacedName{Name: r.NodeName}, nodeCR)

	exists := err == nil
	if client.IgnoreNotFound(err) != nil {
		logger.Error(err, "Failed to fetch RDMAStorageNode")
		return
	}

	// Mock data for initial scaffolding
	nodeCR.Name = r.NodeName
	nodeCR.Spec.NodeName = r.NodeName
	nodeCR.Spec.RDMAIP = "192.168.1.100" // Should detect active RDMA interface
	nodeCR.Spec.Transport = storagev1alpha1.RDMATransportRoCEv2

	if !exists {
		if err := r.Create(ctx, nodeCR); err != nil {
			logger.Error(err, "Failed to create RDMAStorageNode")
			return
		}
	} else {
		if err := r.Update(ctx, nodeCR); err != nil {
			logger.Error(err, "Failed to update RDMAStorageNode Spec")
			return
		}
	}

	// Update Status
	nodeCR.Status.TotalCapacity = resource.MustParse("2Ti") // sum from devices
	nodeCR.Status.FreeCapacity = resource.MustParse("1Ti")  // sum of free from claimed devices
	nodeCR.Status.ActiveExports = 0

	if err := r.Status().Update(ctx, nodeCR); err != nil {
		logger.Error(err, "Failed to update RDMAStorageNode Status")
	}
}

func (r *Reporter) reportDevices(ctx context.Context) {
	logger := log.FromContext(ctx)

	devices, err := DiscoverNVMe()
	if err != nil {
		logger.Error(err, "Failed to discover NVMe devices")
		return
	}

	for _, d := range devices {
		// Device name is usually nodeName-serial
		deviceName := r.NodeName + "-" + strings.ToLower(d.SerialNumber)

		devCR := &storagev1alpha1.NVMeDevice{}
		err := r.Get(ctx, types.NamespacedName{Name: deviceName}, devCR)

		exists := err == nil
		if client.IgnoreNotFound(err) != nil {
			logger.Error(err, "Failed to get NVMeDevice", "device", deviceName)
			continue
		}

		if !exists {
			devCR.Name = deviceName
			devCR.Spec = storagev1alpha1.NVMeDeviceSpec{
				NodeName:      r.NodeName,
				PCIAddress:    d.PCIAddress,
				SerialNumber:  d.SerialNumber,
				Model:         d.Model,
				TotalCapacity: *resource.NewQuantity(d.TotalBytes, resource.BinarySI),
				NUMANode:      d.NUMANode,
			}

			if err := r.Create(ctx, devCR); err != nil {
				logger.Error(err, "Failed to create NVMeDevice", "device", deviceName)
				continue
			}
			logger.Info("Discovered new NVMe device", "device", deviceName, "serial", d.SerialNumber)

			// Initialize Status
			devCR.Status.State = storagev1alpha1.NVMeDeviceStateAvailable
			devCR.Status.FreeCapacity = devCR.Spec.TotalCapacity

			if err := r.Status().Update(ctx, devCR); err != nil {
				logger.Error(err, "Failed to update NVMeDevice status", "device", deviceName)
			}
		} else {
			// In a real environment, you might check if Spec changed.
			// Currently, we assume Spec properties are immutable physical properties.
		}
	}
}
