---
title: "Local Testing Lab"
description: "Create a persistent three-node Vagrant/K3s lab with virtual NVMe and SoftRoCE for manual and automated DISTORT testing."
type: "page"
---

The local lab is the recommended environment for developing DISTORT without using a real storage cluster. It keeps a three-node K3s cluster running between test cycles, so the normal inner loop is:

```text
edit code -> build image -> load image into the VMs -> Helm upgrade -> test
```

There is no need to uninstall the Helm release or rebuild the VMs for each change.

## What the lab provides

| VM | Private IP | Kubernetes role | Emulated storage/network |
|---|---:|---|---|
| `distort-master` | `192.168.56.10` | K3s server and schedulable node | Virtual NVMe controller, two 1 GiB namespaces, SoftRoCE |
| `distort-worker-1` | `192.168.56.11` | K3s agent | Virtual NVMe controller, two 1 GiB namespaces, SoftRoCE |
| `distort-worker-2` | `192.168.56.12` | K3s agent | Virtual NVMe controller, two 1 GiB namespaces, SoftRoCE |

The default low-resource profile gives the master 3 GiB RAM, each worker
1.25 GiB RAM, two virtual CPUs per VM, and 256 MiB of hugepages per node. The
first-time target builds the image while the VMs are stopped and caps SPDK and
Go build parallelism to avoid competing with the IDE. The lab deployment also
uses smaller SPDK iobuf pools and a bounded RDMA shared receive queue that fit
its 256 MiB hugepage reservation; production Helm defaults are unchanged. The
same controls are available to production installations that prefer predictable
DMA-memory use over maximum queue capacity. Because Vagrant provisions the host
hugepages, the lab also tells SPDK setup not to replace that reservation with
its own default. Plan for at least 12 GiB of host memory,
4 GiB of swap, and 30 GiB of free disk space. The first image build is slow
because it builds SPDK; later builds reuse the container cache.

Override the profile only on a larger host:

```bash
DISTORT_MASTER_MEMORY_MB=3072 \
DISTORT_WORKER_MEMORY_MB=2560 \
DISTORT_VM_CPUS=2 \
DISTORT_HUGEPAGES_2MI=256 \
make test-env-create
```

This topology exercises the real agent, manager, CSI controller, CSI node service, SPDK, kernel NVMe target, NVMe-oF, and Kubernetes scheduling paths. It does not reproduce real NVMe/RDMA performance, physical device failure, multipath behavior, or production network faults.

## Host prerequisites

Install:

