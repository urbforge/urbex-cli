# End-to-end test

`e2e/run.sh` runs the real `urbex` binary through a whole project
lifecycle and checks the result at every step. It needs only `podman`
(rootless is fine) and takes about five minutes.

```sh
e2e/run.sh          # run, then remove everything
e2e/run.sh --keep   # leave the machines running afterwards, to poke at them
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
Prometheus/Loki/Grafana, and the images Komodo builds and deploys.

The "operator's machine" is a container too (`e2e/runner`): it holds the
binary, the GitOps repo working copy, the app repo, and
`~/.urbex/credentials.yaml`, all under `e2e/.work/` on the host.

## What it covers

`run.sh` is the scenario; read it top to bottom. Each `step` runs in the
runner and fails the test on the first assertion that doesn't hold:

1. `urbex bootstrap` scaffolds the platform config; a second run
   provisions the five base services and sets up Gitea and Komodo; a
   third changes nothing.
2. `urbex apply staging` brings up the sample app (`e2e/app`) and
   `urbex status` reports it.
3. A pull request merged into `staging` is deployed without any command.
4. `urbex apply prod`, then `urbex promote --merge` moves staging's
   version to production.
5. `urbex deploy staging` applies a manifest change.
6. `urbex destroy staging` leaves production and its Komodo resources
   alone.
7. `urbex apply staging` on a brand-new machine: it rejoins Komodo.

## When a step fails

The output of every step is in `e2e/.work/logs/NN.log`; the last lines of
the failing one are printed. With `--keep` the machines stay up:

```sh
podman ps --filter name=e2e-
podman exec -it e2e-urbex-komodo bash         # a "base-service LXC"
podman exec e2e-hello-api-staging docker ps   # what runs on a "project LXC"
```

Gitea is at `http://10.89.77.200:3000` and Komodo at
`http://10.89.77.201:9120` from inside the test network (user
`urbex-admin`, passwords in `e2e/.work/home/.urbex/credentials.yaml`).

`E2E_NET` (default `10.89.77`) and `E2E_NETWORK` (default `urbex-e2e`)
change the /24 and the podman network name if they clash with yours.

## Limits

It does not prove the Terraform modules create LXCs on a real Proxmox
(pool placement, the `keyctl` feature, the Debian 13 template): that
needs a real node. `terraform validate` on `platform/terraform` checks
their syntax against the real provider.
