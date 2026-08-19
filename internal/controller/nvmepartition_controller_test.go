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
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "distort/api/v1alpha1"
)

type conflictOnceClient struct {
	client.Client
	conflicts atomic.Int32
}

func (c *conflictOnceClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, ok := obj.(*storagev1alpha1.NVMePartition); ok && c.conflicts.CompareAndSwap(0, 1) {
		return apierrors.NewConflict(
			schema.GroupResource{Group: "storage.distort.io", Resource: "nvmepartitions"},
			obj.GetName(),
			errors.New("injected update conflict"),
		)
	}
	return c.Client.Update(ctx, obj, opts...)
}

type listRejectingClient struct {
	client.Client
}

func (c *listRejectingClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("cached list must not be used for placement")
}

var _ = Describe("NVMePartition placement", func() {
	const namespace = "default"
	ctx := context.Background()
	var reconciler *NVMePartitionReconciler

	BeforeEach(func() {
		reconciler = &NVMePartitionReconciler{
			Client:    k8sClient,
			APIReader: k8sClient,
			Scheme:    k8sClient.Scheme(),
		}
	})

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

	createDeviceWithTotal := func(name, node, serial, total, free, backend string, state storagev1alpha1.NVMeDeviceState) {
		device := &storagev1alpha1.NVMeDevice{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"test.distort.io/suite": "placement"}},
			Spec: storagev1alpha1.NVMeDeviceSpec{
				NodeName:      node,
				PCIAddress:    "0000:01:00.0",
				SerialNumber:  serial,
				TotalCapacity: resource.MustParse(total),
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
	createDevice := func(name, node, serial, free, backend string, state storagev1alpha1.NVMeDeviceState) {
		createDeviceWithTotal(name, node, serial, "10Gi", free, backend, state)
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
	createAssignedPartition := func(name, size, node, serial string, finalizers []string) {
		partition := &storagev1alpha1.NVMePartition{
			ObjectMeta: metav1.ObjectMeta{
				Name:       name,
				Namespace:  namespace,
				Labels:     map[string]string{"test.distort.io/suite": "placement"},
				Finalizers: finalizers,
			},
			Spec: storagev1alpha1.NVMePartitionSpec{
				Size:                     resource.MustParse(size),
				NodeName:                 node,
				ParentDeviceSerialNumber: serial,
				ClaimRef: &storagev1alpha1.NVMeDeviceClaimReference{
					Namespace: namespace,
					Name:      "placement-claim",
					UID:       types.UID("placement-claim-uid"),
				},
			},
		}
		Expect(k8sClient.Create(ctx, partition)).To(Succeed())
	}

	reconcilePartition := func(name string) (reconcile.Result, error) {
		return reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}})
	}

	It("chooses the claimed compatible device with the most free capacity", func() {
		createDeviceWithTotal("placement-small", "node-small", "serial-small", "3Gi", "3Gi", "", storagev1alpha1.NVMeDeviceStateClaimed)
		createDeviceWithTotal("placement-large", "node-large", "serial-large", "8Gi", "8Gi", "spdk", storagev1alpha1.NVMeDeviceStateClaimed)
		createDeviceWithTotal("placement-available", "node-available", "serial-available", "10Gi", "10Gi", "", storagev1alpha1.NVMeDeviceStateAvailable)
		createDeviceWithTotal("placement-kernel", "node-kernel", "serial-kernel", "9Gi", "9Gi", "kernel", storagev1alpha1.NVMeDeviceStateClaimed)
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
		createDeviceWithTotal("placement-small", "node-small", "serial-small", "1Gi", "1Gi", "", storagev1alpha1.NVMeDeviceStateClaimed)
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

	It("reserves the upward-rounded allocation size", func() {
		createDeviceWithTotal("placement-rounded", "node-rounded", "serial-rounded", "1048577", "1048577", "", storagev1alpha1.NVMeDeviceStateClaimed)
		createPartition("placement-request", "1048577", "spdk")

		result, err := reconcilePartition("placement-request")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).NotTo(BeZero())

		var actual storagev1alpha1.NVMePartition
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "placement-request", Namespace: namespace}, &actual)).To(Succeed())
		Expect(actual.Spec.NodeName).To(BeEmpty())
	})

	It("uses assigned partitions instead of stale reported free capacity", func() {
		createDevice("placement-stale-high", "node-stale", "serial-stale", "10Gi", "", storagev1alpha1.NVMeDeviceStateClaimed)
		createAssignedPartition("placement-existing", "9500Mi", "node-stale", "serial-stale", nil)
		createPartition("placement-request", "1Gi", "spdk")

		result, err := reconcilePartition("placement-request")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).NotTo(BeZero())

		var actual storagev1alpha1.NVMePartition
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "placement-request", Namespace: namespace}, &actual)).To(Succeed())
		Expect(actual.Spec.NodeName).To(BeEmpty())
	})

	It("can reserve capacity before stale reported free capacity catches up", func() {
		createDevice("placement-stale-low", "node-stale", "serial-stale", "0", "", storagev1alpha1.NVMeDeviceStateClaimed)
		createPartition("placement-request", "1Gi", "spdk")

		result, err := reconcilePartition("placement-request")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())

		var actual storagev1alpha1.NVMePartition
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "placement-request", Namespace: namespace}, &actual)).To(Succeed())
		Expect(actual.Spec.ParentDeviceSerialNumber).To(Equal("serial-stale"))
	})

	It("uses the uncached API reader for placement lists", func() {
		createDevice("placement-reader", "node-reader", "serial-reader", "10Gi", "", storagev1alpha1.NVMeDeviceStateClaimed)
		createPartition("placement-request", "1Gi", "spdk")
		reconciler = &NVMePartitionReconciler{
			Client:    &listRejectingClient{Client: k8sClient},
			APIReader: k8sClient,
			Scheme:    k8sClient.Scheme(),
		}

		result, err := reconcilePartition("placement-request")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())

		var actual storagev1alpha1.NVMePartition
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "placement-request", Namespace: namespace}, &actual)).To(Succeed())
		Expect(actual.Spec.ParentDeviceSerialNumber).To(Equal("serial-reader"))
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

	It("never assigns concurrent reservations beyond device capacity", Label("release-gate", "capacity-concurrency"), func() {
		createDeviceWithTotal("placement-one-device", "node-one", "serial-one", "1Gi", "1Gi", "", storagev1alpha1.NVMeDeviceStateClaimed)
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
		var reservedBytes int64
		for _, name := range []string{"placement-concurrent-a", "placement-concurrent-b"} {
			var actual storagev1alpha1.NVMePartition
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &actual)).To(Succeed())
			if actual.Spec.ParentDeviceSerialNumber == "serial-one" {
				assigned++
				reservedBytes += actual.Spec.Size.Value()
			}
		}
		Expect(assigned).To(Equal(1), "exactly one of two 700Mi reservations should fit on a 1Gi device")
		capacity := resource.MustParse("1Gi")
		Expect(reservedBytes).To(BeNumerically("<=", capacity.Value()))
	})

	It("retries a conflicting partition reservation", func() {
		createDevice("placement-conflict", "node-conflict", "serial-conflict", "10Gi", "", storagev1alpha1.NVMeDeviceStateClaimed)
		createPartition("placement-request", "1Gi", "spdk")
		conflictingClient := &conflictOnceClient{Client: k8sClient}
		reconciler = &NVMePartitionReconciler{
			Client:    conflictingClient,
			APIReader: k8sClient,
			Scheme:    k8sClient.Scheme(),
		}

		result, err := reconcilePartition("placement-request")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())
		Expect(conflictingClient.conflicts.Load()).To(Equal(int32(1)))

		var actual storagev1alpha1.NVMePartition
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "placement-request", Namespace: namespace}, &actual)).To(Succeed())
		Expect(actual.Spec.ParentDeviceSerialNumber).To(Equal("serial-conflict"))
	})

	It("keeps terminating partitions reserved until cleanup completes", func() {
		createDevice("placement-delete", "node-delete", "serial-delete", "10Gi", "", storagev1alpha1.NVMeDeviceStateClaimed)
		createAssignedPartition("placement-terminating", "9Gi", "node-delete", "serial-delete", []string{"test.distort.io/cleanup"})
		var terminating storagev1alpha1.NVMePartition
		terminatingKey := types.NamespacedName{Name: "placement-terminating", Namespace: namespace}
		Expect(k8sClient.Get(ctx, terminatingKey, &terminating)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &terminating)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, terminatingKey, &terminating)).To(Succeed())
			g.Expect(terminating.DeletionTimestamp.IsZero()).To(BeFalse())
		}).Should(Succeed())

		createPartition("placement-request", "2Gi", "spdk")
		result, err := reconcilePartition("placement-request")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).NotTo(BeZero())

		var actual storagev1alpha1.NVMePartition
		requestKey := types.NamespacedName{Name: "placement-request", Namespace: namespace}
		Expect(k8sClient.Get(ctx, requestKey, &actual)).To(Succeed())
		Expect(actual.Spec.NodeName).To(BeEmpty())

		terminating.Finalizers = nil
		Expect(k8sClient.Update(ctx, &terminating)).To(Succeed())
		Eventually(func() bool {
			err := k8sClient.Get(ctx, terminatingKey, &terminating)
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())

		result, err = reconcilePartition("placement-request")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())
		Expect(k8sClient.Get(ctx, requestKey, &actual)).To(Succeed())
		Expect(actual.Spec.ParentDeviceSerialNumber).To(Equal("serial-delete"))
	})
})
