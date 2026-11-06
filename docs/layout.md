# Layout of the code

Where things are in `urbex-cli`.

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
internal/proxmoxapi/         minimal Proxmox API client (container listing: pre-flight check, vanished LXCs; the LXC template; mount points: the applications' volumes)
internal/cloudflare/         minimal Cloudflare API client (tunnel, DNS, Access, Workers) and a fake for tests
internal/knownhosts/         the LXCs' SSH host keys in the GitOps repo (state/known_hosts)
internal/tfstate/            reconciles Terraform state with the LXCs that exist on Proxmox
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
internal/imageref/           resolves a public image's tag to its digest (anonymous registry calls)
internal/keycloak/           minimal Keycloak admin API client (realms, clients, roles of each project)
internal/technitium/         minimal Technitium API client (the internal zone and its A records)
internal/links/              linked applications: what each `provides`/`uses` yields, `${app.key}` references
internal/progress/           the record `bootstrap` and `apply` write and `urbex status --progress` reads
platform/terraform/          embedded Terraform modules (modules/lxc, base/, project/)
platform/ansible/            embedded Ansible playbooks/roles (base services + Periphery for project LXCs)
platform/dockerfiles/        embedded Dockerfiles for the go/java/python runtimes and web frontends
platform/stacks/             embedded compose files of the platform's services (Keycloak, observability, telemetry, tunnel)
platform/komodo/             the Komodo Actions urbex-release, which builds main, moves staging and publishes release tags, and urbex-reconcile, which brings linked applications back
e2e/                         end-to-end test of the real binary (podman)
tools/operator/              urbex-op: urbex and its tools in a container, run against a workspace
```
