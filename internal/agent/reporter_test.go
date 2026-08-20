package agent

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/rdmahealth"
)

const loopbackAddress = "127.0.0.1"

func newReporterTestClient(t *testing.T, objects ...runtime.Object) *Reporter {
	t.Helper()
	testScheme := runtime.NewScheme()
	if err := corev1.AddToScheme(testScheme); err != nil {
		t.Fatal(err)
	}
	if err := storagev1alpha1.AddToScheme(testScheme); err != nil {
		t.Fatal(err)
	}
	clientObjects := make([]runtime.Object, len(objects))
	copy(clientObjects, objects)
	builder := fake.NewClientBuilder().WithScheme(testScheme).WithRuntimeObjects(clientObjects...).
		WithStatusSubresource(&storagev1alpha1.RDMAStorageNode{}, &storagev1alpha1.NVMeDevice{})
	return &Reporter{
		Client: builder.Build(), NodeName: "distort-worker-1",
		discoverRDMA: func() (RDMAEndpoint, error) {
			return RDMAEndpoint{Interface: "rdma0", IP: "192.168.56.11", Transport: storagev1alpha1.RDMATransportRoCEv2, LinkSpeed: "100 Gb/sec"}, nil
		},
	}
}

func TestReporterPublishesNodeInternalIPAndCapacity(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "distort-worker-1"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeHostName, Address: "distort-worker-1"},
			{Type: corev1.NodeInternalIP, Address: "192.168.56.11"},
		}},
	}
	reporter := newReporterTestClient(t, node)
	reporter.reportNode(context.Background(), 4*1024*1024, 3*1024*1024)

	var actual storagev1alpha1.RDMAStorageNode
	if err := reporter.Get(context.Background(), types.NamespacedName{Name: "distort-worker-1"}, &actual); err != nil {
		t.Fatalf("getting RDMAStorageNode: %v", err)
	}
	if actual.Spec.RDMAIP != "192.168.56.11" || actual.Spec.Transport != storagev1alpha1.RDMATransportRoCEv2 {
		t.Fatalf("unexpected RDMA endpoint: %#v", actual.Spec)
	}
	if actual.Status.TotalCapacity.Cmp(*resource.NewQuantity(4*1024*1024, resource.BinarySI)) != 0 ||
		actual.Status.FreeCapacity.Cmp(*resource.NewQuantity(3*1024*1024, resource.BinarySI)) != 0 {
		t.Fatalf("unexpected reported capacity: %#v", actual.Status)
	}
}

