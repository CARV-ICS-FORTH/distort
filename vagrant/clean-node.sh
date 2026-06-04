#!/bin/bash
set -x

# 1. Reset any NVMe devices bound to user-space drivers (uio_pci_generic/vfio-pci) back to kernel nvme driver
for class_file in /sys/bus/pci/devices/*/class; do
  if grep -q '0x0108' "$class_file" 2>/dev/null; then
    pci=$(basename "$(dirname "$class_file")")
    driver=$(basename "$(readlink "/sys/bus/pci/devices/$pci/driver" 2>/dev/null)" 2>/dev/null || true)
    if [ "$driver" != "nvme" ]; then
      echo "Resetting PCI device $pci from driver '$driver' to kernel 'nvme' driver..."
      if [ -n "$driver" ] && [ -d "/sys/bus/pci/drivers/$driver" ]; then
        echo "$pci" > "/sys/bus/pci/drivers/$driver/unbind" 2>/dev/null || true
      fi
      echo "$pci" > "/sys/bus/pci/drivers_probe" 2>/dev/null || true
    fi
  fi
done

# Wait for kernel block devices to appear
sleep 2

# 2. Wipe physical partitions/metadata on all discovered kernel NVMe namespace block devices
for dev in /dev/nvme[0-9]n[0-9]; do
  if [ -b "$dev" ]; then
    echo "Wiping device $dev..."
    wipefs -a "$dev" || true
    # Zero the first 10MB of the disk to clear any persistent metadata (LVM, partition tables, etc.)
    dd if=/dev/zero of="$dev" bs=1M count=10 conv=notrunc 2>/dev/null || true
  fi
done

# 3. Clean kernel NVMe-oF target configfs
for port in /sys/kernel/config/nvmet/ports/*; do
  [ -d "$port" ] || continue
  for link in "$port/subsystems/"*; do
    [ -L "$link" ] && rm "$link"
  done
done
for subsys in /sys/kernel/config/nvmet/subsystems/*; do
  [ -d "$subsys" ] || continue
  for ns in "$subsys/namespaces/"*; do
    [ -d "$ns" ] || continue
    echo 0 > "$ns/enable"
    rmdir "$ns"
  done
  rmdir "$subsys"
done
