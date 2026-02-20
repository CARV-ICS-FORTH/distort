This design document outlines a Kubernetes-native storage engine for high-performance **NVMe-over-Fabrics (NVMe-oF) using RDMA**. It follows the principle of decoupling physical hardware management from the Kubernetes CSI lifecycle using a "State-Machine-via-CRD" approach.

## ---

**1\. System Overview**

The architecture is divided into two distinct logical layers:

### **A. NVMe Management Layer (The "Hardware Control Plane")**

Responsible for physical device discovery, disk partitioning, and kernel-level NVMe-oF target exports.

* **Components:** NVMe-Management-Controller (Deployment), NVMe-Node-Agent (DaemonSet).  
* **Resources:** NVMeDevice, NVMeDeviceClaim, NVMePartition, RDMAStorageNode.

### **B. CSI Layer (The "Kubernetes Bridge")**

Responsible for translating PVC requests into storage allocations and mounting volumes to Pods.

* **Components:** CSI-Provisioner (Sidecar), CSI-Resizer, CSI-Node-Server (DaemonSet).

## ---

**2\. Component Details**

### **NVMe Management Layer**

#### **NVMe-Node-Agent (DaemonSet)**

* **Discovery:** Scans PCIe bus for NVMe controllers and /sys/class/infiniband for RDMA-capable NICs.  
* **Hardware Reporting:** Manages the lifecycle of NVMeDevice CRDs (unique per PCIe address) and RDMAStorageNode (node health/IP).  
* **Execution:** Watches NVMePartition CRDs assigned to its node.  
  * **Action:** Executes parted for slicing and nvmetcli (or configfs) to create NVMe-oF subsystems.  
  * **Feedback:** Populates status.nqn and status.portal once the export is live.

#### **NVMe-Management-Controller (Deployment)**

* **Orchestration:** Reconciles NVMeDeviceClaim objects. When an admin claims a disk, it signals the Agent to initialize the GPT table.  
* **Placement Logic:** When an NVMePartition is requested without a specific node, it selects the optimal RDMAStorageNode based on available capacity and RDMA link health.

### **CSI Layer**

#### **CSI-Provisioner**

* **Trigger:** Watches for new PersistentVolumeClaims.  
* **Allocation:** Instead of calling a backend API, it creates an NVMePartition CRD. It specifies the size, access mode (RW/RO), and requested node (if dictated by K8s topology).  
* **Binding:** Watches for the NVMePartition.status to become Ready, then creates the PersistentVolume (PV) object containing the NQN and Portal IP.

#### **CSI-Node-Server**

* **Connection:** Executes nvme connect \-t rdma \-a \<Portal\_IP\> \-n \<NQN\>.  
* **Mount:** Mounts the resulting block device (e.g., /dev/nvme1n1) into the Pod's path. If the accessMode is ReadOnlyMany, it enforces the \-o ro mount flag.

## ---

**3\. Data Model (CRDs)**

| CRD | Key Fields (Spec/Status) |
| :---- | :---- |
| **NVMeDevice** | pciAddress, totalCapacity, numaNode, status: {state: Available/Claimed} |
| **NVMePartition** | size, nodeName, accessMode, status: {nqn, portalIP, state: Exported} |
| **RDMAStorageNode** | rdmaIP, linkSpeed, status: {health: Online} |

## ---

**4\. Interaction Use Cases**

### **Use Case 1: Private Volume Provisioning (RWO)**

1. **User** creates 100Gi PVC (ReadWriteOnce).  
2. **CSI-Provisioner** creates NVMePartition with size: 100Gi.  
3. **Mgmt-Controller** identifies a node with 100Gi free and assigns spec.nodeName.  
4. **Mgmt-Agent** (on target node) creates the partition and exports the NQN.  
5. **CSI-Provisioner** sees status.nqn, creates the PV.  
6. **CSI-Node-Server** (on app node) connects via RDMA and mounts the disk.

### **Use Case 2: Shared Dataset Provisioning (ROX)**

1. **Admin** pre-partitions a 1Ti NVMe with a dataset and labels the NVMePartition as dataset: imagenet.  
2. **User** creates a PVC requesting a volume with label selector dataset: imagenet and accessMode: ReadOnlyMany.  
3. **CSI-Provisioner** finds the existing NVMePartition CRD.  
4. **CSI-Provisioner** creates a PV pointing to that existing NQN (no new partition is created).  
5. **Multiple Pods** on different nodes trigger CSI-Node-Server to connect to the same NQN simultaneously.

### **Use Case 3: Volume Deletion**

1. **User** deletes PVC.  
2. **CSI-Provisioner** deletes the corresponding NVMePartition CRD.  
3. **Mgmt-Agent** receives the deletion event:  
   * Disconnects/Unexports the subsystem from the kernel.  
   * Wipes the partition table entry.  
   * Updates NVMeDevice free capacity.

