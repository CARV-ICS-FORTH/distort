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

DISTORT does not yet publish a public container image. Build and push version
`0.5.0` to a registry reachable by every cluster node, then install the chart
with that fully qualified repository:

```bash
export DISTORT_IMAGE_REPOSITORY=registry.example.com/your-project/distort
make docker-build docker-push IMG="${DISTORT_IMAGE_REPOSITORY}:0.5.0"
helm install distort ./deploy/charts/distort \
  --namespace distort-system \
  --create-namespace \
  --set-string image.repository="${DISTORT_IMAGE_REPOSITORY}"
```

The tag defaults to the chart application version, `0.5.0`. Production users
can instead set `image.digest=sha256:<digest>` to pin an immutable image. The
chart rejects an omitted or unqualified repository and rejects the `latest`
tag so a release install cannot silently select a local or mutable image.

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
