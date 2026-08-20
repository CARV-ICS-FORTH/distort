package agent

import (
	"context"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "distort/api/v1alpha1"
)

// Reporter handles hardware discovery and submitting RDMAStorageNode + NVMeDevice CRs.
type Reporter struct {
	client.Client
	NodeName        string
	Interval        time.Duration
	discoverDevices func() ([]HardwareNVMe, error)
}

const hardwareAvailableCondition = "HardwareAvailable"

func (r *Reporter) discoverNVMe() ([]HardwareNVMe, error) {
	if r.discoverDevices != nil {
		return r.discoverDevices()
	}
	return DiscoverNVMe()
}

// Start runs the periodic discovery reporting loop.
func (r *Reporter) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("hardware-reporter")
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()

	logger.Info("Starting Hardware Reporter", "node", r.NodeName)

	r.report(ctx)

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
	totalCap, freeCap := r.reportDevices(ctx)
	r.reportNode(ctx, totalCap, freeCap)
}

func (r *Reporter) reportNode(ctx context.Context, totalCapacity, freeCapacity int64) {
	logger := log.FromContext(ctx)

	// Fetch K8s Node to determine Internal IP for RDMA
	rdmaIP := "127.0.0.1" // Fallback
	k8sNode := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: r.NodeName}, k8sNode); err == nil {
		for _, addr := range k8sNode.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				rdmaIP = addr.Address
				break
			}
		}
	} else {
		logger.Error(err, "Failed to get K8s Node for RDMAIP")
	}

	nodeCR := &storagev1alpha1.RDMAStorageNode{}
	err := r.Get(ctx, types.NamespacedName{Name: r.NodeName}, nodeCR)
	exists := err == nil
	if client.IgnoreNotFound(err) != nil {
		logger.Error(err, "Failed to fetch RDMAStorageNode")
		return
	}

	if !exists {
		nodeCR.Name = r.NodeName
		nodeCR.Spec.NodeName = r.NodeName
		nodeCR.Spec.RDMAIP = rdmaIP
		nodeCR.Spec.Transport = storagev1alpha1.RDMATransportRoCEv2
		if err := r.Create(ctx, nodeCR); err != nil {
			logger.Error(err, "Failed to create RDMAStorageNode")
			return
		}
	} else if nodeCR.Spec.NodeName != r.NodeName ||
		nodeCR.Spec.RDMAIP != rdmaIP ||
		nodeCR.Spec.Transport != storagev1alpha1.RDMATransportRoCEv2 {
		base := nodeCR.DeepCopy()
		nodeCR.Spec.NodeName = r.NodeName
		nodeCR.Spec.RDMAIP = rdmaIP
		nodeCR.Spec.Transport = storagev1alpha1.RDMATransportRoCEv2
		if err := r.Patch(ctx, nodeCR, client.MergeFrom(base)); err != nil {
			logger.Error(err, "Failed to update RDMAStorageNode Spec")
			return
		}
	}

	base := nodeCR.DeepCopy()
	nodeCR.Status.TotalCapacity = *resource.NewQuantity(totalCapacity, resource.BinarySI)
	nodeCR.Status.FreeCapacity = *resource.NewQuantity(freeCapacity, resource.BinarySI)
	nodeCR.Status.ActiveExports = 0
	err = r.Status().Patch(ctx, nodeCR, client.MergeFrom(base))
	if err != nil {
		logger.Error(err, "Failed to report RDMAStorageNode")
	}
}

