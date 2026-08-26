---
title: "Review Findings"
description: "Prioritized defects, regression criteria, completed fixes, and production-readiness gates for DISTORT."
type: "page"
---

# Current Project Review — 2026-08-25

> Batch 1 update (2026-08-26): fixes for F3, F4, F11, F12, and the
> host-side portion of F13 are implemented and pass the host CI-equivalent
> suite. Batch 2 resolves F2, F6, and F7 with claim immutability, live ownership
> checks, fail-closed inventory health, safe partial discovery, and hashed
> device names. Host regression coverage and the three-node isolated-lab smoke
> check pass. The corrected F25 takeover remains pending in the isolated Vagrant
> environment.

## 1. Executive summary

DISTORT has a credible, well-structured happy path, but the current working tree is not production-ready. The host-side suite and race detector pass, yet several important recovery, deployment, CSI-contract, and state-consistency defects remain outside those tests.

The most serious unresolved findings are:

- The kernel NVMe target can report a partition as Exported and an attachment as ready when configfs is incomplete or disconnected.
- Multiple CSI 1.5 request semantics are implemented incorrectly.
- SPDK startup and shared process options are not transactional or consistently enforced.

No Critical issue was confirmed. Four High-severity defects and several Medium-severity correctness and reliability gaps were found.

This review covers the current working tree, which was already dirty: 36 modified and 5 untracked files. It therefore does not necessarily represent a committed release.

Project summary:

- Purpose: Kubernetes-native disaggregated NVMe storage exported over NVMe-oF/RDMA.
- Main binaries:
  - distort-manager: claims, scheduling, capacity, and heartbeat expiry.
  - distort-agent: hardware discovery, SPDK/kernel provisioning, exports, ACLs, and status.
  - distort-csi: CSI controller, attachment fencing, node connection, formatting, and mounting.
- Persistence:
  - Kubernetes CRDs store desired state, identities, allocation, and attachment ownership.
  - Physical partition tables or SPDK lvol metadata store backend allocation.
  - Kernel configfs or SPDK JSON-RPC state stores live target configuration.
- Dependencies: Go 1.25.3, Kubebuilder/controller-runtime, Kubernetes 0.35, CSI 1.5, gRPC, SPDK 26.01, nvme-cli, parted, ext4/XFS, Helm, K3s/Vagrant/VirtualBox, and RDMA/SoftRoCE.
- Review limitations:
  - Kernel configfs and real SPDK behavior were code-reviewed but not exercised because the lab deployment is unhealthy.
  - No CSI conformance suite was available.
  - Generated files were checked for drift but were not treated as hand-maintained source.

## 2. Intended behavior map

| Feature or flow | Intended behavior | Implementation | Status |
|---|---|---|---|
| Helm installation | Install a working manager, agent, CSI controller/node, CRDs, and RBAC | deploy/charts/distort | **Host-verified; published image pending** |
| Device discovery | Discover safe, unmounted NVMe controllers and mark disappeared hardware unavailable | internal/agent/nvme_discovery.go, reporter.go | **Host and isolated-lab smoke verified** |
| RDMA readiness | Publish only active, usable, fresh RDMA endpoints | rdma_discovery.go, rdmahealth/readiness.go | **Partially verified** |
| Device claims | Bind exact serial to one device and preserve ownership by UID | nvmedeviceclaim_controller.go | **Host-verified** |
| Partition placement | Select a claimed device on a fresh RDMA node with sufficient capacity | nvmepartition_controller.go | **Host-verified for live ownership and inventory health** |
| SPDK provisioning | Bind hardware, create lvol, export, supervise, recover, and clean up | SPDK backend and lvol manager | **Partially verified** |
| Kernel provisioning | Create GPT partition and complete configfs target, recover idempotently | Kernel backend and parted manager | **Broken under partial state** |
| CSI Create/Delete | Create stable namespaced volume handles and clean exact backend allocation | controller_server.go | **Partially verified** |
| Single-writer attachment | Durable owner, exact host ACL, explicit fenced takeover | controller_attachment.go, attachment CRD, agent ACL reconciliation | **Partially verified; hardware gate pending** |
| CSI node lifecycle | Connect, validate filesystem, format blank volume, stage and publish safely | node_server.go, nvme.go | **Partially verified** |
| Host-side quality gates | Tests, race, lint, tidy, Helm, docs, and E2E compile should pass | Makefile and GitHub workflows | **Host-verified** |
| Vagrant hardware workflow | Redeploy current image, smoke-check lab, and run isolated E2E | Makefile, vagrant, and E2E tests | **Redeploy fixed; lab validation pending** |
| Documentation | Canonical docs describe current implementation and release status | docs/content | **Host-verified** |

## 3. Findings

Status legend:

- [ ] Open, or implemented but still awaiting required verification
- [x] Resolved and verified at the required test level

### 1. Kernel target can falsely report a broken export as ready

