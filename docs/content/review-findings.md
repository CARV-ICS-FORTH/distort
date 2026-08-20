---
title: "Review Findings"
description: "Prioritized defects, regression criteria, completed fixes, and production-readiness gates for DISTORT."
type: "page"
---

# Review Findings and Fix Backlog

This document converts the repository review into an ordered implementation backlog. Work through the items in sequence unless a finding explicitly says it can be handled independently.

Status legend:

- [ ] Not started
- [~] In progress
- [x] Completed and verified

For every completed item, add or update the listed regression tests and record the verification commands in the pull request or commit message.

## Baseline

Original verification baseline before fixes:

- `go test ./...`: passes, but behavior coverage is low.
- `go test -cover ./...`: `internal/controller` 31.6%, `internal/csi` 13.8%, agent/plugin packages 0%.
- `go test -race ./internal/csi`: passes.
- `go vet ./...`: passes.
- `golangci-lint run`: fails with 18 findings.
- All three Go binaries build.
- Helm lint/render and Hugo build pass.
- Generated CRDs, RBAC, and DeepCopy code match their sources.
- Hardware/SPDK E2E tests were not run because no isolated cluster was available.

## Local verification environment

The three-node Vagrant/K3s [local testing lab](/local-testing/) is the execution environment for this backlog. It provides virtual NVMe controllers and SoftRoCE on every node while keeping the cluster and Helm release persistent between code changes.

Use this sequence for each fix:

```bash
make test-regression FINDING=<finding-id>  # Confirm the acceptance test fails
# Implement the fix and remove that test's known-failure guard.
make test-suite
make test-race
make test-env-redeploy
make test-env-smoke
make test-e2e E2E_ARGS='-ginkgo.label-filter=<finding-id>'
```

See the [testing strategy](/testing/) for the finding-by-finding automated coverage matrix. Run `make test-env-all` before marking a hardware or full-stack item complete. The lab has completed a full SPDK/kernel hardware run; future hardware-path fixes still require their focused scenario and the complete green suite before being marked resolved.

## Ordered fix backlog

### 1. Enforce claim ownership before destructive device operations

- [x] **F1 — Critical — Resolved**
- Affected:
  - `internal/agent/partition_manager.go`
  - `internal/controller/nvmedeviceclaim_controller.go`
  - `api/v1alpha1/nvmepartition_types.go`
  - RBAC/admission configuration
- Problem: A partition with user-supplied `nodeName` and `parentDeviceSerialNumber` bypasses manager placement. The agent does not verify that the parent device is claimed before binding drivers, wiping/partitioning media, creating lvols, or exporting it.
- Failure scenario: A user who can create `NVMePartition` objects targets any discovered serial and node without creating an `NVMeDeviceClaim`.
- Required fix:
  1. Record an immutable owning claim reference or UID on the allocated partition/device.
  2. Verify the live claim immediately before every destructive provisioning operation.
  3. Prevent ordinary clients from setting scheduler-owned placement fields, preferably through admission validation and RBAC separation.
  4. Return a clear Condition/Event when admission is missing rather than touching the device.
- Acceptance criteria:
  - No backend or volume-manager method is called for an unclaimed device.
  - Deleting or replacing a claim cannot silently authorize a different workload.
  - Existing valid claim-based provisioning continues to work.
- Regression tests:
  - Assigned partition plus `Available` device: no plugin calls and no host changes.
  - Assigned partition plus mismatched claim UID: rejected.
  - Valid owner claim: provisioning proceeds.
  - E2E permission test proving a partition editor cannot bypass claim admission.
- Resolution:
  - Devices and scheduled partitions now record the owning claim's namespace, name, and immutable Kubernetes UID.
  - The scheduler ignores claimed devices that lack an owner reference, and placement fields are admitted only as a complete node/device/claim allocation and become immutable once assigned.
  - The agent re-fetches and validates the device plus the exact live, active, non-deleting claim immediately before backend setup, storage setup, volume creation, and export. Invalid ownership records a `ClaimAuthorized=False` Condition and never calls a plugin.
  - Claim cleanup releases a device only when the device still records that exact claim UID, preventing a deleted/recreated claim name from inheriting or releasing another claim's device.
- Verification (2026-08-17): the focused regression failed before the fix because `SetupDevice` was called once for an `Available` device. After the fix, all three agent ownership cases, envtest admission/controller coverage, the focused Vagrant F1 test, and the complete seven-spec green hardware E2E suite passed.

### 2. Remove shell injection from SPDK startup

- [x] **F2 — Critical — Resolved**
- Affected:
  - `internal/agent/plugins/backend_spdk.go`
  - `internal/csi/controller_server.go`
  - `api/v1alpha1/nvmepartition_types.go`
- Problem: `spdk-core-mask` reaches `fmt.Sprintf` inside `bash -c`, enabling arbitrary commands in the privileged agent.
- Failure scenario: `spdk-core-mask: "0x1; id >/tmp/proof"` executes the appended command when `nvmf_tgt` starts.
- Required fix:
  1. Start `nvmf_tgt` directly with `exec.CommandContext` and discrete arguments.
  2. Replace the `ulimit` shell expression with a trusted process configuration or fixed wrapper.
  3. Validate the core mask with a strict syntax and length limit at CSI and CRD/admission boundaries.
  4. Reject unknown backend options rather than forwarding them blindly.
