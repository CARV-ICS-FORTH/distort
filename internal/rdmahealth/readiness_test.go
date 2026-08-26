package rdmahealth

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "distort/api/v1alpha1"
)

func readyNode(address string, transport storagev1alpha1.RDMATransportType) *storagev1alpha1.RDMAStorageNode {
	return &storagev1alpha1.RDMAStorageNode{
		Spec: storagev1alpha1.RDMAStorageNodeSpec{RDMAIP: address, Transport: transport},
		Status: storagev1alpha1.RDMAStorageNodeStatus{
			LastHeartbeatTime: metav1.NewTime(time.Now()),
			Conditions:        []metav1.Condition{{Type: ReadyCondition, Status: metav1.ConditionTrue}},
		},
	}
}

func TestValidateAcceptsRoutableIPv4AndIPv6(t *testing.T) {
	for _, address := range []string{"192.0.2.10", "2001:db8::10"} {
		if err := Validate(readyNode(address, storagev1alpha1.RDMATransportRoCEv2), time.Now()); err != nil {
			t.Fatalf("routable address %s was rejected: %v", address, err)
		}
	}
}

func TestValidateRejectsLocalMulticastAndUnsupportedEndpoints(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "169.254.1.1", "fe80::1", "224.0.0.1", "ff02::1"} {
		if err := Validate(readyNode(address, storagev1alpha1.RDMATransportRoCEv2), time.Now()); err == nil {
			t.Fatalf("unsafe address %s was accepted", address)
		}
	}
	if err := Validate(readyNode("192.0.2.10", storagev1alpha1.RDMATransportType("TCP")), time.Now()); err == nil {
		t.Fatal("unsupported TCP transport was accepted")
	}
}
