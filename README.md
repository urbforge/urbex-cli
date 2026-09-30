# urbex-cli

The `urbex` CLI — implementation repo. For architecture, ADRs, and the
manifest/platform spec, see [urbforge/urbex](https://github.com/urbforge/urbex).
For a real-world, step-by-step walkthrough (bootstrap → deploy → test →
promote → iterate → fleet status), see
[`docs/guide.md`](https://github.com/urbforge/urbex/blob/main/docs/guide.md)
there.

## Status

All 8 subcommands are implemented:

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
  `~/.urbex/credentials.yaml`, creates a Gitea token, the org and its
  `gitops` repo, a Komodo API key, the onboarding key project LXCs join
  Komodo with, and Komodo's Gitea git/registry accounts, and pushes the
  GitOps repo to Gitea. Re-runs keep existing secrets and tokens.
- `urbex plan <env>` — reads `urbex.yaml`, allocates a VMID/IP for each
  declared service's LXC (one per service per environment, from a ledger
  shared by every project in the GitOps repo), materializes the
  per-service Terraform module into `terraform/projects/<project>/<env>/`,
  and runs `terraform plan`.
- `urbex apply <env>` — like `plan`, then `terraform apply`, then
  installs Docker and the Komodo Periphery agent on each LXC (Ansible)
  and **hands the environment over to Komodo**: per service a Build
  (from the app's repo on Gitea; the service's own Dockerfile, or
  Urbex's for go/java/python) and a Deployment, a Procedure running
  them, and a Gitea webhook triggering it on every push to the
  environment's branch (`staging`, or `main` for prod). Runs the
  Procedure once and prints each service's URL. From then on, merging a
  pull request into the branch deploys the environment with no command
  at all.
- `urbex deploy <env>` — re-syncs the environment's Komodo resources
  from `urbex.yaml` and has Komodo build and deploy its branch now: the
  manual trigger, and how manifest changes (env vars, port, healthcheck)
  take effect. No Terraform, no SSH.
- `urbex status [env]` — with no argument, which of the 5 base-service
  LXCs exist on Proxmox; with an environment, per service, whether it
  has an allocation, whether its LXC exists, and the container state and
  image Komodo reports.
- `urbex promote` — opens (or finds) the `staging` → `main` pull request
  on Gitea; `--merge` merges it, which deploys production.
- `urbex destroy <env>` — removes the environment's webhook and Komodo
  resources, runs `terraform destroy`, clears the ledger entries, and
  syncs the GitOps repo. Base services are out of scope.

**Not implemented yet:** DNS registration, Keycloak realm/client
provisioning, Cloudflare Tunnel ingress (services are reachable on their
LAN IPs), and applying `urbex.yaml` changes on merge. See
[`docs/status.md`](https://github.com/urbforge/urbex/blob/main/docs/status.md)
in the docs repo.

### What has been validated

Beyond the unit tests, the real `urbex` binary has run end to end
against Debian 13 systemd machines reachable over SSH at the static IPs
it allocates, with only `terraform` and the Proxmox API stubbed:
`bootstrap` (all five base-service roles, idempotent on re-run), `apply`
for staging and prod, a pull request merged into `staging` going live
about 10 seconds later on its own, `promote --merge` doing the same for
prod, `deploy` after a manifest change, `status`, `destroy`, and
re-applying onto a recreated machine. `terraform validate` passes with
the real `bpg/proxmox` provider. **Not yet run against a real Proxmox
server** (`terraform apply` creating the LXCs).

## Build & test

```sh
go build -o urbex ./cmd/urbex
go test ./...
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
go run ./cmd/urbex apply staging     # LXCs + Komodo pipeline + first deploy; merges into staging deploy from now on
go run ./cmd/urbex status            # base-service status
go run ./cmd/urbex status staging    # per-service status, including what Komodo is running
go run ./cmd/urbex deploy staging    # re-sync from urbex.yaml and redeploy now

go run ./cmd/urbex apply prod                 # apply prod first (tracks branch main)
go run ./cmd/urbex promote                    # opens the staging -> main pull request
go run ./cmd/urbex promote --merge            # ...and merges it, deploying prod
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
internal/gitea/              minimal Gitea API client (tokens, org, repos, webhooks, pull requests)
internal/komodo/             minimal Komodo v2 API client (keys, accounts, servers, builds, deployments, procedures)
internal/gitsync/            git commit/push helpers (GitOps repo, environment branches)
internal/rollout/            declares and runs an environment's build+deploy pipeline on Komodo
internal/materialize/        writes embedded terraform/ansible assets into a GitOps repo
internal/runner/             CommandRunner abstraction (terraform/ansible-playbook execution)
internal/bootstrap/          urbex bootstrap orchestration (base services + Gitea/Komodo setup)
internal/provision/          urbex plan/apply/deploy/destroy orchestration (project services)
internal/status/             urbex status: Proxmox presence + allocation-ledger lookups
platform/terraform/          embedded Terraform modules (modules/lxc, base/, project/)
platform/ansible/            embedded Ansible playbooks/roles (base services + Periphery for project LXCs)
platform/dockerfiles/        embedded Dockerfiles for the go/java/python runtimes
```

## Note on the vendored schema

`internal/manifest/schema/urbex.schema.json` is a copy of the schema
maintained in [urbforge/urbex](https://github.com/urbforge/urbex). It
must be kept in sync manually for now; see
[ADR-0016](https://github.com/urbforge/urbex/blob/main/docs/decisions/0016-cli-repo-split.md)
in the docs repo for the reasoning and the plan to automate this.