- [ ] **F1 — High — Open**
- **Confidence:** Confirmed in control flow; hardware consequence not exercised
- **Affected:** internal/agent/plugins/backend_kernel.go:63, internal/agent/partition_manager.go:488
- **Expected:** Reconciliation verifies or repairs the namespace device path, enabled state, listener address and port, subsystem link, and host ACL before publishing Exported or AccessReady=True.
- **Actual:**
  - ExportVolume returns success as soon as the subsystem directory exists, without checking anything beneath it.
  - Creation is multi-step. Any failure after creating the subsystem leaves partial state that every retry treats as successful.
  - KernelBackend does not implement ExportHealthChecker, so periodic reconciliation never validates kernel targets.
  - ReconcileHostAccess returns early when its host list is exact, without checking that the subsystem remains linked to port 1.
  - The backend always writes addr_adrfam=ipv4 even if discovery selected IPv6.
- **Reproduction:** Inject a failure after creating the subsystem but before namespace, listener, or symlink completion. The next retry returns success and marks the partition Exported. Alternatively, remove the port symlink while keeping the desired host ACL; reconciliation reports AccessReady.
- **Why it matters:** Kubernetes can bind a PV or complete attachment while no usable target exists. Endpoint changes and configfs corruption are not repaired.
- **Recommended fix:** Implement an exact kernel CheckExport, make ExportVolume validate and repair each component, derive address family from the portal IP, and always verify or recreate the port link. Revoke ACLs before unexport.
- **Regression test:** Add fake-configfs tests for partial subsystems, missing/wrong namespaces and listeners, missing links, IPv6, and retry after link failure. Assert that invalid state never produces Exported or AccessReady.

### 2. Mutating an active claim creates inconsistent or leaked ownership

- [x] **F2 — High — Resolved**
- **Confidence:** Confirmed
- **Verification (2026-08-26):** The generated API schema rejects empty and mutated serials. Controller envtest covers active claims both with and without dependent partitions, verifies unchanged ownership after rejection, and proves placement ignores inactive or missing live claims. The complete host CI and race gates pass.
- **Affected:** api/v1alpha1/nvmedeviceclaim_types.go:27, internal/controller/nvmedeviceclaim_controller.go:191, internal/controller/nvmepartition_controller.go:74
- **Expected:** The serial establishing physical ownership is immutable, or migration safely coordinates every dependent partition and old device.
- **Actual:** spec.serialNumber has no nonempty or immutability validation. Patching it to a missing serial leaves the old device Claimed. Patching it to another present serial can claim the new device without releasing the old one because the release loop only considers devices matching the new serial. Placement checks device status and ClaimRef but does not resolve the live claim. The agent later rejects old allocations because the claim serial no longer matches.
- **Reproduction:** Bind claim C to serial A, create a partition, then patch C.spec.serialNumber to B. Reconcile and create another partition. The claim can retain A and acquire B while old allocations fail authorization.
- **Why it matters:** Device ownership leaks, partitions become stuck, and stale exports may stop receiving health and ACL reconciliation.
- **Recommended fix:** Add MinLength=1 and CEL immutability to serialNumber. Placement should verify the referenced live claim UID, active state, matched device, node, and serial before reserving capacity.
- **Regression test:** Patch a bound claim with and without dependent partitions in envtest; expect admission rejection and unchanged ownership.

### 3. The documented Helm quick start uses an unsafe non-release image

- [x] **F3 — High — Resolved**
- **Confidence:** Confirmed
- **Verification (2026-08-26):** The chart requires a qualified repository, defaults to version `0.5.0`, rejects `latest`, supports digest pinning, and passes the host Helm and repository-contract gates. Publishing the first project image remains a release operation.
- **Affected:** README.md:20, deploy/charts/distort/values.yaml:1
- **Expected:** A plain documented install deploys a project-owned versioned image or requires an explicit repository.
- **Actual:** The README provides no image override. The chart renders every DISTORT component as unqualified, mutable distort:latest.
- **Reproduction:** helm template distort deploy/charts/distort renders four image: "distort:latest" entries.
- **Why it matters:** Installation may fail, pull an unrelated registry image, or change without a chart update. Deployment and rollback are not reproducible.
- **Recommended fix:** Publish and default to a fully qualified project-controlled image aligned with Chart.appVersion, preferably with optional digest pinning. Otherwise make the repository value mandatory.
- **Regression test:** Assert that the default rendered image is qualified, not latest, and matches the chart release.

### 4. The documented lab redeploy command is a silent no-op

- [ ] **F4 — High — Fix implemented; isolated-lab verification pending**
- **Confidence:** Confirmed
- **Verification (2026-08-26):** The dry-run repository contract passes and emits the complete build/deploy workflow. A real isolated-lab redeploy is still pending.
- **Affected:** Makefile:184, docs/content/local-testing.md:121
- **Expected:** make test-env-redeploy builds, imports, deploys, restarts, and waits for the current image.
- **Actual:** The phony target has no recipe or prerequisite. The deploy prerequisite is attached to the misspelled target test-e test-env-destroynv-redeploy.
- **Reproduction:** make -n test-env-redeploy exits 0 and prints Nothing to be done.
- **Why it matters:** Hardware tests can run against stale code or remain unavailable while the workflow falsely reports success. The current lab has several ErrImageNeverPull workloads and an unhealthy CSI controller.
- **Recommended fix:** Define test-env-redeploy: test-env-deploy.
- **Regression test:** Add a repository contract that dry-runs the target and verifies that build and deploy commands are emitted.