func TestReporterMarksMissingHardwareUnavailableWithoutReleasingItsClaim(t *testing.T) {
	claimRef := &storagev1alpha1.NVMeDeviceClaimReference{Namespace: "default", Name: "claim", UID: "claim-uid"}
	device := &storagev1alpha1.NVMeDevice{
		ObjectMeta: metav1.ObjectMeta{Name: "distort-worker-1-serial-1"},
		Spec: storagev1alpha1.NVMeDeviceSpec{
			NodeName: "distort-worker-1", PCIAddress: "0000:01:00.0", SerialNumber: "SERIAL-1",
			TotalCapacity: resource.MustParse("1Gi"),
		},
		Status: storagev1alpha1.NVMeDeviceStatus{State: storagev1alpha1.NVMeDeviceStateClaimed, ClaimRef: claimRef},
	}
	reporter := newReporterTestClient(t, device)
	reporter.discoverDevices = func() ([]HardwareNVMe, error) { return nil, nil }
	reporter.reportDevices(context.Background())

	var actual storagev1alpha1.NVMeDevice
	if err := reporter.Get(context.Background(), types.NamespacedName{Name: device.Name}, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.State != storagev1alpha1.NVMeDeviceStateUnavailable || actual.Status.ClaimRef == nil || actual.Status.ClaimRef.UID != claimRef.UID {
		t.Fatalf("missing device status = %#v, want Unavailable with ownership retained", actual.Status)
	}
	condition := meta.FindStatusCondition(actual.Status.Conditions, hardwareAvailableCondition)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "DeviceNotDiscovered" {
		t.Fatalf("hardware condition = %#v, want False/DeviceNotDiscovered", condition)
	}
}

func TestReporterRefreshesAndReactivatesRediscoveredHardware(t *testing.T) {
	claimRef := &storagev1alpha1.NVMeDeviceClaimReference{Namespace: "default", Name: "claim", UID: "claim-uid"}
	device := &storagev1alpha1.NVMeDevice{
		ObjectMeta: metav1.ObjectMeta{Name: "distort-worker-1-serial-1"},
		Spec: storagev1alpha1.NVMeDeviceSpec{
			NodeName: "distort-worker-1", PCIAddress: "0000:01:00.0", SerialNumber: "SERIAL-1",
			TotalCapacity: resource.MustParse("1Gi"),
		},
		Status: storagev1alpha1.NVMeDeviceStatus{State: storagev1alpha1.NVMeDeviceStateUnavailable, ClaimRef: claimRef},
	}
	reporter := newReporterTestClient(t, device)
	reporter.discoverDevices = func() ([]HardwareNVMe, error) {
		return []HardwareNVMe{{Name: "nvme2", PCIAddress: "0000:03:00.0", SerialNumber: "SERIAL-1", Model: "new-model", TotalBytes: 2 << 30, NUMANode: 1}}, nil
	}
	reporter.reportDevices(context.Background())

	var actual storagev1alpha1.NVMeDevice
	if err := reporter.Get(context.Background(), types.NamespacedName{Name: device.Name}, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Spec.PCIAddress != "0000:03:00.0" || actual.Spec.Model != "new-model" || actual.Spec.TotalCapacity.Value() != 2<<30 || actual.Spec.NUMANode != 1 {
		t.Fatalf("hardware metadata was not refreshed: %#v", actual.Spec)
	}
	if actual.Status.State != storagev1alpha1.NVMeDeviceStateClaimed || actual.Status.ClaimRef == nil {
		t.Fatalf("rediscovered device status = %#v, want Claimed with ownership retained", actual.Status)
	}
	condition := meta.FindStatusCondition(actual.Status.Conditions, hardwareAvailableCondition)
	if condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("hardware condition = %#v, want True", condition)
	}
}

func TestReporterDoesNotMarkHardwareMissingWhenDiscoveryFails(t *testing.T) {
	device := &storagev1alpha1.NVMeDevice{
		ObjectMeta: metav1.ObjectMeta{Name: "distort-worker-1-serial-1"},
		Spec: storagev1alpha1.NVMeDeviceSpec{
			NodeName: "distort-worker-1", PCIAddress: "0000:01:00.0", SerialNumber: "SERIAL-1",
			TotalCapacity: resource.MustParse("1Gi"),
		},
		Status: storagev1alpha1.NVMeDeviceStatus{State: storagev1alpha1.NVMeDeviceStateAvailable},
	}
	reporter := newReporterTestClient(t, device)
	reporter.discoverDevices = func() ([]HardwareNVMe, error) { return nil, errors.New("temporary discovery failure") }
	reporter.reportDevices(context.Background())

	var actual storagev1alpha1.NVMeDevice
	if err := reporter.Get(context.Background(), types.NamespacedName{Name: device.Name}, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.State != storagev1alpha1.NVMeDeviceStateAvailable {
		t.Fatalf("discovery failure changed device state to %q", actual.Status.State)
	}
}

func TestReporterDoesNotAdvertiseLoopbackWithoutANodeIP(t *testing.T) {
	reporter := newReporterTestClient(t)
	reporter.discoverRDMA = func() (RDMAEndpoint, error) { return RDMAEndpoint{}, errors.New("no RDMA interface") }
	reporter.reportNode(context.Background(), 0, 0)

	var actual storagev1alpha1.RDMAStorageNode
	err := reporter.Get(context.Background(), types.NamespacedName{Name: "distort-worker-1"}, &actual)
	if err == nil && actual.Spec.RDMAIP == loopbackAddress {
		t.Fatalf("missing Kubernetes Node was advertised as a usable loopback RDMA endpoint: %#v", actual)
	}
	condition := meta.FindStatusCondition(actual.Status.Conditions, rdmahealth.ReadyCondition)
	if condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("RDMA readiness = %#v, want False", condition)
	}
}

func TestReporterCountsActiveExports(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "distort-worker-1"},
		Status:     corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.168.56.11"}}},
	}
	partition := &storagev1alpha1.NVMePartition{
		ObjectMeta: metav1.ObjectMeta{Name: "exported-volume", Namespace: "default"},
		Spec: storagev1alpha1.NVMePartitionSpec{
			NodeName: "distort-worker-1",
			Size:     resource.MustParse("1Mi"),
		},
		Status: storagev1alpha1.NVMePartitionStatus{State: storagev1alpha1.NVMePartitionStateExported},
	}
	reporter := newReporterTestClient(t, node, partition)
	reporter.reportNode(context.Background(), 1024, 512)

	var actual storagev1alpha1.RDMAStorageNode
	if err := reporter.Get(context.Background(), types.NamespacedName{Name: "distort-worker-1"}, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.ActiveExports != 1 {
		t.Fatalf("ActiveExports = %d, want 1", actual.Status.ActiveExports)
	}
}
