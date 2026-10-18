# End-to-end test

`e2e/run.sh` runs the real `urbex` binary through a whole project
lifecycle and checks the result at every step. It needs only `podman`
(rootless is fine) and takes about seven minutes.

```sh
e2e/run.sh          # run, then remove everything
e2e/run.sh --keep   # leave the machines running afterwards, to poke at them
E2E_STOP_AFTER=4 e2e/run.sh   # stop after step 4, machines running: a platform to experiment on
```

## What is real and what isn't

The only thing the test can't have is a Proxmox server, so it replaces
exactly what depends on one:

| | In the test |
|---|---|
| LXCs | Debian 13 systemd containers (`e2e/machine`), reachable over SSH as root, at the static IPs urbex allocates. Privileged, so Docker runs inside them, as `nesting` allows in an LXC. |
| `terraform` | A stub (`e2e/runner/terraform`) that logs its arguments and leaves a state file. `run.sh` creates the machines instead. |
| Proxmox API | A stub (`e2e/fakepve.py`) for the one call urbex makes itself: listing a node's containers. |

Everything else is the real thing: the `urbex` binary built from this
checkout, `ansible-playbook` over SSH with the embedded roles, Docker,
Gitea, Komodo (Core, Periphery, its database), Technitium, Keycloak,
Prometheus/Loki/Grafana, `sops` with a freshly generated age key, and
the images Komodo builds and deploys.

The "operator's machine" is a container too (`e2e/runner`): it holds the
binary, the GitOps repo working copy, the app repo, and
`~/.urbex/credentials.yaml`, all under `e2e/.work/` on the host.

## What it covers

`run.sh` is the scenario; read it top to bottom. Each `step` runs in the
runner and fails the test on the first assertion that doesn't hold:

1. `urbex bootstrap` scaffolds the platform config; a second run
   provisions the five base services and sets up Gitea and Komodo, mints
   a Technitium API token and creates its zone (`urbex.urbex.test`) with
   a name per platform service, resolved by Technitium along with
   public names (through its forwarders); a third changes nothing.
2. `urbex apply staging` for the sample app (`e2e/app`): the environment
   exists - folder in the GitOps repo, Build, Stack, internal DNS record
   in Technitium - follows `main`, and runs `main`'s head.
3. A push to `main` reaches staging with no command, through a commit
   to the GitOps repo.
4. Logs and resource metrics reach Loki and Prometheus: the service's,
   labelled `kind="app"`, `project`, `service`, `env` (only its own
   containers' logs), and the base services', labelled
   `kind="platform"`; Grafana has the data sources and the *Urbex logs*
   and *Urbex resources* dashboards.
5. Keycloak: the project has a realm per environment; staging has a test
   user per role (`urbex users staging`), and the sample app's `/member`
   lets `test-member` in and refuses no token or a forged one.
6. `urbex release` tags the commit staging runs and publishes its image
   without a new build; a tag pushed with plain git is a release too.
7. `urbex apply prod` starts empty, with its own internal DNS record;
   `urbex promote` gives it the release staging runs; an unreleased
   commit on staging is refused.
8. `urbex secret set`: the secret reaches the service, is nowhere in
   plaintext in the GitOps repo, and goes away with `unset`.
9. `config.env` edited by hand and pushed with plain git is deployed; an
   unrelated commit restarts nothing.
10. `urbex deploy staging --version` pins staging (a push to `main` no
   longer moves it); `--follow-main` resumes.
11. Rollback with `urbex deploy prod --version`; a release that doesn't
   exist is refused.
12. `urbex destroy staging` leaves production and the project's Builds
    alone, and removes its internal DNS record.
13. `urbex apply staging` on a brand-new machine: it rejoins Komodo,
    follows `main` again, and gets its internal DNS record at the new
    address.
14. A second stack, `site`, of a published image only and with production
    only (`environments: [prod]`): `apply` pins nginx's tag to its
    digest with no Build, `deploy --update` keeps an unchanged digest,
    `deploy --app web --version` moves to another tag; `apply staging`,
    `promote` and `release` are refused.

## When a step fails

The output of every step is in `e2e/.work/logs/NN.log`; the last lines of
the failing one are printed. With `--keep` the machines stay up:

```sh
podman ps --filter name=e2e-
podman exec -it e2e-urbex-komodo bash         # a "base-service LXC"
podman exec e2e-hello-api-staging docker ps   # what runs on a "project LXC"
ls e2e/.work/gitops/environments/             # the GitOps repo's working copy
```

Gitea is at `http://10.89.77.200:3000` and Komodo at
`http://10.89.77.201:9120` from inside the test network (user
`urbex-admin`, passwords in `e2e/.work/home/.urbex/credentials.yaml`).

`E2E_NET` (default `10.89.77`) and `E2E_NETWORK` (default `urbex-e2e`)
change the /24 and the podman network name if they clash with yours.

## Limits

It does not prove the Terraform modules create LXCs on a real Proxmox
(pool placement, the `keyctl` feature, the Debian 13 template): that
needs a real node, and was checked by hand - see `docs/status.md` in
urbforge/urbex. `terraform validate` on `platform/terraform` checks
their syntax against the real provider.