### 5. CSI controller violates multiple CSI 1.5 request contracts

- [ ] **F5 — Medium — Open**
- **Confidence:** Confirmed
- **Affected:** internal/csi/controller_server.go:43, internal/csi/controller_attachment.go:109, CSI 1.5 spec CapacityRange and ControllerUnpublishVolume contracts
- **Expected:** Follow the request semantics in the declared CSI 1.5 dependency.
- **Actual:**
  1. A limit-only CapacityRange is rejected even though required_bytes=0 means unspecified.
  2. ValidateVolumeCapabilities never resolves the volume. Any nonempty fake ID with generic supported capabilities can be confirmed, and context or parameters are not compared with persisted state.
  3. ControllerUnpublishVolume rejects empty node_id even though CSI defines it as unpublish from all nodes.
- **Reproduction:** Submit CapacityRange{RequiredBytes:0, LimitBytes:1Gi}; validate a nonexistent ID with a supported mount capability; or unpublish a valid volume with empty NodeId.
- **Why it matters:** Standards-compliant orchestrators can be rejected while nonexistent or stale volume identities are incorrectly confirmed.
- **Recommended fix:** Normalize limit-only ranges, resolve exact UID-bearing handles during validation, compare filesystem/context/parameters, and treat empty node ID as deletion of the current attachment.
- **Regression test:** Add limit-only, both-zero, nonexistent and recreated volume, altered context, parameter mismatch, and all-node-unpublish tests. Run CSI conformance for advertised methods.

### 6. Discovery failures leave stale devices schedulable

- [x] **F6 — Medium — Resolved**
- **Confidence:** Confirmed
- **Verification (2026-08-26):** Reporter tests prove safe partial results are refreshed while kernel/SPDK and aggregate discovery conditions degrade, recovery requires a complete successful observation, and missing-device invalidation is skipped for incomplete scans. Controller envtest proves `NVMeInventoryReady=False` blocks new placement. The complete host CI and race gates pass. The isolated three-node lab then reported `NVMeInventoryReady=True` for every node and passed smoke validation with three discovered devices.
- **Affected:** internal/agent/nvme_discovery.go:85, internal/agent/reporter.go:142
- **Expected:** A degraded scan publishes safe partial results with explicit source health or makes affected inventory unavailable.
- **Actual:** DiscoverNVMe can return partial devices plus an error. reportDevices discards the entire result, reports zero capacity, and skips invalidating previously reported devices. RDMA readiness can remain true, and placement uses stale NVMeDevice objects instead of reported node capacity.
- **Reproduction:** Begin with a claimed device, then fail only SPDK or kernel discovery. The stale device remains eligible for placement while provisioning subsequently fails discovery.
- **Why it matters:** Requests can be assigned to stale hardware and enter persistent retry loops with misleading node health.
- **Recommended fix:** Track health per discovery source, process safe partial results, publish a degradation condition, and make scheduling reject stale inventory.
- **Regression test:** Inject successful discovery followed by a partial-source error and assert no new partition is scheduled until a fresh successful observation.

### 7. Raw hardware serials are unsafe Kubernetes object names

- [x] **F7 — Medium — Resolved**
- **Confidence:** Highly likely
- **Verification (2026-08-26):** Device names now use a bounded readable node prefix plus a 128-bit SHA-256-derived suffix while preserving the exact serial in spec. Tests cover whitespace, punctuation, long serials, deterministic distinct names, missing serial/PCI identity, zero capacity, partial kernel scans, API schema validation, and generated/chart CRD synchronization. The complete host CI and race gates pass, and the isolated three-node lab successfully created all three hardware-backed `NVMeDevice` objects and passed smoke validation.
- **Affected:** internal/agent/reporter.go:152, internal/agent/nvme_discovery.go:168
- **Expected:** Supported NVMe serials are preserved as exact identities while producing bounded DNS-safe Kubernetes names.
- **Actual:** The object name is nodeName plus a lowercased raw serial. There is no sanitization, hash, length bound, or DNS validation. Critical serial and PCI sysfs reads can also be silently omitted, and Required markers do not reject empty strings.
- **Reproduction:** Return a serial containing spaces, a slash, invalid punctuation, or enough characters to exceed the object-name limit. Kubernetes rejects creation. A missing PCI address reaches later backend setup as an empty address.
- **Why it matters:** Valid hardware can become undiscoverable, while malformed metadata fails only during provisioning.
- **Recommended fix:** Use a bounded DNS-safe hash in metadata.name, preserve the exact serial in spec, and reject empty serial, PCI address, node, or nonpositive capacity.
- **Regression test:** Cover whitespace, punctuation, long serials, missing serial/PCI, and deterministic collision resistance.

### 8. SPDK process configuration is not transactional or consistently enforced

