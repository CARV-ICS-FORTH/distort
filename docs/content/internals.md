---
title: "Project Internals"
description: "Detailed guide to DISTORT's Kubernetes controllers, CSI services, agent, storage backends, state, and recovery model."
type: "page"
---

# Kubernetes, CSI, Reconciliation, and Project Internals

This document explains the concepts and code paths that make DISTORT work. It assumes familiarity with Linux, containers, storage, and basic Kubernetes objects, but it does not assume prior experience writing Kubernetes controllers or CSI drivers.

The goal is to make it possible to read the repository and understand:

- which process is responsible for each operation;
- how a PVC eventually becomes a remote NVMe block device mounted in a Pod;
- why Kubernetes controllers are written as reconciliation loops;
- how CSI and gRPC fit into Kubernetes;
- how the manager, agent, reporter, CRDs, SPDK, and CSI sidecars cooperate;
- which state is durable and which state exists only inside a running process or host;
- where the current implementation is simplified or incomplete.

## 1. Basic context

### 1.1 What DISTORT is

DISTORT is a Kubernetes storage system that exposes capacity from physical NVMe devices to workloads over NVMe over Fabrics (NVMe-oF), primarily using RDMA.

There are two sides to an allocated volume:

- The **storage/target node** owns the physical NVMe device. DISTORT creates a slice of that device and exports the slice as an NVMe-oF target.
- The **consumer/initiator node** runs the application Pod. It connects to the remote target, receives a local Linux block device such as `/dev/nvme2n1`, formats it when necessary, and mounts it for the Pod.

The I/O data path does not pass through the Kubernetes API or the DISTORT manager. Kubernetes and the DISTORT components establish and describe the connection. Once connected, application I/O travels through the Linux filesystem and NVMe initiator on the consumer node, across the network, and into the NVMe-oF target on the storage node.

With the SPDK backend, the target-side path is approximately:

```text
Application
  -> mounted filesystem on consumer node
  -> Linux block and NVMe initiator
  -> NVMe-oF/RDMA network
  -> SPDK nvmf_tgt on storage node
  -> SPDK logical volume
  -> physical NVMe device
```

### 1.2 The control plane and the data plane

It is useful to separate the system into two categories.

The **control plane** makes decisions and configures state:

- Kubernetes API objects describe requested and observed state.
- The DISTORT manager selects a physical device and storage node.
- The DISTORT agent configures the device, logical volume, and NVMe-oF export.
- The CSI driver responds to Kubernetes volume operations.

The **data plane** carries application reads and writes:

- the mounted filesystem;
- the consumer node's NVMe initiator;
- the RDMA network;
- the SPDK or kernel NVMe-oF target;
- the physical NVMe device.

This distinction matters during failures. A temporary manager outage does not necessarily stop existing I/O because the manager is not in the data path. An SPDK target process failure does stop I/O for exports served by that process because SPDK is in the data path.

### 1.3 The three DISTORT binaries

The repository builds three main programs:

| Binary | Deployment form | Main responsibility |
|---|---|---|
| `distort-manager` | Kubernetes Deployment | Cluster-wide decisions: claims, placement, capacity accounting |
| `distort-agent` | DaemonSet | Node-local discovery, physical device setup, volume creation, target export |
| `distort-csi` | Deployment and DaemonSet | CSI gRPC services used by Kubernetes sidecars and kubelet |

The manager and agent use `controller-runtime`, the standard Go framework used by many Kubernetes operators.

The same `distort-csi` binary registers the CSI Identity, Controller, and Node services. The Helm chart runs it in two different contexts:

- in the CSI controller Deployment, next to `csi-provisioner`;
- on every consumer node in the CSI node DaemonSet, next to `csi-node-driver-registrar`.

## 2. Core Kubernetes concepts used by the project

### 2.1 API objects

A Kubernetes API object is durable structured state stored through the Kubernetes API server, normally backed by etcd. Built-in examples include Pods, Nodes, PersistentVolumes, and PersistentVolumeClaims.

Every API object has:

- `metadata`, such as name, namespace, labels, resource version, and deletion timestamp;
- `spec`, normally representing desired state;
- `status`, normally representing observed state reported by controllers.

For example, an `NVMePartition` spec requests a size and target backend. Its status eventually contains the export state, NQN, portal IP, and port.

`spec` and `status` are not enforced as absolute rules by Go itself. They are an API design convention. Correct separation is important because:

- users and higher-level controllers write desired state into `spec`;
- the component observing or implementing that state writes `status`;
- status changes do not normally mean that the user changed the request.

### 2.2 CRDs and custom resources

A **CustomResourceDefinition**, or CRD, extends the Kubernetes API with a new object type. After installing the DISTORT CRDs, the API server understands resources such as:

