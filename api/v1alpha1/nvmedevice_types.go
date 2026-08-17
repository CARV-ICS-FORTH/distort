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

// NVMeDeviceState defines the allocation state of the device.
type NVMeDeviceState string

const (
	// NVMeDeviceStateAvailable means the device is ready to be claimed.
	NVMeDeviceStateAvailable NVMeDeviceState = "Available"
	// NVMeDeviceStateClaimed means the device has been claimed by an admin.
	NVMeDeviceStateClaimed NVMeDeviceState = "Claimed"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// NVMeDeviceSpec defines the desired state of NVMeDevice
type NVMeDeviceSpec struct {
	// NodeName is the name of the Kubernetes node where the device is attached.
	// +kubebuilder:validation:Required
	NodeName string `json:"nodeName"`

	// PCIAddress is the PCIe address of the NVMe controller (e.g., "0000:01:00.0").
	// +kubebuilder:validation:Required
	PCIAddress string `json:"pciAddress"`

	// SerialNumber is the device's hardware serial number. Used for claim matching.
	// +kubebuilder:validation:Required
	SerialNumber string `json:"serialNumber"`

	// Model is the model name/number of the NVMe device.
	// +optional
	Model string `json:"model,omitempty"`

	// TotalCapacity is the total raw size of the device.
	// +kubebuilder:validation:Required
	TotalCapacity resource.Quantity `json:"totalCapacity"`

	// NUMANode indicates the NUMA topology node the device is connected to.
	// +optional
	NUMANode int `json:"numaNode,omitempty"`
}

// NVMeDeviceStatus defines the observed state of NVMeDevice.
type NVMeDeviceStatus struct {
	// State represents the current lifecycle state of the device.
	// +kubebuilder:validation:Enum=Available;Claimed
	// +kubebuilder:default=Available
	State NVMeDeviceState `json:"state,omitempty"`

	// ClaimRef identifies the exact active claim that owns this device.
	// +optional
	ClaimRef *NVMeDeviceClaimReference `json:"claimRef,omitempty"`

	// FreeCapacity is the remaining unallocated capacity on the device.
	FreeCapacity resource.Quantity `json:"freeCapacity,omitempty"`

	// ActiveBackend tracks which target technology currently owns this controller ("spdk", "kernel", or "none").
	// +optional
	ActiveBackend string `json:"activeBackend,omitempty"`

	// Conditions represent the current state of the NVMeDevice resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".spec.nodeName",description="Node where attached"
// +kubebuilder:printcolumn:name="PCI",type="string",JSONPath=".spec.pciAddress",description="PCIe Address"
// +kubebuilder:printcolumn:name="Capacity",type="string",JSONPath=".spec.totalCapacity",description="Total Capacity"
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state",description="Device State"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NVMeDevice is the Schema for the nvmedevices API
type NVMeDevice struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of NVMeDevice
	// +required
	Spec NVMeDeviceSpec `json:"spec"`

	// status defines the observed state of NVMeDevice
	// +optional
	Status NVMeDeviceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NVMeDeviceList contains a list of NVMeDevice
type NVMeDeviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NVMeDevice `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NVMeDevice{}, &NVMeDeviceList{})
}