- [ ] **F8 — Medium — Open**
- **Confidence:** Confirmed for option handling; highly likely for stuck initialization
- **Affected:** internal/agent/plugins/backend_spdk.go:40, internal/storageoptions/options.go:46
- **Expected:** A declared core mask is honored or rejected consistently, and failed initialization leaves a clean state for retry.
- **Actual:**
  - spdk-core-mask=0x0 passes validation despite selecting no CPU.
  - Only the first volume that starts the node-wide nvmf_tgt process controls its mask. Later conflicting options silently succeed.
  - With custom iobuf settings, failures in iobuf_set_options, framework_start_init, or framework_wait_init return without killing the wait-for-rpc process. A retry sees pidof succeed and skips initialization.
- **Reproduction:** Start with mask 0x1, then provision with 0x3; both requests succeed but the second is not applied. Alternatively, fail framework_start_init and retry while the child remains alive.
- **Why it matters:** StorageClass configuration has undocumented first-request-wins behavior, and initialization faults can require manual process cleanup.
- **Recommended fix:** Treat the core mask as node-global, persist and verify it, reject conflicts and zero masks, and terminate/reap the process on every initialization failure.
- **Regression test:** Capture first and second masks, reject conflicts and 0x0, and inject every initialization RPC failure while verifying clean retry.

### 9. RDMA endpoint validation can select an unusable address or state

- [ ] **F9 — Medium — Open**
- **Confidence:** Confirmed
- **Affected:** internal/agent/rdma_discovery.go:24, internal/rdmahealth/readiness.go:19
- **Expected:** Placement accepts only an exactly active port with a routable address family supported by the target and CSI node.
- **Actual:** Port state uses substring matching for ACTIVE; address selection accepts multicast and link-local addresses and returns the first address; readiness repeats the weak validation; node staging disagrees by rejecting multicast; and kernel export always configures IPv4. The CRD also permits TCP while readiness rejects it.
- **Reproduction:** Present an ACTIVE_DEFER state or an interface whose first address is link-local or multicast. It may become schedulable and later fail export or staging.
- **Why it matters:** The system can report false readiness and produce unusable Exported partitions.
- **Recommended fix:** Parse exact state, reject unsupported multicast and link-local addresses, prefer configured routable addresses, derive target address family, and align or remove TCP support.
- **Regression test:** Add address-order, multicast, link-local/global IPv6, ACTIVE_DEFER, and kernel IPv6 cases.

### 10. CSI node publish and unpublish paths are not fail-safe

- [ ] **F10 — Medium — Open**
- **Confidence:** Confirmed
- **Affected:** internal/csi/node_server.go:290 and :332
- **Expected:** Every mount and unmount RPC requires a non-root absolute kubelet path before privileged filesystem operations.
- **Actual:** NodeStageVolume performs this validation, but NodeUnstageVolume, NodePublishVolume, and NodeUnpublishVolume only require nonempty strings. The implementation then calls MkdirAll, mount --bind, or umount directly.
- **Reproduction:** Publish with target / or a relative path, or unstage/unpublish /. The driver attempts filesystem operations instead of returning InvalidArgument.
- **Why it matters:** A malformed local CSI request can modify or mask sensitive paths in the privileged CSI container and potentially affect mount-propagated kubelet paths.
- **Recommended fix:** Centralize clean, absolute, non-root validation and optionally require the configured kubelet-root prefix.
- **Regression test:** Table-test empty, relative, root, cleaned traversal, and valid kubelet paths while asserting no command is invoked on rejection.

### 11. The current GitHub quality gates are deterministically red

- [x] **F11 — Medium — Resolved**
- **Confidence:** Confirmed
- **Verification (2026-08-26):** `make test-ci` passes with tidy verification, the project custom linter, E2E compilation, and the race detector.
- **Affected:** .github/workflows/test.yml:20, .github/workflows/lint.yml:20, .golangci.yml:25, internal/controller/sample_manifests_test.go:11
- **Expected:** The documented GitHub tidy and lint checks pass on the current green tree.
- **Actual:** go mod tidy moves sigs.k8s.io/yaml from indirect to direct. The GitHub lint action installs a stock binary, but the configuration enables the custom module plugin logcheck. Local make lint succeeds only because the Makefile builds a custom binary.
- **Reproduction:** GOCACHE=/tmp/distort-go-build go mod tidy -diff exits 1. The stock 2.8.0 linter exits 3 with plugin logcheck not found.
- **Why it matters:** Pull requests cannot satisfy advertised quality gates, encouraging bypasses and masking genuine regressions.
- **Recommended fix:** Commit tidy output and make the lint workflow invoke the project custom linter through make lint.
- **Regression test:** Add one local CI-equivalence target and make workflows invoke it.

### 12. Canonical documentation contradicts the current attachment implementation