- Acceptance criteria:
  - No user-controlled value is interpreted by a shell.
  - Invalid masks return `InvalidArgument` before a partition is created.
- Regression tests:
  - Valid masks such as `0x1` and `0x3` are accepted.
  - Semicolons, substitutions, whitespace, newlines, flags, and oversized values are rejected.
  - Command test double observes exactly the intended executable and argument vector.
- Resolution:
  - The agent starts `nvmf_tgt` directly with `exec.CommandContext` and a discrete argument vector; no user-controlled value is passed through `bash -c`.
  - Process memlock is raised through `unix.Getrlimit`/`Setrlimit`, removing the former shell `ulimit` expression.
  - A shared storage-option validator accepts only `spdk-core-mask`, applies a strict hexadecimal syntax and length limit, and rejects unknown options in both CSI and agent paths.
  - CRD validation applies the same option allow-list and core-mask constraints at the API boundary.
- Verification (2026-08-17): unit tests proved that shell syntax cannot create a side-effect file and captured the exact `nvmf_tgt` executable/arguments. CSI and admission tests rejected unsafe or unknown options before creating a partition, and the complete Vagrant SPDK hardware suite started and exported targets with the direct command path.

### 3. Make external volume identities globally unique

- [x] **F4 — Critical — Resolved**
- Affected:
  - `internal/agent/plugins/backend_spdk.go`
  - `internal/agent/plugins/backend_kernel.go`
  - `internal/agent/plugins/vol_spdk_lvol.go`
  - `internal/csi/controller_server.go`
  - partition status/API definitions
- Problem: Namespaced partitions use only `metadata.name` for NQN, lvol, and CSI volume IDs. Same-named resources in different namespaces can resolve to the same export. `DeleteVolume` lists all namespaces and deletes the first matching name.
- Required fix:
  1. Derive an immutable external ID from partition UID or a namespace/name/UID tuple.
  2. Persist the ID, NQN, lvol identity, and namespace needed for deletion.
  3. Return an opaque, globally unique CSI `VolumeId`.
  4. Replace cluster-wide name scans with direct keyed lookups.
- Acceptance criteria:
  - Same-named partitions in different namespaces always have distinct NQNs, lvols, and CSI IDs.
  - DeleteVolume can only delete the exact requested partition.
- Regression tests:
  - Same name in two namespaces on one provider node.
  - Delete each volume in both orders and verify the other remains available.
  - Retry CreateVolume and ensure the identity remains stable.
- Resolution:
  - New partitions derive a backend-safe external ID from their immutable Kubernetes UID. The ID is used symmetrically for SPDK lvol names and both SPDK and kernel NQNs.
  - Partition status now persists the external ID, opaque CSI volume handle, exact backend volume path, NQN, and portal data. Existing exported partitions preserve their legacy backend name by recovering it from the persisted NQN rather than silently renaming a live target.
  - CSI volume handles encode the partition namespace, name, and UID. `DeleteVolume` decodes new handles, performs one namespaced `Get`, verifies the live UID, and treats a missing or recreated object as already deleted. A compatibility-only scan supports old plain-name PV handles only when exactly one partition retains that legacy backend identity; ambiguous legacy names fail safely without deleting either object.
  - Node teardown derives the new NQN from the handle's UID identity while retaining the old name-based derivation for already-published legacy handles.
  - Teardown's active-volume check now compares namespace and name together, so deleting one same-named partition cannot hide another partition on the device.
- Verification (2026-08-17): the focused regression failed before the fix because both namespaces received `VolumeId="same-name"`. After the fix, the expanded CSI/agent identity tests, full host suite, race suite, Helm/docs/contracts, focused Vagrant F4 test, and complete eight-spec green hardware E2E suite passed. The Vagrant test provisioned same-named lvols on one SPDK device, deleted them in both orders, and queried SPDK after each first deletion to prove the other NQN remained live.

### 4. Fix SPDK teardown to address the exact created lvol

- [x] **F5 — Critical — Resolved**
- Affected:
  - `internal/agent/partition_manager.go`
  - `internal/agent/plugins/vol_spdk_lvol.go`
  - partition status/API definitions
- Problem: Provisioning uses the SPDK namespace bdev returned by `bdev_get_bdevs`, normally `<controller>n1`. Teardown reconstructs `<node>-<serial>` without the namespace suffix. The lvol lookup can therefore return “absent,” allowing finalizer removal while data remains.
- Required fix:
  1. Persist the exact base bdev, lvstore, lvol UUID/name, and NQN produced during provisioning.
  2. Use those persisted identities for cleanup.
  3. Treat an unresolved guessed identity as an error unless absence is verified through all stable identifiers.
- Acceptance criteria:
  - Finalizer removal occurs only after both subsystem and lvol are verified absent.
  - Cleanup remains idempotent after partial success and agent restart.
