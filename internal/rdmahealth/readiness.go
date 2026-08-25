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
)

func Validate(node *storagev1alpha1.RDMAStorageNode, now time.Time) error {
	condition := meta.FindStatusCondition(node.Status.Conditions, ReadyCondition)
	if condition == nil || condition.Status != metav1.ConditionTrue {
		return fmt.Errorf("RDMAStorageNode %s is not Ready", node.Name)
	}
	if node.Status.LastHeartbeatTime.IsZero() || now.Sub(node.Status.LastHeartbeatTime.Time) > FreshnessWindow {
		return fmt.Errorf("RDMAStorageNode %s heartbeat is stale", node.Name)
	}
	ip := net.ParseIP(node.Spec.RDMAIP)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return fmt.Errorf("RDMAStorageNode %s has no usable RDMA IP", node.Name)
	}
	if node.Spec.Transport != storagev1alpha1.RDMATransportRoCEv2 &&
		node.Spec.Transport != storagev1alpha1.RDMATransportInfiniBand {
		return fmt.Errorf("RDMAStorageNode %s does not advertise an RDMA transport", node.Name)
	}
	return nil
}
