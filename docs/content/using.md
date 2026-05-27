---
title: "Using DISTORT"
description: "Understand how to manage physical drives, allocate devices via NVMeDeviceClaim CRDs, and provision volumes using StorageClasses."
type: "page"
---

Once DISTORT is deployed, it seamlessly integrates with standard Kubernetes storage workflows. This guide covers how administrators manage physical hardware and how developers request high-performance RDMA storage volumes.

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

Create a standard Kubernetes `StorageClass` pointing to the DISTORT CSI driver (`storage.distort.io`):

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: distort-rdma
# Provisioner name defined by the DISTORT CSI template
provisioner: storage.distort.io
volumeBindingMode: Immediate
```

Apply the StorageClass:

```bash
kubectl apply -f storageclass.yaml
```

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
5. The **CSI-Node-Server** on the compute node executing the application Pod connects to the remote target over the SoftRoCE/RDMA network, formats the block device with `ext4`, and bind-mounts it directly into the container!
