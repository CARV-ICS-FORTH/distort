# DISTORT Development Roadmap

## Project Vision

**DISTORT (DISaggregated STorage Over Rdma Transport)** is a high-performance, Kubernetes-native disaggregated storage engine designed to bridge physical NVMe-over-Fabrics (NVMe-oF) RDMA storage directly with containerized workloads.

By offloading the control plane to a declarative Kubernetes Custom Resource Definition (CRD) state machine while maintaining an ultra-lightweight, user-space polling data path via SPDK, DISTORT delivers bare-metal storage speeds at microseconds of latency without standard software-defined storage (SDS) middleware overhead.

## Guiding Principles

* **Zero-Over-Head Data Path:** Bypasses the traditional Linux kernel block layer using SPDK user-space polling drivers (`vfio-pci`) to maximize IOPS and eliminate context-switch or interrupt handling latencies.
* **Declarative Control Plane:** Orchestrates physical hardware operations (discovery, claims, partitions, and bindings) entirely via Kubernetes CRD controllers, aligning bare-metal disaggregated storage with standard GitOps paradigms.
* **RDMA-First Fabric:** Specifically optimized for high-speed RDMA transports (physical RoCE, InfiniBand, iWARP) and local software emulation (SoftRoCE) to ensure bare-metal performance.
* **Explicit Hardware Control:** Empowers cluster administrators with precise, secure hardware allocation controls via declarative `NVMeDeviceClaim` mappings.

## Technical Features Roadmap

- [ ] **Telemetry, Observability, and Auditing:** Develop a telemetry daemon exporting real-time SPDK block metrics (IOPS, bandwidth, latency) to Prometheus, profile CPU polling utilization versus idle loops, and emit native Kubernetes Events during volume lifecycle transitions for cluster-wide diagnostics.
- [ ] **NUMA-Aware Schedulers:** Place `NVMePartition` allocations on the same NUMA socket as the active RDMA NIC, minimizing internal PCIe bus crossings and latency.
- [ ] **Quality of Service (QoS) Throttling:** Enable block-level rate-limiting of IOPS, bandwidth, and burst limits per partition using SPDK's built-in block-level throttling mechanisms.
- [ ] **Modular Backend & Volume Manager Architectures:** Support pluggable target exporter backends (such as user-space SPDK versus kernel-based target configurations) and diverse block-level volume managers (such as SPDK logical volumes, GPT partitioning via `parted`, and integration with other volume managers like LVM).
- [ ] **Exploration: Advanced CSI Capabilities:** Explore support for online volume expansion (resizing lvol and remote file systems dynamically), zero-copy volume snapshots and clones (`bdev_lvol_snapshot`), and NVMe-oF native multipathing for high-availability path failover and load balancing.
- [ ] **Exploration: Fabric Security & Multi-Tenancy:** Explore secure NVMe-oF DH-HMAC-CHAP target authentication, transparent hardware-accelerated AES-XTS block-level data encryption at rest (`bdev_crypto`), and network isolation using VLANs or namespace-level policies.
- [ ] **Exploration: Logical Volume Mirroring (High Availability):** Explore replication of `NVMePartition` block devices across different physical `RDMAStorageNode` hosts for distributed data protection and high availability.

## Get Involved!

DISTORT is an open, cloud-native project. We welcome contributions, RFCs, and discussions to shape the future of high-performance disaggregated storage in Kubernetes! Feel free to open issues or pull requests.