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
  `~/.urbex/credentials.yaml`, creates Gitea tokens, the org and its
  `gitops` repo, a Komodo API key and Komodo's Gitea git/registry
  accounts, and pushes the GitOps repo to Gitea. Re-runs keep existing
  secrets and tokens.
- `urbex plan <env>` — reads `urbex.yaml`, allocates a VMID/IP for each
  declared service's LXC (one per service per environment, from a ledger
  shared by every project in the GitOps repo), materializes the
  per-service Terraform module into `terraform/projects/<project>/<env>/`,
  and runs `terraform plan`.
- `urbex apply <env>` — like `plan`, then `terraform apply`, then
  **builds and deploys the app repo's HEAD commit**: pushes it to the
  app's repo on Gitea, has Komodo build each service's image (the
  service's own Dockerfile, or Urbex's for go/java/python) and push it
  to Gitea's registry tagged with the commit, deploys it with Ansible,
  and commits/pushes the GitOps repo, whose inventory pins the deployed
  image. Prints each service's URL.
- `urbex deploy <env>` — the same build + deploy for LXCs `apply`
  already provisioned (no Terraform), picking up code and `urbex.yaml`
  changes. Images already built for a commit are reused.
- `urbex status [env]` — with no argument, which of the 5 base-service
  LXCs exist on Proxmox; with an environment, per service, whether it
  has an allocation and whether its LXC exists.
- `urbex promote` — creates and pushes an annotated Git tag (`vX.Y.Z`,
  auto-incrementing the patch version unless `--version` is given) to
  `origin` and to Gitea, then deploys that commit to prod, reusing the
  image built for staging (`--skip-deploy` to only tag).
- `urbex destroy <env>` — `terraform destroy` for every service applied
  to the environment, clears their ledger entries, and syncs the GitOps
  repo. Base services are out of scope.

**Not implemented yet:** DNS registration, Keycloak realm/client
provisioning, Cloudflare Tunnel ingress (services are reachable on their
LAN IPs), and Komodo-driven rollouts - Komodo builds the images, but the
rollout itself is Ansible run from the operator's machine. See
[`docs/status.md`](https://github.com/urbforge/urbex/blob/main/docs/status.md)
in the docs repo.

### What has been validated

Beyond the unit tests, the embedded assets and the Gitea/Komodo
integration have been exercised against real software: `terraform
validate` with the `bpg/proxmox` provider; the `common`, `gitea`,
`komodo`, and `service` Ansible roles on Debian 12 systemd containers
standing in for LXCs (idempotent on re-run); and the platform setup plus
Komodo builds against the resulting Gitea 1.24 and Komodo 2.2.0, ending
with a service pulled from Gitea's registry and reported healthy. **Not
yet run against a real Proxmox server** (`terraform apply`, Ansible over
SSH into real LXCs); the `technitium`, `keycloak`, and `observability`
roles haven't run either.

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
go run ./cmd/urbex apply staging     # LXCs + build + deploy of the committed HEAD
go run ./cmd/urbex status            # base-service status
go run ./cmd/urbex status staging    # per-service status for this project+env
go run ./cmd/urbex deploy staging    # build + redeploy after a new commit or urbex.yaml change

go run ./cmd/urbex apply prod                 # apply prod first
go run ./cmd/urbex promote                    # tags, pushes, and deploys the same image to prod
go run ./cmd/urbex promote --skip-deploy       # only tag/push
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
internal/gitea/              minimal Gitea API client (tokens, org, repos, registry lookups)
internal/komodo/             minimal Komodo v2 API client (API keys, accounts, builds)
internal/gitsync/            git commit/push helpers (GitOps repo, app repo to Gitea)
internal/imagebuild/         builds service images on Komodo from an app repo commit
internal/materialize/        writes embedded terraform/ansible assets into a GitOps repo
internal/runner/             CommandRunner abstraction (terraform/ansible-playbook execution)
internal/bootstrap/          urbex bootstrap orchestration (base services + Gitea/Komodo setup)
internal/provision/          urbex plan/apply/deploy/destroy orchestration (project services)
internal/status/             urbex status: Proxmox presence + allocation-ledger lookups
internal/gittag/             semver tag parsing/increment (pure, no I/O beyond listing tags)
internal/promote/            urbex promote: creates/pushes the Git tag
platform/terraform/          embedded Terraform modules (modules/lxc, base/, project/)
platform/ansible/            embedded Ansible playbooks/roles (base services + generic service role)
platform/dockerfiles/        embedded Dockerfiles for the go/java/python runtimes
```

## Note on the vendored schema

`internal/manifest/schema/urbex.schema.json` is a copy of the schema
maintained in [urbforge/urbex](https://github.com/urbforge/urbex). It
must be kept in sync manually for now; see
[ADR-0016](https://github.com/urbforge/urbex/blob/main/docs/decisions/0016-cli-repo-split.md)
in the docs repo for the reasoning and the plan to automate this.
