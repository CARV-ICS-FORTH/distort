package rdmahealth

import (
	"fmt"
	"net"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "distort/api/v1alpha1"
)

const (
	ReadyCondition  = "Ready"
	FreshnessWindow = 45 * time.Second
	MaxFutureSkew   = 5 * time.Second
)

// HeartbeatIsFresh accepts a small forward skew while rejecting corrupt or
// materially future-dated observations that would otherwise remain healthy.
func HeartbeatIsFresh(lastHeartbeat metav1.Time, now time.Time) bool {
	if lastHeartbeat.IsZero() {
		return false
	}
	age := now.Sub(lastHeartbeat.Time)
	return age <= FreshnessWindow && age >= -MaxFutureSkew
}

// ParseUsableIP accepts only unicast addresses that can be used as a remote
// NVMe-oF endpoint. Link-local addresses are excluded because the persisted
// endpoint has no interface zone and is consumed from other nodes.
func ParseUsableIP(address string) (net.IP, error) {
	ip := net.ParseIP(address)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return nil, fmt.Errorf("%q is not a routable unicast IP address", address)
	}
	return ip, nil
}

func Validate(node *storagev1alpha1.RDMAStorageNode, now time.Time) error {
	condition := meta.FindStatusCondition(node.Status.Conditions, ReadyCondition)
	if condition == nil || condition.Status != metav1.ConditionTrue {
		return fmt.Errorf("RDMAStorageNode %s is not Ready", node.Name)
	}
	if !HeartbeatIsFresh(node.Status.LastHeartbeatTime, now) {
		return fmt.Errorf("RDMAStorageNode %s heartbeat is stale", node.Name)
	}
	if _, err := ParseUsableIP(node.Spec.RDMAIP); err != nil {
		return fmt.Errorf("RDMAStorageNode %s has no usable RDMA IP", node.Name)
	}
	if node.Spec.Transport != storagev1alpha1.RDMATransportRoCEv2 &&
		node.Spec.Transport != storagev1alpha1.RDMATransportInfiniBand {
		return fmt.Errorf("RDMAStorageNode %s does not advertise an RDMA transport", node.Name)
	}
	return nil
}
