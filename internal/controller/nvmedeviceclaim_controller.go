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
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "distort/api/v1alpha1"
)

const (
	claimFinalizer       = "storage.distort.io/claim-cleanup"
	claimBoundCondition  = "Bound"
	deviceReadyCondition = "HardwareAvailable"
	claimRequeueInterval = 30 * time.Second
	claimDeleteInterval  = 5 * time.Second
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
		Watches(&storagev1alpha1.NVMeDevice{}, handler.EnqueueRequestsFromMapFunc(r.claimsForDevice)).
		Watches(&storagev1alpha1.NVMePartition{}, handler.EnqueueRequestsFromMapFunc(r.claimForPartition)).
		Named("nvmedeviceclaim").
		Complete(r)
}

// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmedeviceclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmedeviceclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmedeviceclaims/finalizers,verbs=update
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmedevices,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmepartitions,verbs=get;list;watch

func (r *NVMeDeviceClaimReconciler) claimsForDevice(ctx context.Context, object client.Object) []reconcile.Request {
	device, ok := object.(*storagev1alpha1.NVMeDevice)
	if !ok || device.Spec.SerialNumber == "" {
		return nil
	}
	var claims storagev1alpha1.NVMeDeviceClaimList
	if err := r.List(ctx, &claims); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for i := range claims.Items {
		if strings.EqualFold(claims.Items[i].Spec.SerialNumber, device.Spec.SerialNumber) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&claims.Items[i])})
		}
	}
	return requests
}

func (r *NVMeDeviceClaimReconciler) claimForPartition(_ context.Context, object client.Object) []reconcile.Request {
	partition, ok := object.(*storagev1alpha1.NVMePartition)
	if !ok || partition.Spec.ClaimRef == nil {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Namespace: partition.Spec.ClaimRef.Namespace,
		Name:      partition.Spec.ClaimRef.Name,
	}}}
}

func deviceIsPresent(device *storagev1alpha1.NVMeDevice) bool {
	if device.Status.State == storagev1alpha1.NVMeDeviceStateUnavailable {
		return false
	}
	condition := meta.FindStatusCondition(device.Status.Conditions, deviceReadyCondition)
	return condition == nil || condition.Status != metav1.ConditionFalse
}

func claimOwnsDevice(claim *storagev1alpha1.NVMeDeviceClaim, device *storagev1alpha1.NVMeDevice) bool {
	return device.Status.ClaimRef != nil &&
		device.Status.ClaimRef.Namespace == claim.Namespace &&
		device.Status.ClaimRef.Name == claim.Name &&
		device.Status.ClaimRef.UID == claim.UID
}

func (r *NVMeDeviceClaimReconciler) updateClaimStatus(
	ctx context.Context, claim *storagev1alpha1.NVMeDeviceClaim, active bool, device, node string,
	conditionStatus metav1.ConditionStatus, reason, message string,
) error {
	var latest storagev1alpha1.NVMeDeviceClaim
	if err := r.Get(ctx, client.ObjectKeyFromObject(claim), &latest); err != nil {
		return err
	}
	base := latest.DeepCopy()
	latest.Status.Active = active
	latest.Status.MatchedDevice = device
	latest.Status.NodeName = node
	meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
		Type: claimBoundCondition, Status: conditionStatus, ObservedGeneration: latest.Generation,
		Reason: reason, Message: message,
	})
	return r.Status().Patch(ctx, &latest, client.MergeFrom(base))
}

func (r *NVMeDeviceClaimReconciler) releaseDevice(ctx context.Context, claim *storagev1alpha1.NVMeDeviceClaim, device *storagev1alpha1.NVMeDevice) error {
	if !claimOwnsDevice(claim, device) {
		return nil
	}
	base := device.DeepCopy()
	device.Status.ClaimRef = nil
	if deviceIsPresent(device) {
		device.Status.State = storagev1alpha1.NVMeDeviceStateAvailable
	} else {
		device.Status.State = storagev1alpha1.NVMeDeviceStateUnavailable
	}
	return r.Status().Patch(ctx, device, client.MergeFrom(base))
}

func (r *NVMeDeviceClaimReconciler) finalizeClaim(ctx context.Context, claim *storagev1alpha1.NVMeDeviceClaim) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)
	if !controllerutil.ContainsFinalizer(claim, claimFinalizer) {
		return ctrl.Result{}, nil
	}
	var partitions storagev1alpha1.NVMePartitionList
	if err := r.List(ctx, &partitions); err != nil {
		return ctrl.Result{}, err
	}
	for i := range partitions.Items {
		ref := partitions.Items[i].Spec.ClaimRef
		if ref != nil && ref.UID == claim.UID {
			logger.Info("Waiting for dependent NVMePartition deletion", "partition", client.ObjectKeyFromObject(&partitions.Items[i]))
			return ctrl.Result{RequeueAfter: claimDeleteInterval}, nil
		}
	}
	if claim.Status.MatchedDevice != "" {
		var device storagev1alpha1.NVMeDevice
		if err := r.Get(ctx, client.ObjectKey{Name: claim.Status.MatchedDevice}, &device); err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		} else if err := r.releaseDevice(ctx, claim, &device); err != nil {
			logger.Error(err, "Unable to free NVMeDevice status", "device", device.Name)
			return ctrl.Result{}, err
		}
	}
	// Release additional exact-UID ownership left by an interrupted move.
	var devices storagev1alpha1.NVMeDeviceList
	if err := r.List(ctx, &devices); err != nil {
		return ctrl.Result{}, err
	}
	for i := range devices.Items {
		device := &devices.Items[i]
		if device.Name != claim.Status.MatchedDevice && claimOwnsDevice(claim, device) {
			if err := r.releaseDevice(ctx, claim, device); err != nil {
				return ctrl.Result{}, err
			}
		}
	}
	base := claim.DeepCopy()
	controllerutil.RemoveFinalizer(claim, claimFinalizer)
	return ctrl.Result{}, r.Patch(ctx, claim, client.MergeFrom(base))
}

