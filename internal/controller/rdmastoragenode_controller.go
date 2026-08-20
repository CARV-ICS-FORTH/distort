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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "distort/api/v1alpha1"
	"distort/internal/rdmahealth"
)

// RDMAStorageNodeReconciler reconciles a RDMAStorageNode object
type RDMAStorageNodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=storage.distort.io,resources=rdmastoragenodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.distort.io,resources=rdmastoragenodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=storage.distort.io,resources=rdmastoragenodes/finalizers,verbs=update

// Reconcile expires reporter readiness when its heartbeat becomes stale.
func (r *RDMAStorageNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)
	var node storagev1alpha1.RDMAStorageNode
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	condition := meta.FindStatusCondition(node.Status.Conditions, rdmahealth.ReadyCondition)
	if condition != nil && condition.Status == metav1.ConditionTrue &&
		(time.Since(node.Status.LastHeartbeatTime.Time) > rdmahealth.FreshnessWindow || node.Status.LastHeartbeatTime.IsZero()) {
		base := node.DeepCopy()
		meta.SetStatusCondition(&node.Status.Conditions, metav1.Condition{
			Type: rdmahealth.ReadyCondition, Status: metav1.ConditionFalse,
			ObservedGeneration: node.Generation, Reason: "StaleHeartbeat",
			Message: "The RDMA reporter heartbeat is stale",
		})
		if err := r.Status().Patch(ctx, &node, client.MergeFrom(base)); err != nil {
			logger.Error(err, "Failed to expire RDMAStorageNode readiness", "node", node.Name)
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: rdmahealth.FreshnessWindow / 2}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RDMAStorageNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.RDMAStorageNode{}).
		Named("rdmastoragenode").
		Complete(r)
}
