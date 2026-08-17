#!/usr/bin/env bash
set -eo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
export KUBECONFIG="${KUBECONFIG:-$REPO_ROOT/kubeconfig.yaml}"

KUBECTL="${KUBECTL:-kubectl}"
EXPECTED_HUGEPAGES_2MI="${DISTORT_HUGEPAGES_2MI:-128}"
bash "$SCRIPT_DIR/verify-local-kubeconfig.sh" "$KUBECONFIG"

# Helper function to run vagrant ssh from either repository root or vagrant directory
vagrant_ssh() {
  local node="$1"
  local cmd="$2"
  if [ -f "Vagrantfile" ]; then
    vagrant ssh "$node" -c "$cmd"
  else
    (cd vagrant && vagrant ssh "$node" -c "$cmd")
  fi
}

echo "=== Verifying Kubernetes Cluster Access ==="
if ! "$KUBECTL" cluster-info > /dev/null 2>&1; then
  echo "ERROR: Cannot access Kubernetes cluster. Please check your KUBECONFIG."
  exit 1
fi
echo "Kubernetes cluster is accessible."

echo "=== Verifying DISTORT Installation ==="
if ! "$KUBECTL" get namespace distort-system > /dev/null 2>&1; then
  echo "ERROR: Namespace distort-system not found. Has DISTORT been deployed?"
  exit 1
fi

pods_not_running=$("$KUBECTL" get pods -n distort-system --no-headers | awk '{ split($2, ready, "/"); if (ready[1] != ready[2] || ($3 != "Running" && $3 != "Completed")) print }' || true)
if [ -n "$pods_not_running" ]; then
  echo "ERROR: Some pods in distort-system are not running or ready yet:"
  echo "$pods_not_running"
  exit 1
fi
echo "All DISTORT pods are running/healthy."

echo "=== Verifying Physical Disks on Nodes ==="
for node in distort-master distort-worker-1 distort-worker-2; do
  echo "Checking node $node..."
  actual_hugepages=$(vagrant_ssh "$node" "sysctl -n vm.nr_hugepages" 2>/dev/null || true)
  if [ "$actual_hugepages" != "$EXPECTED_HUGEPAGES_2MI" ]; then
    echo "ERROR: Node $node has $actual_hugepages hugepages; expected $EXPECTED_HUGEPAGES_2MI." >&2
    echo "Run 'make test-env-up' to converge the persistent VM resource profile." >&2
    exit 1
  fi

  # Check if there are any unbound NVMe controllers or active SPDK processes on the host
  unbound_controllers=$(vagrant_ssh "$node" "for f in /sys/bus/pci/devices/*/class; do if grep -q '0x0108' \$f 2>/dev/null; then pci=\$(basename \$(dirname \$f)); driver=\$(basename \$(readlink /sys/bus/pci/devices/\$pci/driver 2>/dev/null) 2>/dev/null || echo 'none'); if [ \"\$driver\" != \"nvme\" ]; then echo \"\$pci (\$driver)\"; fi; fi; done" 2>/dev/null || true)
  
  spdk_running=$(vagrant_ssh "$node" "pidof nvmf_tgt" 2>/dev/null || true)
  
  if [ -n "$unbound_controllers" ] || [ -n "$spdk_running" ]; then
    echo "ERROR: Node $node has unbound controllers ($unbound_controllers) or running SPDK ($spdk_running)." >&2
    echo "Run 'make test-env-reset' and inspect failures before retrying." >&2
    exit 1
  fi

  # Verify block devices are present in /dev/
  block_devices=$(vagrant_ssh "$node" "ls /dev/nvme[0-9]n[0-9] 2>/dev/null" 2>/dev/null || true)
  if [ -z "$block_devices" ]; then
    echo "ERROR: No NVMe block devices found on $node in /dev/."
    exit 1
  fi
  echo "Node $node has block devices: $(echo $block_devices | tr '\n' ' ')"
done

echo "=== Checking for Stale Resources ==="
stale_claims=$("$KUBECTL" get nvmedeviceclaims --all-namespaces --no-headers 2>/dev/null || true)
if [ -n "$stale_claims" ]; then
  echo "ERROR: Stale NVMeDeviceClaims found:" >&2
  echo "$stale_claims" >&2
  echo "Run 'make test-env-reset' before E2E." >&2
  exit 1
fi

stale_partitions=$("$KUBECTL" get nvmepartitions --all-namespaces --no-headers 2>/dev/null || true)
if [ -n "$stale_partitions" ]; then
  echo "ERROR: Stale NVMePartitions found:" >&2
  echo "$stale_partitions" >&2
  echo "Run 'make test-env-reset' and inspect finalizer failures before E2E." >&2
  exit 1
fi

echo "=== Verifying NVMe Devices Discovered ==="
devices=$("$KUBECTL" get nvmedevices --no-headers 2>/dev/null || true)
if [ -z "$devices" ]; then
  echo "ERROR: No NVMeDevices discovered by the agent yet. Please wait or check agent logs."
  exit 1
fi
echo "Discovered devices:"
echo "$devices"

# Check if any device has active backend status locked
stale_active_backend=$("$KUBECTL" get nvmedevices -o jsonpath='{.items[*].status.activeBackend}' | tr -d ' ' || true)
if [ -n "$stale_active_backend" ]; then
  echo "ERROR: Some devices retain an active backend: $stale_active_backend" >&2
  echo "Run 'make test-env-reset' and inspect cleanup failures before E2E." >&2
  exit 1
fi

echo "=== Verification Successful ==="
echo ""