```text
NVMeDevice
NVMeDeviceClaim
NVMePartition
RDMAStorageNode
```

The Go types under `api/v1alpha1` define the schemas used by the code. Kubebuilder markers in comments are used to generate CRD schemas, status subresources, RBAC rules, and `kubectl get` columns.

The generated YAML under `config/crd/bases` and generated Go code such as `zz_generated.deepcopy.go` must not be edited manually. Changes begin in the owned Go type files and are propagated with:

```bash
make manifests
make generate
```

### 2.3 Desired state, observed state, and eventual consistency

Kubernetes is an eventually consistent system. A request does not imply that every component changes synchronously within the same API call.

For example:

1. CSI creates an `NVMePartition` with no assigned node.
2. The API server stores it.
3. The manager observes it and updates its spec with a node and physical device.
4. The appropriate node agent observes that update.
5. The agent creates and exports the volume.
6. The agent updates status to `Exported`.
7. CSI observes the status and returns the completed volume to the external provisioner.

Each step is performed by a different component and may be retried. The Kubernetes API object is the shared durable record connecting those steps.

### 2.4 Watches, informer caches, and work queues

Controllers do not normally query the API server in a tight loop.

`controller-runtime` establishes watches for selected object types. Underneath, client-go maintains an **informer cache**:

- it lists existing objects;
- it watches subsequent changes;
- it stores a local cached view;
- it emits events when objects are added, updated, or deleted.

Events enqueue a key, normally namespace plus name, into a rate-limited work queue. A controller worker takes the key and invokes `Reconcile`.

The queue stores keys, not a complete business transaction. The reconciler fetches the latest object when it starts. Multiple rapid updates may be collapsed into one reconciliation, which is expected and correct.

The cache also means reads through the manager's client may be slightly behind the API server. Code must not assume that every read immediately reflects a preceding write.

### 2.5 Reconciliation

**Reconciliation** is the repeated process of comparing desired state with observed state and taking actions that move the system closer to the desired state.

A reconciler is not meant to execute a one-time linear job such as:

```text
start -> perform step 1 -> perform step 2 -> finish forever
```

Instead, it should behave approximately like:

```text
read latest desired state
inspect current Kubernetes and external state
perform the next safe corrections
record observed status
return
```

It can be called:

- after object creation;
- after an update;
- after controller restart, when existing objects are listed again;
- after an error and rate-limited retry;
- after an explicit `RequeueAfter` interval;
- after changes to related watched objects.

This explains several important controller properties.

#### Idempotency

An operation is idempotent when repeating it produces the same final result rather than duplicating or corrupting state.

Examples from DISTORT include:

- checking for an existing SPDK lvstore before creating one;
- checking for an existing lvol before creating one;
- treating an already absent export as successfully unexported;
- checking whether a mount point is already mounted.

Idempotency is necessary because a process can successfully change external state and crash before updating Kubernetes status. On restart, the controller sees the old status and repeats the operation.

#### Requeue and retry

A reconciler returns a `ctrl.Result` and an error.

- Returning an error causes controller-runtime to retry with rate limiting/backoff.
- Returning `Requeue: true` asks for another reconciliation.
- Returning `RequeueAfter: duration` schedules another reconciliation after a delay.
- Returning an empty result and no error means no explicit retry. A future watched event can still enqueue the object.

Retries are not a substitute for idempotency. A retried non-idempotent operation can create duplicates or destroy valid state.

#### Level-triggered behavior

A robust controller reacts to current state, not only to the event that caused it to run. If an update event is missed or merged, reconciliation should still derive the correct action from the latest object and external state.

For example, the agent should care that an `NVMePartition` is assigned to its node and lacks a valid export. It should not depend on remembering the exact event that assigned it.

### 2.6 Resource versions and update conflicts

Kubernetes objects contain a `resourceVersion`. If two actors read the same object and both attempt to update it, the later update may receive a conflict because its copy is stale.

This is why controller code commonly:

- fetches the latest object before updating;
- uses `Patch` with a merge base for narrow changes;
- retries conflicts through reconciliation;
- uses the `/status` subresource when changing status.

DISTORT's agent has helper methods that re-fetch an `NVMePartition` or `NVMeDevice` before patching status. This reduces the risk of overwriting concurrent changes.

### 2.7 Owner references

An owner reference connects the lifecycle of one Kubernetes object to another. Kubernetes garbage collection can delete dependent objects when their owner is deleted.

DISTORT currently coordinates several resources by names and fields rather than using owner references extensively. When extending lifecycle handling, owner references are worth considering where one object is unambiguously owned by another.

### 2.8 Finalizers and deletion