- [x] **F12 — Medium — Resolved**
- **Confidence:** Confirmed
- **Verification (2026-08-26):** Documentation contracts and the Hugo build pass against the five-CRD, `attachRequired: true` implementation.
- **Affected:** docs/content/architecture.md:66 and :124, docs/content/internals.md:425, docs/content/using.md:173
- **Expected:** Canonical documentation provides one description of current deployed behavior.
- **Actual:** Architecture lists four CRDs and omits NVMeVolumeAttachment. Architecture and internals say attachRequired is false and publish/unpublish are absent. The chart and CSI implementation do the opposite. Architecture also lists several resolved items as remaining production work.
- **Reproduction:** Compare the affected documentation with deploy/charts/distort/templates/csidriver.yaml and internal/csi/controller_attachment.go.
- **Why it matters:** Operators can make unsafe decisions based on obsolete fencing claims, and reviewers cannot determine which release gates are outstanding.
- **Recommended fix:** Update architecture and internals from the current implementation and retain this file as the historical ledger.
- **Regression test:** Add documentation contracts for the CRD count, attachRequired, implemented CSI methods, and forbidden stale assertions.

### 13. Some green verification can pass without proving readiness or fencing

- [ ] **F13 — Low — Fix implemented; isolated-lab verification pending**
- **Confidence:** Confirmed
- **Verification (2026-08-26):** Host-side contracts, lint, E2E compilation, and the race suite pass. The strengthened smoke check and corrected F25 takeover require isolated-lab verification.
- **Affected:** vagrant/smoke-test.sh:35, test/e2e/e2e_test.go:34, test/e2e/regression_e2e_test.go:475, test/contracts/repository_contracts_test.go:173
- **Expected:** Green tests prove the behavior claimed by their documentation and labels.
- **Actual:** Smoke and base E2E count RDMAStorageNode objects without requiring Ready=True, a fresh heartbeat, or a usable endpoint. F25 is labeled green and release-gate while its corrected hardware run is pending. F25 verifies the SPDK host list but not failed old-node I/O after takeover. The resolved F24 test remains quarantined and fails when selected because its literal version match does not accept equivalent documentation wording.
- **Why it matters:** Test status and documentation overstate production evidence.
- **Recommended fix:** Validate readiness fields, keep F25 out of release-green filters until hardware completion, prove old-node I/O failure, and unquarantine or make F24 semantic.
- **Regression test:** Make these checks permanent green assertions once the underlying behavior is verified.

## 4. Robustness gaps

The following are material risks inferred from code but not fully proven in the available hardware environment:

- DeleteVolume returns after issuing Kubernetes deletion rather than waiting for agent finalizers and backend cleanup.
- SPDK repair deletes an existing subsystem after any failed health check, including transient RPC or observation failures.
- Capacity serialization uses process-local mutexes; there is no durable reservation transaction across leader overlap.
- Several helpers use background contexts or lack their own timeout, including legacy lvol RPC calls and ResetSPDKDevice.
- KernelBackend.SetupDevice logs and suppresses SPDK reset failure.
- PartedVolumeManager.SetupStorage ignores wipefs failure before creating GPT.
- Helm workloads have no liveness, readiness, or startup probes; CSI Probe does not test dependencies.
- Reset scripts broadly ignore wipe, rebind, and configfs cleanup errors.
- Existing SPDK lvol retries trust requested capacity rather than verifying actual existing bdev size.
- Metrics and Conditions do not expose degraded NVMe inventory discovery.

## 5. Test coverage gaps

| Test type | Scenario and input | Expected result | Suggested location |
|---|---|---|---|
| Kernel integration | Partial configfs subsystem, missing link/listener/namespace | Repair succeeds or partition remains non-exported | internal/agent/plugins/backend_kernel_test.go |
| Envtest | Patch active claim serial after binding | Admission rejection; ownership unchanged | Claim controller tests |
| CSI unit/conformance | Limit-only capacity, fake volume validation, empty-node unpublish | CSI 1.5-compliant responses | CSI controller tests |
| Reporter/controller integration | One discovery source errors after prior success | Stale device cannot be scheduled | Agent/controller tests |
| SPDK failure injection | Fail each custom initialization RPC | Process killed and next retry fully initializes | Plugin tests |
| Configuration | Two volumes request conflicting core masks | Second request rejected observably | Plugin/agent tests |
| RDMA unit/hardware | Link-local, multicast, IPv6, ACTIVE_DEFER | Only explicitly supported endpoint becomes ready | RDMA tests |
| CSI privileged path | Root, relative, and traversal-like cleaned paths | InvalidArgument and no mount command | Node request tests |
| Helm/Make contract | Default image and redeploy dependency | Qualified image and emitted deploy commands | Repository contracts |
| E2E fencing | Continue I/O from old node after forced takeover | Old I/O fails before new writer is authorized | F25 E2E |
| Recovery E2E | Corrupt kernel link/listener while exported | Agent repairs it and preserves attachment | Vagrant kernel E2E |
| Smoke | Stale or non-ready RDMA object exists | Smoke fails despite object count | Shell or E2E test |

The coverage profile also reports important production functions at 0%, including kernel ExportVolume, UnexportVolume and SetupDevice, SPDK ExportVolume and device reset/transport setup, and several real filesystem-formatting paths.

## 6. Recommended improvements

### Immediate fixes