- [Vagrant 2.3 or newer](https://developer.hashicorp.com/vagrant/install).
- [VirtualBox 7 or newer](https://www.virtualbox.org/wiki/Downloads), including its host kernel/system extension permissions.
- [Docker](https://docs.docker.com/engine/install/). The Makefile exposes `CONTAINER_TOOL`, but Docker is the tested implementation.
- [`kubectl`](https://kubernetes.io/docs/tasks/tools/), [Helm 3](https://helm.sh/docs/intro/install/), GNU Make, Git, and Go matching `go.mod`.
- Hardware virtualization enabled in BIOS/UEFI on Linux hosts.

After installation, start Docker and verify the tools before cloning/booting anything:

```bash
vagrant --version
VBoxManage --version
docker info
kubectl version --client
helm version --short
go version
```

On Linux, a VirtualBox error mentioning `/dev/vboxdrv` means its kernel module was not built or loaded; install the matching kernel headers/DKMS package and reboot before continuing. Run the local prerequisite check from the repository root:

```bash
make test-env-prereqs
```

### macOS support

The same workflow should work on an Intel Mac supported by the installed VirtualBox and Bento Ubuntu box versions. Approve the VirtualBox system software when macOS asks, then reboot if required.

Apple Silicon is not currently a supported lab host. This Vagrantfile depends on VirtualBox PCIe NVMe emulation and an amd64 Ubuntu guest/image path; changing only the Vagrant box is not enough to prove SPDK/VFIO behavior. On Apple Silicon, use an amd64 Linux development machine or a remote Linux host for this hardware-oriented suite. Unit and envtest tests can still run locally when the Go dependencies support the host.

## First-time setup

All commands below run from the repository root.

For the complete first-time setup in one command, run:

```bash
make test-env-create
```

That command performs the following three steps. They are also separate targets so a failed stage can be retried without repeating the earlier work.

### 1. Start the persistent cluster

```bash
make test-env-up
```

This provisions the three VMs, installs the Kubernetes 1.35-compatible K3s version pinned in the Vagrantfile, configures the private network, creates virtual NVMe devices, allocates hugepages, and creates a SoftRoCE device on each node. It then writes the lab credentials to `./kubeconfig.yaml`.

The small runtime resource provisioner runs on every `vagrant up`, including
for persistent VMs. This keeps the live hugepage reservation aligned with the
Vagrantfile instead of retaining an older profile from the VM's first creation.

Provisioning downloads the Ubuntu box, OS packages, and K3s, so it requires internet access and can take several minutes on the first run.

### 2. Build and deploy DISTORT

```bash
make test-env-deploy
```

The target:

1. Builds `localhost/distort:0.5.0-dev` on the host.
2. Copies generated CRDs into the chart.
3. Imports the image into K3s on all three VMs.
4. Applies the CRDs and performs `helm upgrade --install`.
5. Restarts the DISTORT workloads and waits for every rollout.

It intentionally preserves the VMs and Helm release. Run the same target after each code change, or use its equivalent alias:

```bash
make test-env-redeploy
```

### 3. Check readiness

```bash
make test-env-smoke
make test-env-status
```

The smoke test fails unless it sees three `Ready` Kubernetes nodes, healthy manager/CSI controller deployments, one ready agent and CSI node Pod per node, at least one discovered `NVMeDevice` per node, and an `RDMAStorageNode` per node. The status target prints the corresponding objects for inspection.

For direct `kubectl` commands, opt in to the isolated kubeconfig explicitly:

```bash
export KUBECONFIG="$PWD/kubeconfig.yaml"
kubectl get nodes -o wide
kubectl get nvmedevices,rdmastoragenodes -o wide
```

The Make targets do not trust the shell's existing `KUBECONFIG`. Before mutating cluster state they verify that the generated file targets `https://192.168.56.10:6443` and contains `distort-master`. This prevents an accidental reset or deployment against an unrelated cluster.

## Fast daily workflow

Use the cheapest relevant test first:

```bash
# Go/controller tests without the VMs
make test

# Build, load, Helm-upgrade, and wait for the changed runtime
make test-env-redeploy

# Inspect the deployed system
make test-env-smoke
make test-env-status

# Run all hardware/full-stack tests
make test-e2e
```

Do not run `helm uninstall` between changes. A redeploy explicitly restarts the workloads because the image keeps the local development tag `localhost/distort:0.5.0-dev`.

If a test leaves device state behind, reset the isolated storage lab without destroying or reinstalling it:

```bash
make test-env-reset
```

The reset deletes all DISTORT claims and partitions in the isolated cluster, stops target processes, disconnects initiators, restores the virtual NVMe devices, and restarts device discovery. It is deliberately guarded and should never be copied into a real cluster workflow. If deletion blocks on a finalizer, inspect the agent/controller logs before forcing removal; that stuck cleanup is usually the behavior under test.

## Automated full-stack tests

For the complete green/known-failure workflow and the finding-by-finding coverage matrix, see [Testing Strategy](/testing/).

Run the complete ordered Ginkgo suite:

```bash
make test-e2e
```

To reset the lab, run its smoke checks, and execute the complete green suite in one command:

```bash
make test-env-all
```

The suite currently covers:

1. Agent discovery and direct NVMe partition/SPDK export.
2. Device claim binding and capacity-based scheduling.
3. CSI provisioning, remote mounting, and data persistence for the SPDK and kernel backends.
4. Admission rejection of client-supplied placement without an owning claim.
5. Namespace-safe CSI, NQN, and lvol identities with deletion verified in both orders.

Run a focused scenario while developing:

```bash
make test-e2e E2E_ARGS='-ginkgo.focus=Discovery'
make test-e2e E2E_ARGS='-ginkgo.focus=Claim Binding'
make test-e2e E2E_ARGS='-ginkgo.focus=backend=kernel'
make test-e2e E2E_ARGS='-ginkgo.label-filter=F1'
make test-e2e E2E_ARGS='-ginkgo.label-filter=F4'
```

Because the suite is ordered and some later cases depend on earlier setup, a focused test may need its own fixture before it becomes independently runnable. Use `make test-env-reset` before retrying after an interrupted or failed storage test.

## Manual end-to-end test

This exercise claims a discovered device, dynamically provisions a volume, writes data, recreates the consumer Pod, and verifies persistence. It uses the kernel target backend; replace `kernel` with `spdk` only after resetting the lab because a physical controller cannot safely switch backends while allocations exist.

### 1. Create a test namespace and choose a device

```bash
export KUBECONFIG="$PWD/kubeconfig.yaml"
kubectl create namespace distort-test --dry-run=client -o yaml | kubectl apply -f -
kubectl get nvmedevices \
  -o custom-columns=NAME:.metadata.name,NODE:.spec.nodeName,SERIAL:.spec.serialNumber,STATE:.status.state,CAPACITY:.spec.totalCapacity
```

Copy the serial number of one `Available` device into the following manifest:

```bash
DEVICE_SERIAL='SN-distort-worker-1'

sed "s/DEVICE_SERIAL/$DEVICE_SERIAL/" <<'EOF' | kubectl apply -f -
apiVersion: storage.distort.io/v1alpha1
kind: NVMeDeviceClaim
metadata:
  name: manual-device
  namespace: distort-test
spec:
  serialNumber: DEVICE_SERIAL
EOF

kubectl wait -n distort-test nvmedeviceclaim/manual-device \
  --for=jsonpath='{.status.active}'=true --timeout=120s
```

### 2. Create a StorageClass, PVC, and consumer Pod

```bash
kubectl apply -f - <<'EOF'
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: distort-manual-kernel
provisioner: storage.distort.io
volumeBindingMode: WaitForFirstConsumer
parameters:
  target-backend: kernel
  volume-manager: partition
  filesystem: ext4
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: manual-volume
  namespace: distort-test
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 500Mi
  storageClassName: distort-manual-kernel
---
apiVersion: v1
kind: Pod
metadata:
  name: manual-consumer
  namespace: distort-test
spec:
  containers:
    - name: shell
      image: busybox:1.36
      command: ["sh", "-c", "sleep 3600"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: manual-volume
EOF

kubectl wait -n distort-test pod/manual-consumer \
  --for=condition=Ready --timeout=180s
kubectl get -n distort-test pvc,pod,nvmedeviceclaim,nvmepartition -o wide
```

If the Pod does not become ready, inspect events before changing anything:

```bash
kubectl describe -n distort-test pvc/manual-volume
kubectl describe -n distort-test pod/manual-consumer
kubectl get events -n distort-test --sort-by=.lastTimestamp
make test-env-logs
```

### 3. Verify data survives Pod recreation

```bash
kubectl exec -n distort-test manual-consumer -- \
  sh -c 'echo distort-local-lab > /data/proof.txt && sync'

kubectl delete pod -n distort-test manual-consumer --wait=true

kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: manual-consumer
  namespace: distort-test
spec:
  containers:
    - name: shell
      image: busybox:1.36
      command: ["sh", "-c", "sleep 3600"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: manual-volume
EOF

kubectl wait -n distort-test pod/manual-consumer \
  --for=condition=Ready --timeout=180s
kubectl exec -n distort-test manual-consumer -- cat /data/proof.txt
```

The final command must print `distort-local-lab`.

### 4. Clean up the manual test

Delete the consumer before the claim so CSI can unstage and unpublish the volume normally:

```bash
kubectl delete pod -n distort-test manual-consumer --wait=true
kubectl delete pvc -n distort-test manual-volume --wait=true --timeout=180s
kubectl delete nvmedeviceclaim -n distort-test manual-device --wait=true --timeout=120s
kubectl delete storageclass distort-manual-kernel
kubectl delete namespace distort-test
```

Then confirm that no allocation remains:

```bash
kubectl get nvmedeviceclaims,nvmepartitions -A
kubectl get nvmedevices,rdmastoragenodes -o wide
```

## Diagnostics and recovery

```bash
# Recent logs for all DISTORT containers
make test-env-logs

# Shell into a storage node
make test-env-ssh NODE=distort-worker-1

# Vagrant and Kubernetes status
make test-env-status
```

Useful node-level checks after opening the VM shell are:

```bash
sudo nvme list
rdma link
sudo systemctl status rdma-link
sudo lsblk
sudo dmesg --level=err,warn | tail -n 100
```

When debugging SPDK, first identify the agent Pod on the provider node:

```bash
AGENT_POD=$(kubectl get pod -n distort-system \
  -l app.kubernetes.io/component=agent \
  --field-selector spec.nodeName=distort-worker-1 \
  -o jsonpath='{.items[0].metadata.name}')

kubectl exec -n distort-system "$AGENT_POD" -- \
  /opt/spdk/scripts/rpc.py bdev_get_bdevs
kubectl exec -n distort-system "$AGENT_POD" -- \
  /opt/spdk/scripts/rpc.py nvmf_get_subsystems
```

Stop and start the persistent VMs without deleting them:

```bash
cd vagrant
vagrant halt
vagrant up
cd ..
make get-kubeconfig
make test-env-status
```

Destroy the lab only when you want to reclaim its virtual disks and VM state:

```bash
make test-env-destroy
```

The destroy target permanently removes the three lab VMs and their virtual NVMe contents. The next `make test-env-up` performs full provisioning again.

## Test strategy for the review backlog

Use three levels while implementing items from the [review findings](/review-findings/):

- Unit/envtest: validation, reconciliation, idempotency, conflicts, malformed objects, and injected command failures. Run with `make test`.
- Focused Vagrant E2E: one backend or recovery sequence using `E2E_ARGS` while preserving the cluster.
- Full Vagrant E2E: both target backends, cross-node mounting, deletion, restart, and persistence using `make test-e2e`.

Keep destructive media, VFIO, SPDK, and NVMe-oF assertions in the isolated Vagrant suite. Envtest provides a real API server and etcd but does not provide kubelets, CSI mounting, host devices, RDMA, or SPDK.

## Last verified state

On 2026-08-17, the measured profile was 3072 MiB RAM for the master, 1280 MiB for each worker, two CPUs per VM, and 128 × 2 MiB hugepages per node. Helm revision 6 was healthy on three Ready nodes. A clean reset and full run passed eight green specs, failed none, and skipped four quarantined findings. The lab used iobuf pools `4096`/`256`, RDMA SRQ depth `128`, and host-managed hugepages; these are constrained functional-lab settings, not production sizing recommendations.
