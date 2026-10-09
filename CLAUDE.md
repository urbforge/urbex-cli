# CLAUDE.md

`urbex-cli` is the `urbex` CLI, in Go. Design decisions (ADRs), user docs
and the manifest schema live in [`urbforge/urbex`](https://github.com/urbforge/urbex).

## How we work

The team's rules - issues, branches, Conventional Commits, pull requests,
definitions of ready and done - are in `docs/development.md` of
[`urbforge/redemptor`](https://github.com/urbforge/redemptor/blob/main/docs/development.md)
(formerly `product-management`). **Read it before starting any work**: it
is the source of truth and wins over anything in this file. With the
repositories cloned side by side it is `../redemptor/docs/development.md`
(`../product-management/...` in older clones); otherwise:

```sh
gh api repos/urbforge/redemptor/contents/docs/development.md -H "Accept: application/vnd.github.raw"
```

What it means for an agent, in practice:

- Work from an issue, in the repository where the work happens; check it
  is assigned and in the *Product Team* project before starting. Propose
  new issues to the person you work with rather than filing them unasked.
- Branch from an up-to-date `main` as `<type>/<short-description>`; commit
  messages and PR titles follow Conventional Commits.
- Write everything that lands on GitHub or in the repositories in
  English.
- Open the PR as a **draft**, with `Fixes urbforge/<repo>#<n>`, and this
  description:
  - `## Summary` - what changes, file by file when that helps;
  - `## Why` - the problem, or the ADR it implements;
  - `## Tests I ran` - what you ran and what it showed, including runs
    against a real Proxmox node; say what you did not run;
  - `## Checklist before review` - the guidelines' checklist, ticked only
    where true;
  - `Depends on ...` for a companion PR in another repository.
- Don't mark a PR ready, request reviews, merge, or push to `main` unless
  the person you work with asks.
- Never commit secrets, tokens, keys or private addresses; credentials
  live in `~/.urbex/credentials.yaml`.

## This repository

```sh
go build -o urbex ./cmd/urbex
go test ./...
go vet ./... && gofmt -l internal cmd   # none of the files you changed may be listed
e2e/run.sh                              # end to end, needs podman - see e2e/README.md
```

- Run `go test ./...` before every commit, and `e2e/run.sh` (about ten
  minutes) when a change touches the commands' behaviour, the platform
  assets (`platform/`: Terraform, Ansible, Dockerfiles) or the GitOps
  layout.
- Changes users can see need their docs in `urbforge/urbex` (reference,
  cookbook, `docs/status.md`, `docs/known-limitations.md`), in a
  companion PR; a design decision needs an ADR there first.
- `internal/manifest/schema/urbex.schema.json` is a copy of
  `urbex/schemas/urbex.schema.json`: change the schema there, then copy
  it here.
- Every service - the platform's, an agent, a project's - follows the
  conventions of ADR-0027 (`urbforge/urbex`): deployed and monitored by
  Komodo, an internal DNS name and references only by name, logs and
  resources in Grafana, public / behind Access / internal exposure, one
  kind (`platform`, `app`, `agent`) on Komodo and Grafana. A new or
  changed service starts from its entry in the service catalog,
  `internal/platform/catalog.go`; its containers carry the labels
  `urbex.kind` and `urbex.service`. `TestCatalog_*` fails otherwise.
- `tools/operator/urbex-op` runs the CLI from a container against a
  workspace with the credentials - the way to test on a real Proxmox
  node. Every LXC, VM and resource urbex creates on Proxmox must be in
  the platform's pool.
- The layout of the code is in the README.
