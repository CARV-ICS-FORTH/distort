package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/agent/plugins"
	"distort/test/knownfailure"
)

type countingBackend struct {
	setupCalls atomic.Int32
}

func (b *countingBackend) Name() string { return "claim-counting-backend" }
func (b *countingBackend) SetupDevice(context.Context, string, string, map[string]string) error {
	b.setupCalls.Add(1)
	return nil
}
func (b *countingBackend) ExportVolume(context.Context, string, string, string, int, map[string]string) (string, error) {
	return "nqn.test", nil
}
func (b *countingBackend) UnexportVolume(context.Context, string) error { return nil }

type countingVolumeManager struct {
	setupCalls atomic.Int32
}

func (m *countingVolumeManager) Name() string { return "claim-counting-manager" }
func (m *countingVolumeManager) SetupStorage(context.Context, string, string) error {
	m.setupCalls.Add(1)
	return nil
}
func (m *countingVolumeManager) CreateVolume(context.Context, string, string, string, int64) (plugins.VolumeIdentity, error) {
	return plugins.VolumeIdentity{BackendVolumeID: "/dev/test", CapacityBytes: 1024 * 1024 * 1024}, nil
}
func (m *countingVolumeManager) DeleteVolume(context.Context, string, string, string, plugins.VolumeIdentity) error {
	return nil
}

type namedTestBackend struct {
	name          string
	unexportCalls atomic.Int32
}

func (b *namedTestBackend) Name() string { return b.name }
func (b *namedTestBackend) SetupDevice(context.Context, string, string, map[string]string) error {
	return nil
}
func (b *namedTestBackend) ExportVolume(context.Context, string, string, string, int, map[string]string) (string, error) {
	return "nqn.test", nil
}
func (b *namedTestBackend) UnexportVolume(context.Context, string) error {
	b.unexportCalls.Add(1)
	return nil
}

type recordingDeleteManager struct {
	devicePath      string
	deviceName      string
	volumeName      string
	backendVolumeID string
	identity        plugins.VolumeIdentity
}

type retryDeleteManager struct {
	calls atomic.Int32
}

func (m *retryDeleteManager) Name() string { return "retry-delete-manager" }
func (m *retryDeleteManager) SetupStorage(context.Context, string, string) error {
	return nil
}
func (m *retryDeleteManager) CreateVolume(context.Context, string, string, string, int64) (plugins.VolumeIdentity, error) {
	return plugins.VolumeIdentity{}, nil
}
func (m *retryDeleteManager) DeleteVolume(context.Context, string, string, string, plugins.VolumeIdentity) error {
	if m.calls.Add(1) == 1 {
		return errors.New("simulated crash after unexport")
	}
	return nil
}

func (m *recordingDeleteManager) Name() string { return "teardown-recording-manager" }
func (m *recordingDeleteManager) SetupStorage(context.Context, string, string) error {
	return nil
}
func (m *recordingDeleteManager) CreateVolume(context.Context, string, string, string, int64) (plugins.VolumeIdentity, error) {
	return plugins.VolumeIdentity{}, nil
}
func (m *recordingDeleteManager) DeleteVolume(_ context.Context, devicePath, deviceName, volumeName string, identity plugins.VolumeIdentity) error {
	m.devicePath = devicePath
	m.deviceName = deviceName
	m.volumeName = volumeName
	m.backendVolumeID = identity.BackendVolumeID
	m.identity = identity
	return nil
}

func TestPartitionIdentityUsesUIDAndPreservesLegacyExports(t *testing.T) {
	first := &storagev1alpha1.NVMePartition{ObjectMeta: metav1.ObjectMeta{
		Namespace: "team-a", Name: "same-name", UID: types.UID("11111111-1111-1111-1111-111111111111"),
	}}
	second := &storagev1alpha1.NVMePartition{ObjectMeta: metav1.ObjectMeta{
		Namespace: "team-b", Name: "same-name", UID: types.UID("22222222-2222-2222-2222-222222222222"),
	}}
	firstExternalID, firstVolumeID := identitiesForPartition(first)
	secondExternalID, secondVolumeID := identitiesForPartition(second)
	if firstExternalID == secondExternalID || firstVolumeID == secondVolumeID {
		t.Fatalf("same-named partitions received colliding identities: %q/%q and %q/%q", firstExternalID, firstVolumeID, secondExternalID, secondVolumeID)
	}

	legacy := first.DeepCopy()
	legacy.Status.NQN = "nqn.2026-02.io.distort:volume-same-name"
	legacyExternalID, _ := identitiesForPartition(legacy)
	if legacyExternalID != "same-name" {
		t.Fatalf("legacy external ID = %q, want same-name", legacyExternalID)
	}
}

func newPartitionManagerClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	testScheme := runtime.NewScheme()
	if err := corev1.AddToScheme(testScheme); err != nil {
		t.Fatal(err)
	}
	if err := storagev1alpha1.AddToScheme(testScheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(testScheme).
		WithStatusSubresource(&storagev1alpha1.NVMeDevice{}, &storagev1alpha1.NVMePartition{}).
		WithObjects(objects...).Build()
}

func TestPartitionManagerRejectsUnclaimedDeviceBeforePluginCalls(t *testing.T) {
	backend := &countingBackend{}
	manager := &countingVolumeManager{}
	plugins.RegisterTargetBackend(backend)
	plugins.RegisterVolumeManager(manager)

	device := &storagev1alpha1.NVMeDevice{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a-unclaimed-serial"},
		Spec: storagev1alpha1.NVMeDeviceSpec{
			NodeName:      "node-a",
			PCIAddress:    "0000:01:00.0",
			SerialNumber:  "UNCLAIMED-SERIAL",
			TotalCapacity: resource.MustParse("1Gi"),
		},
		Status: storagev1alpha1.NVMeDeviceStatus{State: storagev1alpha1.NVMeDeviceStateAvailable},
	}
	partition := &storagev1alpha1.NVMePartition{
		ObjectMeta: metav1.ObjectMeta{Name: "unclaimed-volume", Namespace: "default"},
		Spec: storagev1alpha1.NVMePartitionSpec{
			Size:                     resource.MustParse("100Mi"),
			NodeName:                 "node-a",
			ParentDeviceSerialNumber: "UNCLAIMED-SERIAL",
			TargetBackend:            backend.Name(),
			VolumeManager:            manager.Name(),
		},
	}
	testClient := newPartitionManagerClient(t, device, partition)
	reconciler := &PartitionManager{Client: testClient, NodeName: "node-a"}
	_, _ = reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: partition.Name, Namespace: partition.Namespace},
	})

	if calls := backend.setupCalls.Load(); calls != 0 {
		t.Fatalf("backend SetupDevice was called %d times for an unclaimed device", calls)
	}
	if calls := manager.setupCalls.Load(); calls != 0 {
		t.Fatalf("volume manager SetupStorage was called %d times for an unclaimed device", calls)
	}
	var actual storagev1alpha1.NVMePartition
	if err := testClient.Get(context.Background(), types.NamespacedName{Name: partition.Name, Namespace: partition.Namespace}, &actual); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(actual.Status.Conditions, claimAuthorizationCondition)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "ClaimOwnershipInvalid" {
		t.Fatalf("authorization condition = %#v, want False/ClaimOwnershipInvalid", condition)
	}
}

func TestPartitionManagerRejectsMismatchedClaimUIDBeforePluginCalls(t *testing.T) {
	backend := &countingBackend{}
	manager := &countingVolumeManager{}
	plugins.RegisterTargetBackend(backend)
	plugins.RegisterVolumeManager(manager)

	claim := &storagev1alpha1.NVMeDeviceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "device-owner", Namespace: "default", UID: types.UID("claim-uid")},
		Spec:       storagev1alpha1.NVMeDeviceClaimSpec{SerialNumber: "CLAIMED-SERIAL"},
		Status: storagev1alpha1.NVMeDeviceClaimStatus{
			Active:        true,
			MatchedDevice: "node-a-claimed-serial",
			NodeName:      "node-a",
		},
	}
	deviceClaimRef := &storagev1alpha1.NVMeDeviceClaimReference{
		Namespace: claim.Namespace,
		Name:      claim.Name,
		UID:       claim.UID,
	}
	device := &storagev1alpha1.NVMeDevice{
		ObjectMeta: metav1.ObjectMeta{Name: claim.Status.MatchedDevice},
		Spec: storagev1alpha1.NVMeDeviceSpec{
			NodeName:      "node-a",
			PCIAddress:    "0000:01:00.0",
			SerialNumber:  claim.Spec.SerialNumber,
			TotalCapacity: resource.MustParse("1Gi"),
		},
		Status: storagev1alpha1.NVMeDeviceStatus{
			State:    storagev1alpha1.NVMeDeviceStateClaimed,
			ClaimRef: deviceClaimRef,
		},
	}
	partition := &storagev1alpha1.NVMePartition{
		ObjectMeta: metav1.ObjectMeta{Name: "wrong-owner", Namespace: "default"},
		Spec: storagev1alpha1.NVMePartitionSpec{
			Size:                     resource.MustParse("100Mi"),
			NodeName:                 "node-a",
			ParentDeviceSerialNumber: claim.Spec.SerialNumber,
			ClaimRef: &storagev1alpha1.NVMeDeviceClaimReference{
				Namespace: claim.Namespace,
				Name:      claim.Name,
				UID:       types.UID("replacement-claim-uid"),
			},
			TargetBackend: backend.Name(),
			VolumeManager: manager.Name(),
		},
	}
	testClient := newPartitionManagerClient(t, claim, device, partition)
	reconciler := &PartitionManager{Client: testClient, NodeName: "node-a"}
	_, _ = reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: partition.Name, Namespace: partition.Namespace},
	})

	if calls := backend.setupCalls.Load(); calls != 0 {
		t.Fatalf("backend SetupDevice was called %d times for a mismatched claim UID", calls)
	}
	if calls := manager.setupCalls.Load(); calls != 0 {
		t.Fatalf("volume manager SetupStorage was called %d times for a mismatched claim UID", calls)
	}
}

