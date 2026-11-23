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

Feature and fix pull requests don't touch this file: what users see is
written here in one pull request when a release is prepared (see
`docs/development.md` in urbforge/redemptor), under the release's
version - the release workflow refuses a tag whose version has no entry
here.

## [Unreleased]

## [0.2.0] - 2026-11-20

The forge is Forgejo 15 LTS in place of Gitea: a platform created by
0.1.0 is started again rather than upgraded (see Breaking and
Upgrading), and `urbex bootstrap` refuses it until then. Also the
Java4Shell mitigation in Keycloak and the `runtime: java` images, alerts
to Telegram, Discord or a webhook, the build cache pruned on Komodo,
and `urbex updates`.

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

### Security

- **Java4Shell (CVE-2026-114938) mitigated** (urbforge/urbex-cli#130): Keycloak's JVM, and the images of `runtime: java`, start with `-XX:-UseBASE64Intrinsics`, which turns off the OpenJDK Base64 intrinsic that corrupts memory on x86-64 CPUs with AVX-512 VBMI. Keycloak decodes Base64 before login, and its realms are public with Cloudflare. Run `urbex bootstrap` to apply it to an existing platform, and the `runtime: java` services get it with their next build (a push to `main` for staging, then a release for production). A Keycloak image with a fixed OpenJDK will follow when one is published.

### Added

- **`urbex updates [<env>]`** (#119): for each application from a
  published image, whether a newer image is available - the tag asked
  for moved (`urbex deploy <env> --update`), a newer minor or patch
  (`urbex deploy <env> --app <app> --version <tag>`), a new major
  (listed, not proposed). The tags come from the registry, anonymously.
- **Komodo's build cache is pruned on a schedule**, so its disk stays
  bounded: every day the BuildKit cache older than 7 days, then beyond
  3 GB, and the images no container uses older than 7 days. Change it
  with `komodo.buildCache` in `urbex.platform.yaml`; each run logs what
  it freed (Grafana, Loki). It runs as the platform Stack
  `urbex-build-cache` on Komodo's LXC, created by `urbex bootstrap`.
- **Alerts to Telegram, Discord or a webhook** (urbforge/urbex-cli#95): `observability.alerts` in `urbex.platform.yaml` provisions a Grafana contact point for Urbex's alerts - one short message per alert and machine, its summary, severity, project, service and host, a link to Grafana, and a resolved message; repeated at most every 4 hours. The bot token and the webhooks' URLs are credentials, encrypted in the observability Stack. `urbex doctor` checks them, and `urbex doctor --test-alerts` sends a test message through each channel.

### Changed

- The forge is Forgejo 15 LTS, pinned by digest, and "forge" replaces
  "Gitea" where the name is generic: messages, `urbex status`, the
  credentials and the platform config (see Breaking and Upgrading). The
  LXC, Stack and volume keep their names (`urbex-gitea`, `gitea-data`)
  (#116).
- Releases carry their notes from this file, and fail without them
  (#118).
- **`urbex-op`'s default workspace is `~/urbex-work`**, as in the docs
  and the Claude Code plugin (it was `~/urbex-workspace`). A workspace
  in `~/urbex-workspace` is still found when `~/urbex-work` doesn't
  exist; `-w` and `URBEX_WORKSPACE` choose any other.

### Fixed

- `urbex-op` never pulls an image built locally (`localhost/...`, or
  docker's bare name): it says how to build it instead (#117).
- The allocation ledger frees the addresses of machines that are gone:
  `urbex apply` for an application removed from `urbex.yaml`, `urbex
  destroy` for every machine of the environment's state; `urbex teardown`
  forgets stale entries whose machine is no longer on Proxmox, and names
  those that still are (#120).
- `urbex bootstrap` refuses a platform created by urbex 0.1.0, whose forge
  is still Gitea: Forgejo can't open Gitea 1.24's database, and starting it
  there left the forge down. Nothing is changed; the message says how to
  start the platform again on Forgejo (see Upgrading).

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
