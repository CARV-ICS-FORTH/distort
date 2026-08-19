---
title: "Testing Strategy"
description: "Run DISTORT unit, envtest, contract, race, Vagrant E2E, recovery, and known-defect regression suites."
type: "page"
---

DISTORT uses two deliberately separate test tracks:

- The **green suite** must pass on every change and contains all behavior that is currently expected to work.
- The **known-failure suite** contains executable acceptance tests for confirmed items in the [review findings](/review-findings/). These tests are quarantined until their corresponding fixes are implemented, so known defects remain reproducible without making every normal test run fail.

Do not weaken a regression assertion to match broken behavior. When a fix is complete, remove its `knownfailure.Require`, `requireFinding`, or `requireKnownE2E` guard so the test becomes part of the permanent green suite.

## Host-side suites

Run the standard unit and envtest suite:

```bash
make test
```

Run the complete host validation, including repository contracts, Helm lint/render, Hugo, and E2E compilation:

```bash
make test-suite
```

Run concurrency instrumentation separately because it is slower:

```bash
make test-race
```

The default GitHub test workflow runs `make test`, `make test-race`, checks that
`go mod tidy` produces no module-file drift, and compiles the tagged E2E suite.
Hardware E2E remains a guarded local Vagrant gate because hosted CI runners do
not expose the required nested VirtualBox NVMe/RDMA topology.

The controller tests use `envtest`, which launches a real Kubernetes API server and etcd. CSI, agent, and plugin tests use in-memory Kubernetes clients, fake command executables, temporary sysfs layouts, and pure parsers. They never require root privileges or access to host storage devices.

## Reproducing a known finding

Run all host-side known-failure tests:

```bash
make test-regression
```

This command is expected to fail until the backlog is complete. During a fix, select only its finding ID:

```bash
make test-regression FINDING=F7
```

A correct workflow for a bug fix is:

1. Run the selected regression and confirm it fails for the documented reason.
2. Implement the fix.
3. Run the selected regression until it passes.
4. Remove that test's known-failure guard.
5. Run `make test-suite` and `make test-race`.
6. For hardware/data-path changes, run the focused Vagrant test and then the complete green E2E suite.

## Vagrant E2E suites

Create the environment using the [Local Testing Lab](/local-testing/) guide, then run all currently supported full-stack behavior:

```bash
make test-env-all
```

That target resets only the guarded Vagrant cluster, verifies three ready nodes
and hardware discovery, and runs the green E2E suite. The suite uses separate
physical controllers for SPDK and kernel tests so backend state cannot leak
between them. CSI consumers are pinned through a hostname selector to the
opposite worker, proving a remote path while preserving the driver's currently
supported same-node graceful restart semantics:

| Purpose | Target node/device | Consumer node |
|---|---|---|
| Direct claimed SPDK partition and management scheduling | `distort-master` | n/a |
| CSI SPDK provisioning and persistence | `distort-worker-1` | `distort-worker-2` |
| CSI kernel provisioning and persistence | `distort-worker-2` | `distort-worker-1` |

Run one green scenario:

```bash
make test-e2e E2E_ARGS='-ginkgo.label-filter=F1'
make test-e2e E2E_ARGS='-ginkgo.label-filter=F4'
make test-e2e E2E_ARGS='-ginkgo.focus=Persistence.*backend=kernel'
make test-e2e E2E_ARGS='-ginkgo.label-filter=green'
```

Run a quarantined cluster regression in a freshly reset lab:

```bash
make test-env-regression FINDING=F17
make test-env-regression FINDING=F18
```

The E2E suite refuses to run unless the active kubeconfig server is `https://192.168.56.10:6443` and all three expected Vagrant node names are present. A failed spec automatically captures nodes, workloads, DISTORT resources, events, and recent component logs.

## Review finding coverage

