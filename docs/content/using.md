---
title: "Using DISTORT"
description: "Understand how to manage physical drives, allocate devices via NVMeDeviceClaim CRDs, and provision volumes using StorageClasses."
type: "page"
---

Once DISTORT is deployed, it seamlessly integrates with standard Kubernetes storage workflows. This guide covers how administrators manage physical hardware and how developers request high-performance RDMA storage volumes.

---

## Schedule storage providers and consumers

Storage providers and workload consumers do not need to be the same nodes. Use
the component scheduling values to keep the privileged agent on NVMe/RDMA nodes
while installing the CSI node service wherever application Pods may consume a
DISTORT volume:

```yaml
agent:
  nodeSelector:
    distort.io/storage-provider: "true"

csiNode:
  nodeSelector:
    distort.io/storage-consumer: "true"

manager:
  nodeSelector:
    kubernetes.io/os: linux

csiController:
  nodeSelector:
    kubernetes.io/os: linux
```

Each component also has independent `tolerations` and `affinity` values. The
CSI node selector must include every node on which a consuming Pod may run. The
legacy top-level `nodeSelector`, `tolerations`, and `affinity` values remain as
backward-compatible fallbacks; a non-empty component value takes precedence.

---

## Hardware Discovery & Device Allocation

By design, DISTORT decouples **physical drive discovery** from **workload allocation**. This ensures that administrators have full, declarative control over which physical devices are consumed by the storage engine.

```mermaid
flowchart TD
    classDef admin fill:#1e3a8a,stroke:#3b82f6,stroke-width:2px,color:#fff;
    classDef agent fill:#312e81,stroke:#6366f1,stroke-width:2px,color:#fff;
    classDef state fill:#374151,stroke:#9ca3af,stroke-width:2px,color:#67e8f9;

    agent1(["1. Agent scans PCIe bus"]) --> dev1[("NVMeDevice CRD<br/>Discovered")]
    admin1(["2. Admin writes Claim Spec"]) --> claim1[("NVMeDeviceClaim CRD<br/>Declared")]
    claim1 --> mgr(["3. Manager binds Claim to Device"])
    dev1 --> mgr
    mgr --> active[("Drive marked ACTIVE")]

    class admin1,claim1 admin;
    class agent1,dev1 agent;
    class mgr,active state;
```

---

### Step 1: Discover Underlying Hardware

The `distort-agent` DaemonSet scans the PCIe bus on every storage-providing worker node and dynamically publishes discovered drives as cluster-wide `NVMeDevice` resources. 

To view the discovered hardware in your cluster:

```bash
kubectl get nvmedevices
```

This returns a list of discovered controllers, showing their status, capacity, NUMA alignment, serial number, and hosting node.

### Limit discovery to approved devices

The agent accepts two optional environment variables containing exact, comma-separated PCI addresses:

- `NVME_ALLOWED_DEVICES` limits discovery to the listed controllers.
- `NVME_EXCLUDE_DEVICES` removes listed controllers from discovery; exclusion wins when both variables are set.

For example, `0000:04:00.0,0000:05:00.0` selects two exact addresses. Substring and partial-address matches are deliberately rejected. These variables are not currently first-class Helm values, so installations that need them must add the environment entries to the agent workload through their deployment customization. Confirm the PCI addresses and mounted-device state before enabling an agent; assigning an OS or otherwise in-use controller to SPDK can make the host unavailable or destroy data.

Malformed list entries stop discovery instead of broadening the selection. DISTORT also excludes a controller whenever mounted-state inspection cannot be completed. `NVME_ALLOW_UNSAFE_MOUNT_INSPECTION=true` bypasses only that inspection failure and should be reserved for a controlled recovery environment after an administrator has independently verified that the device is unused.

### RDMA readiness

A storage node is eligible for placement only while its agent discovers a port
whose state is exactly `ACTIVE`, maps it to a supported Ethernet/RoCEv2 or
InfiniBand interface, selects a routable unicast address, and refreshes the node
heartbeat. IPv4 is preferred when both address families are configured; a
global IPv6 address is a supported fallback. Loopback, unspecified, multicast,
and link-local addresses are rejected. DISTORT does not advertise an NVMe/TCP
fallback because its current target and CSI data path is RDMA-only.

