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

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RDMATransportType defines the type of RDMA transport.
type RDMATransportType string

const (
	// RDMATransportRoCEv2 represents RoCEv2 transport.
	RDMATransportRoCEv2 RDMATransportType = "RoCEv2"
	// RDMATransportInfiniBand represents InfiniBand transport.
	RDMATransportInfiniBand RDMATransportType = "InfiniBand"
	// RDMATransportTCP represents TCP transport (standard NVMe/TCP fallback if RDMA fails).
	RDMATransportTCP RDMATransportType = "TCP"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// RDMAStorageNodeSpec defines the desired state of RDMAStorageNode
type RDMAStorageNodeSpec struct {
	// NodeName is the name of the Kubernetes node.
	// +kubebuilder:validation:Required
	NodeName string `json:"nodeName"`

	// RDMAIP is the IP address bound to the RDMA-capable NIC interface.
	// This is the IP that the CSI nodes will connect to.
	// +kubebuilder:validation:Required
	RDMAIP string `json:"rdmaIP"`

	// Transport is the active RDMA transport type (e.g., RoCEv2, InfiniBand).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=RoCEv2;InfiniBand;TCP
	Transport RDMATransportType `json:"transport"`

	// LinkSpeed is the speed of the RDMA link (e.g., "100Gbps").
	// +optional
	LinkSpeed string `json:"linkSpeed,omitempty"`
}

// RDMAStorageNodeStatus defines the observed state of RDMAStorageNode.
type RDMAStorageNodeStatus struct {
	// TotalCapacity is the sum of capacities of all Available or Claimed NVMeDevices on this node.
	TotalCapacity resource.Quantity `json:"totalCapacity,omitempty"`

	// FreeCapacity is the sum of free capacities across all Claimed NVMeDevices on this node.
	// Used by the Mgmt-Controller to schedule new NVMePartitions.
	FreeCapacity resource.Quantity `json:"freeCapacity,omitempty"`

	// ActiveExports is the number of active NVMe-oF subsystems currently exported by this node.
	ActiveExports int `json:"activeExports,omitempty"`

	// Conditions represent the current state of the RDMAStorageNode resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".spec.nodeName",description="Node Name"
// +kubebuilder:printcolumn:name="RDMA IP",type="string",JSONPath=".spec.rdmaIP",description="RDMA Portal IP"
// +kubebuilder:printcolumn:name="Transport",type="string",JSONPath=".spec.transport",description="Transport Type"
// +kubebuilder:printcolumn:name="Free Cap",type="string",JSONPath=".status.freeCapacity",description="Available Storage"
// +kubebuilder:printcolumn:name="Exports",type="integer",JSONPath=".status.activeExports",description="Active Subsystems"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// RDMAStorageNode is the Schema for the rdmastoragenodes API
type RDMAStorageNode struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of RDMAStorageNode
	// +required
	Spec RDMAStorageNodeSpec `json:"spec"`

	// status defines the observed state of RDMAStorageNode
	// +optional
	Status RDMAStorageNodeStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RDMAStorageNodeList contains a list of RDMAStorageNode
type RDMAStorageNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []RDMAStorageNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RDMAStorageNode{}, &RDMAStorageNodeList{})
}
