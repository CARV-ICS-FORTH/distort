---
title: "Contributing Guide"
description: "Learn how to configure your local development environment, boot virtual K3s RDMA nodes, and run E2E Ginkgo testing suites using our simple Makefile commands."
type: "page"
---

Welcome to the DISTORT developer and contributor guide! This page details how to set up an isolated local environment for building, running, and verifying the storage engine.

All standard developer workflows (compilation, image generation, cluster deployment, environment resets, and testing) are automated through targets inside the project's root [Makefile](file:///Users/chazapis/Source/FORTH/distort/Makefile).

---

## Local Development VM Cluster Setup

To test high-performance disaggregated storage and RDMA fabrics locally without specialized bare-metal hardware, DISTORT provides an automated virtual environment using **Vagrant** and **VirtualBox**.

This environment boots a multi-node Kubernetes cluster (using lightweight **K3s**) running on Ubuntu 24.04 VMs, with virtual NVMe storage devices and SoftRoCE-emulated RDMA interfaces.

### Prerequisites

Ensure you have the following installed on your host system:
* **Vagrant** (v2.3+)
* **VirtualBox** (v7.0+)
* **CPU and Memory:** At least 8 GB of RAM and 4 CPU cores free (the VMs require a total of 8 GB RAM and 4 virtual cores).

---

## Booting the Local Cluster