Deleting a Kubernetes object with a finalizer does not immediately remove it.

The API server:

1. sets `metadata.deletionTimestamp`;
2. leaves the object visible;
3. waits until controllers remove all finalizers;
4. completes deletion afterward.

DISTORT uses a finalizer on `NVMePartition`. The agent sees the deletion timestamp and:

1. removes the NVMe-oF export;
2. deletes the logical volume or physical partition;
3. performs any required backend bookkeeping;
4. removes the finalizer.

If cleanup fails, the finalizer remains and reconciliation retries. This prevents Kubernetes from forgetting the object while external storage resources still exist.

A finalizer can also leave an object stuck in `Terminating` if the responsible controller is permanently unavailable or its cleanup can never succeed. Operational tooling must expose that condition clearly.

### 2.9 Controllers versus reporters

A **controller** owns a desired-state relationship. It watches resources and changes Kubernetes or external state to satisfy their specs.

A **reporter** primarily observes external reality and publishes it into Kubernetes.

The DISTORT `Reporter` is a `controller-runtime` runnable, but it is not a reconciler with a per-object work queue. It runs a ticker every 30 seconds:

1. discovers kernel-bound and SPDK-bound NVMe controllers;
2. creates or reads `NVMeDevice` objects;
3. reports capacity and device information;
4. creates or updates the local `RDMAStorageNode`.

The conceptual difference is:

- the partition agent says, “this object requests an export, so I will create one”;
- the reporter says, “this hardware exists, so I will describe it in the API.”

The current reporter is polling-based. It does not receive a hardware event directly from udev.

### 2.10 Conditions

`metav1.Condition` is the Kubernetes convention for reporting meaningful state such as `Ready`, `Degraded`, or `Available`, including:

- a boolean-like status;
- a reason code;
- a human-readable message;
- the observed generation;
- the last transition time.

The DISTORT API types contain condition fields, but most current code uses simpler fields such as `State`, `Active`, and `ActiveBackend` and does not yet populate conditions consistently. Production diagnostics would benefit from using conditions for transient failures and readiness.

## 3. CSI and gRPC

### 3.1 What CSI is

CSI, the Container Storage Interface, is a versioned gRPC contract between a container orchestrator and a storage plugin.

CSI does not specify how the storage backend must work internally. It specifies operations such as:

- create and delete a volume;
- stage and unstage a volume on a node;
- publish and unpublish a staged volume for a workload;
- report plugin identity and capabilities.

Kubernetes uses standard sidecar containers to translate Kubernetes storage workflows into CSI RPCs. The storage vendor implements the CSI server.

### 3.2 What gRPC is doing here

gRPC defines typed services and request/response messages using Protocol Buffers. The CSI specification provides generated Go interfaces such as:

```go
CreateVolume(context.Context, *csi.CreateVolumeRequest)
NodeStageVolume(context.Context, *csi.NodeStageVolumeRequest)
NodePublishVolume(context.Context, *csi.NodePublishVolumeRequest)
```

DISTORT creates a gRPC server and registers three service implementations:

- `IdentityServer`;
- `ControllerServer`;
- `NodeServer`.

The server listens on a Unix domain socket such as:

```text
/csi/csi.sock
```

Unix sockets are used because the sidecar and driver containers share a mounted directory inside the same Pod. On node Pods, the socket is also exposed under kubelet's plugin directory.

The gRPC server is not an HTTP REST service, and users do not normally invoke it directly. Kubernetes sidecars and kubelet are its clients.

### 3.3 CSI Identity service

The Identity service reports:

- plugin name: `storage.distort.io`;
- vendor version;
- whether the plugin provides a Controller service;
- basic health through `Probe`.

The kubelet and sidecars use this to identify and validate the driver.

### 3.4 CSI Controller service

The Controller service runs in the CSI controller Deployment. In DISTORT, its main methods are `CreateVolume` and `DeleteVolume`.

`CreateVolume`:

1. validates the CSI request;
2. reads capacity and StorageClass parameters;
3. creates an `NVMePartition`;
4. waits for its status to become `Exported`;
5. returns a CSI volume containing the NQN, portal IP, and portal port in `VolumeContext`.

The method currently polls the `NVMePartition` every five seconds with a two-minute internal timeout.

`CreateVolume` must be idempotent because the external provisioner may repeat it with the same name. The code handles `AlreadyExists` by fetching the existing partition and checking at least its target backend before continuing.

`DeleteVolume` finds the `NVMePartition` by volume ID and requests its deletion. The agent finalizer performs actual target and volume cleanup. The current CSI method returns without waiting for finalizer completion.

### 3.5 The external provisioner sidecar

