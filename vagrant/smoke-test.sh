#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
kubeconfig_path="${1:-$REPO_ROOT/kubeconfig.yaml}"
kubectl_bin="${KUBECTL:-kubectl}"
expected_nodes=(distort-master distort-worker-1 distort-worker-2)

KUBECTL="$kubectl_bin" bash "$SCRIPT_DIR/verify-local-kubeconfig.sh" "$kubeconfig_path"

kube() {
  "$kubectl_bin" --kubeconfig "$kubeconfig_path" "$@"
}

echo "Waiting for all lab nodes"
kube wait --for=condition=Ready node --all --timeout=180s

actual_node_count="$(kube get nodes -o jsonpath='{.items[*].metadata.name}' | wc -w | tr -d ' ')"
if [[ "$actual_node_count" -ne "${#expected_nodes[@]}" ]]; then
  echo "ERROR: expected ${#expected_nodes[@]} nodes, found $actual_node_count" >&2
  exit 1
fi

for node in "${expected_nodes[@]}"; do
  kube get node "$node" >/dev/null
done

echo "Waiting for DISTORT rollouts"
kube rollout status -n distort-system deployment/distort-manager --timeout=180s
kube rollout status -n distort-system deployment/distort-csi-controller --timeout=180s
kube rollout status -n distort-system daemonset/distort-agent --timeout=180s
kube rollout status -n distort-system daemonset/distort-csi-node --timeout=180s

device_count="$(kube get nvmedevices -o jsonpath='{.items[*].metadata.name}' | wc -w | tr -d ' ')"
rdma_node_count="$(kube get rdmastoragenodes -o jsonpath='{.items[*].metadata.name}' | wc -w | tr -d ' ')"

if [[ "$device_count" -lt "${#expected_nodes[@]}" ]]; then
  echo "ERROR: expected at least one NVMeDevice per node, found $device_count total" >&2
  exit 1
fi

if [[ "$rdma_node_count" -lt "${#expected_nodes[@]}" ]]; then
  echo "ERROR: expected an RDMAStorageNode per node, found $rdma_node_count total" >&2
  exit 1
fi

echo "Smoke test passed: $actual_node_count nodes, $device_count NVMeDevices, $rdma_node_count RDMAStorageNodes"