func TestPartitionManagerAcceptsMatchingLiveClaim(t *testing.T) {
	backend := &countingBackend{}
	manager := &countingVolumeManager{}
	plugins.RegisterTargetBackend(backend)
	plugins.RegisterVolumeManager(manager)

	claim := &storagev1alpha1.NVMeDeviceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "device-owner", Namespace: "default", UID: types.UID("claim-uid")},
		Spec:       storagev1alpha1.NVMeDeviceClaimSpec{SerialNumber: "CLAIMED-SERIAL"},
		Status: storagev1alpha1.NVMeDeviceClaimStatus{
			Active:        true,
			MatchedDevice: "node-a-claimed-serial",
			NodeName:      "node-a",
		},
	}
	claimRef := &storagev1alpha1.NVMeDeviceClaimReference{
		Namespace: claim.Namespace,
		Name:      claim.Name,
		UID:       claim.UID,
	}
	device := &storagev1alpha1.NVMeDevice{
		ObjectMeta: metav1.ObjectMeta{Name: claim.Status.MatchedDevice},
		Spec: storagev1alpha1.NVMeDeviceSpec{
			NodeName:      "node-a",
			PCIAddress:    "0000:01:00.0",
			SerialNumber:  claim.Spec.SerialNumber,
			TotalCapacity: resource.MustParse("1Gi"),
		},
		Status: storagev1alpha1.NVMeDeviceStatus{
			State:    storagev1alpha1.NVMeDeviceStateClaimed,
			ClaimRef: claimRef,
		},
	}
	partition := &storagev1alpha1.NVMePartition{
		ObjectMeta: metav1.ObjectMeta{Name: "authorized-volume", Namespace: "default"},
		Spec: storagev1alpha1.NVMePartitionSpec{
			Size:                     resource.MustParse("100Mi"),
			NodeName:                 "node-a",
			ParentDeviceSerialNumber: claim.Spec.SerialNumber,
			ClaimRef: &storagev1alpha1.NVMeDeviceClaimReference{
				Namespace: claimRef.Namespace,
				Name:      claimRef.Name,
				UID:       claimRef.UID,
			},
			TargetBackend: backend.Name(),
			VolumeManager: manager.Name(),
		},
	}
	testClient := newPartitionManagerClient(t, claim, device, partition)
	reconciler := &PartitionManager{Client: testClient, NodeName: "node-a"}
	_, _ = reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: partition.Name, Namespace: partition.Namespace},
	})

	if calls := backend.setupCalls.Load(); calls != 1 {
		t.Fatalf("backend SetupDevice was called %d times with valid ownership, want 1", calls)
	}
	var actual storagev1alpha1.NVMePartition
	if err := testClient.Get(context.Background(), types.NamespacedName{Name: partition.Name, Namespace: partition.Namespace}, &actual); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(actual.Status.Conditions, claimAuthorizationCondition)
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != "ClaimOwnershipVerified" {
		t.Fatalf("authorization condition = %#v, want True/ClaimOwnershipVerified", condition)
	}
}

func TestInvalidPluginConfigurationBecomesTerminalStatus(t *testing.T) {
	knownfailure.Require(t, "F19")
	partition := &storagev1alpha1.NVMePartition{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-plugin", Namespace: "default"},
		Spec: storagev1alpha1.NVMePartitionSpec{
			Size:          resource.MustParse("100Mi"),
			NodeName:      "node-a",
			TargetBackend: "not-registered",
		},
	}
	testClient := newPartitionManagerClient(t, partition)
	reconciler := &PartitionManager{Client: testClient, NodeName: "node-a"}
	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: partition.Name, Namespace: partition.Namespace},
	})
	if err != nil || result.RequeueAfter != 0 {
		t.Fatalf("permanent configuration error should be terminal, got result=%#v err=%v", result, err)
	}
	var actual storagev1alpha1.NVMePartition
	if getErr := testClient.Get(context.Background(), types.NamespacedName{Name: partition.Name, Namespace: partition.Namespace}, &actual); getErr != nil {
		t.Fatal(getErr)
	}
	if actual.Status.State != storagev1alpha1.NVMePartitionStateFailed {
		t.Fatalf("state = %q, want Failed", actual.Status.State)
	}
}

