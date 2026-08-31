package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	storagev1alpha1 "distort/api/v1alpha1"
)

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

func reporterDeviceName(t *testing.T, serial string) string {
	t.Helper()
	name, err := deviceObjectName("distort-worker-1", serial)
	if err != nil {
		t.Fatal(err)
	}
	return name
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
	reporter.reportNode(context.Background(), 4*1024*1024, 3*1024*1024, nil)

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
		ObjectMeta: metav1.ObjectMeta{Name: reporterDeviceName(t, "SERIAL-1")},
		Spec: storagev1alpha1.NVMeDeviceSpec{
			NodeName: "distort-worker-1", PCIAddress: "0000:01:00.0", SerialNumber: "SERIAL-1",
			TotalCapacity: resource.MustParse("1Gi"),
		},
		Status: storagev1alpha1.NVMeDeviceStatus{State: storagev1alpha1.NVMeDeviceStateClaimed, ClaimRef: claimRef},
	}
	reporter := newReporterTestClient(t, device)
	reporter.discoverDevices = func() ([]HardwareNVMe, error) { return nil, nil }
	if _, _, err := reporter.reportDevices(context.Background()); err != nil {
		t.Fatal(err)
	}

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
		ObjectMeta: metav1.ObjectMeta{Name: reporterDeviceName(t, "SERIAL-1")},
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
	if _, _, err := reporter.reportDevices(context.Background()); err != nil {
		t.Fatal(err)
	}

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
		ObjectMeta: metav1.ObjectMeta{Name: reporterDeviceName(t, "SERIAL-1")},
		Spec: storagev1alpha1.NVMeDeviceSpec{
			NodeName: "distort-worker-1", PCIAddress: "0000:01:00.0", SerialNumber: "SERIAL-1",
			TotalCapacity: resource.MustParse("1Gi"),
		},
		Status: storagev1alpha1.NVMeDeviceStatus{State: storagev1alpha1.NVMeDeviceStateAvailable},
	}
	reporter := newReporterTestClient(t, device)
	reporter.discoverDevices = func() ([]HardwareNVMe, error) { return nil, errors.New("temporary discovery failure") }
	if _, _, err := reporter.reportDevices(context.Background()); err == nil {
		t.Fatal("reportDevices did not return its discovery failure")
	}

	var actual storagev1alpha1.NVMeDevice
	if err := reporter.Get(context.Background(), types.NamespacedName{Name: device.Name}, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.State != storagev1alpha1.NVMeDeviceStateAvailable {
		t.Fatalf("discovery failure changed device state to %q", actual.Status.State)
	}
}