- Regression tests:
  - SPDK fake RPC test verifying create/delete name symmetry.
  - E2E query of `nvmf_get_subsystems`, `bdev_get_bdevs`, and lvstores after deletion.
  - Crash after unexport but before lvol deletion, followed by retry.
- Resolution:
  - Volume provisioning now returns and persists the exact SPDK namespace base bdev, lvstore name and UUID, lvol name and UUID, backend alias, and NQN.
  - Teardown resolves every persisted identifier against live SPDK state and requires them to identify the same lvol. A missing, ambiguous, mismatched, or reused identity returns an error and retains the finalizer.
  - Legacy objects without the new fields recover through their persisted backend alias and external identity only when those stable identifiers resolve uniquely. Legacy ambiguity fails safely instead of guessing.
  - Both subsystem and lvol deletion re-query SPDK to verify absence. If an RPC succeeded but its response was lost, the verified absence is accepted, making cleanup idempotent across partial success and agent restart.
  - The reusable lvstore and attached controller remain in place after deleting an individual lvol.
- Verification (2026-08-19): fake-RPC tests covered exact create/delete symmetry, unique legacy recovery, alias-reuse rejection, and lost delete responses. Agent reconciliation retained the finalizer after a simulated crash between unexport and lvol deletion, then removed it after a successful retry. The focused Vagrant F5 scenario manually removed the subsystem before deleting the partition and verified through `nvmf_get_subsystems`, `bdev_get_bdevs`, and `bdev_lvol_get_lvstores` that the subsystem and exact lvol were absent while the lvstore remained. The complete green hardware suite passed 10 specs with four known-failure scenarios skipped.

### 5. Prevent kernel volumes from aliasing partition 1

- [x] **F3 — Critical — Resolved**
- Affected:
  - `internal/agent/plugins/vol_partition.go`
  - device/partition allocation metadata
- Problem: Every kernel volume creates or reuses `<device>p1`; every deletion removes partition 1. Two volumes share data and deleting either breaks both.
- Short-term safe fix:
  - Enforce one kernel volume per physical namespace/controller and reject further allocations.
- Complete fix:
  1. Implement durable free-extent and partition-number allocation.
  2. Persist partition UID to physical partition-number/path mapping.
  3. Verify ownership before deletion.
- Acceptance criteria:
  - Each allocated logical volume receives its own physical partition; shared partitions are not supported.
  - Deleting one volume never removes another volume's partition.
- Regression tests:
  - Two kernel PVCs on the same device receive different partition paths.
  - Delete in both orders, verify the surviving mapping, and reuse each freed partition number.
  - Agent restart recovers the partition mapping from the GPT partition label.
- Resolution:
  - The partition manager reads the current GPT table and free extents, allocates the lowest available partition number, and creates the volume in the first suitably sized MiB-aligned extent.
  - Each GPT partition is labeled with the volume's immutable external ID. Reconciliation and restart recovery use that label instead of assuming partition 1.
  - Deletion uses the persisted backend path and verifies the GPT label before removing the partition. A stale deletion cannot remove a partition number that has since been reassigned.
  - Allocation and deletion are serialized per physical device so concurrent reconciles cannot select the same free number or extent.
- Verification (2026-08-19): unit regressions covered distinct `p1`/`p2` allocation, restart recovery, deletion in both orders, number reuse, stale-delete rejection, and preservation of a pre-existing `p2` table. The focused Vagrant F3 hardware test provisioned two live kernel exports on one namespace, observed distinct `p1` and `p2` backend paths, deleted each independently, verified the survivor remained exported, and confirmed that the freed partition number was reused.

### 6. Validate capacity and allocate at least the CSI request

- [x] **F7 — High — Resolved**
- Affected:
  - `api/v1alpha1/nvmepartition_types.go`
  - `internal/csi/controller_server.go`
  - `internal/controller/nvmedevice_controller.go`
  - `internal/agent/plugins/vol_partition.go`
  - `internal/agent/plugins/vol_spdk_lvol.go`
- Problem:
  - Zero and negative quantities are admitted.
  - `limit_bytes` is ignored.
  - SPDK and kernel paths round requested bytes down.
  - CSI reports the original size even when the backend created less.
  - Negative partition sizes increase computed free capacity in `NVMeDeviceReconciler`.
- Required fix:
  1. Reject non-positive and unreasonable quantities at API and CSI boundaries.
  2. Validate `required_bytes <= limit_bytes` when a limit is present.
  3. Round backend allocation upward to its unit.
  4. Return actual allocated capacity.
  5. Defensively reject invalid quantities inside `NVMeDeviceReconciler` rather than corrupting status.
- Acceptance criteria:
  - Assigned capacity and returned CSI capacity are never below the request.
  - Invalid objects cannot increase device free capacity.
- Regression tests:
  - Zero, negative, sub-MiB, 1 MiB + 1 byte, required greater than limit, and near-overflow values.
  - Capacity reconciliation with malformed legacy objects.
