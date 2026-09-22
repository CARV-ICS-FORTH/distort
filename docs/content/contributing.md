---
title: "Contributing Guide"
description: "Build, generate, test, and contribute to DISTORT using the supported repository workflows."
type: "page"
---

DISTORT is a Go/Kubebuilder project with hardware-facing agent code and a CSI data path. Keep changes small, preserve idempotent reconciliation, and add a regression test for every behavior change. The [Testing Strategy](/testing/) defines the test layers; the [Local Testing Lab](/local-testing/) is the supported environment for destructive NVMe, SPDK, and RDMA checks.

## Prerequisites

- Go 1.25.3 or newer, matching `go.mod`.
- GNU Make and Git; Helm 3 and Hugo for chart and documentation checks.
- Docker and `kubectl` for image builds and cluster work.
- Kubebuilder/controller-generation tools installed through the Makefile when needed.
- Vagrant 2.3+ and VirtualBox 7+ only when running the local hardware lab.

## Issues and pull requests

Use [GitHub Issues](https://github.com/CARV-ICS-FORTH/distort/issues) for
questions, bug reports, and feature proposals. A useful bug report includes the
DISTORT version or commit, Kubernetes version, backend, reproduction steps,
expected and actual behavior, and relevant logs with credentials removed.
Discuss substantial design changes in an issue first.

Fork the repository, create a branch, and open a pull request describing the
problem, change, and validation performed. Link related issues and identify
checks you could not run. Documentation contributions can use the static checks
below; hardware changes need the appropriate isolated-lab coverage. Follow the
[Code of Conduct](https://github.com/CARV-ICS-FORTH/distort/blob/main/CODE_OF_CONDUCT.md).
Report vulnerabilities privately through the
[security policy](https://github.com/CARV-ICS-FORTH/distort/blob/main/SECURITY.md).

## Normal development workflow

From the repository root:

```bash
make build
make lint-fix
make test
```

Use `make run` only when you intentionally want the manager to use the current kubeconfig context. It does not reproduce node-local CSI, SPDK, or RDMA behavior.

After changing API types or Kubebuilder markers, regenerate the checked-in artifacts:

```bash
make manifests
make generate
make sync-chart-crds
```

Do not edit generated CRDs, RBAC, or `zz_generated.*.go` files by hand. Preserve all Kubebuilder scaffold markers.

For the complete host validation, module consistency check, and race suite:

```bash
make test-ci
```

For documentation and chart changes, run `make test-static`. It checks repository
contracts, CRD copies, Helm rendering, and the Hugo build. Preview the site with
`hugo server --source docs`; verify examples and links as well as the rendered
pages. A successful site build does not validate every command in a guide.

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
- Update [Using DISTORT](/using/), the architecture guide, and the testing guide
  when operator-visible behavior, limitations, or validation changes.
- Keep destructive storage tests inside the guarded Vagrant lab.
- Do not commit local artifacts such as `kubeconfig.yaml`, binaries, VM state, or generated report PDFs.

Maintainer contacts are listed in
[MAINTAINERS.md](https://github.com/CARV-ICS-FORTH/distort/blob/main/MAINTAINERS.md).
The [governance policy](https://github.com/CARV-ICS-FORTH/distort/blob/main/GOVERNANCE.md)
describes decision-making and maintainer membership.
