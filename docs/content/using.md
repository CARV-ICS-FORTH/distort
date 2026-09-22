---
title: "Using DISTORT"
description: "Understand how to manage physical drives, allocate devices via NVMeDeviceClaim CRDs, and provision volumes using StorageClasses."
type: "page"
---

This guide walks through installation, an explicit physical-device claim, and a
filesystem volume mounted by a Pod. DISTORT is an alpha-stage project moving
toward beta; `0.5.0` is a development release, not recommended for production.

## Prepare the cluster

Use a Linux Kubernetes cluster with Helm 3 and `kubectl`, and permissions to
install CRDs, cluster RBAC, and privileged workloads. Tested configurations are
three-node K3s 1.35.4 and kubeadm Kubernetes 1.35.5 clusters. The kubeadm test
covered both SPDK and kernel backends over InfiniBand/RDMA; see the
[release test record](/testing/#kubeadm-release-test). This is not a wider
compatibility guarantee.

Storage providers need unused NVMe devices and an active RDMA interface reachable
from every consumer node. The chart's default agent requests two CPUs, 1 GiB of
memory, and 1 GiB of 2 MiB hugepages. Prepare host hugepages and the required
kernel/VFIO and RDMA support before installing. Consumer nodes need the kernel
NVMe/RDMA initiator and support for the selected filesystem. The chart grants
host device, network, kernel-module, and kubelet access to its privileged
components; install it only on nodes intended for these roles.

Before installation, select the provider and consumer nodes using the
[scheduling values below](#schedule-storage-providers-and-consumers). For a
reproducible disposable environment, use the [local lab](/local-testing/), which
builds the image and prepares virtual NVMe devices and SoftRoCE.

---

## Install DISTORT

Add the chart repository and update its local index:

```bash
helm repo add distort https://distort-csi.dev/charts
helm repo update
```

Install the standard `0.5.0` development release. If you saved the scheduling
example below, add `--values distort-values.yaml` to the command:

```bash
helm install distort distort/distort \
  --namespace distort-system \
  --create-namespace \
  --version 0.5.0
```

Alternatively, install the separate BXI development chart on nodes using BullSequana eXascale
Interconnect V3 (BXI V3):

```bash
helm install distort distort/distort-bxi \
  --namespace distort-system \
  --create-namespace \
  --version 0.5.0
```

Choose one chart for this release, not both. Both are development releases.
They share semantic versions but use separate source branches, charts, and
image tags. The workflow below describes the standard RDMA chart; consult the
[BXI branch](https://github.com/CARV-ICS-FORTH/distort/tree/bxi) for variant-specific setup.

Check the installed workloads and hardware discovery before creating claims:

```bash
kubectl -n distort-system get pods
kubectl get nvmedevices
kubectl get rdmastoragenodes
```

---

## Schedule storage providers and consumers

Storage providers and workload consumers do not need to be the same nodes. Use
the component scheduling values to keep the privileged agent on NVMe/RDMA nodes
while installing the CSI node service wherever application Pods may consume a
DISTORT volume:

Label the selected nodes, replacing the placeholder names with actual node names:

```bash
kubectl label node <provider-node> distort.io/storage-provider=true
kubectl label node <consumer-node> distort.io/storage-consumer=true
```

A node can have both roles. Save the following as `distort-values.yaml` and pass
it to `helm install` with `--values distort-values.yaml`:

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

Claims target the exact **`serialNumber`** reported by discovery, rather than a
PCIe slot. This identifies a device independently of its location; it does not
provide live volume migration when hardware moves.

Use an empty device dedicated to this example. Claiming authorizes DISTORT to
rebind the device and allocate storage on it; do not claim a boot disk or a disk
containing data you need. Claims are not per-namespace tenant isolation: see
[the architecture's capability boundary](/architecture/#current-capability-boundary).

Create a namespace for the example:

```bash
kubectl create namespace distort-example
```

Create `nvme-claim.yaml`, replacing `REPLACE_WITH_UNUSED_DEVICE_SERIAL` with the
serial of your selected device from `kubectl get nvmedevices -o yaml`:

```yaml
apiVersion: storage.distort.io/v1alpha1
kind: NVMeDeviceClaim
metadata:
  name: example-device
  namespace: distort-example
spec:
  # The serial number uniquely identifying the physical disk
  serialNumber: "REPLACE_WITH_UNUSED_DEVICE_SERIAL"
```

Apply the claim:

```bash
kubectl apply -f nvme-claim.yaml
```

The manager matches the claim to the device and sets `status.active` to `true`.
Wait for binding before requesting a volume:

```bash
kubectl -n distort-example wait nvmedeviceclaim/example-device \
  --for=jsonpath='{.status.active}'=true --timeout=120s
kubectl -n distort-example get nvmedeviceclaims
```

---

## Dynamic Storage Provisioning

Once hardware claims are active, developers can request volume allocations using standard Kubernetes StorageClasses and PVCs.

DISTORT rounds each requested volume upward to a 4 MiB allocation unit shared by
the kernel and SPDK backends. Device capacity already excludes a conservative
metadata reserve for backend bookkeeping. This accounting does not provide
data migration between backends. A CSI request with `limitBytes` is rejected if this
rounding would exceed its limit.

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

The user-facing `partition` manager selects SPDK logical volumes for the `spdk`
backend and on-disk partitions managed by `parted` for the `kernel` backend.

> [!WARNING]
> **Data Destruction on Backend Swap:**
> Physical NVMe devices are locked to the target backend driver (SPDK's user-space vfio-pci vs Kernel's nvme driver) of their first provisioned volume.
> If you allocate volumes from StorageClasses with different backends on the same node, they must use separate disks. Do not switch the backend of a disk with allocated volumes; backend migration is not implemented, and reinitializing storage can destroy existing data.
> Because of this, DISTORT does not register a default StorageClass upon installation.

#### Example StorageClass Configurations

Save **one** of the following as `storageclass.yaml`. The PVC example uses
Option A; if you choose another option, change its `storageClassName` to match.
These examples use `reclaimPolicy: Delete`: deleting the PVC after its Pod is
removed also deletes the provisioned volume and its data.

**Option A: SPDK User-Space Target with Logical Volumes**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: distort-spdk-partition
provisioner: storage.distort.io
reclaimPolicy: Delete
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
reclaimPolicy: Delete
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
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
parameters:
  target-backend: "spdk"
  volume-manager: "partition"
  fsType: "xfs"
```

The filesystem choice applies when DISTORT first stages a blank volume. If a volume already contains a filesystem, DISTORT preserves it. Staging fails without formatting or mounting when the detected type differs from the StorageClass request, preventing accidental data loss. DISTORT does not convert existing ext4 volumes to XFS.

Custom formatting options and StorageClass `mountOptions` are not currently supported. XFS also requires XFS support in the consumer node's host kernel, cannot be shrunk, and must not be mounted read-write from multiple nodes because it is not a clustered filesystem.

DISTORT implements controller-side single-writer fencing through `ControllerPublishVolume` and `ControllerUnpublishVolume`. A durable `NVMeVolumeAttachment` authorizes one node and host NQN at a time, while the target backend defaults to closed host access. A competing node is rejected until the current owner unpublishes.

Forced takeover is an administrator operation. First fence the old consumer so
it can no longer access storage; inability to reach its Kubernetes API or node
address is not proof of fencing. Only then annotate the current attachment with
`storage.distort.io/force-detach-node=<current-node>`. The implementation revokes
the old host's access before authorizing the replacement. Final two-node hardware
verification of the corrected takeover path remains pending; do not treat it as
verified automatic failover.

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

Save this as `pvc.yaml`, referencing the StorageClass created above:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: rdma-pvc
  namespace: distort-example
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: distort-spdk-partition
  resources:
    requests:
      storage: 500Mi
```

Apply the PVC:

```bash
kubectl apply -f pvc.yaml
```

The PVC can remain `Pending` until a consumer Pod is scheduled because the
StorageClass uses `WaitForFirstConsumer`.

### 3. Mount and check the volume

Save this as `pod.yaml`. The node selector matches the consumer label used
earlier; ensure that node runs the CSI node service and can reach the RDMA target.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: rdma-consumer
  namespace: distort-example
spec:
  nodeSelector:
    distort.io/storage-consumer: "true"
  containers:
    - name: app
      image: busybox:1.36
      command: ["sh", "-c", "sleep 3600"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: rdma-pvc
```

```bash
kubectl apply -f pod.yaml
kubectl -n distort-example wait pod/rdma-consumer --for=condition=Ready --timeout=300s
kubectl -n distort-example get pvc rdma-pvc
kubectl -n distort-example exec rdma-consumer -- sh -c 'echo hello-distort > /data/check.txt && cat /data/check.txt'
```

The PVC should be `Bound` and the read should return `hello-distort`. This is a
basic functional check, not a performance or failover test.

### 4. Clean up the example

The example's `Delete` reclaim policy removes the volume and its data. Delete
the Pod first so kubelet can unmount it, then delete its PVC:

```bash
kubectl -n distort-example delete pod rdma-consumer
kubectl -n distort-example delete pvc rdma-pvc
kubectl -n distort-example get nvmepartitions,nvmevolumeattachments
```

Wait for the example's partition and attachment to disappear before releasing
the device claim. If cleanup stalls, inspect events and logs; do not remove
finalizers to bypass storage cleanup. Do not release a claim still used by
other volumes. For this dedicated example, finish with:

```bash
kubectl -n distort-example delete nvmedeviceclaim example-device
kubectl delete storageclass distort-spdk-partition
kubectl delete namespace distort-example
```

Use the corresponding StorageClass name if you chose Option B or C. Removing
the Helm release does not substitute for volume cleanup.

## Troubleshooting

```bash
kubectl -n distort-system get pods -o wide
kubectl -n distort-example describe pvc rdma-pvc
kubectl -n distort-example describe pod rdma-consumer
kubectl -n distort-example get events --sort-by=.metadata.creationTimestamp
kubectl get nvmedevices,rdmastoragenodes
kubectl get nvmedeviceclaims,nvmepartitions,nvmevolumeattachments -A
kubectl -n distort-system logs -l app.kubernetes.io/name=distort --all-containers=true --prefix=true --tail=100
```

- **Agent Pending:** check node labels, taints, CPU/memory requests, and available hugepages.
- **Claim inactive:** check that its serial matches an unused discovered device and that another claim does not own it.
- **PVC Pending:** create its consumer Pod, then check claimed capacity, backend compatibility, RDMA `Ready` status, and recent heartbeats.
- **Pod waiting to mount:** inspect attachment ownership, CSI logs, consumer kernel support, and reachability of the published RDMA endpoint.
- **Resources stuck deleting:** inspect agent and CSI logs and restore the affected node/backend so finalizer cleanup can complete.

For help, open a [GitHub issue](https://github.com/CARV-ICS-FORTH/distort/issues)
with versions, backend, reproduction steps, and sanitized diagnostics. Use the
[security policy](https://github.com/CARV-ICS-FORTH/distort/blob/main/SECURITY.md)
for vulnerabilities.

## Under the hood

1. The external-provisioner calls DISTORT's CSI `CreateVolume` after consumer scheduling allows provisioning.
2. CSI creates an `NVMePartition` resource requesting the volume capacity.
3. The manager selects a claimed device on a ready storage node with sufficient capacity.
4. The agent allocates persistent storage and publishes an NVMe-oF endpoint.
5. The external-attacher calls `ControllerPublishVolume`; DISTORT records consumer ownership and authorizes its host NQN.
6. CSI on the consumer connects the kernel initiator, formats a blank volume with ext4 or XFS, and stages and publishes the filesystem for the Pod.
