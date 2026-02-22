#!/bin/bash
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
