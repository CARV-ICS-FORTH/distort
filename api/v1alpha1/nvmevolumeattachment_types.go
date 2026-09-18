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
)

// NVMeVolumeReference identifies one immutable NVMePartition.
type NVMeVolumeReference struct {
	// Name is the partition name in the attachment namespace.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// UID prevents a deleted partition name from authorizing its replacement.
	// +kubebuilder:validation:MinLength=1
	UID string `json:"uid"`
}

// NVMeVolumeAttachmentSpec defines the desired single-writer attachment.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="attachment ownership is immutable; delete and recreate the attachment to change nodes"
type NVMeVolumeAttachmentSpec struct {
	// VolumeRef identifies the exact partition being attached.
	// +kubebuilder:validation:Required
	VolumeRef NVMeVolumeReference `json:"volumeRef"`

	// NodeID is the CSI node which exclusively owns the attachment.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	NodeID string `json:"nodeID"`

	// HostNQN is the deterministic NVMe host identity authorized by the target.
	// +kubebuilder:validation:Pattern=`^nqn\.2026-01\.io\.distort:host-[a-f0-9]{32}$`
	HostNQN string `json:"hostNQN"`

	// AttachmentID distinguishes this attachment lifetime from every retry or takeover.
	// +kubebuilder:validation:MinLength=1
	AttachmentID string `json:"attachmentID"`
}

// NVMeVolumeAttachmentStatus defines the observed state of NVMeVolumeAttachment.
type NVMeVolumeAttachmentStatus struct {
	// ObservedAttachmentID is set only after the provider target authorizes HostNQN.
	// +optional
	ObservedAttachmentID string `json:"observedAttachmentID,omitempty"`

	// Conditions report whether target access is ready or being revoked.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NVMeVolumeAttachment is the durable single-writer fencing record for a volume.
type NVMeVolumeAttachment struct {
	metav1.TypeMeta `json:",inline"`

	// Metadata is standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// Spec defines the desired attachment owner.
	// +required
	Spec NVMeVolumeAttachmentSpec `json:"spec"`

	// Status reports whether the provider target has applied the attachment.
	// +optional
	Status NVMeVolumeAttachmentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NVMeVolumeAttachmentList contains a list of NVMeVolumeAttachment
type NVMeVolumeAttachmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NVMeVolumeAttachment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NVMeVolumeAttachment{}, &NVMeVolumeAttachmentList{})
}