func (r *Reporter) reportDevices(ctx context.Context) (int64, int64) {
	logger := log.FromContext(ctx)
	var nodeTotalCap, nodeFreeCap int64

	devices, err := r.discoverNVMe()
	if err != nil {
		logger.Error(err, "Failed to discover NVMe devices")
		return 0, 0
	}

	seen := make(map[string]struct{}, len(devices))
	for _, d := range devices {
		serial := strings.ToLower(strings.TrimSpace(d.SerialNumber))
		if serial == "" {
			serial = "unknown"
		}
		// Device name is usually nodeName-serial
		deviceName := r.NodeName + "-" + serial
		seen[deviceName] = struct{}{}

		devCR := &storagev1alpha1.NVMeDevice{}
		err := r.Get(ctx, types.NamespacedName{Name: deviceName}, devCR)

		exists := err == nil
		if client.IgnoreNotFound(err) != nil {
			logger.Error(err, "Failed to get NVMeDevice", "device", deviceName)
			continue
		}

		var devTotal, devFree int64

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
			latest := &storagev1alpha1.NVMeDevice{}
			err := r.Get(ctx, types.NamespacedName{Name: deviceName}, latest)
			if err == nil {
				base := latest.DeepCopy()
				latest.Status.State = storagev1alpha1.NVMeDeviceStateAvailable
				latest.Status.FreeCapacity = latest.Spec.TotalCapacity
				meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
					Type: hardwareAvailableCondition, Status: metav1.ConditionTrue,
					ObservedGeneration: latest.Generation, Reason: "DeviceDiscovered",
					Message: "The reporting agent discovered the NVMe device",
				})
				err = r.Status().Patch(ctx, latest, client.MergeFrom(base))
			}
			if err != nil {
				logger.Error(err, "Failed to update NVMeDevice status", "device", deviceName)
			}

			devTotal = d.TotalBytes
			devFree = d.TotalBytes
		} else {
			specBase := devCR.DeepCopy()
			devCR.Spec.NodeName = r.NodeName
			devCR.Spec.PCIAddress = d.PCIAddress
			devCR.Spec.SerialNumber = d.SerialNumber
			devCR.Spec.Model = d.Model
			devCR.Spec.TotalCapacity = *resource.NewQuantity(d.TotalBytes, resource.BinarySI)
			devCR.Spec.NUMANode = d.NUMANode
			if err := r.Patch(ctx, devCR, client.MergeFrom(specBase)); err != nil {
				logger.Error(err, "Failed to refresh NVMeDevice spec", "device", deviceName)
				continue
			}
			statusBase := devCR.DeepCopy()
			if devCR.Status.State == storagev1alpha1.NVMeDeviceStateUnavailable {
				if devCR.Status.ClaimRef == nil {
					devCR.Status.State = storagev1alpha1.NVMeDeviceStateAvailable
				} else {
					devCR.Status.State = storagev1alpha1.NVMeDeviceStateClaimed
				}
			}
			meta.SetStatusCondition(&devCR.Status.Conditions, metav1.Condition{
				Type: hardwareAvailableCondition, Status: metav1.ConditionTrue,
				ObservedGeneration: devCR.Generation, Reason: "DeviceDiscovered",
				Message: "The reporting agent discovered the NVMe device",
			})
			if err := r.Status().Patch(ctx, devCR, client.MergeFrom(statusBase)); err != nil {
				logger.Error(err, "Failed to mark NVMeDevice available", "device", deviceName)
				continue
			}
			devTotal = d.TotalBytes
			devFree = devCR.Status.FreeCapacity.Value()
		}

		if devCR.Status.State == storagev1alpha1.NVMeDeviceStateClaimed {
			nodeTotalCap += devTotal
			nodeFreeCap += devFree
		}
	}

	var reported storagev1alpha1.NVMeDeviceList
	if err := r.List(ctx, &reported); err != nil {
		logger.Error(err, "Failed to list reported NVMeDevices")
		return nodeTotalCap, nodeFreeCap
	}
	for i := range reported.Items {
		dev := &reported.Items[i]
		if dev.Spec.NodeName != r.NodeName {
			continue
		}
		if _, ok := seen[dev.Name]; ok {
			continue
		}
		base := dev.DeepCopy()
		dev.Status.State = storagev1alpha1.NVMeDeviceStateUnavailable
		meta.SetStatusCondition(&dev.Status.Conditions, metav1.Condition{
			Type: hardwareAvailableCondition, Status: metav1.ConditionFalse,
			ObservedGeneration: dev.Generation, Reason: "DeviceNotDiscovered",
			Message: "The reporting agent no longer discovers the NVMe device",
		})
		if err := r.Status().Patch(ctx, dev, client.MergeFrom(base)); err != nil {
			logger.Error(err, "Failed to mark NVMeDevice unavailable", "device", dev.Name)
		}
	}

	return nodeTotalCap, nodeFreeCap
}
