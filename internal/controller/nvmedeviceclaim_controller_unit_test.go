package controller

import (
	"context"
	"errors"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "distort/api/v1alpha1"
)

func deletingClaimFixture() (*storagev1alpha1.NVMeDeviceClaim, *storagev1alpha1.NVMeDevice) {
	now := metav1.Now()
	claim := &storagev1alpha1.NVMeDeviceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "deleting-claim", Namespace: "default", UID: types.UID("claim-uid"),
			Finalizers: []string{claimFinalizer}, DeletionTimestamp: &now,
		},
		Spec:   storagev1alpha1.NVMeDeviceClaimSpec{SerialNumber: "SERIAL-1"},
		Status: storagev1alpha1.NVMeDeviceClaimStatus{Active: true, MatchedDevice: "device-1", NodeName: "node-a"},
	}
	device := &storagev1alpha1.NVMeDevice{
		ObjectMeta: metav1.ObjectMeta{Name: claim.Status.MatchedDevice},
		Spec:       storagev1alpha1.NVMeDeviceSpec{NodeName: "node-a", SerialNumber: claim.Spec.SerialNumber},
		Status: storagev1alpha1.NVMeDeviceStatus{
			State: storagev1alpha1.NVMeDeviceStateClaimed,
			ClaimRef: &storagev1alpha1.NVMeDeviceClaimReference{
				Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID,
			},
		},
	}
	return claim, device
}

func claimDeletionTestClient(t *testing.T, funcs interceptor.Funcs, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
		WithStatusSubresource(&storagev1alpha1.NVMeDeviceClaim{}, &storagev1alpha1.NVMeDevice{}).
		WithInterceptorFuncs(funcs).Build()
}

func TestClaimDeletionRetainsFinalizerOnDeviceReadErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "forbidden", err: apierrors.NewForbidden(schema.GroupResource{Group: storagev1alpha1.GroupVersion.Group, Resource: "nvmedevices"}, "device-1", errors.New("denied"))},
		{name: "timeout", err: apierrors.NewTimeoutError("temporary API timeout", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			claim, device := deletingClaimFixture()
			cli := claimDeletionTestClient(t, interceptor.Funcs{
				Get: func(ctx context.Context, delegated client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
					if _, ok := object.(*storagev1alpha1.NVMeDevice); ok {
						return test.err
					}
					return delegated.Get(ctx, key, object, opts...)
				},
			}, claim, device)
			reconciler := &NVMeDeviceClaimReconciler{Client: cli, Scheme: cli.Scheme()}
			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(claim)})
			if err == nil {
				t.Fatalf("device %s error was ignored", test.name)
			}
			var actual storagev1alpha1.NVMeDeviceClaim
			if err := cli.Get(context.Background(), client.ObjectKeyFromObject(claim), &actual); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(actual.Finalizers, claimFinalizer) {
				t.Fatal("claim finalizer was removed after a retryable device read error")
			}
		})
	}
}

func TestClaimDeletionRetainsFinalizerOnDeviceStatusConflict(t *testing.T) {
	claim, device := deletingClaimFixture()
	cli := claimDeletionTestClient(t, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, delegated client.Client, subresource string, object client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if subresource == "status" {
				if _, ok := object.(*storagev1alpha1.NVMeDevice); ok {
					return apierrors.NewConflict(schema.GroupResource{Group: storagev1alpha1.GroupVersion.Group, Resource: "nvmedevices"}, object.GetName(), errors.New("conflict"))
				}
			}
			return delegated.SubResource(subresource).Patch(ctx, object, patch, opts...)
		},
	}, claim, device)
	reconciler := &NVMeDeviceClaimReconciler{Client: cli, Scheme: cli.Scheme()}
	_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(claim)})
	if !apierrors.IsConflict(err) {
		t.Fatalf("status conflict = %v, want Conflict", err)
	}
	var actual storagev1alpha1.NVMeDeviceClaim
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(claim), &actual); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(actual.Finalizers, claimFinalizer) {
		t.Fatal("claim finalizer was removed after a device status conflict")
	}
}

func TestClaimDeletionAcceptsAnAlreadyReleasedDevice(t *testing.T) {
	claim, device := deletingClaimFixture()
	device.Status.State = storagev1alpha1.NVMeDeviceStateAvailable
	device.Status.ClaimRef = nil
	cli := claimDeletionTestClient(t, interceptor.Funcs{}, claim, device)
	reconciler := &NVMeDeviceClaimReconciler{Client: cli, Scheme: cli.Scheme()}
	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(claim)}); err != nil {
		t.Fatal(err)
	}
	var actualDevice storagev1alpha1.NVMeDevice
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(device), &actualDevice); err != nil {
		t.Fatal(err)
	}
	if actualDevice.Status.State != storagev1alpha1.NVMeDeviceStateAvailable || actualDevice.Status.ClaimRef != nil {
		t.Fatalf("already released device was mutated: %#v", actualDevice.Status)
	}
	var actualClaim storagev1alpha1.NVMeDeviceClaim
	err := cli.Get(context.Background(), client.ObjectKeyFromObject(claim), &actualClaim)
	if err == nil && slices.Contains(actualClaim.Finalizers, claimFinalizer) {
		t.Fatal("claim finalizer remained after confirming the device was already released")
	}
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
}
