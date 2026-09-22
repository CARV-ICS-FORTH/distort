# DISTORT

[![Tests](https://github.com/CARV-ICS-FORTH/distort/actions/workflows/test.yml/badge.svg)](https://github.com/CARV-ICS-FORTH/distort/actions/workflows/test.yml)
[![Lint](https://github.com/CARV-ICS-FORTH/distort/actions/workflows/lint.yml/badge.svg)](https://github.com/CARV-ICS-FORTH/distort/actions/workflows/lint.yml)
[![Documentation](https://github.com/CARV-ICS-FORTH/distort/actions/workflows/hugo.yml/badge.svg)](https://github.com/CARV-ICS-FORTH/distort/actions/workflows/hugo.yml)
[![License](https://img.shields.io/github/license/CARV-ICS-FORTH/distort)](LICENSE)

DISTORT (**DIS**aggregated **ST**orage **O**ver **R**DMA **T**ransport) is a
Kubernetes-native storage system for allocating physical NVMe capacity to
workloads over NVMe-over-Fabrics/RDMA. Kubernetes custom resources coordinate
device ownership and volume lifecycle, while CSI provides standard dynamic
provisioning and mounting for applications.

## Project status

DISTORT is an alpha-stage project moving toward beta. Version `0.5.0` is a
development release and is not recommended for production use. Its custom APIs
are currently `v1alpha1` and may change between development releases.

The project has been tested in a three-node Vagrant environment running K3s
1.35.4 and in a three-node cluster created with kubeadm running Kubernetes
1.35.5. The kubeadm test used the `v0.5` release commit with both SPDK and kernel
backends over InfiniBand/RDMA. These are tested configurations, not a declaration
of a wider Kubernetes compatibility range. See the
[test record](docs/content/testing.md#kubeadm-release-test) for details.

## Components

- `distort-manager` binds device claims and schedules logical partitions.
- `distort-agent` discovers NVMe hardware, creates storage, and exports targets.
- `distort-csi` translates Kubernetes volume operations into DISTORT resources
  and mounts remote NVMe devices on consumer nodes.

## Scope and current capabilities

DISTORT discovers administrator-selected NVMe devices, allocates their capacity,
exports volumes through SPDK or the Linux kernel NVMe-oF target, and dynamically
provisions Kubernetes volumes over RDMA. Physical devices must always be claimed
explicitly before DISTORT can use them.

The current CSI implementation supports mounted, single-node-writer volumes with
ext4 or XFS. Raw block volumes, multi-node access modes, snapshots, cloning,
volume expansion, replicated high availability, multipath failover, and an
NVMe/TCP fallback are not currently implemented. Planned capabilities are
tracked in the [roadmap](ROADMAP.md).

## Prerequisites

- A Linux Kubernetes cluster. The tested Kubernetes configurations are listed
  in [Project status](#project-status).
- Helm 3 and `kubectl` with permission to install CRDs, cluster RBAC, and the
  chart's workloads.
- Storage-provider nodes with unused physical NVMe devices and an active RDMA
  network reachable from every consumer node.
- Permission to run the privileged agent and CSI node workloads with the host
  device, kernel-module, networking, and kubelet access defined by the chart.
- Hugepages and sufficient CPU and memory for SPDK. The default chart requests
  1 GiB of 2 MiB hugepages and two CPUs for each storage agent.

## Quick start

Prepare the hosts and component scheduling as described in
[Using DISTORT](docs/content/using.md#prepare-the-cluster), then add the Helm
repository and install the standard development release. Pass your scheduling
overrides with `--values distort-values.yaml` if configured:

```bash
helm repo add distort https://distort-csi.dev/charts
helm repo update

helm install distort distort/distort \
  --namespace distort-system \
  --create-namespace \
  --version 0.5.0
```

For nodes using **BullSequana eXascale Interconnect V3 (BXI V3)**, install the
separate BXI development chart **instead of** the standard chart:

```bash
helm install distort distort/distort-bxi \
  --namespace distort-system \
  --create-namespace \
  --version 0.5.0
```

Check that the workloads started and that storage and RDMA discovery are ready:

```bash
kubectl -n distort-system get pods
kubectl get nvmedevices
kubectl get rdmastoragenodes
```

DISTORT never claims physical storage automatically. After installation, an
administrator must create an `NVMeDeviceClaim` for each device that DISTORT may
use. See [Using DISTORT](docs/content/using.md) for the complete workflow.

## Documentation

- **Declarative storage resources:** `NVMeDevice` represents discovered hardware,
  `NVMeDeviceClaim` grants DISTORT permission to use it, and `NVMePartition`
  describes an allocated volume. `RDMAStorageNode` and `NVMeVolumeAttachment`
  record storage-node readiness and consumer ownership.
- **Control plane:** the manager binds claims and places partitions, node agents
  configure storage and NVMe-oF targets, and the CSI driver connects Kubernetes
  volume operations to those resources.
- **Provisioning flow:** a PersistentVolumeClaim causes CSI to create a partition,
  the manager schedules it, the assigned agent exports it, and CSI connects and
  mounts the resulting block device on the consumer node.
- **Data path:** after setup, application I/O travels directly from the consumer's
  NVMe initiator over RDMA to the storage node's SPDK or kernel NVMe-oF target;
  the manager is not in the I/O path.
- **Ownership and cleanup:** physical devices must be claimed explicitly, volumes
  allow one authorized consumer at a time, and finalizers keep teardown
  coordinated with external storage state.

Read the published documentation at [distort-csi.dev](https://distort-csi.dev/)
or browse the complete source in [`docs/content`](docs/content/).

## Development

Use Go 1.25.3 or newer and run the normal development loop from the repository
root:

```bash
make build       # Generate artifacts and build all three binaries
make lint-fix    # Check style and apply safe lint fixes
make test        # Run unit and envtest tests
```

When API types or Kubebuilder markers change, regenerate and review the checked-in
artifacts with `make manifests generate sync-chart-crds`. Before submitting a change, run the
complete host-side checks:

```bash
make test-ci
```

To install the current source, updated CRDs, and a newly built image in the
supported local Vagrant/K3s cluster, create the lab once and then redeploy after
each change:

```bash
make test-env-create       # First-time cluster setup and installation
make test-env-redeploy     # Rebuild the image and upgrade the existing cluster
make test-env-smoke        # Verify nodes, workloads, NVMe, and RDMA discovery
```

The redeploy target regenerates and applies the CRDs, syncs them into the Helm
chart, builds `localhost/distort:0.5.0-dev`, imports it on every K3s node, and
upgrades the Helm release. See the [local testing guide](docs/content/local-testing.md)
for prerequisites, status and log commands, and the guarded reset workflow.

## Community

Use [GitHub Issues](https://github.com/CARV-ICS-FORTH/distort/issues) to report
bugs, request features, or ask project questions. Maintainers can also be
contacted through the addresses in [MAINTAINERS.md](MAINTAINERS.md). Report
security vulnerabilities privately according to [SECURITY.md](SECURITY.md).

The project is hosted by [CARV-ICS-FORTH](https://github.com/CARV-ICS-FORTH),
is connected to FORTH, and is currently noncommercial.

## Project policies

See [Contributing](CONTRIBUTING.md), [Governance](GOVERNANCE.md),
[Security](SECURITY.md), [Code of Conduct](CODE_OF_CONDUCT.md),
[Maintainers](MAINTAINERS.md), and the [Roadmap](ROADMAP.md).

## License

Licensed under the [Apache License 2.0](LICENSE).

## Acknowledgements

DISTORT has received funding from the EuroHPC Joint Undertaking through project
NET4EXA (GA-101175702), jointly funded by the European Commission and the
participating member states, including the Greek General Secretariat for
Research and Innovation.