`csi-provisioner` is maintained by Kubernetes SIG Storage. It watches PVC-related state and calls the DISTORT Controller service.

The rough path is:

```text
PVC
  -> Kubernetes persistent-volume controller / external-provisioner
  -> CSI CreateVolume RPC
  -> DISTORT NVMePartition
  -> exported target
  -> CSI CreateVolume response
  -> PersistentVolume
  -> PVC bound to PV
```

The sidecar is not part of the DISTORT Go code, but it is essential. DISTORT does not itself watch PVCs directly.

### 3.6 CSI Node service

The Node service runs on every consumer node and is called by kubelet.

#### NodeStageVolume

Staging prepares the volume once at a node-level staging path:

1. read NQN and portal information from `VolumeContext`;
2. execute `nvme connect -t rdma`;
3. find the Linux NVMe controller and namespace corresponding to the NQN;
4. wait for the device node to appear;
5. detect whether the block device already has a filesystem;
6. format it as ext4 when no filesystem is detected;
7. mount it at kubelet's staging path.

#### NodePublishVolume

Publishing makes the staged mount available at the target path for a specific Pod. The current implementation uses a bind mount from the staging path to the target path.

#### NodeUnpublishVolume

Unpublishing unmounts the Pod-specific target path.

#### NodeUnstageVolume

Unstaging:

1. unmounts the node staging path;
2. disconnects the NVMe-oF controller by NQN.

Staging and publishing are separate because a volume can be prepared once on a node and then made available to a workload path. This separation is required by the CSI capability that DISTORT advertises.

### 3.7 The node-driver-registrar sidecar

`csi-node-driver-registrar` connects to the CSI socket and registers the driver with kubelet through kubelet's plugin registry.

Registration tells kubelet:

- the CSI driver name;
- where kubelet can reach its Unix socket;
- that the node service is available.

The registrar does not mount volumes itself.

### 3.8 Why `attachRequired` is false

The chart creates a `CSIDriver` object with:

```yaml
attachRequired: false
```

This tells Kubernetes not to run a separate CSI `ControllerPublishVolume` attach operation. DISTORT establishes the remote connection during `NodeStageVolume`, so it does not currently implement the normal CSI attach/detach RPC pair or use `VolumeAttachment` as part of its flow.

## 4. DISTORT custom resources

### 4.1 `NVMeDevice`

`NVMeDevice` represents a physical NVMe controller discovered on a node.

Important spec fields:

- node name;
- PCI address;
- hardware serial number;
- model;
- total capacity;
- NUMA node.

Important status fields:

- `Available` or `Claimed`;
- the exact owning claim namespace, name, and immutable UID;
- remaining free capacity;
- active backend, such as `spdk` or `kernel`.

The serial number is the stable identity used to match claims and partitions. The Linux name, such as `nvme0`, is not stable across boots or device changes.

`NVMeDevice` is cluster-scoped according to its Go markers.

### 4.2 `NVMeDeviceClaim`

An `NVMeDeviceClaim` is an administrative reservation of a physical device identified by serial number.

The manager's claim reconciler:

1. finds an `NVMeDevice` with the requested serial;
2. marks the device `Claimed`;
3. writes the matched device and node into claim status.

Its finalizer marks the device `Available` again when the claim is deleted.
Cleanup compares the immutable claim UID before releasing the device, so deleting
an old claim cannot release hardware already adopted by a replacement claim.

This resource is separate from a PVC. A device claim authorizes DISTORT to allocate from a physical drive; a PVC requests a logical volume for a workload.

### 4.3 `NVMePartition`

`NVMePartition` is the central resource in the volume provisioning workflow.

Its spec contains:

- requested size;
- assigned node;
- parent device serial number;
- target backend;
- volume manager;
- backend-specific target options.

Its status contains:

- lifecycle state;
- immutable external/backend identity and opaque CSI volume handle;
- exact backend volume path;
- NQN;
- portal IP;
- portal port;
- conditions, including claim-authorization failures.

Despite the name, an `NVMePartition` is not always a DOS/GPT partition. With the SPDK backend, the default plugin mapping uses an SPDK logical volume. With the kernel backend, it uses a partitioning implementation.

### 4.4 `RDMAStorageNode`

`RDMAStorageNode` summarizes a storage node:

- Kubernetes node name;
- RDMA IP;
- transport;
- aggregate total and free capacity;
- number of exports.

The reporter currently sets the IP from the Kubernetes Node's `InternalIP`, declares `RoCEv2`, and reports capacity. `ActiveExports` is currently set to zero rather than derived from actual exports.

The manager registers an `RDMAStorageNodeReconciler`, but its reconcile method is currently a scaffold with no behavior. Placement currently selects directly from claimed `NVMeDevice` objects rather than using `RDMAStorageNode` health.