- Resolution:
  - A shared capacity helper rejects non-positive and non-roundable-overflow values, and rounds every positive request upward to the 1 MiB backend allocation unit. Positive sub-MiB requests therefore allocate 1 MiB.
  - The CRD rejects invalid quantities at admission. The CSI boundary rejects explicitly non-positive required bytes, negative limits, requests above their limits, and cases where upward rounding would exceed the limit. An omitted capacity range defaults to 1 GiB.
  - `spec.size` preserves the requested capacity while `status.allocatedCapacity` records the actual backend allocation. CSI returns that actual allocation and refuses a backend result below the request or above its limit.
  - Kernel partitions and SPDK lvols both allocate the rounded size. Scheduling and free-capacity reconciliation reserve the same rounded amount, while malformed legacy objects return an error instead of increasing free capacity.
- Verification (2026-08-19): unit, fake-plugin, CSI, controller envtest, and CRD contract coverage passed for zero, negative, omitted, sub-MiB, unaligned, limited, and overflow requests. The focused Vagrant F7 test proved API rejection of unsafe sizes and verified that a 1 MiB + 1 byte SPDK request reached `Exported` with `status.allocatedCapacity: 2Mi`. A clean complete hardware run then passed all 11 green specs with the three remaining known defects skipped.

### 7. Make device capacity reservation concurrency-safe

- [x] **F6 — High — Resolved**
- Affected:
  - `internal/controller/nvmepartition_controller.go`
  - `internal/controller/nvmedevice_controller.go`
- Problem: Placement reads asynchronously derived `status.freeCapacity`, updates only the partition, and waits for the device controller to recalculate later. Multiple requests can reserve the same free space.
- Required fix:
  1. Recalculate candidate usage from a fresh partition list immediately before assignment.
  2. Add an atomic per-device reservation/update using optimistic concurrency or an allocation object.
  3. Retry conflicts and verify the invariant before committing.
- Acceptance criteria:
  - Sum of assigned non-deleting partitions never exceeds device capacity.
  - Conflicts do not produce lost reservations.
- Regression tests:
  - Concurrently schedule requests whose sum exceeds capacity.
  - Inject update conflicts and stale cache reads.
  - Delete/recreate requests while placement is running.
- Resolution:
  - Placement no longer treats asynchronously derived `status.freeCapacity` as the reservation source of truth. It uses the manager's uncached API reader to list current devices and partitions, then calculates each candidate's capacity from total capacity minus all persisted assignments.
  - Reservations are serialized by physical-device serial number inside the single active, leader-elected manager. After taking the device lock, the controller re-fetches the partition and device and recalculates assigned capacity immediately before persisting placement.
  - Partition update conflicts are retried with a fresh API read and a repeated capacity check, preventing a conflict retry from using an obsolete reservation decision.
  - Terminating partitions remain charged until their finalizer completes and the object disappears, so replacement requests cannot reuse capacity while backend cleanup is still running.
- Verification (2026-08-19): the original concurrent envtest failed before the fix by assigning both 700 MiB requests to one 1 GiB device. After the fix, controller regressions passed for concurrent requests, stale high and low capacity status, forced cached-list rejection, an injected update conflict, and delete/recreate while the old partition remained terminating. The complete host and race suites passed, followed by all 11 green hardware specs with zero failures and three known-defect skips.

### 8. Fail kernel partition creation when the command or udev fails

- [x] **F8 — High — Resolved**
- Affected: `internal/agent/plugins/vol_partition.go`
- Problem: `parted mkpart` errors are logged and ignored. The udev wait returns a device path even when it never appeared.
- Required fix:
  - Return command errors immediately.
  - Poll with context cancellation and return a timeout when the block node does not appear.
  - Verify the resulting partition boundaries and size before export.
- Regression tests:
  - `parted` failure, missing executable, insufficient capacity, udev timeout, and context cancellation.
- Resolution:
  - Kernel volume creation now returns `parted` failures immediately, including a missing executable, and rejects requests that do not fit a current free extent.
  - The udev wait polls for the exact partition block node, honors context cancellation, and returns a clear timeout when the node never appears.
  - After `mkpart`, the manager re-reads the GPT and requires the new partition number, ownership label, byte boundaries, and capacity to exactly match the allocation before returning it for export. Existing labeled partitions must also match the requested rounded capacity.
- Verification (2026-08-20): focused tests passed for `mkpart` failure, missing executable, insufficient capacity, missing block node, cancellation, shifted boundaries, and capacity rounding. The combined F8–F10 regression, full host/static suite, and race suite passed.

### 9. Complete CSI CreateVolume validation and idempotency

- [x] **F9 — High — Resolved**
- Affected:
  - `internal/csi/controller_server.go`
  - `internal/csi/filesystem.go`
- Problem: Existing partitions are compared only by target backend. A retry with a different size, volume manager, filesystem, capabilities, or options can reuse incompatible storage and return false metadata.
- Required fix:
  1. Define supported immutable request properties.
  2. Persist a canonical representation or fingerprint.
  3. Compare all immutable properties on retry.
  4. Return `AlreadyExists` for incompatible requests.
