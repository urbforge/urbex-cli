// Komodo Action "urbex-reconcile" - managed by urbex bootstrap, which
// runs it every minute. See reconcile_logic.ts, which bootstrap puts in
// front of this file: what it decides and why it is safe to run it
// unattended; and urbforge/urbex-cli#43.
//
// Each run reads the environments' graphs (the URBEX_GRAPH_* variables
// urbex apply keeps), takes in the outcome of the deploys it started
// before, looks at every Stack's application container, starts the
// deploys the logic decides - without waiting for them: a run lasts
// seconds, so that the next, a minute later, doesn't overlap it (Komodo
// runs an Action again while it is running) - and keeps its state
// (backoffs, circuit breakers, deploys in flight, its heartbeat) in the
// URBEX_RECONCILE_STATE variable. Komodo refuses a deploy of a Stack that
// is being deployed, so nothing is deployed twice even if two runs
// overlap; the lease keeps them from both starting deploys. The run fails,
// so that Komodo shows it, and sends its alerts to Loki, when a circuit
// is open or a provider is in a crash loop.

const STATE_VARIABLE = "URBEX_RECONCILE_STATE";
// Every run sends a line to Loki, and an error line per alert: Grafana's
// alerts on them are the dead man's switch and the reconciler's
// (platform/stacks/observability/grafana/provisioning/alerting).
const LOKI_PUSH_URL = "[[URBEX_LOKI_PUSH_URL]]";
const LOKI_LABELS = { kind: "platform", service: "komodo", source: "urbex-reconcile" };
const GRAPH_PREFIX = "URBEX_GRAPH_";
// Every call to Komodo has a deadline: a hung call can't hold the run,
// whose lease (a little under the schedule) would expire under it.
const CALL_TIMEOUT_MS = 15_000;
const LEASE_MS = 55_000;