## 5. Manager internals

The manager registers four reconcilers.

### 5.1 Device claim reconciler

File: `internal/controller/nvmedeviceclaim_controller.go`

This reconciler binds administrative claims to devices by exact serial number.

Its main limitations today are:

- it watches claims but does not explicitly watch devices, so a claim that initially finds no device may not be retried when a device later appears;
- it still uses basic status fields rather than conditions for most state transitions;
- some updates use full `Update` rather than narrower patches.

Claim ownership itself is explicit: the device status stores the claim namespace,
name, and UID, and deletion releases the device only when that UID still matches.

### 5.2 Partition placement reconciler

File: `internal/controller/nvmepartition_controller.go`

This reconciler handles `NVMePartition` objects with an empty `spec.nodeName`.

It:

1. lists all devices;
2. considers only claimed devices;
3. excludes devices locked to another backend;
4. checks free capacity;
5. selects the device with the greatest free capacity;
6. writes the node name and parent serial number into the partition spec.

If no device fits, it requeues after five seconds.

This is a simple “most free bytes” scheduler. It does not currently account for:

- consumer Pod topology;
- RDMA reachability or node health;
- NUMA preferences;
- reservations made concurrently by multiple scheduling reconciliations;
- access modes;
- anti-affinity or failure domains.

### 5.3 Device capacity reconciler

File: `internal/controller/nvmedevice_controller.go`

This controller calculates:

```text
free capacity = physical total capacity - sum(size of assigned partitions)
```

It watches both `NVMeDevice` and `NVMePartition`. A partition event is mapped back to its parent device so capacity is recalculated.

This is accounting based on Kubernetes objects, not a measurement of SPDK allocation metadata or the on-disk partition table. Correctness therefore depends on keeping Kubernetes lifecycle and external cleanup synchronized.

### 5.4 RDMA storage node reconciler

File: `internal/controller/rdmastoragenode_controller.go`

This is currently an empty Kubebuilder scaffold. The object is populated by the agent reporter, not actively reconciled by the manager.

## 6. Agent internals

The agent runs on nodes with access to storage hardware. It is privileged and mounts host `/dev`, `/sys`, kernel modules, and huge pages. It also uses host networking, IPC, and PID namespaces.

Those privileges are necessary for the current implementation to:

- inspect PCI and NVMe sysfs entries;
- load drivers;
- change PCI driver binding;
- run SPDK with huge pages;
- create kernel or SPDK NVMe-oF targets.

They also make the agent a high-trust component. A compromise of this Pod is effectively a host compromise.

### 6.1 Hardware discovery

File: `internal/agent/nvme_discovery.go`

Discovery combines two sources.

#### Kernel-bound devices

The agent reads `/sys/class/nvme` and `/sys/class/block` to obtain:

- controller name;
- PCI address;
- model;
- serial number;
- NUMA node;
- namespace capacity.

It excludes non-PCIe controllers so that remote NVMe-oF devices connected on the same host are not accidentally advertised as local physical storage.

It uses `lsblk` to skip controllers with mounted namespaces and supports:

- `NVME_ALLOWED_DEVICES`;
- `NVME_EXCLUDE_DEVICES`.

#### SPDK-bound devices

Once a controller is detached from the kernel and owned by a user-space driver, it is no longer represented in the same way through the kernel NVMe subsystem. The agent therefore also queries SPDK JSON-RPC and merges results by serial number.

### 6.2 The reporter loop

File: `internal/agent/reporter.go`

Every 30 seconds the reporter:

- discovers devices;
- creates missing `NVMeDevice` resources;
- reads capacity for existing devices;
- creates or updates the local `RDMAStorageNode`;
- aggregates capacity from claimed devices.

This loop is observational, but it also creates API objects. It does not currently remove stale `NVMeDevice` resources when hardware disappears or mark them unavailable.

### 6.3 The partition manager

File: `internal/agent/partition_manager.go`

The `PartitionManager` is a node-local reconciler for `NVMePartition`.

Every agent watches all partitions but immediately ignores those whose `spec.nodeName` does not match its own node. For a matching partition, it:

1. resolves the target backend plugin;
2. resolves the volume manager plugin;
3. installs a cleanup finalizer;
4. fetches the parent `NVMeDevice`;
5. verifies that the requested backend does not conflict with the device's active backend;
6. prepares the physical device for the backend;
7. records the device's active backend;
8. discovers the device again;
9. prepares the storage layout;
10. creates or finds the requested volume;
11. obtains the node portal IP;
12. exports the volume;
13. updates partition status to `Exported`.

Deletion runs the inverse operations before removing the finalizer.

### 6.4 Plugin interfaces

