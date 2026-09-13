# urbex-cli

The `urbex` CLI — implementation repo. For architecture, ADRs, and the
manifest/platform spec, see [urbforge/urbex](https://github.com/urbforge/urbex).
For a real-world, step-by-step walkthrough (bootstrap → deploy → test →
promote → iterate → fleet status), see
[`docs/guide.md`](https://github.com/urbforge/urbex/blob/main/docs/guide.md)
there.

## Status

Scaffold stage - all 8 subcommands are implemented, but see the caveat
below on the Terraform/Ansible content itself:

- `urbex init` — scaffolds a starter `urbex.yaml` app manifest, or
  validates an existing one against the manifest schema.
- `urbex bootstrap` — the provisioning phase: scaffolds
  `urbex.platform.yaml` if missing, validates it, checks Proxmox for
  base-service hostnames that already exist and aren't in local Terraform
  state (aborting rather than risk adopting/duplicating them), allocates
  static IPs for the 5 base-service LXCs, materializes the embedded
  Terraform/Ansible assets into the GitOps repo, then runs `terraform
  init/apply` and `ansible-playbook` (skippable with `--dry-run`).
  **Pushing the resulting GitOps repo to Gitea is a later phase, not yet
  implemented** — bootstrap prints what to do next instead.
- `urbex plan <env>` / `urbex apply <env>` — the per-project provisioning
  phase: read `urbex.yaml`, allocate a VMID/IP for each declared
  service's LXC (one per service per environment, reusing the same
  static-allocation approach as bootstrap but in its own VMID/IP range so
  it never collides with the base services), materialize the generic
  per-service Terraform module into `terraform/projects/<project>/<env>/`
  in the GitOps repo, then run `terraform plan` (plan) or `terraform
  apply` followed by `ansible-playbook` (apply) to actually configure the
  LXC with Docker + a docker-compose skeleton. Requires
  `urbex.platform.yaml` to already exist (i.e. `urbex bootstrap` to have
  run at least once). **Building/pushing the service's actual container
  image, DNS registration, Keycloak realm/client provisioning, and
  Cloudflare Tunnel ingress are later phases, not yet implemented** — the
  compose file references a placeholder image and `apply` prints what to
  do next.
- `urbex deploy <env>` — redeploys services `urbex apply` already
  provisioned: re-renders the Ansible inventory from the current
  `urbex.yaml` and re-runs `ansible-playbook` against the existing LXCs
  (no Terraform, no new VMID/IP allocation - it errors out by name if any
  declared service hasn't been applied yet). Needs no Proxmox API token,
  only SSH access to the LXCs. **This runs Ansible directly and is not
  yet integrated with Komodo's GitOps-driven reconciliation** (see
  [ADR-0004](https://github.com/urbforge/urbex/blob/main/docs/decisions/0004-gitops-gitea-komodo.md)) —
  there's no push-to-deploy yet, only this direct command.
- `urbex status [env]` — with no argument, queries Proxmox for which of
  the 5 base-service LXCs currently exist. With an environment argument,
  reads `urbex.yaml` and reports, per service, whether `urbex
  plan`/`apply` has allocated it a VMID/IP and whether its LXC currently
  exists on Proxmox. **DNS records, TLS certificates, and the latest
  deployed release are not tracked yet.**
- `urbex promote` — creates and pushes an annotated Git tag (`vX.Y.Z`,
  auto-incrementing the patch version from existing tags unless
  `--version` is given) at HEAD of the app repo, the actual signal a
  production release happened per
  [ADR-0010](https://github.com/urbforge/urbex/blob/main/docs/decisions/0010-promotion-flow.md).
  Since Komodo isn't yet watching tags to redeploy prod automatically,
  promote then immediately calls the same `urbex deploy prod` mechanism
  itself (`--skip-deploy` to only create/push the tag). Requires prod to
  already be applied.
- `urbex destroy <env>` — runs `terraform destroy` for every service
  already applied to the given environment, then clears their entries
  from the allocation ledger. Services never applied are silently
  skipped; a no-op if none were applied. Only tears down project-service
  infrastructure, not the base/platform services from `urbex bootstrap`.

All eight subcommands (`init`, `bootstrap`, `plan`, `apply`, `deploy`,
`status`, `promote`, `destroy`) now have a real implementation - none are
stubs.

### Caveat: untested against real Proxmox

The embedded Terraform modules and Ansible playbooks
(`platform/terraform/`, `platform/ansible/`) were authored carefully but
**have not been run against a live Proxmox server or real
`terraform`/`ansible-playbook` binaries** (neither was available in the
environment this was built in). The Go orchestration logic itself is
covered by unit tests (fake Proxmox API via `httptest`, a fake command
runner) and was smoke-tested end-to-end in `--dry-run` mode against a
stub Proxmox API. Before running `urbex bootstrap` for real: review
`platform/terraform/base/*.tf` and `platform/ansible/roles/*/templates/*.j2`
yourself, run `terraform validate` / `ansible-playbook --syntax-check`,
and double-check the Komodo/Keycloak image tags flagged with `TODO`
comments against current upstream docs.

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

# from an app repo containing urbex.yaml:
export URBEX_GITOPS_REPO=/path/to/gitops-repo
go run ./cmd/urbex plan staging
go run ./cmd/urbex apply staging
go run ./cmd/urbex status            # base-service status
go run ./cmd/urbex status staging    # per-service status for this project+env
go run ./cmd/urbex deploy staging    # re-run Ansible after editing urbex.yaml

go run ./cmd/urbex apply prod                 # apply prod first
go run ./cmd/urbex promote                    # tags, pushes, and redeploys prod
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
internal/materialize/        writes embedded terraform/ansible assets into a GitOps repo
internal/runner/             CommandRunner abstraction (terraform/ansible-playbook execution)
internal/bootstrap/          urbex bootstrap orchestration (base services)
internal/provision/          urbex plan/apply/deploy/destroy orchestration (project services)
internal/status/             urbex status: Proxmox presence + allocation-ledger lookups
internal/gittag/             semver tag parsing/increment (pure, no I/O beyond listing tags)
internal/promote/            urbex promote: creates/pushes the Git tag
platform/terraform/          embedded Terraform modules (modules/lxc, base/, project/)
platform/ansible/            embedded Ansible playbooks/roles (base services + generic service role)
```

## Note on the vendored schema

`internal/manifest/schema/urbex.schema.json` is a copy of the schema
maintained in [urbforge/urbex](https://github.com/urbforge/urbex). It
must be kept in sync manually for now; see
[ADR-0016](https://github.com/urbforge/urbex/blob/main/docs/decisions/0016-cli-repo-split.md)
in the docs repo for the reasoning and the plan to automate this.
