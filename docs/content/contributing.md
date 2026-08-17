---
title: "Contributing Guide"
description: "Build, generate, test, and contribute to DISTORT using the supported repository workflows."
type: "page"
---

DISTORT is a Go/Kubebuilder project with hardware-facing agent code and a CSI data path. Keep changes small, preserve idempotent reconciliation, and add a regression test for every behavior change. The [Testing Strategy](/testing/) defines the test layers; the [Local Testing Lab](/local-testing/) is the supported environment for destructive NVMe, SPDK, and RDMA checks.

## Prerequisites

- Go 1.25.3 or newer, matching `go.mod`.
- GNU Make, Git, Docker, Helm 3, and `kubectl`.
- Kubebuilder/controller-generation tools installed through the Makefile when needed.
- Vagrant 2.3+ and VirtualBox 7+ only when running the local hardware lab.

## Normal development workflow

From the repository root:

```bash
make build
make test
make lint-fix
```

Use `make run` only when you intentionally want the manager to use the current kubeconfig context. It does not reproduce node-local CSI, SPDK, or RDMA behavior.

After changing API types or Kubebuilder markers, regenerate the checked-in artifacts:

```bash
make manifests
make generate
```

Do not edit generated CRDs, RBAC, or `zz_generated.*.go` files by hand. Preserve all Kubebuilder scaffold markers.

For the complete host validation and the separate race suite:

```bash
make test-suite
make test-race
```

For storage-path work, reuse the persistent lab instead of installing components manually:

```bash
make test-env-create       # first use
make test-env-redeploy     # after code changes
make test-env-smoke
make test-e2e
```

The local-lab guide explains prerequisites, safe kubeconfig handling, focused tests, manual persistence checks, resets, and diagnostics.

## Plugin changes

Target backends and volume managers live under `internal/agent/plugins/` and implement the interfaces in `interface.go`. A new plugin should:

1. Implement the relevant interface and register itself through the package registry.
2. Validate every user-controlled option before invoking an external process.
3. Pass executable arguments directly rather than through a shell.
4. Make create/delete operations idempotent and persist exact external identities needed for cleanup.
5. Add command-failure and identity-symmetry unit tests, plus an isolated-lab test when host storage is involved.

Do not advertise an unfinished plugin in the CRD or Helm defaults. The repository currently rejects the unimplemented LVM manager.

## Contribution checklist

- Keep generated files in sync with their source markers and API types.
- Use structured Kubernetes-style log messages.
- Add or promote the relevant regression test into the green suite.
- Update [Using DISTORT](/using/) for operator-visible behavior and [Review Findings](/review-findings/) for resolved or newly discovered risks.
- Keep destructive storage tests inside the guarded Vagrant lab.
- Do not commit local artifacts such as `kubeconfig.yaml`, binaries, VM state, or generated report PDFs.

Project governance is kept in the conventional root files: `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md`, and `MAINTAINERS.md`.
