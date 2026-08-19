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
	"math"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/capacity"
)

func allocatedCapacityForDevice(partitions []storagev1alpha1.NVMePartition, serialNumber string) (int64, error) {
	usedCapacity := int64(0)
	for i := range partitions {
		partition := &partitions[i]
		if partition.Spec.ParentDeviceSerialNumber != serialNumber {
			continue
		}
		allocatedBytes, err := capacity.RoundUp(partition.Spec.Size.Value())
		if err != nil {
			return 0, fmt.Errorf("NVMePartition %s/%s has invalid capacity %q: %w",
				partition.Namespace, partition.Name, partition.Spec.Size.String(), err)
		}
		if usedCapacity > math.MaxInt64-allocatedBytes {
			return 0, fmt.Errorf("allocated capacity overflow for device serial %s", serialNumber)
		}
		usedCapacity += allocatedBytes
	}
	return usedCapacity, nil
}

// NVMeDeviceReconciler reconciles a NVMeDevice object
type NVMeDeviceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmedevices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmedevices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmedevices/finalizers,verbs=update
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmepartitions,verbs=get;list;watch

// Reconcile recalculates FreeCapacity on the NVMeDevice by deducting assigned NVMePartitions.
func (r *NVMeDeviceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	var device storagev1alpha1.NVMeDevice
	if err := r.Get(ctx, req.NamespacedName, &device); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Calculate free capacity by iterating over NVMePartitions assigned to this device
	var partitionList storagev1alpha1.NVMePartitionList
	if err := r.List(ctx, &partitionList); err != nil {
		logger.Error(err, "unable to list NVMePartitions for free capacity calculation")
		return ctrl.Result{}, err
	}

	totalCapacity := device.Spec.TotalCapacity.Value()
	usedCapacity, err := allocatedCapacityForDevice(partitionList.Items, device.Spec.SerialNumber)
	if err != nil {
		logger.Error(err, "Could not calculate NVMeDevice free capacity", "device", device.Name)
		return ctrl.Result{}, err
	}

	freeCapacity := max(totalCapacity-usedCapacity,
		// Should not happen if scheduling is correct
		0)

	// Update the device status only if it changed
	newFreeCapacity := *resource.NewQuantity(freeCapacity, resource.BinarySI)
	if device.Status.FreeCapacity.Cmp(newFreeCapacity) != 0 {
		base := device.DeepCopy()
		device.Status.FreeCapacity = newFreeCapacity
		if err := r.Status().Patch(ctx, &device, client.MergeFrom(base)); err != nil {
			logger.Error(err, "Failed to update NVMeDevice Status with FreeCapacity")
			return ctrl.Result{}, err
		}
		logger.Info("Updated NVMeDevice FreeCapacity", "device", device.Name, "newFreeCapacity", newFreeCapacity.String())
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NVMeDeviceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Map changes in NVMePartition to the corresponding NVMeDevice parent
	mapFn := func(ctx context.Context, obj client.Object) []reconcile.Request {
		partition, ok := obj.(*storagev1alpha1.NVMePartition)
		if !ok || partition.Spec.ParentDeviceSerialNumber == "" {
			return nil
		}
		var deviceList storagev1alpha1.NVMeDeviceList
		if err := r.List(ctx, &deviceList); err == nil {
			for _, dev := range deviceList.Items {
				if dev.Spec.SerialNumber == partition.Spec.ParentDeviceSerialNumber {
					return []reconcile.Request{
						{NamespacedName: types.NamespacedName{Name: dev.Name, Namespace: dev.Namespace}},
					}
				}
			}
		}
		return nil
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.NVMeDevice{}).
		Watches(
			&storagev1alpha1.NVMePartition{},
			handler.EnqueueRequestsFromMapFunc(mapFn),
		).
		Named("nvmedevice").
		Complete(r)
}