- Regression tests:
  - Compatible identical retry.
  - Conflict on size, limits, manager, filesystem, capabilities, access mode, and target options.
- Resolution:
  - CreateVolume canonicalizes the requested capacity bounds, target backend, volume manager, backend options, filesystem, and supported volume capability, then persists a SHA-256 fingerprint with the human-readable access mode and filesystem.
  - An identical retry reuses the existing partition. A retry whose valid immutable properties differ returns `AlreadyExists`; unsupported capabilities are rejected earlier with `InvalidArgument`.
  - CRD transition validation makes the persisted request properties immutable. Legacy partitions without a fingerprint fail closed rather than being silently reused with unverifiable properties.
- Verification (2026-08-20): CSI regressions passed for identical retries and conflicts in required size, capacity limit, manager, filesystem, and target options. Capability and access-mode differences are covered by F10 validation. The combined F8–F10 regression, full host/static suite, and race suite passed.

### 10. Reject unsupported CSI capabilities and honor read-only publication

- [x] **F10 — High — Resolved**
- Affected:
  - `internal/csi/controller_server.go`
  - `internal/csi/node_server.go`
- Problem: Raw block and unsupported access modes are accepted. Block requests enter filesystem formatting. `NodePublishVolume` ignores `Readonly`, capability, and mount flags.
- Required fix:
  1. Implement `ValidateVolumeCapabilities`.
  2. Initially accept only the mount/access modes actually implemented.
  3. Reject raw block until it is implemented.
  4. Enforce read-only bind-remount semantics when requested.
- Regression tests:
  - RWO mount, raw block, ROX, RWX, conflicting capabilities, read-only publish, and unsupported mount flags.
- Resolution:
  - A shared capability validator is used by CreateVolume, ValidateVolumeCapabilities, NodeStageVolume, and NodePublishVolume. It currently accepts only filesystem mounts with `SINGLE_NODE_WRITER`, rejects raw block and multi-node modes, rejects conflicting filesystems, and rejects mount flags until their semantics are implemented.
  - Node publish validates its required fields and capability. Read-only requests first create the bind mount and then remount it with `remount,bind,ro`; a failed read-only remount removes the bind mount rather than leaving writable publication behind.
  - ValidateVolumeCapabilities confirms supported requests and returns an explanatory unconfirmed response for unsupported combinations.
- Verification (2026-08-20): controller and node regressions passed for supported RWO mounts, raw block, ROX, RWX, conflicting capabilities, mount flags, required publish fields, and the two-step read-only bind remount. The combined F8–F10 regression, full host/static suite, and race suite passed.

### 11. Validate NodeStageVolume before side effects and roll back failures

- [x] **F11 — High — Resolved**
- Affected:
  - `internal/csi/node_server.go`
  - `internal/csi/nvme.go`
- Problem: NVMe connect runs before validating the staging path, endpoint fields, filesystem, and capability. Failures after connect do not disconnect the newly created connection.
- Required fix:
  1. Validate all request fields before `nvme connect`.
  2. Track whether the current call created a connection.
  3. Roll back connection/mount/directory state on failure.
  4. Make connect, discovery, sleep, probe, format, and mount context-aware.
- Regression tests:
  - Inject failure after every staging step and assert no leaked connection or mount.
  - Cancellation during udev wait and external commands.
- Resolution:
  - Node staging now validates the volume ID, absolute non-root staging path, capability, filesystem, portal address and port, and attachment publish context before creating a directory or running `nvme connect`.
  - Connect reports whether the call may have created a connection. Staging tracks directory, connection, and mount ownership and rolls back only state created by that call, using an independent bounded cleanup context so request cancellation cannot suppress cleanup.
  - NVMe connect/discovery, device polling, filesystem probing/formatting, mount, and rollback commands now honor context cancellation.
- Verification (2026-08-20): focused CSI regressions passed for validation before connect, discovery and mount failures, directory/connection/mount rollback, cancellation during device discovery, and cancellation of `nvme connect`. The complete unit, vet, contract, and controller envtest suite passed.

### 12. Verify mount source during idempotent stage/publish

- [x] **F12 — High — Resolved**
- Affected: `internal/csi/node_server.go`
- Problem: Any mount at the target path is accepted as success without verifying source device, bind relationship, filesystem, or flags.
- Required fix:
  - Parse mount information and compare expected source identity, major/minor device, filesystem, and read-only flags.
  - Return `FailedPrecondition` for a mismatched existing mount.
- Regression tests:
  - Wrong source at staging target.
  - Wrong bind source at publish target.
  - Correct existing mount remains idempotent.
  - Correct source with incompatible flags is rejected or remounted safely.
- Resolution:
  - Stage and publish idempotency now use strict `/proc/self/mountinfo` parsing instead of accepting any mount at the requested path.
  - Existing staging mounts must match the expected block-device major/minor number, normalized filesystem, and writable state. Existing publications must refer to the same bind source and match the requested read-only state.
  - A conflicting mount returns `FailedPrecondition`; a newly issued mount is verified after the command and rolled back if kernel mount state does not match the request.
