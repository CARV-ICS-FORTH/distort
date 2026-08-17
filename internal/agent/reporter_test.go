package agent

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/test/knownfailure"
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
	return &Reporter{Client: builder.Build(), NodeName: "distort-worker-1"}
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

func TestReporterDoesNotAdvertiseLoopbackWithoutANodeIP(t *testing.T) {
	knownfailure.Require(t, "F16")
	reporter := newReporterTestClient(t)
	reporter.reportNode(context.Background(), 0, 0)

	var actual storagev1alpha1.RDMAStorageNode
	err := reporter.Get(context.Background(), types.NamespacedName{Name: "distort-worker-1"}, &actual)
	if err == nil && actual.Spec.RDMAIP == loopbackAddress {
		t.Fatalf("missing Kubernetes Node was advertised as a usable loopback RDMA endpoint: %#v", actual)
	}
}

func TestReporterCountsActiveExports(t *testing.T) {
	knownfailure.Require(t, "F16")
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
