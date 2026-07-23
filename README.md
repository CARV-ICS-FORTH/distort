# DISTORT: DISaggregated STorage Over Rdma Transport

DISTORT is a high-performance, Kubernetes-native storage engine specifically designed to manage dynamic, disaggregated physical disk allocation. Utilizing NVMe-over-Fabrics (NVMe-oF) via Remote Direct Memory Access (RDMA) target exports, it orchestrates direct block storage attachments directly between worker nodes at near-local speeds.

## Documentation

For in-depth architectural details, local VM setups, testing workflows, and user manuals, visit our official documentation site:

👉 **[DISTORT Documentation](https://distort-csi.dev/)**

## Quick Start Installation

DISTORT is packaged as a Helm chart under `deploy/charts/distort` which bundles the manager control plane, node-level storage agents, CSI driver sidecars, and all required RBAC permissions and Custom Resource Definitions (CRDs).

To deploy the entire stack into your cluster inside the `distort-system` namespace:

```bash
helm install distort ./deploy/charts/distort \
  --namespace distort-system \
  --create-namespace
```

> [!IMPORTANT]
> **Allocate Hardware Devices:**
> After installation, physical storage devices are **not** automatically claimed. You must explicitly allocate storage controllers to DISTORT by applying **`NVMeDeviceClaim`** Custom Resources.
>
> Learn how to claim hardware and configure StorageClasses in the **[Using section of the docs](https://distort-csi.dev/using/)**.

## Configuration & Troubleshooting

### NVMe Device Discovery Filtering

By default, the DISTORT agent discovers all physical PCIe NVMe devices (skipping those with mounted filesystems). You can explicitly restrict which devices the agent discovers by configuring environment variables in the agent deployment:

- `NVME_ALLOWED_DEVICES`: A comma-separated list of PCI addresses (e.g., `0000:01:00.0,0000:02:00.0`). Only these devices will be discovered.
- `NVME_EXCLUDE_DEVICES`: A comma-separated list of PCI addresses to explicitly ignore.

### Manual Device Unbinding (SPDK Setup Failures)

When provisioning partitions using the SPDK backend, the agent must unbind the device from the host kernel (`nvme`) and bind it to a user-space driver (`uio_pci_generic` or `vfio-pci`). 
In some locked-down container environments (AppArmor, SELinux, read-only `/sys`), this operation may fail, leading to `spdk_setup.sh failed` errors.

**Workaround**: Ensure the target kernel module is loaded on the host (`modprobe uio_pci_generic`), and manually unbind the NVMe device on the host node before letting the agent take over:
```bash
# On the host node
FORCE=1 PCI_ALLOWED="0000:01:00.0" /opt/spdk/scripts/setup.sh
```

## License

This software is distributed under the terms of the [Apache License 2.0](LICENSE).

## Acknowledgements

We thankfully acknowledge the support of the European Commission and the Greek General Secretariat for Research and Innovation to this project. DISTORT has received funding from the EuroHPC Joint Undertaking through project NET4EXA (GA-101175702). EuroHPC JU projects are jointly funded by the European Commission and the involved state members (including the Greek General Secretariat for Research and Innovation).