func TestReporterPostponesNodeUntilInitialRDMADiscoverySucceeds(t *testing.T) {
	reporter := newReporterTestClient(t)
	reporter.discoverRDMA = func() (RDMAEndpoint, error) { return RDMAEndpoint{}, errors.New("no RDMA interface") }
	reporter.reportNode(context.Background(), 0, 0, errors.New("inventory unavailable"))

	var actual storagev1alpha1.RDMAStorageNode
	err := reporter.Get(context.Background(), types.NamespacedName{Name: "distort-worker-1"}, &actual)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("initial discovery failure created RDMAStorageNode %#v: %v", actual, err)
	}

	reporter.discoverRDMA = func() (RDMAEndpoint, error) {
		return RDMAEndpoint{Interface: "rdma0", IP: "192.168.56.11", Transport: storagev1alpha1.RDMATransportRoCEv2}, nil
	}
	reporter.reportNode(context.Background(), 0, 0, nil)
	if err := reporter.Get(context.Background(), types.NamespacedName{Name: "distort-worker-1"}, &actual); err != nil {
		t.Fatalf("successful rediscovery did not create RDMAStorageNode: %v", err)
	}
	if actual.Spec.RDMAIP != "192.168.56.11" || actual.Spec.Transport != storagev1alpha1.RDMATransportRoCEv2 {
		t.Fatalf("successful rediscovery created unexpected endpoint: %#v", actual.Spec)
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
	reporter.reportNode(context.Background(), 1024, 512, nil)

	var actual storagev1alpha1.RDMAStorageNode
	if err := reporter.Get(context.Background(), types.NamespacedName{Name: "distort-worker-1"}, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.ActiveExports != 1 {
		t.Fatalf("ActiveExports = %d, want 1", actual.Status.ActiveExports)
	}
}

func TestDeviceObjectNameIsDeterministicBoundedAndDNSSafe(t *testing.T) {
	serials := []string{
		"SERIAL WITH SPACES/AND:PUNCTUATION",
		strings.Repeat("very-long-serial-", 40),
	}
	for _, serial := range serials {
		name, err := deviceObjectName(strings.Repeat("node.segment-", 30), serial)
		if err != nil {
			t.Fatal(err)
		}
		again, err := deviceObjectName(strings.Repeat("node.segment-", 30), serial)
		if err != nil || again != name {
			t.Fatalf("device name is not deterministic: %q and %q, err=%v", name, again, err)
		}
		if len(name) > 253 {
			t.Fatalf("device name length = %d, want <= 253: %q", len(name), name)
		}
		if problems := validation.IsDNS1123Subdomain(name); len(problems) != 0 {
			t.Fatalf("device name %q is not DNS-safe: %v", name, problems)
		}
	}
	first, _ := deviceObjectName("node-a", "serial/a")
	second, _ := deviceObjectName("node-a", "serial:a")
	if first == second {
		t.Fatalf("distinct serials produced the same object name %q", first)
	}
}

func TestReporterProcessesPartialInventoryAndBlocksPlacementReadiness(t *testing.T) {
	serial := "SERIAL WITH SPACES/AND:PUNCTUATION"
	deviceName := reporterDeviceName(t, serial)
	device := &storagev1alpha1.NVMeDevice{
		ObjectMeta: metav1.ObjectMeta{Name: deviceName},
		Spec: storagev1alpha1.NVMeDeviceSpec{
			NodeName: "distort-worker-1", PCIAddress: "0000:01:00.0", SerialNumber: serial,
			Model: "old-model", TotalCapacity: resource.MustParse("1Gi"),
		},
		Status: storagev1alpha1.NVMeDeviceStatus{State: storagev1alpha1.NVMeDeviceStateAvailable},
	}
	reporter := newReporterTestClient(t, device)
	reporter.discoverDevices = func() ([]HardwareNVMe, error) {
		return []HardwareNVMe{{
			Name: "nvme2", PCIAddress: "0000:03:00.0", SerialNumber: serial,
			Model: "refreshed-model", TotalBytes: 2 << 30,
		}}, &NVMeDiscoveryError{SPDK: errors.New("temporary SPDK RPC failure")}
	}
	reporter.report(context.Background())

	var actual storagev1alpha1.NVMeDevice
	if err := reporter.Get(context.Background(), types.NamespacedName{Name: deviceName}, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Spec.Model != "refreshed-model" || actual.Spec.PCIAddress != "0000:03:00.0" {
		t.Fatalf("safe partial discovery result was not processed: %#v", actual.Spec)
	}
	var node storagev1alpha1.RDMAStorageNode
	if err := reporter.Get(context.Background(), types.NamespacedName{Name: reporter.NodeName}, &node); err != nil {
		t.Fatal(err)
	}
	aggregate := meta.FindStatusCondition(node.Status.Conditions, storagev1alpha1.NVMeInventoryReadyCondition)
	kernel := meta.FindStatusCondition(node.Status.Conditions, kernelDiscoveryReadyCondition)
	spdk := meta.FindStatusCondition(node.Status.Conditions, spdkDiscoveryReadyCondition)
	if aggregate == nil || aggregate.Status != metav1.ConditionFalse ||
		kernel == nil || kernel.Status != metav1.ConditionTrue ||
		spdk == nil || spdk.Status != metav1.ConditionFalse {
		t.Fatalf("unexpected inventory conditions: aggregate=%#v kernel=%#v spdk=%#v", aggregate, kernel, spdk)
	}

	reporter.discoverDevices = func() ([]HardwareNVMe, error) {
		return []HardwareNVMe{{
			Name: "nvme2", PCIAddress: "0000:03:00.0", SerialNumber: serial,
			Model: "refreshed-model", TotalBytes: 2 << 30,
		}}, nil
	}
	reporter.report(context.Background())
	if err := reporter.Get(context.Background(), types.NamespacedName{Name: reporter.NodeName}, &node); err != nil {
		t.Fatal(err)
	}
	aggregate = meta.FindStatusCondition(node.Status.Conditions, storagev1alpha1.NVMeInventoryReadyCondition)
	if aggregate == nil || aggregate.Status != metav1.ConditionTrue {
		t.Fatalf("inventory did not recover after a complete scan: %#v", aggregate)
	}
}

func TestReporterRejectsIncompleteHardwareMetadata(t *testing.T) {
	reporter := newReporterTestClient(t)
	reporter.discoverDevices = func() ([]HardwareNVMe, error) {
		return []HardwareNVMe{
			{Name: "missing-serial", PCIAddress: "0000:01:00.0", TotalBytes: 1 << 30},
			{Name: "missing-pci", SerialNumber: "SERIAL-2", TotalBytes: 1 << 30},
			{Name: "missing-capacity", PCIAddress: "0000:02:00.0", SerialNumber: "SERIAL-3"},
		}, nil
	}
	reporter.report(context.Background())

	var devices storagev1alpha1.NVMeDeviceList
	if err := reporter.List(context.Background(), &devices); err != nil {
		t.Fatal(err)
	}
	if len(devices.Items) != 0 {
		t.Fatalf("unsafe hardware metadata created API objects: %#v", devices.Items)
	}
	var node storagev1alpha1.RDMAStorageNode
	if err := reporter.Get(context.Background(), types.NamespacedName{Name: reporter.NodeName}, &node); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(node.Status.Conditions, storagev1alpha1.NVMeInventoryReadyCondition)
	if condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("invalid metadata did not degrade inventory readiness: %#v", condition)
	}
}
