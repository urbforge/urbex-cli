# urbex-cli

The `urbex` CLI — implementation repo. For architecture, ADRs, and the
manifest/platform spec, see [urbforge/urbex](https://github.com/urbforge/urbex):
[getting started](https://github.com/urbforge/urbex/blob/main/docs/getting-started.md),
the [cookbook](https://github.com/urbforge/urbex/blob/main/docs/cookbook.md),
and the [reference](https://github.com/urbforge/urbex/blob/main/docs/reference/)
(every command and flag in
[CLI reference](https://github.com/urbforge/urbex/blob/main/docs/reference/cli.md)).

## Status

The model ([ADR-0020](https://github.com/urbforge/urbex/blob/main/docs/decisions/0020-trunk-releases-gitops-environments.md)):
projects are developed trunk-based on Gitea; every push to `main` is
built by Komodo and deployed to staging, which follows `main`
([ADR-0021](https://github.com/urbforge/urbex/blob/main/docs/decisions/0021-staging-follows-main.md));
a release is a tag (`vX.Y.Z`) that gives a commit's images the version,
and production runs releases. What each environment runs - version,
configuration, SOPS-encrypted secrets - is a folder of the GitOps repo,
which Komodo deploys whenever it changes.

Commands:

- `urbex init` — scaffolds a starter `urbex.yaml` app manifest, or
  validates an existing one against the manifest schema.
- `urbex bootstrap` — scaffolds `urbex.platform.yaml` if missing,
  validates it, checks Proxmox for base-service hostnames that already
  exist and aren't in local Terraform state (aborting rather than risk
  adopting/duplicating them), allocates static IPs for the 5
  base-service LXCs, materializes the embedded Terraform/Ansible assets
  into the GitOps repo, runs `terraform init/apply` and
  `ansible-playbook` (skippable with `--dry-run`), then **sets up the
  platform**: generates admin passwords and secrets into
  `~/.urbex/credentials.yaml`; creates a Gitea token, the org and its
  `gitops` repo; a Komodo API key, the onboarding key project LXCs join
  Komodo with, Komodo's Gitea git/registry accounts and `URBEX_*`
  variables; the Komodo Action that builds `main` and publishes release
  tags, and the Procedure (webhook + every 15 minutes) that deploys the
  GitOps repo; `.sops.yaml`; and pushes the
  GitOps repo to Gitea. Re-runs keep existing secrets and tokens.
- `urbex plan <env>` — reads `urbex.yaml`, allocates a VMID/IP for each
  declared service's LXC (one per service per environment, from a ledger
  shared by every project in the GitOps repo), materializes the
  per-service Terraform module into `terraform/projects/<project>/<env>/`,
  and runs `terraform plan`.
- `urbex apply <env>` — like `plan`, then `terraform apply`, then
  Docker, the Komodo Periphery agent and sops on each LXC (Ansible), the
  project's repo on Gitea with a Komodo Build per service and its
  webhook, and the environment's folder in the GitOps repo
  (`environments/<env>/<project>/<service>/`) with the Komodo Stack that
  deploys it. A new staging follows `main` and runs its head; a new prod
  runs nothing until promoted.
- `urbex release` — tags the next release (`vX.Y.Z`, or `--version`) on
  the commit staging runs (or `main`, or `--ref`) and waits for Komodo to
  publish its images - the ones `main` built, retagged. Pushing such a
  tag by hand does the same.
- `urbex deploy <env>` — sets what the environment runs in the GitOps
  repo: the latest release, `--version` (a release or a commit; also how
  to roll back, and it stops the environment following `main`), or
  `--follow-main`; pushes, and waits for Komodo to run it. No Terraform,
  no SSH.
- `urbex promote` — sets prod to the release staging runs: prod runs the
  very images staging ran.
- `urbex secret set|unset|list <env> <service> [KEY]` — manages a
  service's secrets in the GitOps repo, encrypted with SOPS for the
  platform's age key and decrypted only on the LXC at deploy time.
  Needs the `sops` binary.
- `urbex status [env]` — with no argument, which of the 5 base-service
  LXCs exist on Proxmox; with an environment, per service, its
  allocation, whether its LXC exists, the version the GitOps repo asks
  for, and what Komodo reports running.
- `urbex destroy <env>` — takes the environment's Stacks down, removes
  its folder from the GitOps repo, runs `terraform destroy`, clears the
  ledger entries. The project's Builds go with its last environment.
  Base services are out of scope.

**Not implemented yet:** DNS registration, Keycloak realm/client
provisioning, Cloudflare Tunnel ingress (services are reachable on their
LAN IPs), promotion by pull request. See
[`docs/status.md`](https://github.com/urbforge/urbex/blob/main/docs/status.md)
in the docs repo.

### What has been validated

[`e2e/run.sh`](e2e/README.md) runs the real binary through all of the
above - bootstrap, apply, continuous deployment to staging, release,
promote, secrets, a hand-edited configuration, pinning, rollback,
destroy, re-apply onto a new machine - against Debian 13 systemd machines reachable over SSH, with
only `terraform` and the Proxmox API stubbed. `terraform validate`
passes with the real `bpg/proxmox` provider, and the whole flow has been
run on a real Proxmox VE 9.2 node with a pool-scoped, non-root token.

## Build & test

```sh
go build -o urbex ./cmd/urbex
go test ./...
e2e/run.sh        # end-to-end, needs podman - see e2e/README.md
```

## Try it

```sh
go run ./cmd/urbex init          # scaffolds urbex.yaml in the current directory
# ... edit urbex.yaml to declare a frontend and/or services ...
go run ./cmd/urbex init          # validates it

go run ./cmd/urbex bootstrap --gitops-repo /path/to/gitops-repo
# scaffolds urbex.platform.yaml there; edit it, then run again
export URBEX_PROXMOX_TOKEN=... URBEX_AGE_KEY=...
go run ./cmd/urbex bootstrap --gitops-repo /path/to/gitops-repo --dry-run
go run ./cmd/urbex bootstrap --gitops-repo /path/to/gitops-repo   # provisions + sets up Gitea/Komodo

# from an app repo containing urbex.yaml:
export URBEX_GITOPS_REPO=/path/to/gitops-repo
go run ./cmd/urbex plan staging
go run ./cmd/urbex apply staging     # LXCs, builds, folder and stacks; staging runs main's head
git push gitea main                  # ...and every push to main is deployed to staging
go run ./cmd/urbex release           # tag what staging runs as the next release
go run ./cmd/urbex status            # base-service status
go run ./cmd/urbex status staging    # per service: wanted version against what Komodo runs
printf %s "$TOKEN" | go run ./cmd/urbex secret set staging api API_TOKEN

go run ./cmd/urbex apply prod                 # apply prod first
go run ./cmd/urbex promote                    # prod runs the release staging runs
go run ./cmd/urbex deploy prod --version 0.1.0 # roll back
go run ./cmd/urbex deploy staging --version 0.1.0  # pin staging; --follow-main resumes
go run ./cmd/urbex destroy staging            # tears down staging's LXCs
```

## Layout

```
cmd/urbex/                 main package, CLI entry point
internal/cli/               cobra command tree (one file per command)
internal/manifest/           urbex.yaml loading, validation, scaffolding
internal/manifest/schema/      vendored copy of the manifest JSON Schema
                                (source of truth: urbforge/urbex/schemas/urbex.schema.json)
internal/platform/           urbex.platform.yaml loading, validation, scaffolding
internal/credentials/        loads secrets from env vars / ~/.urbex/credentials.yaml
internal/netalloc/           static IP block allocation (base-service LXCs)
internal/allocation/         persistent per-hostname VMID/IP ledger (project-service LXCs)
internal/sizeparse/          parses "512Mi"/"2Gi" manifest resource quantities
internal/proxmoxapi/         minimal Proxmox API client (bootstrap's pre-flight check only)
internal/gitea/              minimal Gitea API client (tokens, org, repos, webhooks, tags, registry lookups)
internal/komodo/             minimal Komodo v2 API client (keys, accounts, servers, builds, stacks, actions, procedures)
internal/gitsync/            git commit/push helpers (GitOps repo, a project's main branch)
internal/rollout/            a project's Builds and an environment's Stacks on Komodo; releases; waiting for deploys
internal/envrepo/            the environments/ tree of the GitOps repo: compose files, versions, configuration
internal/secrets/            SOPS-encrypted secrets files (age recipient, sops invocation)
internal/semver/             release versions (vX.Y.Z): parse, latest, next
internal/materialize/        writes embedded terraform/ansible assets into a GitOps repo
internal/runner/             CommandRunner abstraction (terraform/ansible-playbook execution)
internal/bootstrap/          urbex bootstrap orchestration (base services + Gitea/Komodo setup)
internal/provision/          urbex plan/apply/deploy/destroy orchestration (project services)
internal/status/             urbex status: Proxmox presence + allocation-ledger lookups
platform/terraform/          embedded Terraform modules (modules/lxc, base/, project/)
platform/ansible/            embedded Ansible playbooks/roles (base services + Periphery for project LXCs)
platform/dockerfiles/        embedded Dockerfiles for the go/java/python runtimes
platform/komodo/             the Komodo Action that builds main, moves staging, and publishes release tags
e2e/                         end-to-end test of the real binary (podman)
```

## Note on the vendored schema

`internal/manifest/schema/urbex.schema.json` is a copy of the schema
maintained in [urbforge/urbex](https://github.com/urbforge/urbex). It
must be kept in sync manually for now; see
[ADR-0016](https://github.com/urbforge/urbex/blob/main/docs/decisions/0016-cli-repo-split.md)
in the docs repo for the reasoning and the plan to automate this.