| Recommendation | Impact | Effort |
|---|---:|---:|
| Make kernel export and ACL reconciliation exact and repairable | High | Medium |
| Make claim serial nonempty and immutable; validate live claim during placement | High | Low–Medium |
| Correct test-env-redeploy and rerun the isolated hardware suite | High | Low |
| Replace distort:latest with a qualified versioned release image | High | Low |
| Correct CSI capacity, validation, and all-node unpublish semantics | High | Medium |
| Commit tidy output and align GitHub lint with the custom binary | Medium | Low |

### Near-term hardening

| Recommendation | Impact | Effort |
|---|---:|---:|
| Surface degraded discovery and make inventory freshness schedulable state | High | Medium |
| Make SPDK initialization transactional and core-mask configuration node-global | High | Medium |
| Strengthen RDMA address, state, and family validation | Medium | Medium |
| Validate every CSI mount and unmount path consistently | Medium | Low |
| Add workload health probes and meaningful CSI dependency checks | Medium | Medium |
| Update architecture and internals to the current five-CRD design | Medium | Low |

### Longer-term improvements

| Recommendation | Impact | Effort |
|---|---:|---:|
| Run CSI conformance for the advertised controller/node capability set | High | High |
| Make capacity reservation safe across manager failover | High | High |
| Add target corruption, timeout, and node-loss failure-injection E2E | High | High |
| Verify finalizer completion before acknowledging CSI deletion | Medium | Medium |
| Publish signed or digest-pinned reproducible release artifacts | Medium | High |
| Add target health, discovery, cleanup, and attachment-revocation metrics | Medium | Medium |

## 7. Verification performed

Successful commands and checks:

- make test-suite passed unit, envtest, contract, vet, manifests/generate/fmt, Helm lint/render, Hugo, custom golangci-lint, and tagged E2E compilation.
- Reported package coverage was 59.8% for agent, 58.7% for plugins, 71.4% for controllers, and 67.6% for CSI.
- make test-race passed all non-E2E packages.
- GOCACHE=/tmp/distort-go-build go build ./cmd/... passed for all three binaries.
- git diff --check passed.
- Generated and Helm CRD copies compared equal.
- helm template rendered successfully and confirmed distort:latest and attachRequired: true.
- vagrant status from vagrant/ reported all three VMs running.
- make test-env-status verified the guarded cluster and three Ready Kubernetes nodes, but showed several ErrImageNeverPull workloads and an unhealthy CSI controller.
- GOCACHE=/tmp/distort-go-build go tool cover -func=cover.out completed with aggregate statement coverage of 52.6%.

Commands that exposed project failures:

- GOCACHE=/tmp/distort-go-build go mod tidy -diff exited 1 because sigs.k8s.io/yaml must become a direct dependency.
- The stock golangci-lint 2.8.0 binary exited 3 because logcheck was not present.
- make -n test-env-redeploy exited 0 but performed nothing.
- The selected F24 contract test failed because its literal version assertion does not accept the architecture guide wording.

Limitations:

- Initial tidy and coverage attempts using the default cache hit the read-only sandbox cache and were rerun with a cache under /tmp.
- Hardware E2E, destructive lab reset, Docker image build, real SPDK/configfs operations, and CSI conformance were not run.
- Hardware E2E was not meaningful while the guarded lab workloads were unhealthy and the documented F25 worker-disk problem remained unresolved.
- No intentional source edits were made during the audit. The test-suite target inherently invokes generation and formatting; the same 41 working-tree entries remained present, but no pre-run byte-level snapshot was available.

## 8. Final assessment

| Area | Rating | Rationale |
|---|---:|---|
| Functional correctness | **6/10** | Major normal flows are implemented and tested, but kernel recovery, claim mutation, CSI semantics, and deployment defaults contain real defects. |
| Robustness | **4/10** | Partial configfs state, stale discovery, SPDK initialization failures, and incomplete recovery validation remain fragile. |
| Error handling | **5/10** | Many failures are surfaced with Conditions and structured logs, but several device errors are suppressed or converted into misleading success. |
| Test quality | **6/10** | Broad host, race, envtest, Helm, docs, and E2E coverage exists, but critical hardware functions remain untested and some green assertions mask invalid behavior. |
| Maintainability | **7/10** | Code is reasonably modular with stable identities and structured controllers; stale documentation and node-global configuration reduce clarity. |
| Production readiness | **3/10** | Default installation is not reproducible, hardware release gates remain incomplete, the current lab cannot run the full suite, and kernel target recovery is not trustworthy. |

Overall, the project is suitable for continued development and controlled lab testing, but not for production storage workloads until the High findings, CSI conformance gaps, and hardware recovery and fencing gates are resolved.

---

## Historical Review Findings and Fix Backlog

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

- [x] **F15 — High — Resolved**
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
- Resolution:
  - Allow and exclude settings are parsed as normalized, exact PCI-address sets; malformed entries fail discovery and exclusion wins consistently for kernel- and SPDK-discovered controllers.
  - Mount inspection now returns errors instead of treating inspection failure as an unmounted device. Devices fail closed unless the administrator explicitly sets `NVME_ALLOW_UNSAFE_MOUNT_INSPECTION=true`.
  - Kernel sysfs, capacity, SPDK RPC, and mount-inspection failures are returned to the reporter so degraded discovery is visible rather than silently producing an unsafe inventory.
