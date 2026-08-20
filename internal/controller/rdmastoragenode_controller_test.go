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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/rdmahealth"
)

var _ = Describe("RDMAStorageNode Controller", func() {
	var reconciler *RDMAStorageNodeReconciler

	BeforeEach(func() {
		reconciler = &RDMAStorageNodeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	})

	It("leaves reporter-owned node data unchanged", func() {
		ctx := context.Background()
		key := types.NamespacedName{Name: "reporter-owned-node"}
		resource := &storagev1alpha1.RDMAStorageNode{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name},
			Spec: storagev1alpha1.RDMAStorageNodeSpec{
				NodeName:  "test-node",
				RDMAIP:    "192.0.2.10",
				Transport: storagev1alpha1.RDMATransportRoCEv2,
			},
		}
		Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, resource) })

		result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(rdmahealth.FreshnessWindow / 2))

		actual := &storagev1alpha1.RDMAStorageNode{}
		Expect(k8sClient.Get(ctx, key, actual)).To(Succeed())
		Expect(actual.Spec).To(Equal(resource.Spec))
	})

	It("expires a stale reporter heartbeat", func() {
		ctx := context.Background()
		key := types.NamespacedName{Name: "stale-rdma-node"}
		resource := &storagev1alpha1.RDMAStorageNode{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name},
			Spec: storagev1alpha1.RDMAStorageNodeSpec{
				NodeName: "test-node", RDMAIP: "192.0.2.10", Transport: storagev1alpha1.RDMATransportRoCEv2,
			},
		}
		Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, resource) })
		resource.Status.LastHeartbeatTime = metav1.NewTime(time.Now().Add(-2 * rdmahealth.FreshnessWindow))
		meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
			Type: rdmahealth.ReadyCondition, Status: metav1.ConditionTrue, Reason: "ReporterReady", Message: "ready",
		})
		Expect(k8sClient.Status().Update(ctx, resource)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
		condition := meta.FindStatusCondition(resource.Status.Conditions, rdmahealth.ReadyCondition)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal("StaleHeartbeat"))
	})

	It("treats a deleted reporter-owned node as an idempotent no-op", func() {
		result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "missing-rdma-node"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
	})
})
