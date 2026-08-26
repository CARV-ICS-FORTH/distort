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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// NVMeDeviceClaimSpec defines the desired state of NVMeDeviceClaim
type NVMeDeviceClaimSpec struct {
	// SerialNumber is the exact hardware serial number of the NVMe device to claim.
	// This uniquely identifies the drive even if it moves to another node or PCIe slot.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="serialNumber is immutable"
	SerialNumber string `json:"serialNumber"`
}

// NVMeDeviceClaimReference identifies the exact claim that authorizes use of a device.
// The UID prevents a deleted claim from being silently replaced by another object with
// the same namespace and name.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="claimRef is immutable"
// +kubebuilder:validation:XValidation:rule="size(self.uid) > 0",message="claimRef UID must not be empty"
type NVMeDeviceClaimReference struct {
	// Namespace is the namespace containing the claim.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// Name is the name of the claim.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// UID is the immutable Kubernetes UID of the claim.
	UID types.UID `json:"uid"`
}

// NVMeDeviceClaimStatus defines the observed state of NVMeDeviceClaim.
type NVMeDeviceClaimStatus struct {
	// Active indicates whether the claim has successfully bound to a matching NVMeDevice.
	// +kubebuilder:default=false
	Active bool `json:"active"`

	// MatchedDevice is the name of the NVMeDevice resource that this claim is bound to.
	// +optional
	MatchedDevice string `json:"matchedDevice,omitempty"`

	// NodeName is the node where the matched device currently resides.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// Conditions represent the current state of the NVMeDeviceClaim resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="SerialNumber",type="string",JSONPath=".spec.serialNumber",description="Target Serial Number"
// +kubebuilder:printcolumn:name="Active",type="boolean",JSONPath=".status.active",description="Is Active"
// +kubebuilder:printcolumn:name="MatchedDevice",type="string",JSONPath=".status.matchedDevice",description="Matched Device"
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".status.nodeName",description="Node"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NVMeDeviceClaim is the Schema for the nvmedeviceclaims API
type NVMeDeviceClaim struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of NVMeDeviceClaim
	// +required
	Spec NVMeDeviceClaimSpec `json:"spec"`

	// status defines the observed state of NVMeDeviceClaim
	// +optional
	Status NVMeDeviceClaimStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NVMeDeviceClaimList contains a list of NVMeDeviceClaim
type NVMeDeviceClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NVMeDeviceClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NVMeDeviceClaim{}, &NVMeDeviceClaimList{})
}
