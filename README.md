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

## License

This software is distributed under the terms of the [Apache License 2.0](LICENSE).