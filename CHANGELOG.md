# Changelog

What changed in each release of urbex-cli: the `urbex` CLI, the
`urbex-op` script and its image `ghcr.io/urbforge/urbex-op`, which are
released together. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), with **Breaking**
and **Upgrading** sections when a release needs them. While urbex is 0.x
a minor release may still change how something works: read Breaking and
Upgrading before you move to it.

The Claude Code plugin and the docs have their own changelog, in
[urbforge/urbex](https://github.com/urbforge/urbex/blob/main/CHANGELOG.md).
Every release also lists its commits, generated from their titles.

A pull request that changes what users see adds a line under
*Unreleased*; a release moves them under its version - the release
workflow refuses a tag whose version has no entry here.

## [Unreleased]

### Changed

- **`urbex-op`'s default workspace is `~/urbex-work`**, as in the docs
  and the Claude Code plugin (it was `~/urbex-workspace`). A workspace
  in `~/urbex-workspace` is still found when `~/urbex-work` doesn't
  exist; `-w` and `URBEX_WORKSPACE` choose any other.
### Added

- **Komodo's build cache is pruned on a schedule**, so its disk stays
  bounded: every day the BuildKit cache older than 7 days, then beyond
  3 GB, and the images no container uses older than 7 days. Change it
  with `komodo.buildCache` in `urbex.platform.yaml`; each run logs what
  it freed (Grafana, Loki). It runs as the platform Stack
  `urbex-build-cache` on Komodo's LXC, created by `urbex bootstrap`.
### Added

- **Alerts to Telegram, Discord or a webhook** (urbforge/urbex-cli#95): `observability.alerts` in `urbex.platform.yaml` provisions a Grafana contact point for Urbex's alerts - one short message per alert and machine, its summary, severity, project, service and host, a link to Grafana, and a resolved message; repeated at most every 4 hours. The bot token and the webhooks' URLs are credentials, encrypted in the observability Stack. `urbex doctor` checks them, and `urbex doctor --test-alerts` sends a test message through each channel.
### Security

- **Java4Shell (CVE-2026-114938) mitigated** (urbforge/urbex-cli#130): Keycloak's JVM, and the images of `runtime: java`, start with `-XX:-UseBASE64Intrinsics`, which turns off the OpenJDK Base64 intrinsic that corrupts memory on x86-64 CPUs with AVX-512 VBMI. Keycloak decodes Base64 before login, and its realms are public with Cloudflare. Run `urbex bootstrap` to apply it to an existing platform, and the `runtime: java` services get it with their next build (a push to `main` for staging, then a release for production). A Keycloak image with a fixed OpenJDK will follow when one is published.
### Added

- **The Proxmox node in Grafana** (#94): with an optional read-only token
  (`proxmoxMonitorToken`, role `PVEAuditor`), `urbex bootstrap` runs
  `pve-exporter` in the observability Stack. The *Urbex resources*
  dashboard then shows the node's CPU, memory (guests outside urbex and
  ZFS's ARC included, as Proxmox counts it) and each storage's use, and
  Grafana alerts when the node's memory stays above 90% or a storage
  passes 85% / 95%. Only the node and storage series are kept, never other
  guests'. `urbex doctor` checks the token, or says the node metrics are
  off without it.

## [0.1.0] - 2026-11-17

The first preview release: urbex as binaries for Linux and macOS (amd64,
arm64) and as `urbex-op` with its published image, instead of a build
from source.

### Added

- **The platform**, from an empty Proxmox node, with one command
  (`urbex bootstrap`): Gitea for repositories and images, Komodo for
  builds and deploys, Technitium for internal DNS, Keycloak for the
  applications' users, Grafana, Prometheus and Loki for logs, metrics and
  alerts, and a Cloudflare tunnel with Access in front of the admin
  tools - or a Quick Tunnel without a Cloudflare account. A prepared LXC
  template makes every new machine quick to create.
- **Projects** from an `urbex.yaml`: `urbex apply <env>` gives each
  service its own LXC (or VM, `machine: vm`), staging follows `main` on
  every push, `urbex release` and `urbex promote` put the very images
  staging ran in production, `urbex deploy` pins or rolls back a version.
- Applications from published images, pinned to their digest; named
  volumes and NAS shares; linked applications, which start once what
  they use is ready; a PostgreSQL per project, with a role and a schema
  per application.
- Configuration and SOPS-encrypted secrets per environment, in the GitOps
  repo (`urbex secret`).
- `urbex status` (also `--progress`, `--output json`), `urbex users`,
  `urbex doctor`, `urbex discover`, `urbex version`, and progress events
  (`--events json`) for programs and the Claude Code plugin.
- `urbex destroy` and `urbex teardown`.
- `urbex-op`: urbex and the tools it runs, in a container, against a
  workspace folder; podman or docker, Linux or macOS.

### Upgrading

The first release: nothing to upgrade from. A workspace built from a
checkout of urbex-cli works with it; see
[Upgrading](https://github.com/urbforge/urbex/blob/main/docs/upgrading.md).

### Known limitations

A preview: no backups of the platform yet, Proxmox only, a single
operator. See the
[known limitations](https://github.com/urbforge/urbex/blob/main/docs/known-limitations.md).

[Unreleased]: https://github.com/urbforge/urbex-cli/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/urbforge/urbex-cli/releases/tag/v0.1.0
