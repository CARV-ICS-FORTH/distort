# DISTORT

DISTORT (**DIS**aggregated **ST**orage **O**ver **R**DMA **T**ransport) is a
Kubernetes-native storage system that exports claimed NVMe capacity over
NVMe-over-Fabrics/RDMA and provisions it through CSI.

> [!WARNING]
> DISTORT is under active development. The SPDK and kernel data paths run in the
> isolated project testbed, but the documented production-readiness backlog is
> not complete. Review the [open findings](docs/content/review-findings.md)
> before evaluating it for production workloads.

## Components

- `distort-manager` binds device claims and schedules logical partitions.
- `distort-agent` discovers NVMe hardware, creates storage, and exports targets.
- `distort-csi` translates Kubernetes volume operations into DISTORT resources
  and mounts remote NVMe devices on consumer nodes.

## Quick start

Add the DISTORT Helm repository and install the BXI chart:

```bash
helm repo add distort https://distort-csi.dev/charts
helm repo update
helm install distort distort/distort-bxi \
  --namespace distort-system \
  --create-namespace
```

The chart deploys the public `docker.io/kampia99/distort:bxi-dev` image pinned
to its published manifest digest. To use another image, set `image.repository`
and `image.tag` and clear the default digest with `--set-string image.digest=`.

DISTORT never claims physical storage automatically. After installation, an
administrator must create an `NVMeDeviceClaim` for each device that DISTORT may
use. See [Using DISTORT](docs/content/using.md) for the complete workflow.

## Documentation

The Hugo site under [`docs/content`](docs/content/) is the canonical source for
detailed documentation:

- [Architecture](docs/content/architecture.md)
- [Project internals](docs/content/internals.md)
- [Installation and usage](docs/content/using.md)
- [Contributing](docs/content/contributing.md)
- [Testing strategy](docs/content/testing.md)
- [Local Vagrant testbed](docs/content/local-testing.md)
- [Review findings and production-readiness backlog](docs/content/review-findings.md)

The published documentation is available at
[distort-csi.dev](https://distort-csi.dev/).

## Development

```bash
make test-suite
make test-race
```

Package or update the static Helm repository served by the documentation site:

```bash
make package-chart-repository
```

Commit the resulting package and `index.yaml` under `docs/static/charts/`. The
GitHub Pages workflow publishes them at `https://distort-csi.dev/charts` after
those generated files are merged into `main` or `master`.

Hardware and full-stack changes are validated in the guarded, isolated
three-node Vagrant environment described in the
[local testing guide](docs/content/local-testing.md). Do not run its destructive
reset workflow against another cluster.

## Project policies

See [Contributing](CONTRIBUTING.md), [Security](SECURITY.md),
[Code of Conduct](CODE_OF_CONDUCT.md), [Maintainers](MAINTAINERS.md), and the
[Roadmap](ROADMAP.md).

## License

Licensed under the [Apache License 2.0](LICENSE).

## Acknowledgements

DISTORT has received funding from the EuroHPC Joint Undertaking through project
NET4EXA (GA-101175702), jointly funded by the European Commission and the
participating member states, including the Greek General Secretariat for
Research and Innovation.
