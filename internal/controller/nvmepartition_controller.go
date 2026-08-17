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
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "distort/api/v1alpha1"
)

// NVMePartitionReconciler reconciles a NVMePartition object
type NVMePartitionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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

	// List all NVMeDevices
	var deviceList storagev1alpha1.NVMeDeviceList
	if err := r.List(ctx, &deviceList); err != nil {
		logger.Error(err, "unable to list NVMeDevices")
		return ctrl.Result{}, err
	}

	var bestDevice *storagev1alpha1.NVMeDevice
	var maxFree int64 = -1

	for i := range deviceList.Items {
		device := &deviceList.Items[i]

		// Only a device with a persisted, immutable owner identity can authorize
		// an allocation. The agent independently verifies the live claim again.
		if device.Status.State != storagev1alpha1.NVMeDeviceStateClaimed || device.Status.ClaimRef == nil {
			continue
		}

		// Ensure target backend matches
		requestedBackend := partition.Spec.TargetBackend
		if requestedBackend == "" {
			requestedBackend = "spdk"
		}
		if device.Status.ActiveBackend != "" && device.Status.ActiveBackend != requestedBackend {
			continue
		}

		// Ensure the device has enough free capacity
		if device.Status.FreeCapacity.Cmp(partition.Spec.Size) >= 0 {
			freeBytes := device.Status.FreeCapacity.Value()
			// Simple strategy: pick the device with the most free capacity
			if freeBytes > maxFree {
				maxFree = freeBytes
				bestDevice = device
			}
		}
	}

	if bestDevice == nil {
		logger.Info("No suitable NVMeDevice found with enough free capacity", "partition", partition.Name)
		// Requeue periodically in case a new device is claimed or capacity frees up.
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	logger.Info("Assigning NVMePartition to NVMeDevice", "partition", partition.Name, "node", bestDevice.Spec.NodeName, "device", bestDevice.Spec.SerialNumber)

	partition.Spec.NodeName = bestDevice.Spec.NodeName
	partition.Spec.ParentDeviceSerialNumber = bestDevice.Spec.SerialNumber
	partition.Spec.ClaimRef = &storagev1alpha1.NVMeDeviceClaimReference{
		Namespace: bestDevice.Status.ClaimRef.Namespace,
		Name:      bestDevice.Status.ClaimRef.Name,
		UID:       bestDevice.Status.ClaimRef.UID,
	}
	if err := r.Update(ctx, &partition); err != nil {
		logger.Error(err, "unable to update NVMePartition with selected node and device", "partition", partition.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}