function deadline<T>(promise: Promise<T>, ms: number, what: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${what}: no answer in ${ms / 1000} s`)), ms);
  });
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

function message(err: unknown): string {
  if (err instanceof Error) return err.message;
  try {
    return JSON.stringify(err);
  } catch {
    return String(err);
  }
}

/** Sends the run's summary and its alerts to Loki; a failure is only logged. */
async function report(summary: string, alerts: string[]): Promise<void> {
  const ns = `${Date.now()}000000`;
  const streams = [{ stream: { ...LOKI_LABELS, level: "info" }, values: [[ns, summary]] }];
  if (alerts.length > 0) streams.push({ stream: { ...LOKI_LABELS, level: "error" }, values: alerts.map((a) => [ns, a]) });
  try {
    const res = await deadline(
      fetch(LOKI_PUSH_URL, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ streams }) }),
      CALL_TIMEOUT_MS,
      "sending to Loki",
    );
    if (!res.ok) console.log(`Loki answered ${res.status}: ${await res.text()}`);
  } catch (err) {
    console.log(`could not send to Loki (${message(err)})`);
  }
}

async function saveState(state: State, exists: boolean): Promise<void> {
  const value = JSON.stringify(state);
  const call = exists
    ? komodo.write("UpdateVariableValue", { name: STATE_VARIABLE, value })
    : komodo.write("CreateVariable", {
      name: STATE_VARIABLE,
      value,
      description: "Managed by urbex-reconcile: backoffs, circuit breakers, heartbeat",
      is_secret: false,
    });
  await deadline(call, CALL_TIMEOUT_MS, `saving ${STATE_VARIABLE}`);
}

const OPERATOR_STOPS = ["StopStack", "DestroyStack", "PauseStack", "StopStackService", "DestroyStackService"];

/** The application container of a Stack as the logic sees it; undefined if unknown. */
/** Whether the Stack's files in the repo differ from the ones it runs. */
async function pendingFiles(name: string): Promise<boolean> {
  const stack: any = await deadline(komodo.read("GetStack", { stack: name }), CALL_TIMEOUT_MS, `reading ${name}`);
  const files = (list: any[] | undefined) => JSON.stringify((list ?? []).map((f) => [f.path, f.contents]).sort());
  const remote = stack?.info?.remote_contents;
  return Array.isArray(remote) && remote.length > 0 && files(remote) !== files(stack?.info?.deployed_contents);
}

async function look(name: string, info: any, consumer: boolean): Promise<Observation | undefined> {
  // The application's container as Komodo lists it (a wait that gave up
  // leaves one "created"; one removed by hand, none): its state, and its
  // status for the health and the uptime. Not docker inspect, which
  // carries the container's environment, secrets included.
  let container: any;
  const at = Date.now();
  try {
    const services: any[] = await deadline(komodo.read("ListStackServices", { stack: name }), CALL_TIMEOUT_MS, `services of ${name}`);
    container = services.find((s) => s.service === "app")?.container;
  } catch (err) {
    console.log(`${name}: could not look at it (${message(err)}): left alone`);
    return undefined;
  }
  let busy = false;
  try {
    const actions = await deadline(komodo.read("GetStackActionState", { stack: name }), CALL_TIMEOUT_MS, `action state of ${name}`);
    busy = Object.values(actions ?? {}).some((v) => v === true);
  } catch (err) {
    console.log(`${name}: could not tell whether it is busy (${message(err)}): left alone`);
    return undefined;
  }
  const state: Observation["state"] = container?.state ?? "missing";
  let stopped = false;
  if (state !== "running") {
    // Only the Stacks that are down: an operator's stop is respected.
    try {
      const page: any = await deadline(
        komodo.read("ListUpdates", { query: { "target.type": "Stack", "target.id": info.id } }),
        CALL_TIMEOUT_MS,
        `updates of ${name}`,
      );
      // The latest first. The reconciler only deploys, and urbex destroy
      // removes the graph before the Stacks: a stop is an operator's.
      const last = (page?.updates ?? [])[0];
      stopped = OPERATOR_STOPS.includes(last?.operation);
    } catch (err) {
      console.log(`${name}: could not read its last operations (${message(err)}): left alone`);
      return undefined;
    }
  }
  // A version not deployed: only a running consumer's matters (one that
  // is down is deployed anyway), compared file by file - the repo's hash
  // moves with any commit.
  let pending = false;
  if (consumer && state === "running") {
    try {
      pending = await pendingFiles(name);
    } catch (err) {
      console.log(`${name}: could not compare its files (${message(err)}): left alone`);
      return undefined;
    }
  }
  return {
    state,
    container: container?.id ?? "",
    ...fromStatus(state === "running" ? container?.status : undefined, at),
    busy,
    pending,
    stopped_by_operator: stopped,
    server: info.server_id ?? "",
  };
}

async function readVariables(): Promise<{ graphs: Graph[]; state: State; kept: boolean }> {
  const vars: any[] = await deadline(komodo.read("ListVariables", {}), CALL_TIMEOUT_MS, "listing variables");
  const graphs: Graph[] = [];
  for (const v of vars.filter((v) => v.name.startsWith(GRAPH_PREFIX))) {
    try {
      graphs.push(JSON.parse(v.value));
    } catch (err) {
      console.log(`${v.name}: not a graph (${message(err)}): skipped`);
    }
  }
  const kept = vars.find((v) => v.name === STATE_VARIABLE);
  return { graphs, state: parseState(kept?.value), kept: kept !== undefined };
}

// The state is written only when it changed, or to refresh the heartbeat
// urbex status reads: every write is an update in Komodo's history.
const HEARTBEAT_REFRESH_MS = 4 * 60_000;

// A deploy in flight is kept as "since:<ms>", the moment it was started.
// Komodo's answer to execute is the update in progress without its id,
// and DeployStackIfChanged runs the DeployStack it decides on as an
// update of its own, later: the deploy is the Stack's first DeployStack
// update from then on. None after a while: nothing had changed.
const SINCE = "since:";
const NOTHING_DEPLOYED_AFTER_MS = 5 * 60_000;

/** Takes in the outcome of the deploys started by earlier runs. */
async function land(state: State, info: Record<string, any>, log: (line: string) => void): Promise<void> {
  for (const [name, s] of Object.entries(state.stacks)) {
    if (!s.in_flight.startsWith(SINCE)) {
      s.in_flight = ""; // an older form, or unreadable: nothing to wait for
      continue;
    }
    const since = Number(s.in_flight.slice(SINCE.length));
    try {
      const page: any = await deadline(
        komodo.read("ListUpdates", { query: { "target.type": "Stack", "target.id": info[name]?.id } }),
        CALL_TIMEOUT_MS,
        `the deploys of ${name}`,
      );
      // Newest first: the last one in the list started since is the first.
      const u = (page?.updates ?? []).filter((u: any) => u.operation === "DeployStack" && u.start_ts >= since).at(-1);
      if (!u) {
        if (Date.now() - since < NOTHING_DEPLOYED_AFTER_MS) continue;
        s.in_flight = "";
        log(`${name}: the deploy started earlier found nothing to change`);
        continue;
      }
      if (u.status !== "Complete") continue;
      recordOutcome(s, Boolean(u.success), Date.now());
      log(`${name}: the deploy started earlier ${u.success ? "succeeded" : "failed"}`);
    } catch (err) {
      console.log(`${name}: could not read its deploys (${message(err)}): asked again next run`);
    }
  }
}

/** Takes the lease for starting deploys; false if another run holds it. */
async function lease(state: State, owner: string): Promise<boolean> {
  const held = (s: State) => s.lease_until > Date.now() && s.lease_owner !== owner;
  const fresh = await readVariables();
  if (held(fresh.state)) return false;
  state.lease_owner = owner;
  state.lease_until = Date.now() + LEASE_MS;
  await saveState(state, fresh.kept);
  // Written without a compare-and-set: read it back, the last writer wins.
  return !held((await readVariables()).state);
}

async function main(): Promise<void> {
  const { graphs, state: kept, kept: exists } = await readVariables();
  const before = JSON.stringify(kept.stacks);
  const now = Date.now();
  const lines: string[] = [];
  const log = (line: string) => {
    console.log(line);
    lines.push(line);
  };
  const stacks: any[] = await deadline(komodo.read("ListStacks", {}), CALL_TIMEOUT_MS, "listing stacks");
  const info: Record<string, any> = {};
  for (const s of stacks) info[s.name] = { id: s.id, ...s.info };
  await land(kept, info, log);

  const seen: Record<string, Observation | undefined> = {};
  for (const g of graphs) {
    for (const [name, node] of Object.entries(g.stacks)) {
      seen[name] = info[name] ? await look(name, info[name], node.uses.length > 0) : undefined;
    }
  }
  const decision = decide(kept, graphs, seen, now);
  let state = decision.state;

  let started = 0;
  if (decision.deploys.length > 0 && !(await lease(state, crypto.randomUUID()))) {
    log("Another run holds the lease: nothing deployed this run.");
  } else {
    for (const d of decision.deploys) {
      const operation = d.if_changed ? "DeployStackIfChanged" : "DeployStack";
      try {
        const since = Date.now();
        await deadline(komodo.execute(operation, { stack: d.stack }), CALL_TIMEOUT_MS, `${operation} ${d.stack}`);
        state.stacks[d.stack].in_flight = `${SINCE}${since}`;
        started++;
        log(`${d.stack}: ${d.reason} - ${operation} started`);
      } catch (err) {
        recordOutcome(state.stacks[d.stack], false, Date.now());
        log(`${d.stack}: ${d.reason} - ${operation} failed to start (${message(err)})`);
      }
    }
  }
  for (const [name, why] of Object.entries(decision.held)) log(`${name}: ${why}`);
  if (lines.length === 0) log(`${graphs.length} environment(s) with links: nothing to do.`);

  const changed = started > 0 || JSON.stringify(state.stacks) !== before;
  if (changed || now - kept.heartbeat >= HEARTBEAT_REFRESH_MS || !exists) {
    state = { ...state, heartbeat: Date.now(), lease_until: 0, lease_owner: "" };
    await saveState(state, exists || decision.deploys.length > 0);
  }
  const alerts = [...new Set([
    ...decision.alerts.filter((a) => !a.includes("circuit open")),
    ...Object.entries(state.stacks).filter(([, s]) => s.breaker === "open").map(([n, s]) => `${n}: circuit open after ${s.failures} failed deploys`),
  ])];
  await report(lines.join("; "), alerts);
  if (alerts.length > 0) throw new Error(alerts.join("; "));
}

await main();
