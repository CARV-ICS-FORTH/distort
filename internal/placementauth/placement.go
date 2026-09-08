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

package placementauth

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	storagev1alpha1 "distort/api/v1alpha1"
)

const ConditionType = "PlacementAuthorized"

// Fingerprint binds a placement decision to one partition and one exact claim.
// Length-prefixing prevents ambiguous concatenations without allocating an
// intermediate serialization buffer.
func Fingerprint(
	partitionUID types.UID,
	nodeName, deviceSerial string,
	claimRef *storagev1alpha1.NVMeDeviceClaimReference,
) string {
	if partitionUID == "" || nodeName == "" || deviceSerial == "" || claimRef == nil ||
		claimRef.Namespace == "" || claimRef.Name == "" || claimRef.UID == "" {
		return ""
	}

	digest := sha256.New()
	for _, component := range []string{
		string(partitionUID), nodeName, deviceSerial,
		claimRef.Namespace, claimRef.Name, string(claimRef.UID),
	} {
		writeComponent(digest, component)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeComponent(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = io.WriteString(digest, value)
}

// IsAuthorized verifies both manager ownership and the exact selected fields.
func IsAuthorized(partition *storagev1alpha1.NVMePartition) bool {
	condition := meta.FindStatusCondition(partition.Status.Conditions, ConditionType)
	if condition == nil || condition.Status != metav1.ConditionTrue ||
		condition.ObservedGeneration != partition.Generation {
		return false
	}
	expected := Fingerprint(
		partition.UID,
		partition.Spec.NodeName,
		partition.Spec.ParentDeviceSerialNumber,
		partition.Spec.ClaimRef,
	)
	return expected != "" && partition.Status.PlacementFingerprint == expected
}
