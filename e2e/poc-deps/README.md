# PoC, dependencies between Komodo Stacks

A block is a `compose.yaml` that Komodo deploys as one Stack, with its own
services (`platform/observability` has Prometheus, Loki and Grafana). Compose
orders the services inside a block. Nothing orders two blocks, and `uses`
between applications needs that.

This PoC creates three blocks that depend on each other and tries to order
them with what Komodo already has, before anything is built in the CLI.

| Block | Services | Needs |
|---|---|---|
| `data` | `postgres` (slow to start, with a healthcheck), `cache` (Redis) | nothing |
| `api` | `migrate` (one-shot), `api` | `postgres` |
| `web` | `assets`, `web` | `api` and `cache` |

A chain of three levels with a diamond. Inside `api` and `web`, Compose orders
the services with `depends_on`. A service that finds its dependency missing
exits and restarts, as many applications do.

## Run it

It needs only podman, and takes about fifteen minutes the first time (it
pulls Komodo, PostgreSQL, Redis and busybox inside the machine).

```sh
e2e/poc-deps/run.sh --clean                       # Komodo 2.2.0, the version urbex pins
e2e/poc-deps/run.sh --clean --komodo-version 2.3.4 # the latest at the time of writing
e2e/poc-deps/run.sh --tests T5,T6 --reboot         # some tests, and a reboot after each
```

The machine is a privileged Debian container with systemd and Docker, like
the one the e2e uses for an LXC. Komodo Core and Periphery run in it as they
do on urbex's Komodo LXC. Credentials are generated in the machine and never
written in the repository. Use `--clean` when the Komodo version changes.

## The tests

| Test | What orders the blocks |
|---|---|
| T0 | Nothing. The three Stacks are deployed at once (the control). |
| T1 | A Procedure with a stage per block. |
| T2 | The same, with `--wait` in each Stack's `extra_args`. |
| T3 | `pre_deploy` of the consumer Stacks, waiting for TCP connections. |
| T4 | `compose_cmd_wrapper` of the consumer Stacks, the same wait. |
| T5 | A `wait` service inside each consumer block, ignored by Komodo with `ignore_services`. |
| T5b | The same without `ignore_services`, to see what the exited service does to the Stack's state. |
| T6 | A Procedure with an Action between the stages, which reads the container's health with `InspectStackContainer`. |

With `--reboot`, after each test the machine restarts, as a host reboot, and
the script watches how long the blocks take to be healthy and how many times
the consumers restart.

## What it measures

For each test, from a monitor that looks at the containers every second.

- Whether each consumer's containers started after what they need was healthy.
- How long the deploy took to return and how long everything took to be healthy.
- Restarts of `api` and `web`, and the lines that say a dependency was missing.
- What Komodo says about each Stack (`running`, `unhealthy`, `down`).
- What Periphery has (`bash`, `curl`, `nc`, `python3`, the Compose version).

## Results

`results/<komodo version>/summary.md` has one row per test. Each test also has
a `.md` with its timeline and the errors Komodo returned, and a `.json` with
everything. The `periphery-tools.txt` file lists what the Periphery image has.
Send `summary.md` and the `.md` of any failed test back.

## What it does not cover

- Several hosts. All the blocks run on one Docker, on a shared network, so
  the names resolve as the internal DNS names will. Two machines and two
  Peripheries are a second phase.
- Proxmox, Technitium and the GitOps repository.

## Known unknowns, to look at first when a test fails

- The field names of `pre_deploy` (`path`, `command`) and of
  `compose_cmd_wrapper` are from Komodo's documentation, not tried.
- The Action uses the `komodo` client Komodo gives to its scripts, and looks for
  `"healthy"` in the JSON that `InspectStackContainer` returns. The first
  iteration prints the JSON in the Action's log.
- `docker compose up -d --wait` with a one-shot service that exits with 0
  (`migrate`) may be reported as an error. T2 shows it.
