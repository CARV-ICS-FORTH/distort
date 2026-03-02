#!/bin/bash
# import_nvme.sh - Script to connect to an NVMe-oF target and mount it

# --- Configuration & Performance Tunables ---
# The IP address of the target exporter node
TARGET_IP="192.168.56.10"

# The RDMA port the target is listening on
TARGET_PORT="4420"

# The NVMe Qualified Name we want to connect to
NQN="nqn.2026-02.io.distort:eval-target"

# Number of I/O queues (ideally matches the number of CPU cores for scaling)
NR_IO_QUEUES="4"

# Queue depth (qd) - Number of elements in each I/O queue. Higher qd can increase throughput for parallel workloads.
QUEUE_SIZE="128"

# Optional: Number of specific write queues (if distinct from typical I/O)
# NR_WRITE_QUEUES="1"

# The directory where the imported device will be mounted
MOUNT_POINT="/mnt/distort_eval"

# Filesystem type to use. (If the drive is already formatted, it will just be mounted)
FS_TYPE="ext4"
# --------------------------------------------

set -e

if [ "$EUID" -ne 0 ]; then
  echo "Please run as root"
  exit 1
fi

echo "============================================="
echo " Importing NVMe-oF (RDMA) Target"
echo "   Target:  $TARGET_IP:$TARGET_PORT"
echo "   NQN:     $NQN"
echo "   Queues:  $NR_IO_QUEUES   Queue-Depth: $QUEUE_SIZE"
echo "============================================="

# Ensure required kernel modules and tools are present
modprobe nvme-rdma || { echo "Failed to load nvme-rdma module"; exit 1; }
command -v nvme >/dev/null 2>&1 || { echo >&2 "nvme-cli is required but not installed. Aborting."; exit 1; }

echo "Discovering target..."
nvme discover -t rdma -a "$TARGET_IP" -s "$TARGET_PORT"

echo "Connecting to target..."
# Standard connection parameters for performance evaluation
nvme connect -t rdma -n "$NQN" -a "$TARGET_IP" -s "$TARGET_PORT" \
    --nr-io-queues="$NR_IO_QUEUES" \
    --queue-size="$QUEUE_SIZE"

# Wait briefly for udev to populate the /dev/nvmeXn1 block device
sleep 2

# Identify the newly attached device by looking for our subsystem NQN
echo "Locating attached device path..."
# Use nvme list-subsys and parse json, or find via sysfs
ATTACHED_DEV=""
for syspath in /sys/class/nvme/nvme*/subsysnqn; do
    if grep -q "$NQN" "$syspath" 2>/dev/null; then
        CTRL_DIR=$(dirname "$syspath")
        # Find the block device inside the controller directory (e.g., nvme1n1)
        BLOCK_DEV_NAME=$(ls -1d "$CTRL_DIR"/nvme*n* 2>/dev/null | head -n 1 | awk -F'/' '{print $NF}')
        if [ -n "$BLOCK_DEV_NAME" ]; then
            ATTACHED_DEV="/dev/$BLOCK_DEV_NAME"
            break
        fi
    fi
done

if [ -z "$ATTACHED_DEV" ] || [ ! -b "$ATTACHED_DEV" ]; then
    echo "Error: Successfully connected, but could not locate the block device in /dev!"
    exit 1
fi

echo "Successfully attached at $ATTACHED_DEV"

# Verify/Format Filesystem
FSTYPE_CHECK=$(blkid -o value -s TYPE "$ATTACHED_DEV" || true)

if [ "$FSTYPE_CHECK" != "$FS_TYPE" ]; then
    echo "Block device $ATTACHED_DEV doesn't have a $FS_TYPE filesystem (found '$FSTYPE_CHECK'). Formatting..."
    mkfs.${FS_TYPE} "$ATTACHED_DEV"
else
    echo "Block device $ATTACHED_DEV is already formatted with $FS_TYPE."
fi

# Mount the device
echo "Mounting $ATTACHED_DEV to $MOUNT_POINT..."
mkdir -p "$MOUNT_POINT"

if mountpoint -q "$MOUNT_POINT"; then
    echo "Warning: Something is already mounted at $MOUNT_POINT. Unmounting first..."
    umount "$MOUNT_POINT"
fi

mount "$ATTACHED_DEV" "$MOUNT_POINT"
echo "Mount complete. Ready for evaluation."
df -h "$MOUNT_POINT"