- Verification (2026-08-20): mountinfo and node regressions passed for escaped paths, wrong staging devices, filesystem and flag mismatches, wrong bind sources, read-only mismatches, and idempotent correct mounts. The complete unit, vet, contract, and controller envtest suite passed.

### 13. Make claims converge when hardware appears, moves, or disappears

- [x] **F13 — High — Resolved**
- Affected:
  - `internal/controller/nvmedeviceclaim_controller.go`
  - `internal/agent/reporter.go`
- Problem: The claim controller watches only claims and does not requeue unmatched claims. Active claims return early forever. Existing device specs are not refreshed, and disappeared hardware is not marked unavailable.
- Required fix:
  1. Watch `NVMeDevice` events and map them to claims by serial.
  2. Periodically validate pending and active claims.
  3. Update device location/capacity metadata safely or replace stale objects.
  4. Represent unavailable/stale hardware explicitly.
- Regression tests:
  - Claim before device, device recreation, PCI move, node move, disappearance, and duplicate serial.
- Resolution:
  - The hardware reporter now refreshes device location and capacity metadata, publishes a `HardwareAvailable` condition, and marks missing hardware `Unavailable` without discarding its claim owner.
  - Rediscovery restores the device to `Claimed` or `Available` according to its retained ownership. Claims watch device events and also revalidate periodically so both pending and active claims converge.
  - Claim matching fails closed when more than one available device reports the same serial. A unique replacement is adopted after a move and stale ownership is released only after the new binding is persisted.
- Verification (2026-08-20): reporter regressions passed for disappearance, retained ownership, metadata refresh, reactivation, and discovery failure. Controller envtests passed for late devices, node movement, unavailable devices, and duplicate serials. The complete unit, vet, contract, and controller envtest suite passed.

### 14. Make claim deletion ownership-safe and retryable

- [x] **F14 — High — Resolved**
- Affected: `internal/controller/nvmedeviceclaim_controller.go`
- Problem: Claim deletion does not check dependent partitions or explicit ownership. Any device `Get` error is ignored before the finalizer is removed.
- Required fix:
  1. Persist and verify claim owner UID.
  2. Block release while dependent partitions exist, or coordinate their cleanup.
  3. Ignore only confirmed NotFound; retry timeouts, authorization errors, conflicts, and other failures.
- Regression tests:
  - Active partitions, replacement claim, NotFound, Forbidden, timeout, conflict, and already-released device.
- Resolution:
  - Claim deletion waits until every partition referencing the claim UID has disappeared and watches partition events to resume cleanup promptly.
  - Device release verifies the full namespace/name/UID owner identity. Replacement-owned and already-released devices are left untouched, while every device still owned by the deleting UID is released to avoid interrupted-move leftovers.
  - Only a confirmed missing device is treated as already released; list, read, status patch, conflict, and finalizer update failures retain the finalizer and retry.
- Verification (2026-08-20): controller regressions passed for dependent partitions, missing devices, replacement ownership, already-released hardware, Forbidden and timeout reads, and status conflicts. The complete unit, vet, contract, and controller envtest suite passed.

### 15. Make discovery filtering exact and mount inspection fail safe

- [ ] **F15 — High — Confirmed**
- Affected: `internal/agent/nvme_discovery.go`
- Problem:
  - `/sys/class/block` or `lsblk` failures are treated as “not mounted.”
  - Allow/exclude variables use substring matching rather than exact comma-separated entries.
  - Filters are not applied to SPDK discovery.
  - Discovery-source failures are logged but hidden from callers.
- Required fix:
  1. Parse normalized exact PCI-address sets.
  2. Apply policy consistently to kernel and SPDK results.
  3. Exclude a device when mounted-state inspection fails unless an explicit unsafe override is enabled.
  4. Surface degraded discovery through errors/Conditions/metrics.
- Regression tests:
  - Exact, substring, whitespace, duplicate, and malformed lists.
  - Mounted namespace, unreadable sysfs, failed `lsblk`, and SPDK-bound device.

### 16. Use real RDMA readiness and endpoint discovery

- [ ] **F16 — High — Confirmed known limitation**
- Affected:
  - `internal/agent/reporter.go`
  - `internal/controller/rdmastoragenode_controller.go`
  - `internal/controller/nvmepartition_controller.go`
  - `internal/agent/partition_manager.go`
- Problem: Placement does not consult RDMA nodes. Reporter always claims RoCEv2, assumes Node InternalIP is RDMA-capable, falls back to loopback, and always reports zero exports. The RDMA node controller is empty.
- Required fix:
  1. Discover actual RDMA interfaces, addresses, link state, and transport.
  2. Publish a timestamped Ready condition.
  3. Require fresh RDMA readiness during placement and export.
  4. Never publish loopback as a successful remote portal.
  5. Report actual active exports.
- Regression tests:
  - Missing Node, missing InternalIP, no RDMA interface, link down, stale heartbeat, valid RoCE, valid InfiniBand, and export count changes.