File: `internal/agent/plugins/interface.go`

The agent separates two decisions.

A `TargetBackend` controls how a block device is exported:

- set up physical device ownership;
- export a volume;
- unexport a volume.

A `VolumeManager` controls how capacity is carved:

- initialize storage layout;
- create a volume;
- delete a volume.

Current implementations include:

- SPDK target backend;
- kernel configfs target backend;
- SPDK lvol volume manager;
- `parted` volume manager.

The partition manager translates the user-facing default `volume-manager: partition` to:

- `spdk-lvol` when the target backend is SPDK;
- `parted` when the target backend is kernel.

This mapping is important because the CRD-facing name and internal registered plugin name are not always the same.

## 7. SPDK internals

### 7.1 What SPDK changes

The Linux kernel normally owns a PCIe NVMe controller through the `nvme` driver. SPDK needs direct user-space ownership, commonly through `vfio-pci` or `uio_pci_generic`.

The SPDK backend:

1. starts `nvmf_tgt` if it is not running;
2. waits for the JSON-RPC service;
3. runs SPDK's setup script to change driver binding;
4. attaches the physical NVMe controller to SPDK;
5. creates or discovers an lvol store;
6. creates or discovers an lvol;
7. creates an NVMe-oF transport, subsystem, namespace, and listener.

Only one backend should own a physical controller at a time. `NVMeDevice.status.activeBackend` is used as a control-plane lock against mixing kernel and SPDK allocations on the same device.

### 7.2 SPDK JSON-RPC

File: `internal/agent/plugins/spdk_rpc.go`

DISTORT invokes SPDK's `rpc.py` command for methods such as:

```text
bdev_nvme_attach_controller
bdev_lvol_get_lvstores
bdev_lvol_create_lvstore
bdev_lvol_create
nvmf_create_transport
nvmf_create_subsystem
nvmf_subsystem_add_ns
nvmf_subsystem_add_listener
```

The helper captures stdout and stderr and decodes JSON responses. Some SPDK methods return an unquoted UUID, so the helper accounts for that response format.

### 7.3 SPDK lvol stores and lvols

File: `internal/agent/plugins/vol_spdk_lvol.go`

An SPDK logical volume store manages allocation from a base block device. Logical volumes are bdevs created from that store.

DISTORT uses deterministic names:

```text
lvstore: lvs_<device-name>
lvol alias: <lvstore>/<partition-name>
```

The code queries existing lvstores and bdev aliases before creation. This is what allows reconciliation after a status update failure or agent restart without deliberately creating a second lvol with the same logical identity.

The lvol's allocation metadata is associated with storage managed by SPDK, while NVMe-oF transports, subsystems, and listeners are runtime target configuration. A restart may therefore require rediscovering the former and rebuilding the latter.

### 7.4 NVMe-oF terminology

An **NQN**, or NVMe Qualified Name, identifies an NVMe subsystem. DISTORT generates:

```text
nqn.2026-02.io.distort:volume-<volume-name>
```

An **NVMe subsystem** is the target-side logical entity presented to initiators.

A **namespace** is a block storage unit exposed by the subsystem. DISTORT adds the lvol or partition bdev as a namespace.

A **listener** specifies how initiators reach the subsystem, including transport, address, and service port.

A **transport** configures the target's protocol implementation, here RDMA.

The **initiator** is the consumer-side host running `nvme connect`. The **target** is the storage-side SPDK or kernel service exporting the namespace.

### 7.5 Kernel backend

The kernel backend performs equivalent target configuration through Linux configfs under `/sys/kernel/config/nvmet`.

Instead of an SPDK lvol, the normal pairing uses an on-disk partition created through `parted`. The backend loads kernel modules, builds the subsystem/configfs hierarchy, links the namespace, and configures a port.

Both backends implement the same Go interfaces, but their persistence, device ownership, cleanup behavior, and failure modes differ.

## 8. Complete provisioning sequence

The following is the current end-to-end sequence for dynamic provisioning.

```text
1. User creates PVC
2. Kubernetes external-provisioner observes it
3. external-provisioner calls DISTORT CreateVolume over gRPC
4. CSI ControllerServer creates NVMePartition
5. manager partition reconciler assigns claimed NVMeDevice and node
6. node's PartitionManager observes assignment
7. agent prepares physical controller
8. agent creates/discovers lvol or partition
9. agent creates NVMe-oF export
10. agent writes NQN and portal into NVMePartition status
11. CSI CreateVolume returns volume metadata
12. external-provisioner creates/binds PersistentVolume
13. scheduler places a Pod using the PVC
14. kubelet calls NodeStageVolume
15. node CSI service runs nvme connect
16. node CSI service formats and mounts the device at staging path
17. kubelet calls NodePublishVolume
18. node CSI service bind-mounts staging path into Pod target path
19. application performs I/O
```

