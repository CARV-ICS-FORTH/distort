package agent

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "distort/api/v1alpha1"
)

const partitionFinalizer = "storage.distort.io/partition-cleanup"

// PartitionManager watches for NVMePartitions assigned to this node and acts on them.
type PartitionManager struct {
	client.Client
	NodeName string
}

// Reconcile handles partition configuration when an admin/controller assigns them to this node.
func (p *PartitionManager) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var partition storagev1alpha1.NVMePartition
	if err := p.Get(ctx, req.NamespacedName, &partition); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Double check we only process partitions for our own node
	if partition.Spec.NodeName != p.NodeName {
		return ctrl.Result{}, nil
	}

	isMarkedToBeDeleted := partition.GetDeletionTimestamp() != nil
	if isMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(&partition, partitionFinalizer) {
			logger.Info("Cleaning up NVMePartition", "partition", partition.Name)

			// 1. Unexport the NVMe-oF target
			if partition.Status.NQN != "" {
				if err := UnexportNVMeTarget(partition.Status.NQN, 1); err != nil {
					logger.Error(err, "Failed to unexport NVMe target")
					// In theory we might return err to retry, but let's press on for teardown
				}
			}

			// 2. Remove the partition from the block device
			// Mocking parent device + partition number.
			pw := NewPartedWrapper("/dev/nvme0n1")
			if err := pw.RemovePartition(1); err != nil {
				logger.Error(err, "Failed to remove partition via parted")
			}

			// Remove the finalizer so Kubernetes can delete the object
			controllerutil.RemoveFinalizer(&partition, partitionFinalizer)
			if err := p.Update(ctx, &partition); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present
	if !controllerutil.ContainsFinalizer(&partition, partitionFinalizer) {
		controllerutil.AddFinalizer(&partition, partitionFinalizer)
		if err := p.Update(ctx, &partition); err != nil {
			return ctrl.Result{}, err
		}
	}

	// If it's already exported or failed, do nothing for now (simplified)
	if partition.Status.State == storagev1alpha1.NVMePartitionStateExported || partition.Status.State == storagev1alpha1.NVMePartitionStateFailed {
		return ctrl.Result{}, nil
	}

	logger.Info("Provisioning partition on local node", "partition", partition.Name, "size", partition.Spec.Size.String())

	// ============================================
	// 1. Execute `parted` to create the slice.
	// ============================================
	parentDevice := "/dev/nvme0n1" // TODO: Select appropriately from matched NVMeDevice
	pw := NewPartedWrapper(parentDevice)

	// Ensure label exists just in case (ignores error if already exists, but we can do a fallback)
	_ = pw.MakeLabel()

	// In a real system you would calculate available start sectors.
	startMB := int64(1)
	endMB := startMB + partition.Spec.Size.Value()/(1024*1024)

	blockPath, err := pw.CreatePartition(partition.Name, startMB, endMB)
	if err != nil {
		logger.Error(err, "Failed to create block partition")
		partition.Status.State = storagev1alpha1.NVMePartitionStateFailed
		_ = p.Status().Update(ctx, &partition)
		return ctrl.Result{}, err
	}

	// ============================================
	// 2. Configure `nvmetcli` (configfs) to export via RDMA.
	// ============================================
	nqn := "nqn.2026-02.io.distort:volume-" + partition.Name

	// Fetch real K8s Node IP for the export portal
	portalIP := "127.0.0.1"
	k8sNode := &corev1.Node{}
	if err := p.Get(ctx, types.NamespacedName{Name: p.NodeName}, k8sNode); err == nil {
		for _, addr := range k8sNode.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				portalIP = addr.Address
				break
			}
		}
	} else {
		logger.Error(err, "Failed to fetch node IP for NVMe-oF portal")
	}

	portalPort := 4420
	portID := 1

	if err := ExportNVMeTarget(nqn, blockPath, portalIP, portID, portalPort); err != nil {
		logger.Error(err, "Failed to export NVMe-oF target")
		partition.Status.State = storagev1alpha1.NVMePartitionStateFailed
		_ = p.Status().Update(ctx, &partition)
		return ctrl.Result{}, err
	}

	// ============================================
	// 3. Update the CRD Status
	// ============================================
	partition.Status.State = storagev1alpha1.NVMePartitionStateExported
	partition.Status.NQN = nqn
	partition.Status.PortalIP = portalIP
	partition.Status.PortalPort = portalPort

	if err := p.Status().Update(ctx, &partition); err != nil {
		logger.Error(err, "Failed to update partition status")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully provisioned and exported partition", "partition", partition.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (p *PartitionManager) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.NVMePartition{}).
		Named("agent-partition-manager").
		Complete(p)
}
