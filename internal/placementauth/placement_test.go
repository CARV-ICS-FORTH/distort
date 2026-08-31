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
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	storagev1alpha1 "distort/api/v1alpha1"
)

func TestIsAuthorizedBindsExactPlacementAndGeneration(t *testing.T) {
	partition := &storagev1alpha1.NVMePartition{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID("partition-uid"), Generation: 7},
		Spec: storagev1alpha1.NVMePartitionSpec{
			NodeName:                 "node-a",
			ParentDeviceSerialNumber: "serial-a",
			ClaimRef: &storagev1alpha1.NVMeDeviceClaimReference{
				Namespace: "tenant-a", Name: "claim-a", UID: types.UID("claim-uid"),
			},
		},
	}
	partition.Status.PlacementFingerprint = Fingerprint(
		partition.UID,
		partition.Spec.NodeName,
		partition.Spec.ParentDeviceSerialNumber,
		partition.Spec.ClaimRef,
	)
	meta.SetStatusCondition(&partition.Status.Conditions, metav1.Condition{
		Type: ConditionType, Status: metav1.ConditionTrue, ObservedGeneration: partition.Generation,
		Reason: "TestAuthorized", Message: "Test authorization",
	})
	if !IsAuthorized(partition) {
		t.Fatal("exact manager placement was rejected")
	}

	partition.Spec.ParentDeviceSerialNumber = "serial-b"
	if IsAuthorized(partition) {
		t.Fatal("authorization remained valid after device identity changed")
	}
	partition.Spec.ParentDeviceSerialNumber = "serial-a"
	partition.Generation++
	if IsAuthorized(partition) {
		t.Fatal("authorization remained valid for an unobserved generation")
	}
}
