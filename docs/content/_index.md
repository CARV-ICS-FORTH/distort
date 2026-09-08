---
title: "DISTORT"
description: "Cloud-native Disaggregated Storage over RDMA with DISTORT"
type: "home"
---

## Welcome to DISTORT

**DISTORT (DISaggregated STorage Over Rdma Transport)** is a high-performance, Kubernetes-native storage engine that bridges the gap between physical disaggregated storage fabrics and dynamic container environments.

As cloud-native architectures become the standard for deploying scalable applications, the demand for high-performance, low-latency storage has surged. Disaggregated storage, particularly NVMe-over-Fabrics (NVMe-oF) using Remote Direct Memory Access (RDMA), offers a promising solution by decoupling storage capacity from compute nodes, maintaining near-local access speeds. 

However, integrating physical RDMA targets with the dynamic, containerized environments of Kubernetes presents significant orchestration challenges. 

DISTORT is a Kubernetes-native storage engine designed to bridge this gap. It leverages a Custom Resource Definition (CRD)-driven state machine to manage the lifecycle of physical NVMe devices, partition them asynchronously, and expose them as NVMe-oF RDMA targets directly to Kubernetes Pods via a Container Storage Interface (CSI) driver. By offloading the control plane to Kubernetes while maintaining a lightweight data path, DISTORT achieves high-performance, dynamic storage provisioning suitable for data-intensive cloud applications.

---

### Core Pillars

```mermaid
graph TD
    classDef k8s fill:#326ce5,stroke:#fff,stroke-width:2px,color:#fff;
    classDef engine fill:#1f2937,stroke:#3b82f6,stroke-width:2px,color:#fff;
    classDef hardware fill:#059669,stroke:#fff,stroke-width:2px,color:#fff;

    subgraph Orchestration [Kubernetes Control Plane]
        PVC["Persistent Volume Claim"]
        CRD["Declarative CRDs<br/>(NVMePartition, NVMeDevice)"]
        MGMT["Management Controller"]
        PVC -.-> CRD
        MGMT --> CRD
    end
    
    subgraph DataPath [DISTORT High Performance Data Path]
        SPDK["SPDK User-space Polling<br/>(VFIO-PCI / Lvol)"]
        RDMA["SoftRoCE / Physical RDMA"]
        CSI["DISTORT CSI Driver<br/>(Staging & Mounting)"]
    end

    subgraph Media [Physical Hardware Layer]
        NVMe["Physical NVMe Disks"]
    end

    CRD --> CSI
    CSI --> SPDK
    SPDK --> RDMA
    RDMA --> NVMe

    class PVC,CRD,MGMT k8s;
    class SPDK,RDMA,CSI engine;
    class NVMe hardware;
```

---

### Getting Started

Explore the different sections of the documentation to understand and consume DISTORT:

- **[Architecture](/architecture/)**: Read our high-level design, component roles, interactive control sequence, and implementation layout.
- **[Project Internals](/internals/)**: Follow the controller, agent, CSI, and storage data paths in detail, including recovery boundaries.
- **[Using DISTORT](/using/)**: Learn how to discover underlying storage controllers, claim hardware drives using `NVMeDeviceClaim` specs, and request StorageClasses.
- **[Local Testing Lab](/local-testing/)**: Keep a three-node Vagrant/K3s cluster running for fast redeploys, manual storage checks, and automated E2E tests.
- **[Testing Strategy](/testing/)**: Run the green suite and the finding-keyed regression tests used to drive each review fix.
- **[Review Findings](/review-findings/)**: Track confirmed defects, resolved regressions, acceptance criteria, and production-readiness gates.
- **[Contributing](/contributing/)**: Build, generate, validate, and submit changes using the repository's supported workflows.