The API-server portion and the CSI call can overlap in time: `CreateVolume` remains open while polling the `NVMePartition`. The manager and agent progress asynchronously.

## 9. Deletion sequence

The intended reverse sequence is:

```text
1. Pod stops using volume
2. kubelet calls NodeUnpublishVolume
3. DISTORT unmounts Pod target path
4. kubelet calls NodeUnstageVolume
5. DISTORT unmounts staging path and runs nvme disconnect
6. external-provisioner calls DeleteVolume according to PV reclaim policy
7. CSI requests NVMePartition deletion
8. API server sets deletionTimestamp because finalizer exists
9. storage-node agent removes target export
10. agent deletes lvol or partition
11. agent removes finalizer
12. API server deletes NVMePartition
13. device capacity reconciler recalculates free capacity
```

The exact PVC/PV deletion behavior also depends on the PersistentVolume reclaim policy managed by Kubernetes.

## 10. State and restart behavior

Understanding where state lives is essential for reasoning about recovery.

| State | Location | Survives component restart? |
|---|---|---|
| PVC, PV, CR specs and statuses | Kubernetes API/etcd | Yes |
| Controller work queue | Process memory | No, but objects are listed/watched again |
| Informer cache | Process memory | No, rebuilt from API server |
| SPDK target subsystems/listeners | `nvmf_tgt` runtime | Generally no unless explicitly restored |
| SPDK lvol allocation metadata | Storage managed by SPDK | Intended to be rediscovered |
| Linux mounts | Host mount namespace, depending on container propagation | Not represented by CSI process memory |
| NVMe initiator connection | Host kernel | Independent of CSI process memory |
| PCI driver binding | Host kernel/sysfs | Persists beyond an individual Go process |
| Reporter ticker | Agent process memory | Restarts with agent |

After a controller restart, existing API objects are listed into the cache and normally enqueue reconciliation. This allows the controller to reconstruct work from durable desired state.

That recovery is reliable only when reconciliation checks the complete external state. Checking only a Kubernetes status field can miss an SPDK or host failure. Checking only that an NQN exists can miss a subsystem with no namespace or listener.

## 11. Error handling and reliability model

### 11.1 Transient versus terminal failures

A transient failure may succeed later:

- API conflict;
- SPDK RPC socket not ready;
- udev delay;
- temporary device transition;
- target process restart.

These should normally return an error or scheduled requeue without permanently suppressing future work.

A terminal failure requires a change to the request or environment:

- invalid backend name;
- missing parent serial number;
- incompatible active backend;
- requested size outside supported constraints.

Production controllers should report both categories through conditions and events so operators know whether the system is retrying.

### 11.2 Partial success

External operations and Kubernetes status updates are not one atomic transaction.

For example:

1. SPDK creates an lvol.
2. The agent crashes before status is updated.
3. Kubernetes still shows the previous state.
4. Reconciliation repeats.

The correct response is to discover the existing lvol and continue. It is not safe to assume that an error means nothing changed.

The same concern applies to multi-step exports:

```text
create subsystem
add namespace
add listener
update Kubernetes status
```

A robust implementation validates and repairs every component, not only the subsystem name.

### 11.3 Current recovery limitations

The current code has several areas that should be understood as engineering work rather than guaranteed production behavior:

- An exported SPDK partition is checked when reconciliation occurs, but there is no periodic requeue after a healthy result. If `nvmf_tgt` exits while the agent remains alive and no Kubernetes object changes, recovery may not start automatically.
- SPDK export validation currently checks only whether the NQN exists. It does not verify the namespace and listener.
- The child `nvmf_tgt` exit is logged, but there is no dedicated supervisor loop.
- Several external commands do not consistently use the reconciliation context, so cancellation and command timeouts are incomplete.
- CSI `CreateVolume` uses polling and an internal timeout rather than a watch.
- CSI `DeleteVolume` does not wait for finalizer-driven cleanup.
- `GetDeviceByNQN` assumes namespace 1 and constructs `<controller>n1`.
- Formatting supports ext4 and XFS, preserves an existing matching filesystem, and rejects a mismatch. Custom format flags and StorageClass mount options remain unsupported.
- Access modes are not comprehensively enforced.
- Controller-side attachment fencing is absent (`attachRequired: false` and no ControllerPublish/ControllerUnpublish implementation), so forced cross-node migration can create concurrent filesystem users.
- `RDMAStorageNode` health and active export reporting are incomplete.
- The claim reconciler does not watch device creation and may leave an unmatched claim idle until another claim event.
- Stale hardware resources are not marked unavailable or removed by the reporter.

