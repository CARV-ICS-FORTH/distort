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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "distort/api/v1alpha1"
)

var _ = Describe("NVMeDevice capacity reconciliation", func() {
	const namespace = "default"
	ctx := context.Background()

	AfterEach(func() {
		for _, object := range []client.Object{
			&storagev1alpha1.NVMePartition{ObjectMeta: metav1.ObjectMeta{Name: "capacity-a", Namespace: namespace}},
			&storagev1alpha1.NVMePartition{ObjectMeta: metav1.ObjectMeta{Name: "capacity-b", Namespace: namespace}},
			&storagev1alpha1.NVMePartition{ObjectMeta: metav1.ObjectMeta{Name: "capacity-negative", Namespace: namespace}},
			&storagev1alpha1.NVMeDevice{ObjectMeta: metav1.ObjectMeta{Name: "capacity-device"}},
		} {
			_ = k8sClient.Delete(ctx, object)
		}
	})

	createDevice := func() {
		device := &storagev1alpha1.NVMeDevice{
			ObjectMeta: metav1.ObjectMeta{Name: "capacity-device"},
			Spec: storagev1alpha1.NVMeDeviceSpec{
				NodeName:      "distort-worker-1",
				PCIAddress:    "0000:01:00.0",
				SerialNumber:  "capacity-serial",
				TotalCapacity: resource.MustParse("10Gi"),
			},
		}
		Expect(k8sClient.Create(ctx, device)).To(Succeed())
	}

	reconcileDevice := func() (storagev1alpha1.NVMeDevice, error) {
		reconciler := &NVMeDeviceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "capacity-device"}})
		var actual storagev1alpha1.NVMeDevice
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "capacity-device"}, &actual)).To(Succeed())
		return actual, err
	}
	assignedSpec := func(size, serial string) storagev1alpha1.NVMePartitionSpec {
		return storagev1alpha1.NVMePartitionSpec{
			Size:                     resource.MustParse(size),
			NodeName:                 "distort-worker-1",
			ParentDeviceSerialNumber: serial,
			ClaimRef: &storagev1alpha1.NVMeDeviceClaimReference{
				Namespace: namespace,
				Name:      "capacity-claim",
				UID:       types.UID("capacity-claim-uid"),
			},
		}
	}

	It("subtracts only partitions assigned to the matching serial", func() {
		createDevice()
		Expect(k8sClient.Create(ctx, &storagev1alpha1.NVMePartition{
			ObjectMeta: metav1.ObjectMeta{Name: "capacity-a", Namespace: namespace},
			Spec:       assignedSpec("2Gi", "capacity-serial"),
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &storagev1alpha1.NVMePartition{
			ObjectMeta: metav1.ObjectMeta{Name: "capacity-b", Namespace: namespace},
			Spec:       assignedSpec("7Gi", "another-serial"),
		})).To(Succeed())

		actual, err := reconcileDevice()
		Expect(err).NotTo(HaveOccurred())
		Expect(actual.Status.FreeCapacity.Cmp(resource.MustParse("8Gi"))).To(Equal(0))
	})

	It("clamps oversubscription at zero", func() {
		createDevice()
		Expect(k8sClient.Create(ctx, &storagev1alpha1.NVMePartition{
			ObjectMeta: metav1.ObjectMeta{Name: "capacity-a", Namespace: namespace},
			Spec:       assignedSpec("11Gi", "capacity-serial"),
		})).To(Succeed())

		actual, err := reconcileDevice()
		Expect(err).NotTo(HaveOccurred())
		Expect(actual.Status.FreeCapacity.IsZero()).To(BeTrue())
	})

	It("subtracts the upward-rounded allocation size", func() {
		createDevice()
		Expect(k8sClient.Create(ctx, &storagev1alpha1.NVMePartition{
			ObjectMeta: metav1.ObjectMeta{Name: "capacity-a", Namespace: namespace},
			Spec:       assignedSpec("1048577", "capacity-serial"),
		})).To(Succeed())

		actual, err := reconcileDevice()
		Expect(err).NotTo(HaveOccurred())
		want := resource.MustParse("10735321088") // 10 GiB - 2 MiB
		Expect(actual.Status.FreeCapacity.Cmp(want)).To(Equal(0))
	})

	It("rejects malformed negative allocations instead of increasing capacity", func() {
		partitions := []storagev1alpha1.NVMePartition{{
			ObjectMeta: metav1.ObjectMeta{Name: "capacity-negative", Namespace: namespace},
			Spec:       assignedSpec("-1Gi", "capacity-serial"),
		}}
		used, err := allocatedCapacityForDevice(partitions, "capacity-serial")
		Expect(err).To(HaveOccurred())
		Expect(used).To(BeZero())
	})

	It("rejects unsafe discovered-device identity and capacity at admission", func() {
		valid := storagev1alpha1.NVMeDeviceSpec{
			NodeName:      "distort-worker-1",
			PCIAddress:    "0000:01:00.0",
			SerialNumber:  "safe-serial",
			TotalCapacity: resource.MustParse("1Gi"),
		}
		tests := []struct {
			name   string
			mutate func(*storagev1alpha1.NVMeDeviceSpec)
		}{
			{name: "empty-node", mutate: func(spec *storagev1alpha1.NVMeDeviceSpec) { spec.NodeName = "" }},
			{name: "empty-serial", mutate: func(spec *storagev1alpha1.NVMeDeviceSpec) { spec.SerialNumber = "" }},
			{name: "invalid-pci", mutate: func(spec *storagev1alpha1.NVMeDeviceSpec) { spec.PCIAddress = "not-pci" }},
			{name: "zero-capacity", mutate: func(spec *storagev1alpha1.NVMeDeviceSpec) {
				spec.TotalCapacity = resource.MustParse("0")
			}},
		}

		for _, test := range tests {
			spec := valid
			test.mutate(&spec)
			err := k8sClient.Create(ctx, &storagev1alpha1.NVMeDevice{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-device-" + test.name},
				Spec:       spec,
			})
			Expect(err).To(HaveOccurred(), test.name)
		}
	})
})