### 17. Supervise SPDK and bound every external operation

- [ ] **F17 — High — Confirmed known limitation**
- Affected:
  - `internal/agent/plugins/backend_spdk.go`
  - `internal/agent/plugins/spdk_rpc.go`
  - `internal/agent/partition_manager.go`
- Problem: `nvmf_tgt` exit is only logged, exported objects receive no periodic healthy requeue, RPC calls have no context/timeout, and validation checks only NQN presence.
- Required fix:
  1. Add target-process supervision and controlled restart behavior.
  2. Periodically reconcile exported partitions.
  3. Verify subsystem namespace, listener, and backing bdev.
  4. Add context and timeouts to all SPDK RPC calls.
- Regression tests:
  - Target crash, hung RPC, missing listener, wrong backing bdev, cancellation, and restart recovery.

### 18. Split Helm service accounts and reduce RBAC

- [ ] **F18 — High — Confirmed**
- Affected:
  - `deploy/charts/distort/templates/rbac.yaml`
  - manager, agent, CSI controller, and CSI node workload templates
- Problem: Every component shares one service account that can create/update/delete nodes, pods, PVs, PVCs, StorageClasses, attachments, and all DISTORT resources.
- Required fix:
  - Create separate least-privilege service accounts and roles for manager, agent/reporter, CSI controller, CSI node, provisioner, and registrar.
- Regression tests:
  - Render the chart and run a `kubectl auth can-i` allow/deny matrix for each service account.

### 19. Distinguish retryable and terminal partition failures

- [x] **F19 — High — Resolved**
- Affected: `internal/agent/partition_manager.go`
- Problem: Some transient kernel errors set `Failed`; the following reconciliation immediately returns success for every failed kernel partition, permanently suppressing recovery.
- Required fix:
  1. Introduce Conditions/reasons that distinguish retryable from terminal failure.
  2. Do not suppress retries based solely on `status.state == Failed`.
  3. Record observed generation and clear obsolete errors after spec/environment recovery.
- Regression tests:
  - Temporary device/API failure followed by recovery.
  - Permanent invalid configuration remains terminal without hot-looping.
- Resolution:
  - Provisioning failures now publish a generation-aware `ProvisioningReady` condition with explicit `Retryable...` or `Terminal...` reasons.
  - Transient driver, discovery, backend-availability, storage, export, and API failures return errors for controller-runtime backoff even when state is `Failed`; kernel partitions are no longer suppressed solely by that state.
  - Invalid plugins, options, capacity, and invalid volume-manager results become terminal for the observed generation. A successful retry replaces the failure condition with `Provisioned=True`, clearing the obsolete error.
- Verification (2026-08-20): agent regressions passed for retry after a temporary device-setup failure and terminal invalid configuration without hot-looping. The complete unit, vet, contract, and controller envtest suite passed.

### 20. Do not admit unimplemented `lvm` configuration

- [ ] **F20 — Medium — Confirmed**
- Affected:
  - `api/v1alpha1/nvmepartition_types.go`
  - `internal/csi/controller_server.go`
- Problem: The API enum accepts `lvm`, but no LVM plugin is registered.
- Required fix: Remove `lvm` from current validation or implement and register it. CSI should return an immediate actionable error for unsupported combinations.
- Regression test: Every admitted backend/volume-manager combination resolves to a registered compatible plugin.

### 21. Replace invalid sample manifests

- [ ] **F21 — Medium — Confirmed**
- Affected: `config/samples/*.yaml`
- Problem: All sample specs contain scaffold TODOs and omit required fields.
- Required fix: Provide safe, realistic examples. Avoid including agent-owned discovery objects in the default sample kustomization unless their use is explicitly explained.
- Regression test: Server-side dry-run every sample against envtest in CI.

### 22. Add behavior-focused test coverage

- [ ] **F22 — Medium — Confirmed**
- Affected: all test packages
- Problem: Agent/plugins have 0% coverage; controller tests only assert that reconciliation returns no error; CSI server operations are mostly untested.
- Required fix:
  1. Introduce injectable command runners and fake plugin/RPC interfaces.
  2. Add the regression tests listed under every finding.
  3. Add multi-volume, multi-namespace, failure-injection, concurrency, recovery, and XFS E2E cases.
  4. Verify provider resource deletion through SPDK/configfs, not only CR disappearance.
- Acceptance criteria: Critical storage isolation and deletion behavior is exercised in required CI checks.

### 23. Restore lint CI

- [ ] **F23 — Low — Confirmed**
- Affected:
  - Go sources reported by `golangci-lint`
  - `.github/workflows/lint.yml`
- Problem: The latest 2026-08-17 lint run reports 19 findings, including a cyclomatic complexity of 70 in `PartitionManager.Reconcile`, 15 logging violations, 2 repeated constants, and 1 comment-spacing violation.
- Required fix:
  - Split provisioning/deletion/recovery into smaller functions.
  - Convert logging to the configured structured Kubernetes style.
  - Resolve repeated constant and comment-spacing findings.
- Verification: `GOLANGCI_LINT_CACHE=/tmp/distort-golangci-cache ./bin/golangci-lint run` passes.