These limitations do not mean the architecture is invalid. They identify places where a production reliability review should focus.

## 12. Deployment and process wiring

### 12.1 Manager Deployment

The manager is a Deployment and enables leader election. Leader election ensures that if multiple manager replicas exist, only one actively performs controller work for the shared leader-election identity.

Leader election protects cluster-wide reconcilers from duplicate active instances. It does not replace idempotency; leadership can change after a partial operation.

### 12.2 Agent DaemonSet

A DaemonSet schedules an agent Pod on each selected storage node. The Pod obtains its node name through the downward API:

```yaml
fieldRef:
  fieldPath: spec.nodeName
```

The agent uses this value to:

- name reported device resources;
- process only partitions assigned to its node;
- locate the Kubernetes Node's internal IP.

### 12.3 CSI controller Deployment

This Pod contains:

- the upstream `csi-provisioner`;
- the DISTORT CSI driver;
- a shared `emptyDir` containing the Unix socket.

The provisioner calls the DISTORT Controller service through that socket.

### 12.4 CSI node DaemonSet

This Pod contains:

- the upstream node-driver registrar;
- the privileged DISTORT CSI driver;
- host `/dev`;
- kubelet plugin and registration directories;
- the kubelet directory with bidirectional mount propagation.

Bidirectional mount propagation is required so mounts performed inside the CSI container become visible in the host/kubelet mount namespace and vice versa.

## 13. Repository map

```text
api/v1alpha1/
    Owned Go definitions for DISTORT Kubernetes APIs

cmd/distort-manager/
    Manager process entry point and controller registration

cmd/distort-agent/
    Agent process entry point, partition reconciler, reporter registration

cmd/distort-csi/
    CSI process entry point and Kubernetes client creation

internal/controller/
    Cluster-wide manager reconcilers

internal/agent/
    Hardware discovery, reporter, and node-local partition reconciliation

internal/agent/plugins/
    Target backends, volume managers, and SPDK RPC wrapper

internal/csi/
    CSI Identity, Controller, and Node gRPC implementations

deploy/charts/distort/
    Helm installation templates and default values

config/crd/bases/
    Generated CRD manifests

config/rbac/
    Generated or assembled Kubernetes permissions

config/samples/
    Example custom resources

internal/controller/*_test.go
    envtest-based controller tests

test/e2e/
    End-to-end test suite
```

## 14. How to read and modify this project safely

For a controller change, trace five things:

1. Which object is watched?
2. What desired state is read from its spec?
3. What external or related state is inspected?
4. Which actions are safe to repeat?
5. What event or requeue will cause recovery after a later failure?

For a CSI change, trace:

1. Which CSI actor calls the RPC: external provisioner or kubelet?
2. What idempotency does the CSI specification require?
3. Which request fields and volume capabilities must be validated?
4. Which operation changes control-plane state versus host mount/device state?
5. What happens if the RPC is repeated after partial success?

For an SPDK or host-operation change, trace:

1. Whether the state is durable or process-local;
2. whether command success can occur before the caller receives a response;
3. how the next reconciliation discovers partial state;
4. how device ownership is protected;
5. whether the operation honors context cancellation and has a timeout.

After editing Go code, the project instructions require:

```bash
make lint-fix
make test
```

After changing API types or Kubebuilder markers:

```bash
make manifests
make generate
make lint-fix
make test
```

End-to-end tests must run against the isolated Vagrant/K3s lab rather than a development or production cluster.

## 15. A concise mental model

The complete system can be summarized without hiding its separate responsibilities:

- Kubernetes stores durable desired and observed state.
- The external CSI provisioner converts PVC demand into a CSI `CreateVolume` call.
- The DISTORT CSI Controller service converts that call into an `NVMePartition`.
- The manager assigns the partition to a claimed physical device.
- The node agent reconciles that assignment into real storage and an NVMe-oF export.
- The agent records connection metadata in partition status.
- The CSI Node service uses that metadata to connect and mount the remote block device.
- The application's I/O then flows through NVMe-oF, outside the Kubernetes API and manager.
- Reconciliation, idempotency, status, retries, and finalizers allow independently running components to converge despite restarts and partial failures.

When debugging, identify which boundary has failed:

```text
PVC/PV and external provisioner
        |
CSI controller gRPC
        |
NVMePartition placement
        |
storage-node agent reconciliation
        |
SPDK/kernel target configuration
        |
network and NVMe initiator connection
        |
CSI node staging/publishing and mounts
```

That boundary-based approach is more reliable than treating “volume provisioning” as one indivisible operation, because in Kubernetes it is a distributed sequence involving multiple processes and multiple forms of state.