func (r *NVMeDeviceClaimReconciler) reconcileClaimBinding(ctx context.Context, claim *storagev1alpha1.NVMeDeviceClaim) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)
	var deviceList storagev1alpha1.NVMeDeviceList
	if err := r.List(ctx, &deviceList); err != nil {
		logger.Error(err, "Unable to list NVMeDevices")
		return ctrl.Result{}, err
	}

	matching := make([]*storagev1alpha1.NVMeDevice, 0)
	present := make([]*storagev1alpha1.NVMeDevice, 0)
	for i := range deviceList.Items {
		dev := &deviceList.Items[i]
		if strings.EqualFold(dev.Spec.SerialNumber, claim.Spec.SerialNumber) {
			matching = append(matching, dev)
			if deviceIsPresent(dev) {
				present = append(present, dev)
			}
		}
	}

	if len(present) == 0 {
		logger.Info("No available matching NVMeDevice found", "serial", claim.Spec.SerialNumber)
		if err := r.updateClaimStatus(ctx, claim, false, claim.Status.MatchedDevice, claim.Status.NodeName,
			metav1.ConditionFalse, "DeviceUnavailable", "No currently available NVMeDevice has the requested serial number"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: claimRequeueInterval}, nil
	}
	if len(present) > 1 {
		logger.Info("Multiple available NVMeDevices have the requested serial number", "serial", claim.Spec.SerialNumber, "count", len(present))
		if err := r.updateClaimStatus(ctx, claim, false, claim.Status.MatchedDevice, claim.Status.NodeName,
			metav1.ConditionFalse, "AmbiguousDevices", "Multiple available NVMeDevices have the requested serial number"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: claimRequeueInterval}, nil
	}

	device := present[0]
	legacyMatch := device.Status.ClaimRef == nil && claim.Status.Active && claim.Status.MatchedDevice == device.Name
	ownedByAnotherClaim := device.Status.ClaimRef != nil && !claimOwnsDevice(claim, device)
	legacyOwnershipConflict := device.Status.State == storagev1alpha1.NVMeDeviceStateClaimed && !claimOwnsDevice(claim, device) && !legacyMatch
	if ownedByAnotherClaim || legacyOwnershipConflict {
		if err := r.updateClaimStatus(ctx, claim, false, claim.Status.MatchedDevice, claim.Status.NodeName,
			metav1.ConditionFalse, "OwnedByAnotherClaim", "The matching NVMeDevice is owned by another claim"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: claimRequeueInterval}, nil
	}

	base := device.DeepCopy()
	device.Status.State = storagev1alpha1.NVMeDeviceStateClaimed
	device.Status.ClaimRef = &storagev1alpha1.NVMeDeviceClaimReference{Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID}
	if err := r.Status().Patch(ctx, device, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, err
	}
	for _, old := range matching {
		if old.Name != device.Name && claimOwnsDevice(claim, old) {
			if err := r.releaseDevice(ctx, claim, old); err != nil {
				return ctrl.Result{}, err
			}
		}
	}
	if err := r.updateClaimStatus(ctx, claim, true, device.Name, device.Spec.NodeName,
		metav1.ConditionTrue, "DeviceBound", "The claim is bound to an available NVMeDevice"); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("Successfully bound NVMeDeviceClaim to NVMeDevice", "device", device.Name)
	return ctrl.Result{RequeueAfter: claimRequeueInterval}, nil
}

// Reconcile matches NVMeDeviceClaims to NVMeDevices based on SerialNumber.
func (r *NVMeDeviceClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var claim storagev1alpha1.NVMeDeviceClaim
	if err := r.Get(ctx, req.NamespacedName, &claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !claim.DeletionTimestamp.IsZero() {
		return r.finalizeClaim(ctx, &claim)
	}
	if !controllerutil.ContainsFinalizer(&claim, claimFinalizer) {
		controllerutil.AddFinalizer(&claim, claimFinalizer)
		if err := r.Update(ctx, &claim); err != nil {
			return ctrl.Result{}, err
		}
	}
	return r.reconcileClaimBinding(ctx, &claim)
}
