# urbex-cli

The `urbex` CLI — implementation repo. For architecture, ADRs, and the
manifest/platform spec, see [urbforge/urbex](https://github.com/urbforge/urbex).

## Status

Scaffold stage. Implemented so far:

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

Every other subcommand (`plan`, `apply`, `deploy`, `promote`, `status`,
`destroy`) exists as a stub that points to the relevant ADR in
`urbforge/urbex` and fails with "not implemented yet".

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
internal/netalloc/           static IP allocation for base-service LXCs
internal/proxmoxapi/         minimal Proxmox API client (bootstrap's pre-flight check only)
internal/bootstrap/          urbex bootstrap orchestration (materialize, terraform, ansible)
platform/terraform/          embedded Terraform modules for base-service LXCs
platform/ansible/            embedded Ansible playbook/roles for base-service LXCs
```

## Note on the vendored schema

`internal/manifest/schema/urbex.schema.json` is a copy of the schema
maintained in [urbforge/urbex](https://github.com/urbforge/urbex). It
must be kept in sync manually for now; see
[ADR-0016](https://github.com/urbforge/urbex/blob/main/docs/decisions/0016-cli-repo-split.md)
in the docs repo for the reasoning and the plan to automate this.
