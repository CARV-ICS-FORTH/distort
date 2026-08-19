/*
Copyright 2026, FORTH-ICS.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/capacity"
)

// NVMePartitionReconciler reconciles a NVMePartition object
type NVMePartitionReconciler struct {
	client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme

	// The manager deployment uses leader election, so one active reconciler owns
	// these locks. Each lock is held until its partition assignment is persisted.
	deviceLocks sync.Map
}

type placementCandidate struct {
	key       client.ObjectKey
	serial    string
	freeBytes int64
}

func (r *NVMePartitionReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *NVMePartitionReconciler) deviceLock(serialNumber string) *sync.Mutex {
	lock, _ := r.deviceLocks.LoadOrStore(serialNumber, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func requestedBackend(partition *storagev1alpha1.NVMePartition) string {
	if partition.Spec.TargetBackend == "" {
		return "spdk"
	}
	return partition.Spec.TargetBackend
}

func deviceCanHostPartition(device *storagev1alpha1.NVMeDevice, partition *storagev1alpha1.NVMePartition) bool {
	if device.Status.State != storagev1alpha1.NVMeDeviceStateClaimed || device.Status.ClaimRef == nil {
		return false
	}
	backend := requestedBackend(partition)
	return device.Status.ActiveBackend == "" || device.Status.ActiveBackend == backend
}

func availableCapacity(device *storagev1alpha1.NVMeDevice, partitions []storagev1alpha1.NVMePartition) (int64, error) {
	usedBytes, err := allocatedCapacityForDevice(partitions, device.Spec.SerialNumber)
	if err != nil {
		return 0, err
	}
	return max(device.Spec.TotalCapacity.Value()-usedBytes, 0), nil
}

func (r *NVMePartitionReconciler) placementCandidates(
	ctx context.Context,
	partition *storagev1alpha1.NVMePartition,
	requestedBytes int64,
) ([]placementCandidate, error) {
	reader := r.reader()
	var devices storagev1alpha1.NVMeDeviceList
	if err := reader.List(ctx, &devices); err != nil {
		return nil, err
	}
	var partitions storagev1alpha1.NVMePartitionList
	if err := reader.List(ctx, &partitions); err != nil {
		return nil, err
	}

	candidates := make([]placementCandidate, 0, len(devices.Items))
	for i := range devices.Items {
		device := &devices.Items[i]
		if !deviceCanHostPartition(device, partition) {
			continue
		}
		freeBytes, err := availableCapacity(device, partitions.Items)
		if err != nil {
			return nil, err
		}
		if freeBytes >= requestedBytes {
			candidates = append(candidates, placementCandidate{
				key:       client.ObjectKeyFromObject(device),
				serial:    device.Spec.SerialNumber,
				freeBytes: freeBytes,
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].freeBytes == candidates[j].freeBytes {
			return candidates[i].key.String() < candidates[j].key.String()
		}
		return candidates[i].freeBytes > candidates[j].freeBytes
	})
	return candidates, nil
}

func (r *NVMePartitionReconciler) reserveCandidate(
	ctx context.Context,
	partitionKey client.ObjectKey,
	candidate placementCandidate,
	requestedBytes int64,
) (bool, error) {
	lock := r.deviceLock(candidate.serial)
	lock.Lock()
	defer lock.Unlock()

	assigned := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		reader := r.reader()
		var partition storagev1alpha1.NVMePartition
		if err := reader.Get(ctx, partitionKey, &partition); err != nil {
			return client.IgnoreNotFound(err)
		}
		if partition.Spec.NodeName != "" || !partition.DeletionTimestamp.IsZero() {
			assigned = partition.Spec.NodeName != ""
			return nil
		}

		var device storagev1alpha1.NVMeDevice
		if err := reader.Get(ctx, candidate.key, &device); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if device.Spec.SerialNumber != candidate.serial || !deviceCanHostPartition(&device, &partition) {
			return nil
		}

		var partitions storagev1alpha1.NVMePartitionList
		if err := reader.List(ctx, &partitions); err != nil {
			return err
		}
		freeBytes, err := availableCapacity(&device, partitions.Items)
		if err != nil {
			return err
		}
		if freeBytes < requestedBytes {
			return nil
		}

		partition.Spec.NodeName = device.Spec.NodeName
		partition.Spec.ParentDeviceSerialNumber = device.Spec.SerialNumber
		partition.Spec.ClaimRef = &storagev1alpha1.NVMeDeviceClaimReference{
			Namespace: device.Status.ClaimRef.Namespace,
			Name:      device.Status.ClaimRef.Name,
			UID:       device.Status.ClaimRef.UID,
		}
		if err := r.Update(ctx, &partition); err != nil {
			return err
		}
		assigned = true
		return nil
	})
	return assigned, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *NVMePartitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.NVMePartition{}).
		Named("nvmepartition").
		Complete(r)
}

// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmepartitions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmepartitions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmepartitions/finalizers,verbs=update
// +kubebuilder:rbac:groups=storage.distort.io,resources=rdmastoragenodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmedevices,verbs=get;list;watch

// Reconcile assigns unassigned NVMePartitions to optimal RDMAStorageNodes based on available free capacity.
func (r *NVMePartitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	var partition storagev1alpha1.NVMePartition
	if err := r.Get(ctx, req.NamespacedName, &partition); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// We only care about partitions that haven't been assigned a node yet.
	if partition.Spec.NodeName != "" {
		return ctrl.Result{}, nil
	}

	logger.Info("Finding suitable NVMeDevice for NVMePartition", "partition", partition.Name, "requestedSize", partition.Spec.Size.String())
	requestedBytes, err := capacity.RoundUp(partition.Spec.Size.Value())
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid NVMePartition capacity %q: %w", partition.Spec.Size.String(), err)
	}
	candidates, err := r.placementCandidates(ctx, &partition, requestedBytes)
	if err != nil {
		logger.Error(err, "Unable to calculate fresh NVMeDevice capacity")
		return ctrl.Result{}, err
	}
	if len(candidates) == 0 {
		logger.Info("No suitable NVMeDevice found with enough free capacity", "partition", partition.Name)
		// Requeue periodically in case a new device is claimed or capacity frees up.
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	partitionKey := client.ObjectKeyFromObject(&partition)
	for _, candidate := range candidates {
		assigned, err := r.reserveCandidate(ctx, partitionKey, candidate, requestedBytes)
		if err != nil {
			logger.Error(err, "Unable to reserve NVMeDevice capacity", "partition", partition.Name, "device", candidate.serial)
			return ctrl.Result{}, err
		}
		if assigned {
			logger.Info("Assigned NVMePartition to NVMeDevice", "partition", partition.Name, "device", candidate.serial)
			return ctrl.Result{}, nil
		}
	}

	logger.Info("NVMeDevice capacity changed before reservation", "partition", partition.Name)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}