- Verification (2026-08-25): fake-sysfs and command tests passed for exact/partial/malformed policies, mounted namespaces, failed inspection, the explicit unsafe override, and SPDK-bound devices. The complete host test suite passed.

### 16. Use real RDMA readiness and endpoint discovery

- [x] **F16 — High — Resolved**
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
- Resolution:
  - Each agent discovers active ports from `/sys/class/infiniband`, resolves their associated network device and non-loopback address, and reports the observed transport and link speed without a Kubernetes Node-IP or loopback fallback.
  - `RDMAStorageNode` status now publishes a timestamped `Ready` condition and the actual number of exported partitions. The controller expires stale heartbeats after 45 seconds.
  - Placement and agent export both require a fresh ready RDMA node, and the exporter uses that verified RDMA endpoint as its portal address.
- Verification (2026-08-25): agent and controller tests passed for active/down/unreadable RDMA sysfs, stale status, endpoint validation, and export counts. The isolated three-node smoke test reported three ready RoCEv2 endpoints at `192.168.56.10`–`192.168.56.12`, each with its discovered link speed and fresh heartbeat.

### 17. Supervise SPDK and bound every external operation

- [x] **F17 — High — Resolved**
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
- Resolution:
  - The agent supervises the managed `nvmf_tgt` process, restarts it on demand, and periodically health-checks every exported partition.
  - SPDK RPC calls accept caller cancellation and enforce a default 15-second timeout, including bounded child-process termination.
  - Export health validates the exact NQN, RDMA listener address and port, and the namespace's canonical bdev identity (including lvol UUID/alias resolution). Missing or mismatched exports are replaced idempotently.
  - Recovery remains authorized when transient SPDK loss hides the PCI device only if the already-provisioned partition, live claim UID, matched device, node, serial, and device owner reference still agree; new allocations remain fail-closed.
- Verification (2026-08-25): focused plugin/agent tests passed for hung RPCs, cancellation, listener and backing-bdev mismatches, process exit, and authorized recovery. The isolated Vagrant F17 E2E killed the live `nvmf_tgt` PID and observed the original NQN restored; the scenario passed with clean partition and claim teardown in 32 seconds.

### 18. Split Helm service accounts and reduce RBAC

- [x] **F18 — High — Resolved**
- Affected:
  - `deploy/charts/distort/templates/rbac.yaml`
  - manager, agent, CSI controller, and CSI node workload templates
- Problem: Every component shares one service account that can create/update/delete nodes, pods, PVs, PVCs, StorageClasses, attachments, and all DISTORT resources.
- Required fix:
  - Create separate least-privilege service accounts and roles for manager, agent/reporter, CSI controller, CSI node, provisioner, and registrar.
- Regression tests:
  - Render the chart and run a `kubectl auth can-i` allow/deny matrix for each service account.
- Resolution:
  - The chart now creates separate identities and least-privilege ClusterRoles for the manager, agent/reporter, and CSI controller, plus an unprivileged CSI node identity. Kubernetes assigns identity per Pod, so the provisioner/attacher share the CSI controller identity and the registrar shares the CSI node identity with their respective drivers.
  - Workload templates use component-specific service accounts. The CSI node/registrar Pod receives no Kubernetes API role, and no DISTORT workload can mutate Nodes.
- Verification (2026-08-25): chart lint/render and repository contracts passed. The isolated Vagrant RBAC E2E verified required manager, agent, and CSI-controller access plus eight forbidden cross-component/Node operations across all four identities.

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

- [x] **F20 — Medium — Resolved**
- Affected:
  - `api/v1alpha1/nvmepartition_types.go`
  - `internal/csi/controller_server.go`
- Problem: The API enum accepts `lvm`, but no LVM plugin is registered.
- Required fix: Remove `lvm` from current validation or implement and register it. CSI should return an immediate actionable error for unsupported combinations.
- Regression test: Every admitted backend/volume-manager combination resolves to a registered compatible plugin.
- Resolution:
  - `NVMePartition.spec.volumeManager` now admits only the implemented `partition` strategy; both generated CRD copies were regenerated from the API marker.
  - CSI validates backend/manager combinations before creating an object and returns `InvalidArgument` for unknown backends or unimplemented managers. Plugin registry coverage confirms the admitted SPDK and kernel implementations are registered.
- Verification (2026-08-25): CSI unit tests, generated-schema contract tests, and the isolated Vagrant admission E2E passed.

### 21. Replace invalid sample manifests

- [x] **F21 — Medium — Resolved**
- Affected: `config/samples/*.yaml`
- Problem: All sample specs contain scaffold TODOs and omit required fields.
- Required fix: Provide safe, realistic examples. Avoid including agent-owned discovery objects in the default sample kustomization unless their use is explicitly explained.
- Regression test: Server-side dry-run every sample against envtest in CI.
- Resolution:
  - All four samples now contain concrete, schema-valid fields and replacement guidance where cluster-specific hardware values are required.
  - Agent-owned `NVMeDevice` and `RDMAStorageNode` examples are explicitly marked as documentation-only and excluded from the default sample kustomization.
  - Controller envtest loads every sample into its typed API object and performs a server-side dry-run against the generated CRDs.
