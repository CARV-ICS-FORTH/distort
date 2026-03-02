#!/bin/bash
# export_nvme.sh - Script to export a local block device as an NVMe-oF target

# --- Configuration & Performance Tunables ---
# The block device to export (e.g., a physical NVMe drive or partition)
DEVICE="/dev/nvme0n1"

# The NVMe Qualified Name for this target subsystem
NQN="nqn.2026-02.io.distort:eval-target"

# The network interface IP to bind to (use 0.0.0.0 for all interfaces)
PORTAL_IP="0.0.0.0"

# The RDMA port to listen on
PORTAL_PORT="4420"

# Target Port ID used internally by ConfigFS
PORT_ID="1"
# --------------------------------------------

set -e

if [ "$EUID" -ne 0 ]; then
  echo "Please run as root"
  exit 1
fi

if [ ! -b "$DEVICE" ]; then
    echo "Error: Device $DEVICE does not exist or is not a block device."
    exit 1
fi

echo "============================================="
echo " Exporting $DEVICE via NVMe-oF (RDMA)"
echo "   NQN:   $NQN"
echo "   Addr:  $PORTAL_IP:$PORTAL_PORT"
echo "============================================="

# Ensure kernel modules are loaded
modprobe nvmet
modprobe nvmet-rdma

NVMET_PATH="/sys/kernel/config/nvmet"

# Clean up existing export if it exists (for idempotency during evaluation rounds)
if [ -d "$NVMET_PATH/subsystems/$NQN" ]; then
    echo "Cleaning up existing export for $NQN..."
    rm -f "$NVMET_PATH/ports/$PORT_ID/subsystems/$NQN" || true
    echo 0 > "$NVMET_PATH/subsystems/$NQN/namespaces/1/enable" 2>/dev/null || true
    rmdir "$NVMET_PATH/subsystems/$NQN/namespaces/1" 2>/dev/null || true
    rmdir "$NVMET_PATH/subsystems/$NQN" 2>/dev/null || true
fi

# 1. Create Subsystem
echo "Creating NVMe subsystem $NQN..."
mkdir -p "$NVMET_PATH/subsystems/$NQN"
echo 1 > "$NVMET_PATH/subsystems/$NQN/attr_allow_any_host"

# 2. Create Namespace and link to physical device
echo "Mapping device $DEVICE to namespace 1..."
mkdir -p "$NVMET_PATH/subsystems/$NQN/namespaces/1"
echo -n "$DEVICE" > "$NVMET_PATH/subsystems/$NQN/namespaces/1/device_path"
echo 1 > "$NVMET_PATH/subsystems/$NQN/namespaces/1/enable"

# 3. Create Port
echo "Configuring RDMA port $PORT_ID..."
mkdir -p "$NVMET_PATH/ports/$PORT_ID"
echo "ipv4" > "$NVMET_PATH/ports/$PORT_ID/addr_adrfam"
echo "rdma" > "$NVMET_PATH/ports/$PORT_ID/addr_trtype"
echo "$PORTAL_PORT" > "$NVMET_PATH/ports/$PORT_ID/addr_trsvcid"
echo "$PORTAL_IP" > "$NVMET_PATH/ports/$PORT_ID/addr_traddr"

# 4. Link Subsystem to Port to enable the export
echo "Linking subsystem to port..."
ln -s "$NVMET_PATH/subsystems/$NQN" "$NVMET_PATH/ports/$PORT_ID/subsystems/$NQN" || true

echo "Done. Target successfully exported."