Native InfiniBand nodes must have an IPoIB interface, such as `ib0` or `ibs2`,
configured with an address reachable from every NVMe/RDMA initiator. The agent
matches that interface to the active InfiniBand device and port through its
network type, PCI parent, and port index. This also supports drivers where the
InfiniBand `gid_attrs/ndevs` files do not expose the associated IPoIB interface.
The agent loads the `ib_ipoib` module when available, but host networking must
still configure the interface address and route persistently.

Inspect the published endpoint and readiness with:

```bash
kubectl get rdmastoragenodes
kubectl get rdmastoragenode <node-name> -o yaml
```

The reported transport, link speed, IP address, active-export count, `Ready`
condition, and `lastHeartbeatTime` come from the node's live RDMA state.
Placement waits when no endpoint is ready or when its heartbeat is older than
45 seconds; DISTORT does not substitute a Kubernetes InternalIP or loopback
address.

### Step 2: Allocate Drives via `NVMeDeviceClaim`

To make a discovered physical device available for partitioning and pod allocation, an administrator must **claim** it. 

Claims are declared using the **`NVMeDeviceClaim`** resource, targeting the exact hardware **`serialNumber`** of the discovered device. This guarantees that if a drive is moved to a different PCIe slot or worker node, the system preserves the allocation mapping.

Create an `nvme-claim.yaml` file:

```yaml
apiVersion: storage.distort.io/v1alpha1
kind: NVMeDeviceClaim
metadata:
  name: claim-samsung-evo-1
spec:
  # The serial number uniquely identifying the physical disk
  serialNumber: "SN-distort-worker-1"
```

Apply the claim:

```bash
kubectl apply -f nvme-claim.yaml
```

The `distort-manager` matches the claim with the physical `NVMeDevice`. Once bound, the claim's status transitions to `Active: true`. You can verify this by running:

```bash
kubectl get nvmedeviceclaims
```

---

## Dynamic Storage Provisioning

Once hardware claims are active, developers can request volume allocations using standard Kubernetes StorageClasses and PVCs.

### 1. Define a StorageClass

DISTORT supports multiple backends and volume carving configurations. These are specified through standard Kubernetes StorageClass parameters.

#### StorageClass Parameters

| Parameter | Type | Allowed Values | Default | Description |
|---|---|---|---|---|
| `target-backend` | String | `spdk`, `kernel` | `spdk` | The target export technology to run on the storage nodes. |
| `volume-manager` | String | `partition` | `partition` | The volume carving method to slice physical drives. Unimplemented managers such as `lvm` are rejected. |
| `spdk-core-mask` | String | Nonzero CPU mask (e.g., `0x1`, `0x3`) | `0x1` | Node-global core affinity mask for the SPDK target daemon (SPDK only). |
| `fsType` | String | `ext4`, `xfs` | `ext4` | Filesystem created on a blank exported volume.|
| `csi.storage.k8s.io/fstype` | String | `ext4`, `xfs` | `ext4` | CSI ecosystem spelling for the filesystem type. |

Use only one filesystem parameter in a StorageClass. DISTORT accepts both spellings for compatibility; if both are set, their values must agree. Filesystem values are case-insensitive.

> [!WARNING]
> **Data Destruction on Backend Swap:**
> Physical NVMe devices are locked to the target backend driver (SPDK's user-space vfio-pci vs Kernel's nvme driver) of their first provisioned volume.
> If you allocate volumes from different StorageClasses with conflicting backends on the same node, they must target separate disks. Reconfiguring a disk to shift between SPDK and kernel backends requires wiping all partition tables and is highly destructive.
> Because of this, DISTORT does not register a default StorageClass upon installation.

#### Example StorageClass Configurations

**Option A: SPDK User-Space Target with Logical Volumes (Sane Default)**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: distort-spdk-partition
provisioner: storage.distort.io
volumeBindingMode: WaitForFirstConsumer
parameters:
  target-backend: "spdk"
  volume-manager: "partition"
  spdk-core-mask: "0x1"
```

**Option B: Linux Kernel Target with Partitions**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: distort-kernel-partition
provisioner: storage.distort.io
volumeBindingMode: WaitForFirstConsumer
parameters:
  target-backend: "kernel"
  volume-manager: "partition"
```