### 24. Align documented and actual Go versions

- [x] **F24 — Low — Resolved**
- Affected:
  - `go.mod`
  - `docs/content/architecture.md`
  - `docs/content/contributing.md`
- Problem: Documentation says Go 1.23+, while `go.mod` requires Go 1.25.3 and the code uses newer APIs.
- Required fix: Document Go 1.25.3+ or remove newer language/library dependencies and lower the module version.
- Regression test: Add CI for the declared minimum Go version.
- Resolution: The architecture and contributing guides now require Go 1.25.3+.

### 25. Enforce single-writer attachment fencing

- [ ] **F25 — High — Fix implemented; hardware verification pending**
- Affected:
  - CSI controller service and advertised capabilities
  - `CSIDriver` chart configuration
  - CSI node stage/publish behavior
- Problem: the chart declares `attachRequired: false`, and DISTORT does not implement `ControllerPublishVolume`/`ControllerUnpublishVolume` or an equivalent durable attachment lease. `ReadWriteOnce` limits Kubernetes scheduling intent but does not itself fence a stale node. A forced cross-node migration can therefore leave two nodes using the same non-clustered filesystem, risking corruption.
- Required fix:
  1. Add controller publish/unpublish attachment state or a durable lease with an immutable volume identity and node owner.
  2. Reject a concurrent node while a valid attachment exists, and define recovery for stale or unreachable owners.
  3. Align the `CSIDriver` capability advertisement with the implemented attach flow.
- Acceptance criteria:
  - At most one node may hold a read-write attachment for a single-writer volume.
  - Publish/unpublish retries are idempotent, and stale attachment recovery is explicit and observable.
  - Forced migration cannot silently create concurrent ext4 or XFS users.
- Regression tests:
  - Competing consumers on two nodes and a forced migration sequence.
  - Stale-owner recovery after node loss.
  - Repeated publish and unpublish calls for the same volume/node pair.
- Implemented fix:
  - Controller publish/unpublish now owns one deterministic, namespaced `NVMeVolumeAttachment` per immutable partition UID. Its immutable node, deterministic host NQN, unique attachment lifetime, status condition, and fencing finalizer make retries observable and prevent a delayed unpublish from deleting a replacement owner.
  - Kernel and SPDK targets default to closed host access and authorize exactly the attachment owner. The provider agent revokes and disconnects the old host before releasing the attachment finalizer. A competing node is rejected unless an administrator explicitly annotates the current attachment with `storage.distort.io/force-detach-node=<current-node>` after independently fencing that node.
  - The CSI controller advertises publish/unpublish support, the chart enables `attachRequired`, deploys the external-attacher, and grants its required attachment/status permissions. The guarded Vagrant deploy recreates the immutable `CSIDriver` registration when upgrading the lab.
- Verification status (2026-08-20): controller, agent, target-backend, chart contract, and E2E compile regressions pass, including same-node retry, competing-node rejection, explicit takeover, stale unpublish, ACL ordering, and finalizer release. In the isolated lab, initial SPDK attach and concurrent-node rejection passed and exposed two integration gaps that were corrected: missing `volumeattachments/status` RBAC and use of a nonexistent standalone SPDK disconnect RPC. The final two-node rerun remains pending because permission to rebuild the corrected local agent image was declined.

## Cross-cutting release gates

Before considering the implementation production-ready, require all of the following:

- [x] Full unit and envtest suite passes.
- [x] Race tests pass for controller, agent state logic, and CSI packages.
- [ ] Lint and vet pass.
- [x] Generated CRDs, RBAC, DeepCopy code, and Helm CRDs have no drift.
- [ ] CSI conformance tests pass for the explicitly supported capability set.
- [x] Two-volume isolation tests pass for every enabled backend.
- [x] Same-name/different-namespace isolation tests pass.
- [ ] Single-writer attachment fencing prevents concurrent cross-node filesystem use.
- [x] Finalizer cleanup proves backend resources are gone.
- [x] Capacity concurrency tests prove no overcommit.
- [ ] SPDK target crash and agent restart recovery tests pass.
- [ ] RDMA link/node failure produces actionable non-ready state rather than a false `Exported` result.
- [ ] Helm RBAC allow/deny matrix passes.
- [x] E2E tests run only against an isolated disposable cluster.

## Original environmental verification limitations

- No Vagrant/Kind E2E environment or kubeconfig was available during the review.
- Docker image construction, physical NVMe operations, SPDK execution, and real RDMA transport were not exercised.
- The rendered Helm manifests passed Helm validation, but `kubectl --dry-run` was blocked by the locally installed snap confinement configuration.

These statements describe the original review session, not the current lab state. On 2026-08-17 the subsequent three-node Vagrant/K3s run built and deployed the image. After F1, F2, and F4 were resolved, the complete eight-spec green hardware E2E suite passed over virtual NVMe and SoftRoCE; four known-failure regressions remained quarantined. The focused F4 run also proved same-name/different-namespace SPDK isolation and deletion in both orders.
