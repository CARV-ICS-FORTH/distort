package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/rdmahealth"
)

// Reporter handles hardware discovery and submitting RDMAStorageNode + NVMeDevice CRs.
type Reporter struct {
	client.Client
	NodeName        string
	Interval        time.Duration
	discoverDevices func() ([]HardwareNVMe, error)
	discoverRDMA    func() (RDMAEndpoint, error)
}

const (
	hardwareAvailableCondition    = "HardwareAvailable"
	kernelDiscoveryReadyCondition = "KernelNVMeDiscoveryReady"
	spdkDiscoveryReadyCondition   = "SPDKNVMeDiscoveryReady"
)

func deviceObjectName(nodeName, serialNumber string) (string, error) {
	if strings.TrimSpace(nodeName) == "" {
		return "", errors.New("node name is empty")
	}
	if strings.TrimSpace(serialNumber) == "" {
		return "", errors.New("serial number is empty")
	}
	var prefix strings.Builder
	lastSeparator := false
	for _, char := range strings.ToLower(nodeName) {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
		if valid {
			prefix.WriteRune(char)
			lastSeparator = false
		} else if !lastSeparator {
			prefix.WriteByte('-')
			lastSeparator = true
		}
		if prefix.Len() >= 40 {
			break
		}
	}
	readable := strings.Trim(prefix.String(), "-")
	if readable == "" {
		readable = "nvme"
	}
	digest := sha256.Sum256([]byte(nodeName + "\x00" + serialNumber))
	return fmt.Sprintf("%s-%x", readable, digest[:16]), nil
}

func (r *Reporter) discoverNVMe() ([]HardwareNVMe, error) {
	if r.discoverDevices != nil {
		return r.discoverDevices()
	}
	return DiscoverNVMe()
}

func (r *Reporter) discoverRDMAEndpoint() (RDMAEndpoint, error) {
	if r.discoverRDMA != nil {
		return r.discoverRDMA()
	}
	return DiscoverRDMAEndpoint()
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
	totalCap, freeCap, inventoryErr := r.reportDevices(ctx)
	r.reportNode(ctx, totalCap, freeCap, inventoryErr)
}

func (r *Reporter) reportNode(ctx context.Context, totalCapacity, freeCapacity int64, inventoryErr error) {
	logger := log.FromContext(ctx)
	endpoint, discoveryErr := r.discoverRDMAEndpoint()

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
		nodeCR.Spec.RDMAIP = endpoint.IP
		nodeCR.Spec.Transport = endpoint.Transport
		if nodeCR.Spec.Transport == "" {
			nodeCR.Spec.Transport = storagev1alpha1.RDMATransportRoCEv2
		}
		nodeCR.Spec.LinkSpeed = endpoint.LinkSpeed
		if err := r.Create(ctx, nodeCR); err != nil {
			logger.Error(err, "Failed to create RDMAStorageNode")
			return
		}
	} else if discoveryErr == nil && (nodeCR.Spec.NodeName != r.NodeName ||
		nodeCR.Spec.RDMAIP != endpoint.IP || nodeCR.Spec.Transport != endpoint.Transport ||
		nodeCR.Spec.LinkSpeed != endpoint.LinkSpeed) {
		base := nodeCR.DeepCopy()
		nodeCR.Spec.NodeName = r.NodeName
		nodeCR.Spec.RDMAIP = endpoint.IP
		nodeCR.Spec.Transport = endpoint.Transport
		nodeCR.Spec.LinkSpeed = endpoint.LinkSpeed
		if err := r.Patch(ctx, nodeCR, client.MergeFrom(base)); err != nil {
			logger.Error(err, "Failed to update RDMAStorageNode Spec")
			return
		}
	}

	base := nodeCR.DeepCopy()
	nodeCR.Status.TotalCapacity = *resource.NewQuantity(totalCapacity, resource.BinarySI)
	nodeCR.Status.FreeCapacity = *resource.NewQuantity(freeCapacity, resource.BinarySI)
	var partitions storagev1alpha1.NVMePartitionList
	if err := r.List(ctx, &partitions); err != nil {
		logger.Error(err, "Failed to count active NVMe exports")
		return
	}
	nodeCR.Status.ActiveExports = 0
	for i := range partitions.Items {
		if partitions.Items[i].Spec.NodeName == r.NodeName &&
			partitions.Items[i].Status.State == storagev1alpha1.NVMePartitionStateExported &&
			partitions.Items[i].DeletionTimestamp.IsZero() {
			nodeCR.Status.ActiveExports++
		}
	}
	nodeCR.Status.LastHeartbeatTime = metav1.Now()
	condition := metav1.Condition{
		Type: rdmahealth.ReadyCondition, Status: metav1.ConditionTrue, ObservedGeneration: nodeCR.Generation,
		Reason: "RDMAEndpointReady", Message: "An active RDMA interface has a usable non-loopback IP address",
	}
	if discoveryErr != nil {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "RDMAEndpointUnavailable"
		condition.Message = discoveryErr.Error()
	}
	meta.SetStatusCondition(&nodeCR.Status.Conditions, condition)
	r.setInventoryConditions(nodeCR, inventoryErr)
	err = r.Status().Patch(ctx, nodeCR, client.MergeFrom(base))
	if err != nil {
		logger.Error(err, "Failed to report RDMAStorageNode")
	}
}

