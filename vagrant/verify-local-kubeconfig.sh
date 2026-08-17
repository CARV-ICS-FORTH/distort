#!/usr/bin/env bash
set -euo pipefail

kubeconfig_path="${1:-}"
kubectl_bin="${KUBECTL:-kubectl}"
expected_server="https://192.168.56.10:6443"

if [[ -z "${kubeconfig_path}" || ! -f "${kubeconfig_path}" ]]; then
  echo "ERROR: local Vagrant kubeconfig was not found at ${kubeconfig_path:-<empty>}" >&2
  echo "Run 'make get-kubeconfig' after the VMs are ready." >&2
  exit 1
fi

actual_server="$(${kubectl_bin} --kubeconfig "${kubeconfig_path}" config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
if [[ "${actual_server}" != "${expected_server}" ]]; then
  echo "ERROR: refusing to mutate Kubernetes cluster ${actual_server}" >&2
  echo "Expected the isolated DISTORT Vagrant cluster at ${expected_server}." >&2
  exit 1
fi

node_check_error=""
for attempt in $(seq 1 6); do
  if node_check_error="$(${kubectl_bin} --kubeconfig "${kubeconfig_path}" --request-timeout=10s get node distort-master -o name 2>&1)"; then
    node_check_error=""
    break
  fi
  [[ "${attempt}" -eq 6 ]] || sleep 5
done

if [[ -n "${node_check_error}" ]]; then
  echo "ERROR: kubeconfig points at ${expected_server}, but the isolated cluster did not confirm node distort-master." >&2
  echo "Last kubectl error: ${node_check_error}" >&2
  exit 1
fi

echo "Verified isolated DISTORT Vagrant cluster at ${expected_server}."
