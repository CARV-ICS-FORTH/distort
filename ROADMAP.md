# DISTORT Development Roadmap

## Project Vision

**DISTORT (DISaggregated STorage Over RDMA Transport)** manages physical NVMe
capacity through Kubernetes custom resources and exposes it to workloads through
CSI and NVMe-over-Fabrics/RDMA.

The aim is to separate storage allocation and lifecycle management from the I/O
path. SPDK and Linux kernel targets are implemented; performance depends on the
hardware, network, backend, and workload. No latency or throughput guarantee is
made here.

## Guiding Principles

* **Direct Data Path:** Keep management controllers out of application I/O. SPDK provides target-side user-space NVMe access; consumer nodes use the kernel NVMe initiator.
* **Declarative Control Plane:** Orchestrates physical hardware operations (discovery, claims, partitions, and bindings) entirely via Kubernetes CRD controllers, aligning bare-metal disaggregated storage with standard GitOps paradigms.
* **RDMA Fabric:** Use NVMe-oF/RDMA with readiness-aware endpoint discovery. The local lab uses SoftRoCE for functional testing, not performance characterization.
* **Explicit Hardware Control:** Empowers cluster administrators with precise, secure hardware allocation controls via declarative `NVMeDeviceClaim` mappings.

## Technical Features Roadmap

These are proposed directions, not release commitments. Current releases are
development releases and are not recommended for production. The
[README](README.md#scope-and-current-capabilities) describes what is implemented.

Near-term validation priorities are completing the corrected two-node takeover
test, checking capacity reservation across manager leadership overlap, running
CSI conformance tests, and recording reproducible hardware test results.

- [ ] **Telemetry, Observability, and Auditing:** Develop a telemetry daemon exporting real-time SPDK block metrics (IOPS, bandwidth, latency) to Prometheus, profile CPU polling utilization versus idle loops, and emit native Kubernetes Events during volume lifecycle transitions for cluster-wide diagnostics.
- [ ] **NUMA-Aware Schedulers:** Place `NVMePartition` allocations on the same NUMA socket as the active RDMA NIC, minimizing internal PCIe bus crossings and latency.
- [ ] **Quality of Service (QoS) Throttling:** Enable block-level rate-limiting of IOPS, bandwidth, and burst limits per partition using SPDK's built-in block-level throttling mechanisms.
- [ ] **Additional Volume Managers:** Extend the existing plugin interfaces beyond the implemented SPDK logical-volume and kernel/`parted` paths. LVM is not implemented and is currently rejected.
- [ ] **Exploration: Advanced CSI Capabilities:** Explore support for online volume expansion (resizing lvol and remote file systems dynamically), zero-copy volume snapshots and clones (`bdev_lvol_snapshot`), and NVMe-oF native multipathing for high-availability path failover and load balancing.
- [ ] **Exploration: Fabric Security & Multi-Tenancy:** Explore secure NVMe-oF DH-HMAC-CHAP target authentication, transparent hardware-accelerated AES-XTS block-level data encryption at rest (`bdev_crypto`), and network isolation using VLANs or namespace-level policies.
- [ ] **Exploration: Logical Volume Mirroring (High Availability):** Explore replication of `NVMePartition` block devices across different physical `RDMAStorageNode` hosts for distributed data protection and high availability.

## Get Involved!

Propose or discuss roadmap work in [GitHub Issues](https://github.com/CARV-ICS-FORTH/distort/issues).
Include the use case, expected behavior, and how it could be tested. See the
[contributor guide](CONTRIBUTING.md) for development workflows.
