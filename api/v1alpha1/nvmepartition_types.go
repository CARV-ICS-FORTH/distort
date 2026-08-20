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

// NVMePartitionState defines the lifecycle state of the partition.
type NVMePartitionState string

const (
	// NVMePartitionStatePending means the partition is waiting to be assigned to a node.
	NVMePartitionStatePending NVMePartitionState = "Pending"
	// NVMePartitionStateCreating means the agent is slicing the drive and exporting it.
	NVMePartitionStateCreating NVMePartitionState = "Creating"
	// NVMePartitionStateExported means the NVMe-oF target is active and ready.
	NVMePartitionStateExported NVMePartitionState = "Exported"
	// NVMePartitionStateFailed means the creation or export failed.
	NVMePartitionStateFailed NVMePartitionState = "Failed"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// NVMePartitionSpec defines the desired state of NVMePartition.
// +kubebuilder:validation:XValidation:rule="(!has(self.nodeName) && !has(self.parentDeviceSerialNumber) && !has(self.claimRef)) || (has(self.nodeName) && has(self.parentDeviceSerialNumber) && has(self.claimRef))",message="nodeName, parentDeviceSerialNumber, and claimRef must either all be omitted or all be set by the scheduler"
// +kubebuilder:validation:XValidation:rule="!has(self.targetOptions) || !('spdk-core-mask' in self.targetOptions) || !has(self.targetBackend) || self.targetBackend == 'spdk'",message="spdk-core-mask is supported only by the spdk backend"
type NVMePartitionSpec struct {
	// Size is the requested capacity for the volume.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).isGreaterThan(quantity('0')) && quantity(string(self)).compareTo(quantity('9223372036853727232')) <= 0",message="size must be positive and safely roundable to a 1 MiB allocation unit"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="size is immutable"
	Size resource.Quantity `json:"size"`

	// NodeName is the node where this partition should be created.
	// Often left empty by the CSI provisioner and populated by the mutating scheduler.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="nodeName is immutable after assignment"
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// ParentDeviceSerialNumber is the serial number of the NVMeDevice this partition is allocated from.
	// Populated by the mutating scheduler (Mgmt Controller).
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="parentDeviceSerialNumber is immutable after assignment"
	// +optional
	ParentDeviceSerialNumber string `json:"parentDeviceSerialNumber,omitempty"`

	// ClaimRef identifies the exact live claim that authorized this allocation.
	// Populated by the management controller together with placement fields.
	// +optional
	ClaimRef *NVMeDeviceClaimReference `json:"claimRef,omitempty"`

	// AccessMode indicates the PVC access mode (e.g., ReadWriteOnce, ReadOnlyMany).
	// +kubebuilder:validation:Enum=SINGLE_NODE_WRITER
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="accessMode is immutable"
	// +optional
	AccessMode string `json:"accessMode,omitempty"`

	// Filesystem is the canonical filesystem selected by the CSI request.
	// +kubebuilder:validation:Enum=ext4;xfs
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="filesystem is immutable"
	// +optional
	Filesystem string `json:"filesystem,omitempty"`

	// RequestFingerprint identifies all immutable CSI CreateVolume properties.
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="requestFingerprint is immutable"
	// +optional
	RequestFingerprint string `json:"requestFingerprint,omitempty"`

	// TargetBackend specifies the export technology (e.g., "spdk" or "kernel").
	// +kubebuilder:validation:Enum=spdk;kernel
	// +kubebuilder:default=spdk
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="targetBackend is immutable"
	// +optional
	TargetBackend string `json:"targetBackend,omitempty"`

	// VolumeManager specifies the volume manager (e.g., "partition" or "lvm").
	// +kubebuilder:validation:Enum=partition;lvm
	// +kubebuilder:default=partition
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="volumeManager is immutable"
	// +optional
	VolumeManager string `json:"volumeManager,omitempty"`

	// TargetOptions provides backend-specific customization flags (e.g., spdk core masks).
	// +kubebuilder:validation:MaxProperties=1
	// +kubebuilder:validation:XValidation:rule="self.all(k, k == 'spdk-core-mask')",message="targetOptions contains an unsupported backend option"
	// +kubebuilder:validation:XValidation:rule="!('spdk-core-mask' in self) || (size(self['spdk-core-mask']) <= 258 && self['spdk-core-mask'].matches('^0x[0-9A-Fa-f]+$'))",message="spdk-core-mask must be at most 258 characters and match 0x followed by hexadecimal digits"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="targetOptions is immutable"
	// +optional
	TargetOptions map[string]string `json:"targetOptions,omitempty"`
}

// NVMePartitionStatus defines the observed state of NVMePartition.
type NVMePartitionStatus struct {
	// State is the current status of the partition creation/export.
	// +kubebuilder:validation:Enum=Pending;Creating;Exported;Failed
	// +kubebuilder:default=Pending
	State NVMePartitionState `json:"state,omitempty"`

	// ExternalID is the immutable, globally unique identifier used for backend resources.
	// +optional
	ExternalID string `json:"externalID,omitempty"`

	// VolumeID is the opaque CSI handle identifying this exact namespaced object and UID.
	// +optional
	VolumeID string `json:"volumeID,omitempty"`

	// BackendVolumeID is the block path or logical-volume identity returned by the volume manager.
	// +optional
	BackendVolumeID string `json:"backendVolumeID,omitempty"`

	// AllocatedCapacity is the actual backend capacity after allocation-unit rounding.
	// +optional
	AllocatedCapacity resource.Quantity `json:"allocatedCapacity,omitempty"`

	// SPDKBaseBdev is the exact SPDK namespace bdev backing the logical volume.
	// +optional
	SPDKBaseBdev string `json:"spdkBaseBdev,omitempty"`

	// SPDKLvstoreName is the logical volume store name used during provisioning.
	// +optional
	SPDKLvstoreName string `json:"spdkLvstoreName,omitempty"`

	// SPDKLvstoreUUID is the immutable UUID of the logical volume store.
	// +optional
	SPDKLvstoreUUID string `json:"spdkLvstoreUUID,omitempty"`

	// SPDKLvolName is the logical volume name within its store.
	// +optional
	SPDKLvolName string `json:"spdkLvolName,omitempty"`

	// SPDKLvolUUID is the immutable UUID of the logical volume bdev.
	// +optional
	SPDKLvolUUID string `json:"spdkLvolUUID,omitempty"`

	// NQN is the NVMe Qualified Name generated by the agent upon successful export.
	// +optional
	NQN string `json:"nqn,omitempty"`

	// PortalIP is the RDMA IP address for the CSI node side to connect to.
	// +optional
	PortalIP string `json:"portalIP,omitempty"`

	// PortalPort is the RDMA port (default: 4420).
	// +optional
	PortalPort int `json:"portalPort,omitempty"`

	// Conditions represent the current state of the NVMePartition resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Size",type="string",JSONPath=".spec.size",description="Partition Size"
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".spec.nodeName",description="Assigned Node"
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state",description="Current State"
// +kubebuilder:printcolumn:name="NQN",type="string",JSONPath=".status.nqn",description="NVMe-oF NQN"
// +kubebuilder:printcolumn:name="Portal",type="string",JSONPath=".status.portalIP",description="Portal IP"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NVMePartition is the Schema for the nvmepartitions API
type NVMePartition struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of NVMePartition
	// +required
	Spec NVMePartitionSpec `json:"spec"`

	// status defines the observed state of NVMePartition
	// +optional
	Status NVMePartitionStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NVMePartitionList contains a list of NVMePartition
type NVMePartitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NVMePartition `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NVMePartition{}, &NVMePartitionList{})
}
