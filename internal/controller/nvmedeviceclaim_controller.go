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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "distort/api/v1alpha1"
)

// NVMeDeviceClaimReconciler reconciles a NVMeDeviceClaim object
type NVMeDeviceClaimReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// SetupWithManager sets up the controller with the Manager.
func (r *NVMeDeviceClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.NVMeDeviceClaim{}).
		Named("nvmedeviceclaim").
		Complete(r)
}

// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmedeviceclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmedeviceclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmedeviceclaims/finalizers,verbs=update
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmedevices,verbs=get;list;watch;update;patch

// Reconcile matches NVMeDeviceClaims to NVMeDevices based on SerialNumber.
func (r *NVMeDeviceClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	var claim storagev1alpha1.NVMeDeviceClaim
	if err := r.Get(ctx, req.NamespacedName, &claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	finalizerName := "storage.distort.io/claim-cleanup"

	// Examine DeletionTimestamp to determine if object is under deletion
	if claim.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then lets add the finalizer and update the object.
		if !containsString(claim.GetFinalizers(), finalizerName) {
			claim.SetFinalizers(append(claim.GetFinalizers(), finalizerName))
			if err := r.Update(ctx, &claim); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		// The object is being deleted
		if containsString(claim.GetFinalizers(), finalizerName) {
			// Find the device and unclaim it
			if claim.Status.MatchedDevice != "" {
				var dev storagev1alpha1.NVMeDevice
				if err := r.Get(ctx, client.ObjectKey{Name: claim.Status.MatchedDevice, Namespace: claim.Namespace}, &dev); err == nil {
					dev.Status.State = storagev1alpha1.NVMeDeviceStateAvailable
					if err := r.Status().Update(ctx, &dev); err != nil {
						logger.Error(err, "unable to free NVMeDevice status", "device", dev.Name)
						return ctrl.Result{}, err
					}
					logger.Info("Successfully freed NVMeDevice", "device", dev.Name)
				}
			}

			// Remove our finalizer from the list and update it.
			claim.SetFinalizers(removeString(claim.GetFinalizers(), finalizerName))
			if err := r.Update(ctx, &claim); err != nil {
				return ctrl.Result{}, err
			}
		}
		// Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}

	// If already bound to a device, nothing to do (simplified).
	if claim.Status.Active && claim.Status.MatchedDevice != "" {
		return ctrl.Result{}, nil
	}

	// Find the device with the matching serial number
	var deviceList storagev1alpha1.NVMeDeviceList
	if err := r.List(ctx, &deviceList); err != nil {
		logger.Error(err, "unable to list NVMeDevices")
		return ctrl.Result{}, err
	}

	for _, dev := range deviceList.Items {
		if dev.Spec.SerialNumber == claim.Spec.SerialNumber {
			// Found a match
			if dev.Status.State == storagev1alpha1.NVMeDeviceStateClaimed && dev.Name != claim.Status.MatchedDevice {
				// The device is claimed by someone else
				logger.Info("Target device is already claimed by another resource", "device", dev.Name)
				continue
			}

			// Transition device to Claimed
			dev.Status.State = storagev1alpha1.NVMeDeviceStateClaimed
			if err := r.Status().Update(ctx, &dev); err != nil {
				logger.Error(err, "unable to update NVMeDevice status", "device", dev.Name)
				return ctrl.Result{}, err
			}

			// Update Claim Status
			claim.Status.Active = true
			claim.Status.MatchedDevice = dev.Name
			claim.Status.NodeName = dev.Spec.NodeName
			if err := r.Status().Update(ctx, &claim); err != nil {
				logger.Error(err, "unable to update NVMeDeviceClaim status")
				return ctrl.Result{}, err
			}

			logger.Info("Successfully bound NVMeDeviceClaim to NVMeDevice", "device", dev.Name)
			return ctrl.Result{}, nil
		}
	}

	logger.Info("No matching NVMeDevice found for serial number", "serial", claim.Spec.SerialNumber)
	// Optionally requeue if we expect devices to appear later
	return ctrl.Result{}, nil
}

// Helper functions to check and remove string from a slice of strings.
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}