func TestSPDKTeardownPassesThePersistedLvolIdentity(t *testing.T) {
	originalSPDK, err := plugins.GetTargetBackend("spdk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { plugins.RegisterTargetBackend(originalSPDK) })
	plugins.RegisterTargetBackend(&namedTestBackend{name: "spdk"})
	manager := &recordingDeleteManager{}
	plugins.RegisterVolumeManager(manager)

	now := metav1.Now()
	partition := &storagev1alpha1.NVMePartition{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "spdk-delete",
			Namespace:         "default",
			Finalizers:        []string{partitionFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: storagev1alpha1.NVMePartitionSpec{
			Size:                     resource.MustParse("100Mi"),
			NodeName:                 "node-a",
			ParentDeviceSerialNumber: "SERIAL-1",
			TargetBackend:            "spdk",
			VolumeManager:            manager.Name(),
		},
		Status: storagev1alpha1.NVMePartitionStatus{
			NQN:             "nqn.test:spdk-delete",
			BackendVolumeID: "lvs_node-a-serial-1n1/volume-id",
			SPDKBaseBdev:    "node-a-serial-1n1",
			SPDKLvstoreName: "lvs_node-a-serial-1n1",
			SPDKLvstoreUUID: "store-uuid",
			SPDKLvolName:    "volume-id",
			SPDKLvolUUID:    "lvol-uuid",
		},
	}
	testClient := newPartitionManagerClient(t, partition)
	reconciler := &PartitionManager{Client: testClient, NodeName: "node-a"}
	_, err = reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: partition.Name, Namespace: partition.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	wantIdentity := plugins.VolumeIdentity{
		BackendVolumeID: "lvs_node-a-serial-1n1/volume-id",
		CapacityBytes:   0,
		BaseBdev:        "node-a-serial-1n1",
		VolumeStoreName: "lvs_node-a-serial-1n1",
		VolumeStoreUUID: "store-uuid",
		VolumeName:      "volume-id",
		VolumeUUID:      "lvol-uuid",
	}
	if manager.identity != wantIdentity {
		t.Fatalf("teardown identity = %#v, want %#v", manager.identity, wantIdentity)
	}
}

func TestSPDKTeardownRetriesAfterUnexportBeforeLvolDeletion(t *testing.T) {
	originalSPDK, err := plugins.GetTargetBackend("spdk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { plugins.RegisterTargetBackend(originalSPDK) })
	backend := &namedTestBackend{name: "spdk"}
	plugins.RegisterTargetBackend(backend)
	manager := &retryDeleteManager{}
	plugins.RegisterVolumeManager(manager)

	now := metav1.Now()
	partition := &storagev1alpha1.NVMePartition{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "retry-spdk-delete",
			Namespace:         "default",
			Finalizers:        []string{partitionFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: storagev1alpha1.NVMePartitionSpec{
			NodeName:                 "node-a",
			ParentDeviceSerialNumber: "SERIAL-1",
			TargetBackend:            "spdk",
			VolumeManager:            manager.Name(),
		},
		Status: storagev1alpha1.NVMePartitionStatus{
			NQN:             "nqn.test:retry-spdk-delete",
			BackendVolumeID: "store/volume",
		},
	}
	testClient := newPartitionManagerClient(t, partition)
	reconciler := &PartitionManager{Client: testClient, NodeName: "node-a"}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: partition.Name, Namespace: partition.Namespace}}

	if _, err := reconciler.Reconcile(context.Background(), request); err == nil {
		t.Fatal("first teardown unexpectedly succeeded after simulated lvol deletion crash")
	}
	var retained storagev1alpha1.NVMePartition
	if err := testClient.Get(context.Background(), request.NamespacedName, &retained); err != nil {
		t.Fatal(err)
	}
	if len(retained.Finalizers) != 1 || retained.Finalizers[0] != partitionFinalizer {
		t.Fatalf("cleanup finalizer was removed after partial failure: %v", retained.Finalizers)
	}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("retry teardown returned error: %v", err)
	}
	if manager.calls.Load() != 2 || backend.unexportCalls.Load() != 2 {
		t.Fatalf("retry calls: delete=%d unexport=%d, want 2 each", manager.calls.Load(), backend.unexportCalls.Load())
	}
	var cleaned storagev1alpha1.NVMePartition
	if err := testClient.Get(context.Background(), request.NamespacedName, &cleaned); err == nil {
		if len(cleaned.Finalizers) != 0 {
			t.Fatalf("cleanup finalizer remains after verified retry: %v", cleaned.Finalizers)
		}
	} else if !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
}
