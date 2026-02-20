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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "distort/api/v1alpha1"
)

// NVMePartitionReconciler reconciles a NVMePartition object
type NVMePartitionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// SetupWithManager sets up the controller with the Manager.
func (r *NVMePartitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.NVMePartition{}).
		Named("nvmepartition").
		Complete(r)
}

// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmepartitions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmepartitions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=storage.distort.io,resources=nvmepartitions/finalizers,verbs=update
// +kubebuilder:rbac:groups=storage.distort.io,resources=rdmastoragenodes,verbs=get;list;watch

// Reconcile assigns unassigned NVMePartitions to optimal RDMAStorageNodes based on available free capacity.
func (r *NVMePartitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	var partition storagev1alpha1.NVMePartition
	if err := r.Get(ctx, req.NamespacedName, &partition); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// We only care about partitions that haven't been assigned a node yet.
	if partition.Spec.NodeName != "" {
		return ctrl.Result{}, nil
	}

	logger.Info("Finding suitable RDMAStorageNode for NVMePartition", "partition", partition.Name, "requestedSize", partition.Spec.Size.String())

	// List all RDMAStorageNodes
	var nodeList storagev1alpha1.RDMAStorageNodeList
	if err := r.List(ctx, &nodeList); err != nil {
		logger.Error(err, "unable to list RDMAStorageNodes")
		return ctrl.Result{}, err
	}

	var bestNode *storagev1alpha1.RDMAStorageNode
	var maxFree int64 = -1

	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		// Ensure the node has enough free capacity
		if node.Status.FreeCapacity.Cmp(partition.Spec.Size) >= 0 {
			freeBytes := node.Status.FreeCapacity.Value()
			// Simple strategy: pick the node with the most free capacity
			if freeBytes > maxFree {
				maxFree = freeBytes
				bestNode = node
			}
		}
	}

	if bestNode == nil {
		logger.Info("No suitable RDMAStorageNode found with enough free capacity", "partition", partition.Name)
		// We could wait and requeue, but there's no state change to trigger this unless a node updates.
		// So we won't requeue immediately, but rely on node watch events (if configured) or manual intervention.
		return ctrl.Result{}, nil
	}

	logger.Info("Assigning NVMePartition to RDMAStorageNode", "partition", partition.Name, "node", bestNode.Name)

	partition.Spec.NodeName = bestNode.Name
	if err := r.Update(ctx, &partition); err != nil {
		logger.Error(err, "unable to update NVMePartition with selected node", "partition", partition.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}