The environment is defined in the [Vagrantfile](file:///Users/chazapis/Source/FORTH/distort/vagrant/Vagrantfile) inside the `vagrant/` folder. It provisions two virtual machines:
1. **`distort-master` (IP: `192.168.56.10`):** K3s Control Plane / Master Node.
2. **`distort-worker-1` (IP: `192.168.56.11`):** Storage Provider / Worker Node.

To spin up the cluster, navigate to the project root and run:

```bash
cd vagrant
vagrant up
```

### What Happens Behind the Scenes?
1. **Virtual NVMe Provisioning:** Vagrant uses `VBoxManage` to attach a virtual PCIe NVMe controller to each VM. It binds two virtual disks (`1 GB` each) to the controller, simulating real NVMe block devices. It also injects a custom serial number (`SN-distort-master` and `SN-distort-worker-1`) for precise discovery tracking.
2. **SoftRoCE (RDMA) Emulation:** The script loads the `rdma_rxe` kernel module and starts a systemd service (`rdma-link.service`) to bind a SoftRoCE link (`rxe_eth1`) on top of the host-only private network adapter (`eth1`). This emulates an RDMA fabric without hardware overhead.
3. **SPDK Requirements Setup:**
   * Loads `vfio-pci` and `uio_pci_generic` user-space driver modules.
   * Allocates **1024 hugepages of 2 MB** (`vm.nr_hugepages=1024`, totaling 2 GB memory) to grant SPDK direct memory access (DMA) buffers.
4. **K3s Cluster Installation:**
   * Installs K3s server on the master node, disabling `traefik` and binding flannel network to `eth1`.
   * Saves the join token to a shared folder (`/vagrant/cluster_info/node-token`).
   * Workers read the token and automatically join the cluster as K3s agents.

## Standard Development Workflows & Compilation

### Prerequisites

To compile or deploy DISTORT manually, ensure you have the following installed on your host system:
* **Go 1.23+**
* A Kubernetes cluster (or a local environment like Minikube, Kind, or Vagrant)
* **Kubebuilder / controller-gen** installed
* **Helm 3+**

---

### Workflow Commands

#### 1. Compile Binaries
The project compiles three distinct binaries directly using the Makefile:

```bash
make build
```

This compiles the Go code and places the binaries under `bin/`:
* `bin/distort-manager`: The centralized control plane manager.
* `bin/distort-agent`: The privileged node storage agent.
* `bin/distort-csi`: The standard CSI driver.

#### 2. Manifests & Code Generation
After modifying any CRD schema in `api/` or updating controller annotations, regenerate deepcopy methods and Kubernetes manifest files:

```bash
make generate   # Regenerates api/v1/zz_generated.deepcopy.go
make manifests  # Regenerates CRD definitions and RBAC rules
```

#### 3. Formatting & Linting
Verify code style and automatically apply lint fixes:

```bash
make lint-fix
```

#### 4. Local Host Execution
You can run the centralized manager controller directly on your host machine to speed up developer iteration. It will connect to your cluster using the active local kubeconfig:

```bash
make run
```

---

## Deploying via Helm (Production/Manual)

DISTORT includes a robust Helm chart under `deploy/charts/distort` that bundles the manager, node-level storage agent DaemonSet, CSI driver DaemonSet, and all required RBAC roles and Custom Resource Definitions.

To manually deploy the stack into your active Kubernetes cluster under the `distort-system` namespace:

```bash
helm install distort ./deploy/charts/distort \
  --namespace distort-system \
  --create-namespace
```

To view the values and override resource limits, image repositories, or tags:

```bash
helm show values ./deploy/charts/distort
```

---

## Testing & Environment Validation

DISTORT is equipped with a robust two-layer testing framework: unit tests (which run in a simulated K8s API server using `envtest`) and end-to-end integration tests (which run the full storage lifecyle inside the Vagrant VMs).

### 1. Running Unit Tests

Unit tests verify controller reconciliation logic by booting an in-memory K8s API server. Execute them on your host with:

```bash
make test
```

### 2. End-to-End (E2E) Integration Tests

The E2E test suite validates the entire storage engine stack inside your active Vagrant cluster. The validation suite is written in the BDD **Ginkgo + Gomega** framework and asserts behavior across three layers:
1. **Hardware Discovery (Agent Layer):** Asserts that the node agent detects virtual NVMe disks on worker nodes and publishes them as `NVMeDevice` CRDs with accurate capacities and NUMA alignments.
2. **Scheduling Schedulers (Manager Layer):** Validates that the manager claims and schedules partitions onto nodes with available storage capacity.
3. **CSI provisioning & Persistence (Data Path Layer):** Creates a PVC, exports an NVMe-oF target over SoftRoCE, connects via `nvme connect`, formats the volume (ext4), mounts it to a Pod, and asserts that written data survives Pod restarts.

To run the integration pipeline, use the following automated Makefile targets in order:

#### Step 1: Fetch the Cluster Kubeconfig
Fetch the active Kubeconfig from the master VM and save it locally on your host as `kubeconfig.yaml`. This target automatically translates internal VM addresses so your host can communicate with the cluster:

```bash
make get-kubeconfig
```

#### Step 2: Build and Deploy DISTORT
Build the manager image locally, export it to a tarball, load it into the VirtualBox K3s VMs, apply the CRD manifests, and upgrade/install the DISTORT Helm chart into the `distort-system` namespace:

```bash
make test-env-deploy
```

#### Step 3: Run the E2E Test Suite
Execute the Ginkgo validation suite against the active cluster:

```bash
make test-e2e
```

#### Step 4: Reset the Test Environment
If you need to wipe your partitions, clean up the RDMA targets, delete active CRD claims, and reset the storage state back to clean defaults for the next run, execute:

```bash
make test-env-reset
```

---

## Developing Plugins for Target Backends and Volume Managers

DISTORT features an extensible plugin-based architecture for both **Target Backends** (the target export technology) and **Volume Managers** (the storage carving engines). Each plugin is self-contained and resides under `internal/agent/plugins/`.

### 1. The Plugin Interfaces

Plugins are defined by two Go interfaces inside `internal/agent/plugins/interface.go`:

```go
type TargetBackend interface {
    Name() string
    SetupDevice(ctx context.Context, pciAddress string, deviceName string, options map[string]string) error
    ExportVolume(ctx context.Context, volumeName string, blockPath string, portalIP string, portalPort int, options map[string]string) (string, error)
    UnexportVolume(ctx context.Context, nqn string) error
}

type VolumeManager interface {
    Name() string
    SetupStorage(ctx context.Context, devicePath string, deviceName string) error
    CreateVolume(ctx context.Context, devicePath string, deviceName string, volumeName string, sizeBytes int64) (string, error)
    DeleteVolume(ctx context.Context, devicePath string, deviceName string, volumeName string) error
}
```

### 2. Registering a Plugin

Plugins must register themselves within their file using a standard Go `init()` registration block.

```go
package plugins

type MyCustomBackend struct{}

func init() {
    RegisterTargetBackend(&MyCustomBackend{})
}

func (m *MyCustomBackend) Name() string {
    return "custom-backend"
}
// Implement other interface methods...
```

Because the `agent` package imports the `plugins` package, your plugin is automatically loaded and compiled into the `distort-agent` binary.

### 3. Step-by-Step: Adding a New Plugin

To add a new volume manager (e.g. `lvm`):

1. **Create the file**: Create `internal/agent/plugins/vol_lvm.go`.
2. **Implement the interface**: Implement `VolumeManager` methods. For instance, `SetupStorage` will initialize a Volume Group (`vgcreate`), `CreateVolume` will run `lvcreate`, and `DeleteVolume` will run `lvremove`.
3. **Register the plugin**: Call `RegisterVolumeManager(&LVMVolumeManager{})` in an `init()` block.
4. **Use it in a StorageClass**: Define a new StorageClass specifying your plugin name under `volume-manager`:
   ```yaml
   apiVersion: storage.k8s.io/v1
   kind: StorageClass
   metadata:
     name: distort-kernel-lvm
   provisioner: storage.distort.io
   parameters:
     target-backend: "kernel"
     volume-manager: "lvm"
   ```
5. **Add tests**: Update your E2E validation matrix to assert volume operations on the new volume manager.