func (r *Reporter) setInventoryConditions(node *storagev1alpha1.RDMAStorageNode, discoveryErr error) {
	aggregate := metav1.Condition{
		Type: storagev1alpha1.NVMeInventoryReadyCondition, Status: metav1.ConditionTrue,
		ObservedGeneration: node.Generation, Reason: "NVMeDiscoverySucceeded",
		Message: "Kernel and SPDK NVMe inventory sources completed successfully",
	}
	kernel := metav1.Condition{
		Type: kernelDiscoveryReadyCondition, Status: metav1.ConditionTrue,
		ObservedGeneration: node.Generation, Reason: "KernelDiscoverySucceeded",
		Message: "Kernel NVMe discovery completed successfully",
	}
	spdk := metav1.Condition{
		Type: spdkDiscoveryReadyCondition, Status: metav1.ConditionTrue,
		ObservedGeneration: node.Generation, Reason: "SPDKDiscoverySucceeded",
		Message: "SPDK NVMe discovery completed successfully",
	}
	if discoveryErr != nil {
		aggregate.Status = metav1.ConditionFalse
		aggregate.Reason = "NVMeDiscoveryDegraded"
		aggregate.Message = discoveryErr.Error()
		kernel.Status, spdk.Status = metav1.ConditionUnknown, metav1.ConditionUnknown
		kernel.Reason, spdk.Reason = "DiscoveryHealthUnknown", "DiscoveryHealthUnknown"
		kernel.Message, spdk.Message = discoveryErr.Error(), discoveryErr.Error()
		var sourceErr *NVMeDiscoveryError
		if errors.As(discoveryErr, &sourceErr) {
			if sourceErr.Kernel == nil {
				kernel.Status, kernel.Reason = metav1.ConditionTrue, "KernelDiscoverySucceeded"
				kernel.Message = "Kernel NVMe discovery completed successfully"
			} else {
				kernel.Status, kernel.Reason, kernel.Message = metav1.ConditionFalse, "KernelDiscoveryFailed", sourceErr.Kernel.Error()
			}
			if sourceErr.SPDK == nil {
				spdk.Status, spdk.Reason = metav1.ConditionTrue, "SPDKDiscoverySucceeded"
				spdk.Message = "SPDK NVMe discovery completed successfully"
			} else {
				spdk.Status, spdk.Reason, spdk.Message = metav1.ConditionFalse, "SPDKDiscoveryFailed", sourceErr.SPDK.Error()
			}
		}
	}
	meta.SetStatusCondition(&node.Status.Conditions, aggregate)
	meta.SetStatusCondition(&node.Status.Conditions, kernel)
	meta.SetStatusCondition(&node.Status.Conditions, spdk)
}

func (r *Reporter) reportDevices(ctx context.Context) (int64, int64, error) {
	logger := log.FromContext(ctx)
	var nodeTotalCap, nodeFreeCap int64

	devices, discoveryErr := r.discoverNVMe()
	if discoveryErr != nil {
		logger.Error(discoveryErr, "NVMe inventory discovery is degraded")
	}

	seen := make(map[string]struct{}, len(devices))
	for _, d := range devices {
		if err := validateHardwareNVMe(d); err != nil {
			discoveryErr = errors.Join(discoveryErr, fmt.Errorf("validate discovered NVMe device %q: %w", d.Name, err))
			logger.Error(err, "Ignoring NVMe device with unsafe metadata", "device", d.Name)
			continue
		}
		deviceName, err := deviceObjectName(r.NodeName, d.SerialNumber)
		if err != nil {
			discoveryErr = errors.Join(discoveryErr, err)
			logger.Error(err, "Unable to derive NVMeDevice object name", "device", d.Name)
			continue
		}
		seen[deviceName] = struct{}{}

		devCR := &storagev1alpha1.NVMeDevice{}
		err = r.Get(ctx, types.NamespacedName{Name: deviceName}, devCR)

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
				PCIAddress:    strings.ToLower(strings.TrimSpace(d.PCIAddress)),
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
			devCR.Spec.PCIAddress = strings.ToLower(strings.TrimSpace(d.PCIAddress))
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
		return nodeTotalCap, nodeFreeCap, errors.Join(discoveryErr, err)
	}
	if discoveryErr != nil {
		return nodeTotalCap, nodeFreeCap, discoveryErr
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

	return nodeTotalCap, nodeFreeCap, nil
}
