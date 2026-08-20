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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "distort/api/v1alpha1"
)

var _ = Describe("NVMeDeviceClaim lifecycle", func() {
	const (
		namespace       = "default"
		claimSuiteLabel = "claim"
	)
	ctx := context.Background()

	AfterEach(func() {
		var partitions storagev1alpha1.NVMePartitionList
		Expect(k8sClient.List(ctx, &partitions, client.InNamespace(namespace))).To(Succeed())
		for i := range partitions.Items {
			if partitions.Items[i].Labels["test.distort.io/suite"] == claimSuiteLabel {
				partitions.Items[i].Finalizers = nil
				_ = k8sClient.Update(ctx, &partitions.Items[i])
				_ = k8sClient.Delete(ctx, &partitions.Items[i])
			}
		}
		var claims storagev1alpha1.NVMeDeviceClaimList
		Expect(k8sClient.List(ctx, &claims, client.InNamespace(namespace))).To(Succeed())
		for i := range claims.Items {
			if claims.Items[i].Labels["test.distort.io/suite"] == claimSuiteLabel {
				claims.Items[i].Finalizers = nil
				_ = k8sClient.Update(ctx, &claims.Items[i])
				_ = k8sClient.Delete(ctx, &claims.Items[i])
			}
		}
		var devices storagev1alpha1.NVMeDeviceList
		Expect(k8sClient.List(ctx, &devices)).To(Succeed())
		for i := range devices.Items {
			if devices.Items[i].Labels["test.distort.io/suite"] == claimSuiteLabel {
				_ = k8sClient.Delete(ctx, &devices.Items[i])
			}
		}
	})

	createDevice := func(name, serial, node string, state storagev1alpha1.NVMeDeviceState) {
		device := &storagev1alpha1.NVMeDevice{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"test.distort.io/suite": claimSuiteLabel}},
			Spec: storagev1alpha1.NVMeDeviceSpec{
				NodeName:      node,
				PCIAddress:    "0000:02:00.0",
				SerialNumber:  serial,
				TotalCapacity: resource.MustParse("2Gi"),
			},
		}
		Expect(k8sClient.Create(ctx, device)).To(Succeed())
		device.Status.State = state
		device.Status.FreeCapacity = device.Spec.TotalCapacity
		Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())
	}

	createClaim := func(name, serial string) {
		Expect(k8sClient.Create(ctx, &storagev1alpha1.NVMeDeviceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    map[string]string{"test.distort.io/suite": claimSuiteLabel},
			},
			Spec: storagev1alpha1.NVMeDeviceClaimSpec{SerialNumber: serial},
		})).To(Succeed())
	}

	reconcileClaim := func(name string) (reconcile.Result, error) {
		r := &NVMeDeviceClaimReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		return r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}})
	}

	It("adds a finalizer and binds an available matching device", func() {
		createDevice("claim-device", "claim-serial", "node-a", storagev1alpha1.NVMeDeviceStateAvailable)
		createClaim("claim-request", "claim-serial")

		_, err := reconcileClaim("claim-request")
		if err != nil {
			// An update conflict after adding the finalizer is retryable.
			_, err = reconcileClaim("claim-request")
		}
		Expect(err).NotTo(HaveOccurred())

		var claim storagev1alpha1.NVMeDeviceClaim
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "claim-request", Namespace: namespace}, &claim)).To(Succeed())
		Expect(claim.Finalizers).To(ContainElement("storage.distort.io/claim-cleanup"))
		Expect(claim.Status.Active).To(BeTrue())
		Expect(claim.Status.MatchedDevice).To(Equal("claim-device"))
		Expect(claim.Status.NodeName).To(Equal("node-a"))

		var device storagev1alpha1.NVMeDevice
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "claim-device"}, &device)).To(Succeed())
		Expect(device.Status.State).To(Equal(storagev1alpha1.NVMeDeviceStateClaimed))
		Expect(device.Status.ClaimRef).NotTo(BeNil())
		Expect(device.Status.ClaimRef.Namespace).To(Equal(namespace))
		Expect(device.Status.ClaimRef.Name).To(Equal(claim.Name))
		Expect(device.Status.ClaimRef.UID).To(Equal(claim.UID))
	})

	It("removes its finalizer when the matched device is already absent", func() {
		createClaim("claim-delete", "gone-serial")
		_, err := reconcileClaim("claim-delete")
		Expect(err).NotTo(HaveOccurred())

		var claim storagev1alpha1.NVMeDeviceClaim
		key := types.NamespacedName{Name: "claim-delete", Namespace: namespace}
		Expect(k8sClient.Get(ctx, key, &claim)).To(Succeed())
		claim.Status.Active = true
		claim.Status.MatchedDevice = "missing-device"
		Expect(k8sClient.Status().Update(ctx, &claim)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &claim)).To(Succeed())

		_, err = reconcileClaim("claim-delete")
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			err := k8sClient.Get(ctx, key, &storagev1alpha1.NVMeDeviceClaim{})
			return errors.IsNotFound(err)
		}).Should(BeTrue())
	})

	It("requeues a claim when its hardware has not appeared yet", func() {
		createClaim("claim-late-device", "late-serial")

		result, err := reconcileClaim("claim-late-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).NotTo(BeZero(), "the controller does not watch NVMeDevice creation")
	})

	It("refreshes an active claim after the device moves to another node", func() {
		createDevice("claim-moving-device", "moving-serial", "node-old", storagev1alpha1.NVMeDeviceStateClaimed)
		createClaim("claim-moving", "moving-serial")
		var claim storagev1alpha1.NVMeDeviceClaim
		key := types.NamespacedName{Name: "claim-moving", Namespace: namespace}
		Expect(k8sClient.Get(ctx, key, &claim)).To(Succeed())
		claim.Finalizers = []string{"storage.distort.io/claim-cleanup"}
		Expect(k8sClient.Update(ctx, &claim)).To(Succeed())
		claim.Status.Active = true
		claim.Status.MatchedDevice = "claim-moving-device"
		claim.Status.NodeName = "node-old"
		Expect(k8sClient.Status().Update(ctx, &claim)).To(Succeed())

		var device storagev1alpha1.NVMeDevice
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "claim-moving-device"}, &device)).To(Succeed())
		device.Spec.NodeName = "node-new"
		Expect(k8sClient.Update(ctx, &device)).To(Succeed())
		_, err := reconcileClaim("claim-moving")
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, &claim)).To(Succeed())
		Expect(claim.Status.NodeName).To(Equal("node-new"))
	})

	It("does not release hardware now owned by a replacement claim", func() {
		createDevice("claim-shared-device", "shared-serial", "node-a", storagev1alpha1.NVMeDeviceStateClaimed)
		createClaim("claim-old", "shared-serial")
		createClaim("claim-replacement", "shared-serial")

		for _, name := range []string{"claim-old", "claim-replacement"} {
			var claim storagev1alpha1.NVMeDeviceClaim
			key := types.NamespacedName{Name: name, Namespace: namespace}
			Expect(k8sClient.Get(ctx, key, &claim)).To(Succeed())
			claim.Finalizers = []string{"storage.distort.io/claim-cleanup"}
			Expect(k8sClient.Update(ctx, &claim)).To(Succeed())
			claim.Status.Active = true
			claim.Status.MatchedDevice = "claim-shared-device"
			Expect(k8sClient.Status().Update(ctx, &claim)).To(Succeed())
		}

		var old storagev1alpha1.NVMeDeviceClaim
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "claim-old", Namespace: namespace}, &old)).To(Succeed())
		var replacement storagev1alpha1.NVMeDeviceClaim
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "claim-replacement", Namespace: namespace}, &replacement)).To(Succeed())
		var ownedDevice storagev1alpha1.NVMeDevice
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "claim-shared-device"}, &ownedDevice)).To(Succeed())
		ownedDevice.Status.ClaimRef = &storagev1alpha1.NVMeDeviceClaimReference{
			Namespace: replacement.Namespace, Name: replacement.Name, UID: replacement.UID,
		}
		Expect(k8sClient.Status().Update(ctx, &ownedDevice)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &old)).To(Succeed())
		_, err := reconcileClaim("claim-old")
		Expect(err).NotTo(HaveOccurred())

		var device storagev1alpha1.NVMeDevice
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "claim-shared-device"}, &device)).To(Succeed())
		Expect(device.Status.State).To(Equal(storagev1alpha1.NVMeDeviceStateClaimed))
		Expect(device.Status.ClaimRef.UID).To(Equal(replacement.UID))
	})

	It("fails closed when multiple available devices report the same serial", func() {
		createDevice("claim-duplicate-a", "duplicate-serial", "node-a", storagev1alpha1.NVMeDeviceStateAvailable)
		createDevice("claim-duplicate-b", "duplicate-serial", "node-b", storagev1alpha1.NVMeDeviceStateAvailable)
		createClaim("claim-duplicate", "duplicate-serial")

		result, err := reconcileClaim("claim-duplicate")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).NotTo(BeZero())
		var claim storagev1alpha1.NVMeDeviceClaim
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "claim-duplicate", Namespace: namespace}, &claim)).To(Succeed())
		Expect(claim.Status.Active).To(BeFalse())
		condition := meta.FindStatusCondition(claim.Status.Conditions, claimBoundCondition)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Reason).To(Equal("AmbiguousDevices"))
		for _, name := range []string{"claim-duplicate-a", "claim-duplicate-b"} {
			var device storagev1alpha1.NVMeDevice
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, &device)).To(Succeed())
			Expect(device.Status.ClaimRef).To(BeNil())
		}
	})

	It("retains ownership but deactivates the claim while hardware is unavailable", func() {
		createDevice("claim-missing-device", "missing-serial", "node-a", storagev1alpha1.NVMeDeviceStateUnavailable)
		createClaim("claim-missing", "missing-serial")
		var claim storagev1alpha1.NVMeDeviceClaim
		key := types.NamespacedName{Name: "claim-missing", Namespace: namespace}
		Expect(k8sClient.Get(ctx, key, &claim)).To(Succeed())
		claim.Finalizers = []string{claimFinalizer}
		Expect(k8sClient.Update(ctx, &claim)).To(Succeed())
		claim.Status.Active = true
		claim.Status.MatchedDevice = "claim-missing-device"
		claim.Status.NodeName = "node-a"
		Expect(k8sClient.Status().Update(ctx, &claim)).To(Succeed())
		var device storagev1alpha1.NVMeDevice
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "claim-missing-device"}, &device)).To(Succeed())
		device.Status.ClaimRef = &storagev1alpha1.NVMeDeviceClaimReference{Namespace: namespace, Name: claim.Name, UID: claim.UID}
		Expect(k8sClient.Status().Update(ctx, &device)).To(Succeed())

		_, err := reconcileClaim(claim.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, &claim)).To(Succeed())
		Expect(claim.Status.Active).To(BeFalse())
		Expect(claim.Status.MatchedDevice).To(Equal(device.Name))
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: device.Name}, &device)).To(Succeed())
		Expect(device.Status.ClaimRef.UID).To(Equal(claim.UID))
	})

	It("keeps its finalizer while a dependent partition exists", func() {
		createClaim("claim-with-partition", "partition-serial")
		_, err := reconcileClaim("claim-with-partition")
		Expect(err).NotTo(HaveOccurred())
		var claim storagev1alpha1.NVMeDeviceClaim
		key := types.NamespacedName{Name: "claim-with-partition", Namespace: namespace}
		Expect(k8sClient.Get(ctx, key, &claim)).To(Succeed())
		partition := &storagev1alpha1.NVMePartition{
			ObjectMeta: metav1.ObjectMeta{Name: "claim-dependent", Namespace: namespace, Labels: map[string]string{"test.distort.io/suite": claimSuiteLabel}},
			Spec: storagev1alpha1.NVMePartitionSpec{
				Size: resource.MustParse("1Gi"), NodeName: "node-a", ParentDeviceSerialNumber: claim.Spec.SerialNumber,
				ClaimRef: &storagev1alpha1.NVMeDeviceClaimReference{Namespace: namespace, Name: claim.Name, UID: claim.UID},
			},
		}
		Expect(k8sClient.Create(ctx, partition)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &claim)).To(Succeed())

		result, err := reconcileClaim(claim.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).NotTo(BeZero())
		Expect(k8sClient.Get(ctx, key, &claim)).To(Succeed())
		Expect(claim.Finalizers).To(ContainElement(claimFinalizer))
	})
})
