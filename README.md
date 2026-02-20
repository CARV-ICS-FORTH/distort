# Distort

Distort is a Kubernetes-native storage engine specifically designed to manage dynamic, high-performance physical disk allocation. Utilizing NVMe-over-Fabrics (NVMe-oF) via RDMA target exports, it orchestrates direct block storage attachments directly between worker nodes. 

## High-Level Design

Distort consists of three main components:
1. **Manager (`distort-manager`)**: The control plane component containing controllers for assigning claims to devices and scheduling placement of NVMe partitions on healthy `RDMAStorageNode`s.
2. **Agent (`distort-agent`)**: A daemonset running on storage provider nodes. It discovers physical NVMe controllers (`NVMeDevice`), executes partitioning using `parted`, and exports storage targets using `nvmetcli`.
3. **CSI Driver (`distort-csi`)**: A standard CSI driver which integrates with Kubernetes PersistentVolumeClaims. It intercepts PVC allocation and translates them into `NVMePartition` CRDs, and handles the logic for connecting and mounting the RDMA block devices on consumer nodes.

### CRD Architecture

- `NVMeDevice`: Discovered physical underlying drives ready for chunking.
- `NVMeDeviceClaim`: Allows cluster administrators to claim existing hardware to be managed by Distort.
- `NVMePartition`: A dynamically created block device representing a "pod volume".
- `RDMAStorageNode`: Represents the health, capacity and connectivity profile of a node that provides RDMA-enabled storage.

## Building and Running

### Prerequisites
- Go 1.23+
- A Kubernetes cluster (or a local environment like Minikube/Kind)
- Kubebuilder / controller-gen installed
- Helm 3+

### Build Binaries

The project generates three specific binaries. Compile them directly via the Makefile:
```sh
make build
```
The binaries are placed in `bin/`:
- `bin/distort-manager`
- `bin/distort-agent`
- `bin/distort-csi`

### Deploying via Helm

The project includes a Helm chart to deploy the Manager, Agent, and CSI Driver to your cluster, including all necessary RBAC roles and Custom Resource Definitions.

To deploy the stack into the `distort-system` namespace:

```sh
helm install distort ./deploy/charts/distort \
  --namespace distort-system \
  --create-namespace
```

To view the values and override resource limits or image tags:
```sh
helm show values ./deploy/charts/distort
```

### License

Copyright 2026, FORTH-ICS.

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.