- Verification (2026-08-25): all four envtest dry-runs and the repository sample contracts passed in the consolidated test suite.

### 22. Add behavior-focused test coverage

- [x] **F22 — Medium — Resolved**
- Affected: all test packages
- Problem: Agent/plugins have 0% coverage; controller tests only assert that reconciliation returns no error; CSI server operations are mostly untested.
- Required fix:
  1. Introduce injectable command runners and fake plugin/RPC interfaces.
  2. Add the regression tests listed under every finding.
  3. Add multi-volume, multi-namespace, failure-injection, concurrency, recovery, and XFS E2E cases.
  4. Verify provider resource deletion through SPDK/configfs, not only CR disappearance.
- Acceptance criteria: Critical storage isolation and deletion behavior is exercised in required CI checks.
- Resolution:
  - Injectable command runners and fake plugin/RPC boundaries now cover agent discovery, provisioning, deletion, recovery, and backend failure paths without hardware.
  - Controller, CSI, contract, and E2E suites cover multi-volume and multi-namespace isolation, concurrent capacity decisions, command and RPC failures, restart recovery, XFS behavior, and exact kernel/SPDK resource deletion.
  - The consolidated host suite contains 143 behavior tests and reports 59.8% coverage for `internal/agent`, 58.7% for `internal/agent/plugins`, 71.4% for `internal/controller`, and 67.6% for `internal/csi`.
- Verification (2026-08-25): `make test-suite` passed the unit, envtest, repository-contract, Helm, documentation, lint, and E2E-compilation gates.

### 23. Restore lint CI

- [x] **F23 — Low — Resolved**
- Affected:
  - Go sources reported by `golangci-lint`
  - `.github/workflows/lint.yml`
- Problem: The latest 2026-08-17 lint run reports 19 findings, including a cyclomatic complexity of 70 in `PartitionManager.Reconcile`, 15 logging violations, 2 repeated constants, and 1 comment-spacing violation.
- Required fix:
  - Split provisioning/deletion/recovery into smaller functions.
  - Convert logging to the configured structured Kubernetes style.
  - Resolve repeated constant and comment-spacing findings.
- Resolution:
  - `PartitionManager.Reconcile` is split into focused discovery, deletion, identity, export, and provisioning helpers while preserving terminal versus retryable failure reasons.
  - Reported logging sites now use structured Kubernetes logging, repeated constants and loop-variable findings are removed, and CSI driver startup returns errors to `main` instead of terminating from a goroutine.
  - `make test-suite` now requires the lint gate.
- Verification (2026-08-25): the consolidated `make test-suite` run passed with `golangci-lint` reporting zero issues.

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
- Verification status (2026-08-25): controller, agent, target-backend, chart contract, and E2E compile regressions pass, including same-node retry, competing-node rejection, explicit takeover, stale unpublish, ACL ordering, and finalizer release. The focused isolated-lab run proved initial SPDK attach and concurrent-node rejection, then exposed an SPDK CLI compatibility defect: `nvmf_subsystem_remove_host` was passed an unsupported `--timeout-ms` method argument. The argument was removed and covered by the exact-command regression. The corrected image was built, but final two-node re-verification is still pending because the lab's worker-1 virtual root disk developed ext4 corruption and raw read errors during the host memory incident; image extraction now fails digest validation even after offline filesystem repair.

## Cross-cutting release gates

Before considering the implementation production-ready, require all of the following:

- [x] Full unit and envtest suite passes.
- [x] Race tests pass for controller, agent state logic, and CSI packages.
- [x] Lint and vet pass.
- [x] Generated CRDs, RBAC, DeepCopy code, and Helm CRDs have no drift.
- [ ] CSI conformance tests pass for the explicitly supported capability set.
- [x] Two-volume isolation tests pass for every enabled backend.
- [x] Same-name/different-namespace isolation tests pass.
- [ ] Single-writer attachment fencing prevents concurrent cross-node filesystem use.
- [x] Finalizer cleanup proves backend resources are gone.
- [x] Capacity concurrency tests prove no overcommit.
- [x] SPDK target crash and agent restart recovery tests pass.
- [ ] RDMA link/node failure produces actionable non-ready state rather than a false `Exported` result.
- [x] Helm RBAC allow/deny matrix passes.
- [x] E2E tests run only against an isolated disposable cluster.

## Original environmental verification limitations

- No Vagrant/Kind E2E environment or kubeconfig was available during the review.
- Docker image construction, physical NVMe operations, SPDK execution, and real RDMA transport were not exercised.
- The rendered Helm manifests passed Helm validation, but `kubectl --dry-run` was blocked by the locally installed snap confinement configuration.

These statements describe the original review session, not the current lab state. On 2026-08-17 the subsequent three-node Vagrant/K3s run built and deployed the image. After F1, F2, and F4 were resolved, the complete eight-spec green hardware E2E suite passed over virtual NVMe and SoftRoCE; four known-failure regressions remained quarantined. The focused F4 run also proved same-name/different-namespace SPDK isolation and deletion in both orders.