| Finding | Automated coverage | Layer |
|---|---|---|
| F1 (resolved) | Available devices and mismatched claim UIDs cannot reach plugins; valid live ownership provisions; ownerless client placement is rejected | Agent unit + envtest + E2E admission/full stack |
| F2 (resolved) | Shell metacharacters in `spdk-core-mask` cannot execute; validation and the direct command vector are captured | Plugin, CSI, admission + Vagrant SPDK E2E |
| F3 (resolved) | Multiple kernel volumes receive distinct reusable partition numbers; deletion preserves surviving mappings | Plugin unit + Vagrant kernel E2E |
| F4 (resolved) | Same names in different namespaces receive distinct CSI IDs/NQNs/lvols; exact deletion is verified in both orders | CSI/agent unit + Vagrant SPDK E2E |
| F5 (resolved) | Exact SPDK base-bdev/lvstore/lvol identities are persisted and verified absent across partial cleanup and retry | Plugin/agent unit + Vagrant SPDK E2E |
| F6 (resolved) | Concurrent reservations, stale status/cache reads, update conflicts, and terminating-volume capacity retention | Envtest concurrency and conflict injection |
| F7 (resolved) | Capacity-range validation, negative legacy objects, upward rounding for kernel/SPDK, and persisted actual allocation | CSI, plugin, envtest, CRD contract + E2E admission/SPDK |
| F8 | A failing `parted` command must fail volume creation | Plugin command-failure unit |
| F9 | CreateVolume retries compare size, manager, filesystem, access mode, and options | CSI unit |
| F10 | Unsupported capabilities are rejected and read-only publish uses read-only mounts | CSI controller/node unit |
| F11 | Validation precedes connection and failed staging disconnects the target | CSI node failure-injection unit |
| F12 | Existing stage/publish mounts must match the expected source | CSI node unit |
| F13 | Missing hardware requeues and active claims follow device movement | Envtest |
| F14 | Deleting an old claim cannot release a replacement claim's device | Envtest |
| F15 | Exact allow/exclude semantics, mounted devices, and failed mount inspection | Agent fake-sysfs unit |
| F16 | Real Node IP/capacity reporting, no loopback fallback, and active export count | Agent unit + Vagrant smoke |
| F17 | SPDK process crash must restore the exported target | Vagrant recovery E2E |
| F18 | Separate chart identities plus forbidden Node mutation | Repository contract + E2E RBAC |
| F19 | Permanent plugin errors become terminal rather than hot-looping | Agent unit |
| F20 | Unimplemented LVM is rejected by CSI, CRD, and admission | CSI + repository contract + E2E admission |
| F21 | Every sample has a concrete usable spec and no scaffold TODO | Repository contract |
| F22 | Behavior-focused controller, CSI, agent, plugin, contract, and E2E suites | Entire suite |
| F23 | Existing lint configuration remains an explicit required command | `make lint`; promote to `make test-suite` after the current lint backlog is fixed |
| F24 (resolved) | Documentation version matches the `go.mod` directive | Repository contract |
| F25 | A single-writer volume cannot be concurrently attached read-write on two nodes; publish/unpublish and stale-owner recovery are idempotent | CSI controller + two-node E2E |

## Last verified lab run

On 2026-08-19, a clean reset followed by the complete hardware suite produced 11 passing green specs, zero failures, and three explicitly quarantined skips. SPDK and kernel targets both passed cross-node provisioning, mounting, graceful same-node Pod recreation, persistence, and cleanup. The suite also covered concurrency-safe capacity scheduling, same-device kernel partition-number reuse, exact SPDK lvol teardown after the subsystem had already been removed, API capacity rejection, and upward-rounded allocation reporting.

The host `make test-suite` and `make test-race` gates passed at that point. `make lint` still reported the 19-item F23 backlog and must not be described as green.

## Additional coverage beyond the review

The green suite also verifies:

- Built-in plugin registration and rejection of unknown plugin names.
- NVMe subsystem JSON parsing, malformed data, missing NQNs, and non-live paths.
- Kernel discovery metadata, capacity calculation, non-PCIe filtering, and mounted namespace exclusion.
- Filesystem alias resolution, blank-device formatting, filesystem preservation, and mismatch refusal.
- Controller placement selection, backend compatibility, insufficient capacity, capacity subtraction, and oversubscription clamping.
- Claim finalizer behavior and deletion when hardware is already absent.
- Reporter-owned RDMA node objects remain unchanged by the passive manager reconciler.
- CSI defaulting, backend-option filtering, stable compatible retries, identity capabilities, and mandatory request fields.
- Required chart/CRD artifacts and E2E package compilation.

## Interpreting failures

- A green-suite failure is a regression or an environment/setup failure and must be investigated immediately.
- A selected known-failure test should initially fail at its acceptance assertion. A compile error, timeout in the fixture, inability to start envtest, or wrong-cluster guard failure is a test infrastructure problem instead.
- Vagrant failures involving `/dev/vboxdrv`, virtualization permissions, image pulls, or host-only networking are environment failures. Product failures begin after the smoke test confirms all three nodes, workloads, NVMe devices, and RDMA nodes.
- Do not force-remove finalizers before capturing logs and object state; cleanup/finalizer behavior is part of several regressions.
