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

### Breaking

- **The forge is Forgejo 15 LTS** in place of Gitea 1.24 (urbforge/urbex
  ADR-0034, #116). A platform installed with 0.1.0 runs Gitea 1.24, whose
  database Forgejo can't open, so it can't move to this release in place:
  `urbex bootstrap` sees that its forge is Gitea and refuses, changing
  nothing (#134).

### Upgrading

- From 0.1.0: start the platform again to get this release's platform,
  the Java4Shell mitigation included. Back up the applications' named
  volumes, `urbex destroy` every environment, `urbex teardown --yes`, then
  `urbex bootstrap` with this release, push the projects' repositories
  again and `urbex apply` their environments (urbforge/urbex
  `docs/upgrading.md`, *From 0.1.0 to 0.2.0*). Or keep 0.1.0 on that
  platform. The workspace needs no change: the credentials and the
  platform config written by 0.1.0 are still read (`giteaAdminPassword`,
  `giteaToken`, `git.giteaUrl`); the new names (`forgeAdminPassword`,
  `forgeToken`, `git.forgeUrl`) win when both are there.

### Added

- **`urbex updates [<env>]`** (#119): for each application from a
  published image, whether a newer image is available - the tag asked
  for moved (`urbex deploy <env> --update`), a newer minor or patch
  (`urbex deploy <env> --app <app> --version <tag>`), a new major
  (listed, not proposed). The tags come from the registry, anonymously.

### Changed

- The forge is Forgejo 15 LTS, pinned by digest, and "forge" replaces
  "Gitea" where the name is generic: messages, `urbex status`, the
  credentials and the platform config (see Breaking and Upgrading). The
  LXC, Stack and volume keep their names (`urbex-gitea`, `gitea-data`)
  (#116).
- Releases carry their notes from this file, and fail without them
  (#118).

### Fixed

- `urbex-op` never pulls an image built locally (`localhost/...`, or
  docker's bare name): it says how to build it instead (#117).
- The allocation ledger frees the addresses of machines that are gone:
  `urbex apply` for an application removed from `urbex.yaml`, `urbex
  destroy` for every machine of the environment's state; `urbex teardown`
  forgets stale entries whose machine is no longer on Proxmox, and names
  those that still are (#120).

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
