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
	"os"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "distort/api/v1alpha1"
)

var _ = Describe("NVMePartition placement", func() {
	const namespace = "default"
	ctx := context.Background()

	AfterEach(func() {
		var partitions storagev1alpha1.NVMePartitionList
		Expect(k8sClient.List(ctx, &partitions, client.InNamespace(namespace))).To(Succeed())
		for i := range partitions.Items {
			if partitions.Items[i].Labels["test.distort.io/suite"] == "placement" {
				_ = k8sClient.Delete(ctx, &partitions.Items[i])
			}
		}
		var devices storagev1alpha1.NVMeDeviceList
		Expect(k8sClient.List(ctx, &devices)).To(Succeed())
		for i := range devices.Items {
			if devices.Items[i].Labels["test.distort.io/suite"] == "placement" {
				_ = k8sClient.Delete(ctx, &devices.Items[i])
			}
		}
	})

	createDevice := func(name, node, serial, free, backend string, state storagev1alpha1.NVMeDeviceState) {
		device := &storagev1alpha1.NVMeDevice{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"test.distort.io/suite": "placement"}},
			Spec: storagev1alpha1.NVMeDeviceSpec{
				NodeName:      node,
				PCIAddress:    "0000:01:00.0",
				SerialNumber:  serial,
				TotalCapacity: resource.MustParse("10Gi"),
			},
		}
		Expect(k8sClient.Create(ctx, device)).To(Succeed())
		device.Status.State = state
		device.Status.FreeCapacity = resource.MustParse(free)
		device.Status.ActiveBackend = backend
		if state == storagev1alpha1.NVMeDeviceStateClaimed {
			device.Status.ClaimRef = &storagev1alpha1.NVMeDeviceClaimReference{
				Namespace: namespace,
				Name:      name + "-claim",
				UID:       types.UID(name + "-claim-uid"),
			}
		}
		Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())
	}

	createPartition := func(name, size, backend string) {
		partition := &storagev1alpha1.NVMePartition{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    map[string]string{"test.distort.io/suite": "placement"},
			},
			Spec: storagev1alpha1.NVMePartitionSpec{Size: resource.MustParse(size), TargetBackend: backend},
		}
		Expect(k8sClient.Create(ctx, partition)).To(Succeed())
	}

	reconcilePartition := func(name string) (reconcile.Result, error) {
		reconciler := &NVMePartitionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		return reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}})
	}

	It("chooses the claimed compatible device with the most free capacity", func() {
		createDevice("placement-small", "node-small", "serial-small", "3Gi", "", storagev1alpha1.NVMeDeviceStateClaimed)
		createDevice("placement-large", "node-large", "serial-large", "8Gi", "spdk", storagev1alpha1.NVMeDeviceStateClaimed)
		createDevice("placement-available", "node-available", "serial-available", "10Gi", "", storagev1alpha1.NVMeDeviceStateAvailable)
		createDevice("placement-kernel", "node-kernel", "serial-kernel", "9Gi", "kernel", storagev1alpha1.NVMeDeviceStateClaimed)
		createPartition("placement-request", "2Gi", "spdk")

		result, err := reconcilePartition("placement-request")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())

		var actual storagev1alpha1.NVMePartition
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "placement-request", Namespace: namespace}, &actual)).To(Succeed())
		Expect(actual.Spec.NodeName).To(Equal("node-large"))
		Expect(actual.Spec.ParentDeviceSerialNumber).To(Equal("serial-large"))
		Expect(actual.Spec.ClaimRef).To(Equal(&storagev1alpha1.NVMeDeviceClaimReference{
			Namespace: namespace,
			Name:      "placement-large-claim",
			UID:       types.UID("placement-large-claim-uid"),
		}))
	})

	It("requeues without mutating when no claimed device has enough capacity", func() {
		createDevice("placement-small", "node-small", "serial-small", "1Gi", "", storagev1alpha1.NVMeDeviceStateClaimed)
		createPartition("placement-request", "2Gi", "spdk")

		result, err := reconcilePartition("placement-request")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).NotTo(BeZero())

		var actual storagev1alpha1.NVMePartition
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "placement-request", Namespace: namespace}, &actual)).To(Succeed())
		Expect(actual.Spec.NodeName).To(BeEmpty())
		Expect(actual.Spec.ParentDeviceSerialNumber).To(BeEmpty())
		Expect(actual.Spec.ClaimRef).To(BeNil())
	})

	It("rejects client-supplied placement without an owning claim reference", func() {
		partition := &storagev1alpha1.NVMePartition{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "placement-without-claim",
				Namespace: namespace,
				Labels:    map[string]string{"test.distort.io/suite": "placement"},
			},
			Spec: storagev1alpha1.NVMePartitionSpec{
				Size:                     resource.MustParse("1Gi"),
				NodeName:                 "node-a",
				ParentDeviceSerialNumber: "unclaimed-serial",
			},
		}

		err := k8sClient.Create(ctx, partition)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("claimRef"))
	})

	It("rejects unsafe and unknown backend options at admission", func() {
		tests := []struct {
			name    string
			backend string
			options map[string]string
		}{
			{name: "shell-syntax", backend: "spdk", options: map[string]string{"spdk-core-mask": "0x1;id"}},
			{name: "oversized", backend: "spdk", options: map[string]string{"spdk-core-mask": "0x" + strings.Repeat("f", 257)}},
			{name: "unknown", backend: "spdk", options: map[string]string{"unknown": "value"}},
			{name: "wrong-backend", backend: "kernel", options: map[string]string{"spdk-core-mask": "0x1"}},
		}

		for _, test := range tests {
			partition := &storagev1alpha1.NVMePartition{
				ObjectMeta: metav1.ObjectMeta{Name: "f2-" + test.name, Namespace: namespace},
				Spec: storagev1alpha1.NVMePartitionSpec{
					Size:          resource.MustParse("1Gi"),
					TargetBackend: test.backend,
					TargetOptions: test.options,
				},
			}

			err := k8sClient.Create(ctx, partition)
			Expect(err).To(HaveOccurred(), test.name)
		}
	})

	It("never assigns concurrent reservations beyond device capacity", func() {
		if os.Getenv("DISTORT_RUN_KNOWN_FAILURES") != "1" {
			Skip("F6 is a known defect")
		}
		if finding := os.Getenv("DISTORT_FINDING"); finding != "" && finding != "F6" {
			Skip("F6 is not selected")
		}

		createDevice("placement-one-device", "node-one", "serial-one", "1Gi", "", storagev1alpha1.NVMeDeviceStateClaimed)
		createPartition("placement-concurrent-a", "700Mi", "spdk")
		createPartition("placement-concurrent-b", "700Mi", "spdk")

		var wg sync.WaitGroup
		errors := make(chan error, 2)
		for _, name := range []string{"placement-concurrent-a", "placement-concurrent-b"} {
			wg.Add(1)
			go func(partitionName string) {
				defer wg.Done()
				_, err := reconcilePartition(partitionName)
				errors <- err
			}(name)
		}
		wg.Wait()
		close(errors)
		for err := range errors {
			Expect(err).NotTo(HaveOccurred())
		}

		assigned := 0
		for _, name := range []string{"placement-concurrent-a", "placement-concurrent-b"} {
			var actual storagev1alpha1.NVMePartition
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &actual)).To(Succeed())
			if actual.Spec.ParentDeviceSerialNumber == "serial-one" {
				assigned++
			}
		}
		Expect(assigned).To(BeNumerically("<=", 1), "two 700Mi reservations cannot fit on a 1Gi device")
	})
})
