#!/bin/env bash
set -eo pipefail

export KUBECONFIG="${KUBECONFIG:-$(pwd)/kubeconfig.yaml}"

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
if ! kubectl cluster-info > /dev/null 2>&1; then
  echo "ERROR: Cannot access Kubernetes cluster. Please check your KUBECONFIG."
  exit 1
fi
echo "Kubernetes cluster is accessible."

echo "=== Verifying DISTORT Installation ==="
if ! kubectl get namespace distort-system > /dev/null 2>&1; then
  echo "ERROR: Namespace distort-system not found. Has DISTORT been deployed?"
  exit 1
fi

pods_not_running=$(kubectl get pods -n distort-system --no-headers | grep -v -E "Running|Completed" || true)
if [ -n "$pods_not_running" ]; then
  echo "ERROR: Some pods in distort-system are not running or ready yet:"
  echo "$pods_not_running"
  exit 1
fi
echo "All DISTORT pods are running/healthy."

echo "=== Verifying Physical Disks on Nodes ==="
for node in distort-master distort-worker-1; do
  echo "Checking node $node..."
  # Check if there are any unbound NVMe controllers or active SPDK processes on the host
  unbound_controllers=$(vagrant_ssh "$node" "for f in /sys/bus/pci/devices/*/class; do if grep -q '0x0108' \$f 2>/dev/null; then pci=\$(basename \$(dirname \$f)); driver=\$(basename \$(readlink /sys/bus/pci/devices/\$pci/driver 2>/dev/null) 2>/dev/null || echo 'none'); if [ \"\$driver\" != \"nvme\" ]; then echo \"\$pci (\$driver)\"; fi; fi; done" 2>/dev/null || true)
  
  spdk_running=$(vagrant_ssh "$node" "pidof nvmf_tgt" 2>/dev/null || true)
  
  if [ -n "$unbound_controllers" ] || [ -n "$spdk_running" ]; then
    echo "WARNING: Node $node has unbound controllers ($unbound_controllers) or running SPDK ($spdk_running)."
    echo "Running clean-node.sh on $node to restore to a clean state..."
    vagrant_ssh "$node" "sudo bash /vagrant/clean-node.sh" >/dev/null 2>&1 || true
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
stale_claims=$(kubectl get nvmedeviceclaims --all-namespaces --no-headers 2>/dev/null || true)
if [ -n "$stale_claims" ]; then
  echo "WARNING: Stale NVMeDeviceClaims found."
  echo "Deleting stale claims..."
  kubectl delete nvmedeviceclaims --all --all-namespaces --timeout=30s || true
fi

stale_partitions=$(kubectl get nvmepartitions --all-namespaces --no-headers 2>/dev/null || true)
if [ -n "$stale_partitions" ]; then
  echo "WARNING: Stale NVMePartitions found."
  echo "Deleting stale partitions..."
  # Remove finalizers if they are stuck
  for p in $(kubectl get nvmepartitions -o jsonpath='{.items[*].metadata.name}' -n distort-system 2>/dev/null || true); do
    kubectl patch nvmepartition "$p" -n distort-system -p '{"metadata":{"finalizers":null}}' --type=merge || true
  done
  for p in $(kubectl get nvmepartitions -o jsonpath='{.items[*].metadata.name}' --all-namespaces 2>/dev/null || true); do
    kubectl patch nvmepartition "$p" -p '{"metadata":{"finalizers":null}}' --type=merge || true
  done
  kubectl delete nvmepartitions --all --all-namespaces --timeout=30s || true
fi

echo "=== Verifying NVMe Devices Discovered ==="
devices=$(kubectl get nvmedevices --no-headers 2>/dev/null || true)
if [ -z "$devices" ]; then
  echo "ERROR: No NVMeDevices discovered by the agent yet. Please wait or check agent logs."
  exit 1
fi
echo "Discovered devices:"
echo "$devices"

# Check if any device has active backend status locked
stale_active_backend=$(kubectl get nvmedevices -o jsonpath='{.items[*].status.activeBackend}' | tr -d ' ' || true)
if [ -n "$stale_active_backend" ]; then
  echo "WARNING: Some devices still have active backend status locked: $stale_active_backend"
  echo "Attempting to reset device statuses to clear active backends..."
  for dev in $(kubectl get nvmedevices -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true); do
    # Try using patch status subresource
    kubectl patch nvmedevice "$dev" --type='json' -p='[{"op": "remove", "path": "/status/activeBackend"}]' --subresource=status >/dev/null 2>&1 || true
    # Fallback to merge patch on status if subresource isn't enabled/working in client
    kubectl patch nvmedevice "$dev" -p '{"status":{"activeBackend":""}}' --type=merge --subresource=status >/dev/null 2>&1 || true
  done
fi

echo "=== Verification Successful ==="
echo ""
