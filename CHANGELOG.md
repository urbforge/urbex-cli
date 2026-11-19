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

### Added

- **Alerts to Telegram, Discord or a webhook** (urbforge/urbex-cli#95): `observability.alerts` in `urbex.platform.yaml` provisions a Grafana contact point for Urbex's alerts - one short message per alert and machine, its summary, severity, project, service and host, a link to Grafana, and a resolved message; repeated at most every 4 hours. The bot token and the webhooks' URLs are credentials, encrypted in the observability Stack. `urbex doctor` checks them, and `urbex doctor --test-alerts` sends a test message through each channel.

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
