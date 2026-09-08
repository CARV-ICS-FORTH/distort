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

if [[ "$device_count" -lt "${#expected_nodes[@]}" ]]; then
  echo "ERROR: expected at least one NVMeDevice per node, found $device_count total" >&2
  exit 1
fi

echo "Validating fresh, usable RDMA endpoints"
for node in "${expected_nodes[@]}"; do
  kube wait --for=condition=Ready "rdmastoragenode/$node" --timeout=90s
  kube wait --for=condition=NVMeInventoryReady "rdmastoragenode/$node" --timeout=90s
  endpoint="$(kube get "rdmastoragenode/$node" -o jsonpath='{.spec.nodeName}{"|"}{.spec.rdmaIP}{"|"}{.spec.transport}{"|"}{.status.lastHeartbeatTime}')"
  IFS='|' read -r reported_node rdma_ip transport heartbeat <<<"$endpoint"
  if [[ "$reported_node" != "$node" ]]; then
    echo "ERROR: RDMAStorageNode $node reports nodeName $reported_node" >&2
    exit 1
  fi
  case "$rdma_ip" in
    ""|0.0.0.0|127.*|::|::1)
      echo "ERROR: RDMAStorageNode $node has unusable RDMA IP $rdma_ip" >&2
      exit 1
      ;;
  esac
  if [[ "$transport" != "RoCEv2" && "$transport" != "InfiniBand" ]]; then
    echo "ERROR: RDMAStorageNode $node has unsupported transport $transport" >&2
    exit 1
  fi
  if ! heartbeat_epoch="$(date -u -d "$heartbeat" +%s 2>/dev/null)"; then
    echo "ERROR: RDMAStorageNode $node has invalid lastHeartbeatTime $heartbeat" >&2
    exit 1
  fi
  heartbeat_age="$(( $(date -u +%s) - heartbeat_epoch ))"
  if (( heartbeat_age < -5 || heartbeat_age > 45 )); then
    echo "ERROR: RDMAStorageNode $node heartbeat age is ${heartbeat_age}s, expected 0-45s" >&2
    exit 1
  fi
done

echo "Smoke test passed: $actual_node_count nodes, $device_count NVMeDevices, ${#expected_nodes[@]} ready RDMAStorageNodes with healthy NVMe inventory"