**Option C: XFS Filesystem**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: distort-spdk-xfs
provisioner: storage.distort.io
volumeBindingMode: WaitForFirstConsumer
parameters:
  target-backend: "spdk"
  volume-manager: "partition"
  fsType: "xfs"
```

The filesystem choice applies when DISTORT first stages a blank volume. If a volume already contains a filesystem, DISTORT preserves it. Staging fails without formatting or mounting when the detected type differs from the StorageClass request, preventing accidental data loss. DISTORT does not convert existing ext4 volumes to XFS.

Custom formatting options and StorageClass `mountOptions` are not currently supported. XFS also requires XFS support in the consumer node's host kernel, cannot be shrunk, and must not be mounted read-write from multiple nodes because it is not a clustered filesystem.

DISTORT implements controller-side single-writer fencing through `ControllerPublishVolume` and `ControllerUnpublishVolume`. A durable `NVMeVolumeAttachment` authorizes one node and host NQN at a time, while the target backend defaults to closed host access. A competing node is rejected until the current owner unpublishes.

Forced takeover is deliberately an administrator operation. First fence or confirm the old node is unreachable, then annotate the current attachment with `storage.distort.io/force-detach-node=<current-node>`. DISTORT revokes and disconnects the old host before granting the replacement. Never apply this annotation while the old node can still access the volume. Final two-node hardware re-verification of this path remains tracked under F25 in the [review findings](/review-findings/).

Apply the chosen StorageClass:

```bash
kubectl apply -f storageclass.yaml
```

### SPDK memory and RDMA queue tuning

The chart exposes resource controls for installations with a deliberately sized hugepage budget:

| Helm value | Default | Purpose |
|---|---|---|
| `agent.spdk.iobufSmallPoolCount` | SPDK default | Number of small iobuf entries; configure together with the large pool. |
| `agent.spdk.iobufLargePoolCount` | SPDK default | Number of large iobuf entries; configure together with the small pool. |
| `agent.spdk.maxSrqDepth` | SPDK default | Maximum RDMA shared receive queue depth; lowering it reduces DMA memory at the cost of queue capacity. |
| `agent.spdk.skipHugepageSetup` | `false` | Preserve a hugepage reservation managed by the host instead of allowing SPDK setup to replace it. |

Leaving the values unset preserves upstream SPDK behavior. Size them from measured workload concurrency and the node's hugepage reservation; the small values used by the local lab are functional-test settings, not universal production recommendations. The agent validates paired iobuf settings and positive numeric values before starting SPDK.

The chart reserves two CPUs for the agent and deliberately leaves its CPU
limit unset. `nvmf_tgt` uses polling reactors, so a CFS quota can throttle the
target even when the node has otherwise-idle CPUs and can substantially reduce
IOPS. Keep the CPU request at least as large as the number of CPUs selected by
`spdk-core-mask`; if you set a CPU limit explicitly, it must also cover the
polling reactors and the agent's control-plane work.

The SPDK target is one shared node process. Consequently, every SPDK-backed
StorageClass used on the same node must request the same `spdk-core-mask`.
DISTORT rejects `0x0` and rejects a request that conflicts with the running
process instead of silently applying first-request-wins behavior.

### 2. Request a Volume via PVC

Developers can now create a `PersistentVolumeClaim` (PVC) referencing the DISTORT StorageClass:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: rdma-pvc
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: distort-rdma
  resources:
    requests:
      storage: 500Mi
```

Apply the PVC:

```bash
kubectl apply -f pvc.yaml
```

### 3. Under the Hood Lifecycle
1. The **CSI-Provisioner** intercepts the PVC request.
2. Rather than creating a block loop file on a local filesystem, it creates a new **`NVMePartition`** CRD requesting `500Mi` of capacity.
3. The centralized **`distort-manager`** reconciles the partition, finding an active `NVMeDeviceClaim` on a healthy node with sufficient free space.
4. The local **`distort-agent`** DaemonSet on that node watches the partition, dynamically carves out an SPDK user-space Logical Volume (Lvol) in memory, and exposes it over the network as an NVMe-oF target.
5. The **CSI-Node-Server** on the compute node executing the application Pod connects to the remote target over the SoftRoCE/RDMA network, formats a blank block device with the StorageClass filesystem (`ext4` by default or `xfs`), and bind-mounts it directly into the container!
