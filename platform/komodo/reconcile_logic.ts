// The decisions of the Komodo Action "urbex-reconcile" (reconcile.ts),
// kept apart from Komodo so that they can be tested (`deno test
// platform/komodo`). urbex bootstrap puts this file, without its
// `export`s, in front of reconcile.ts as the Action's code.
//
// The Action converges the environments whose applications use each
// other (ADR-0028, urbforge/urbex-cli#43): a consumer that is down or
// unhealthy, or that has a version it couldn't deploy, is deployed again
// once what it uses is ready - after a wait that gave up, a provider that
// came back late, a reboot. What keeps it from making things worse:
//
//   - hysteresis: a Stack is ready after `ready_runs` runs seen ready,
//     and needs a deploy after `unready_runs` runs seen down, so that one
//     odd look acts on nothing;
//   - a crash loop detector: a provider whose container restarted since
//     the last run is unstable, not ready, until it stays put for
//     `ready_runs` runs; its consumers aren't deployed meanwhile. Only a
//     provider seen restarting again within `loop_window_ms` is in a
//     crash loop, and alerts: one restart, a reboot, is no alarm. A
//     restart is told from the uptime Docker reports in the container's
//     status, as ListStackServices lists it: the container's environment,
//     which docker inspect would show with its secrets, is never read;
//   - exponential backoff with jitter between the deploys of a Stack
//     that fail;
//   - a circuit breaker per Stack: after `failure_threshold` failed
//     deploys in a row it opens and nothing is tried for `cooldown_ms`;
//     then one deploy is tried (half open) and closes it, or opens it
//     again;
//   - a retry budget per run, and a bulkhead per server: a reboot doesn't
//     turn into a storm of deploys;
//   - fail safe: a Stack it couldn't observe (its server unreachable
//     included: a node that reboots), a Stack being deployed,
//     one an operator stopped, or one in a crash loop of its own is left
//     alone. It only ever deploys: it never stops anything.

/** What `urbex apply` keeps of an environment (rollout.Graph). */
export interface Graph {
  project: string;
  env: string;
  stacks: Record<string, { uses: string[]; ready_within: number; wait: number }>;
}

/** What a run sees of a Stack's application container. */
export interface Observation {
  state: "running" | "created" | "exited" | "restarting" | "paused" | "dead" | "missing";
  /** "none" when the container has no healthcheck. */
  health: "healthy" | "unhealthy" | "starting" | "none";
  /** The container's id, "" when there is none. */
  container: string;
  /** When a running container started, as far as its status tells (ms): no
   *  earlier than started_after, no later than started_before; 0 when not
   *  running or unknown. */
  started_after: number;
  started_before: number;
  /** A Komodo operation on the Stack is in progress. */
  busy: boolean;
  /** The GitOps repo has a version of the Stack's files it hasn't deployed. */
  pending: boolean;
  /** The last operation on the Stack was an operator's stop or destroy. */
  stopped_by_operator: boolean;
  server: string;
}

export interface StackState {
  failures: number;
  breaker: "closed" | "open" | "half_open";
  opened_at: number;
  next_attempt_at: number;
  /** The container seen running at the last run, "" if none. */
  container: string;
  /** The latest it can have started (ms), 0 if not known. */
  started_before: number;
  ready_runs: number;
  unready_runs: number;
  unstable: boolean;
  stable_runs: number;
  /** When a run last saw it restart (ms), 0 if never. */
  last_restart_at: number;
  /** Seen restarting at the last run: its next start is that same restart. */
  restarting: boolean;
  /** Restarted again within loop_window_ms: a crash loop, which alerts. */
  looping: boolean;
  /** The Komodo update of a deploy started and not yet seen finished, "" if none. */
  in_flight: string;
}

export interface State {
  /** When the last run finished (ms): the dead man's switch reads it. */
  heartbeat: number;
  /** A run holds the state until then (ms); lease_owner is which. */
  lease_until: number;
  lease_owner: string;
  stacks: Record<string, StackState>;
}

export interface Config {
  failure_threshold: number;
  cooldown_ms: number;
  backoff_base_ms: number;
  backoff_max_ms: number;
  max_deploys_per_run: number;
  max_deploys_per_server: number;
  ready_runs: number;
  unready_runs: number;
  loop_window_ms: number;
}

export const DEFAULTS: Config = {
  failure_threshold: 5,
  cooldown_ms: 10 * 60_000,
  backoff_base_ms: 60_000,
  backoff_max_ms: 15 * 60_000,
  max_deploys_per_run: 5,
  max_deploys_per_server: 1,
  ready_runs: 2,
  unready_runs: 2,
  loop_window_ms: 10 * 60_000,
};

export interface Deploy {
  stack: string;
  /** Deploy only if the repo changed (DeployStackIfChanged). */
  if_changed: boolean;
  reason: string;
}

export interface Decision {
  deploys: Deploy[];
  /** Why each Stack that needs something gets nothing this run. */
  held: Record<string, string>;
  /** What an operator should know: the run fails with them. */
  alerts: string[];
  state: State;
}

export function emptyState(): State {
  return { heartbeat: 0, lease_until: 0, lease_owner: "", stacks: {} };
}

export function freshStack(): StackState {
  return {
    failures: 0, breaker: "closed", opened_at: 0, next_attempt_at: 0,
    container: "", started_before: 0, ready_runs: 0, unready_runs: 0, unstable: false, stable_runs: 0,
    last_restart_at: 0, restarting: false, looping: false, in_flight: "",
  };
}

/** The Komodo variable of an environment's graph (bootstrap.GraphVariable). */
export function graphVariable(project: string, env: string): string {
  return "URBEX_GRAPH_" + `${project}_${env}`.replaceAll("-", "_").toUpperCase();
}

/**
 * The graph variables of the environments that have Stacks, read by name
 * so that no call lists every variable, secret ones included
 * (urbforge/urbex-cli#55). urbex apply names a Stack
 * <project>-<application>-<env> and tags it with the project and the
 * environment: the pair of its tags that frames its name is the
 * environment. An environment without links has no variable.
 */
export function graphVariables(stacks: { name: string; tags: string[] }[], tagNames: Record<string, string>): string[] {
  const names = new Set<string>();
  for (const s of stacks) {
    const tags = s.tags.map((id) => tagNames[id]).filter((t): t is string => Boolean(t));
    for (const project of tags) {
      for (const env of tags) {
        if (project !== env && s.name.length > project.length + env.length + 2 &&
          s.name.startsWith(`${project}-`) && s.name.endsWith(`-${env}`)) {
          names.add(graphVariable(project, env));
        }
      }
    }
  }
  return [...names].sort();
}

/** The state as kept in the Komodo variable; anything unreadable starts over. */
export function parseState(text: string | undefined): State {
  try {
    const s = JSON.parse(text ?? "");
    if (s && typeof s === "object" && s.stacks && typeof s.stacks === "object") {
      return {
        heartbeat: Number(s.heartbeat) || 0,
        lease_until: Number(s.lease_until) || 0,
        lease_owner: String(s.lease_owner ?? ""),
        stacks: s.stacks,
      };
    }
  } catch { /* starts over */ }
  return emptyState();
}

// How old Komodo's view of the containers can be: it polls them every 15 s.
const STATUS_STALE_MS = 30_000;

// The units of Docker's human durations ("Up 3 minutes", go-units'
// HumanDuration), in seconds; hours and above are rounded, not truncated.
const UNITS: Record<string, number> = {
  second: 1, minute: 60, hour: 3600, day: 86400, week: 7 * 86400, month: 30 * 86400, year: 365 * 86400,
};

/** The bounds of an uptime as Docker writes it, in seconds; undefined if unreadable. */
export function uptime(text: string): [number, number] | undefined {
  if (text === "Less than a second") return [0, 1];
  const about = text.match(/^About an? (minute|hour)$/);
  if (about) return about[1] === "minute" ? [60, 120] : [3600, 5400];
  const m = text.match(/^(\d+) (second|minute|hour|day|week|month|year)s?$/);
  if (!m) return undefined;
  const n = Number(m[1]), unit = UNITS[m[2]];
  if (unit < 3600) return [n * unit, (n + 1) * unit];
  // Rounded to the nearest hour, then truncated to the unit.
  return [Math.max(0, n * unit - 1800), (n + 1) * unit + 1800];
}

/** What a container's status (e.g. "Up 13 hours (healthy)") tells, seen at `now`. */
export function fromStatus(status: string | undefined, now: number): Pick<Observation, "health" | "started_after" | "started_before"> {
  const m = (status ?? "").match(/^Up (.+?)(?: \((.+)\))?$/);
  const health = m?.[2] === "healthy" ? "healthy" : m?.[2] === "unhealthy" ? "unhealthy" : m?.[2] === "health: starting" ? "starting" : "none";
  const up = m ? uptime(m[1]) : undefined;
  if (!up) return { health, started_after: 0, started_before: 0 };
  // Komodo's view may be older than now: a start can only be earlier.
  return { health, started_after: now - STATUS_STALE_MS - up[1] * 1000, started_before: now - up[0] * 1000 };
}

/** Whether the container restarted since the last run: the same one, started after it was last seen running. */
function restarted(s: StackState, o: Observation): boolean {
  if (o.state === "restarting") return true;
  return o.container !== "" && o.container === s.container && s.started_before > 0 && o.started_after > s.started_before;
}

function healthy(o: Observation): boolean {
  return o.state === "running" && (o.health === "healthy" || o.health === "none");
}

/** Takes this run's observations into the state: hysteresis, crash loops. */
export function observe(prev: State, graphs: Graph[], seen: Record<string, Observation | undefined>, now: number, cfg: Config = DEFAULTS): { state: State; alerts: string[] } {
  const state: State = { ...prev, stacks: {} };
  const alerts: string[] = [];
  for (const g of graphs) {
    for (const [name, node] of Object.entries(g.stacks)) {
      const { restarts: _, ...kept } = (prev.stacks[name] ?? {}) as StackState & { restarts?: number };
      const s: StackState = { ...freshStack(), ...kept };
      const o = seen[name];
      state.stacks[name] = s;
      if (!o) continue; // not observed: nothing learnt
      // A new container (a deploy) is not a restart, nor one that was down
      // at the last run. The counters stop at what they are compared with: a
      // state that doesn't change isn't written again (reconcile.ts).
      const again = restarted(s, o);
      // One restart seen as "restarting", then running again, is one restart.
      if (again && !(s.restarting && o.state === "running")) {
        if (s.last_restart_at > 0 && now - s.last_restart_at <= cfg.loop_window_ms) s.looping = true;
        s.last_restart_at = now;
      }
      s.restarting = o.state === "restarting";
      if (again) {
        s.unstable = true;
        s.stable_runs = 0;
      } else if (s.unstable) {
        s.stable_runs = Math.min(s.stable_runs + 1, cfg.ready_runs);
        if (s.stable_runs >= cfg.ready_runs) s.unstable = false;
      }
      if (!s.unstable) Object.assign(s, { stable_runs: 0, looping: false });
      if (o.state === "restarting" && o.container !== "") {
        // It starts again after now: any start seen later is a restart.
        Object.assign(s, { container: o.container, started_before: now });
      } else if (o.state === "running" && o.started_before > 0) {
        const same = o.container === s.container && s.started_before > 0 && !again;
        Object.assign(s, { container: o.container, started_before: same ? Math.min(s.started_before, o.started_before) : o.started_before });
      } else {
        Object.assign(s, { container: "", started_before: 0 });
      }
      if (s.looping) alerts.push(`${name} is restarting (crash loop): what uses it waits`);
      // Still within its ready_within, its healthcheck not yet passed: not
      // ready, and not down either - a deploy now would start it over.
      const settling = o.state === "running" && o.health === "starting" && o.started_before > 0 &&
        now - o.started_before < node.ready_within * 1000;
      if (healthy(o) && !s.unstable) {
        s.ready_runs = Math.min(s.ready_runs + 1, cfg.ready_runs);
        s.unready_runs = 0;
      } else if (settling && !s.unstable) {
        s.ready_runs = 0;
        s.unready_runs = 0;
      } else {
        s.unready_runs = Math.min(s.unready_runs + 1, cfg.unready_runs);
        s.ready_runs = 0;
      }
    }
  }
  return { state, alerts };
}

/** What to deploy this run, consumers in the order of the graph. */
export function decide(prev: State, graphs: Graph[], seen: Record<string, Observation | undefined>, now: number, cfg: Config = DEFAULTS): Decision {
  const { state, alerts } = observe(prev, graphs, seen, now, cfg);
  const deploys: Deploy[] = [];
  const held: Record<string, string> = {};
  const perServer: Record<string, number> = {};
  const consumers = graphs.flatMap((g) =>
    Object.entries(g.stacks).filter(([, s]) => s.uses.length > 0).map(([name, s]) => ({ name, s }))
  ).sort((a, b) => a.s.wait - b.s.wait || a.name.localeCompare(b.name));

  for (const { name, s: node } of consumers) {
    const o = seen[name];
    const s = state.stacks[name];
    if (!o) {
      held[name] = "not observed";
      continue;
    }
    const down = s.unready_runs >= cfg.unready_runs;
    const update = o.pending && o.state === "running";
    if (!down && !update) {
      // Back by itself (Docker restarted it): its failed deploys count no more.
      if ((s.breaker !== "closed" || s.failures > 0) && healthy(o)) Object.assign(s, { breaker: "closed", failures: 0, next_attempt_at: 0 });
      continue;
    }
    if (o.busy || s.in_flight) {
      held[name] = "a deploy is in progress";
      continue;
    }
    if (o.stopped_by_operator) {
      held[name] = "stopped by an operator";
      continue;
    }
    if (s.unstable) {
      // Docker restarts it already; a deploy wouldn't help.
      held[name] = s.looping ? "restarting by itself (crash loop)" : "restarted by itself: left alone until it stays up";
      continue;
    }
    const waiting = node.uses.filter((p) => (state.stacks[p]?.ready_runs ?? 0) < cfg.ready_runs);
    if (waiting.length > 0) {
      held[name] = `waiting for ${waiting.join(", ")}`;
      continue;
    }
    if (s.breaker === "open") {
      if (now < s.opened_at + cfg.cooldown_ms) {
        held[name] = `circuit open until ${new Date(s.opened_at + cfg.cooldown_ms).toISOString()}`;
        continue;
      }
      s.breaker = "half_open";
    }
    if (s.breaker === "closed" && now < s.next_attempt_at) {
      held[name] = `backing off until ${new Date(s.next_attempt_at).toISOString()}`;
      continue;
    }
    if (deploys.length >= cfg.max_deploys_per_run) {
      held[name] = "over this run's budget of deploys";
      continue;
    }
    if ((perServer[o.server] ?? 0) >= cfg.max_deploys_per_server) {
      held[name] = "its server already has a deploy this run";
      continue;
    }
    perServer[o.server] = (perServer[o.server] ?? 0) + 1;
    deploys.push({
      stack: name,
      if_changed: !down,
      reason: down ? `${o.state}${o.health === "unhealthy" ? ", unhealthy" : ""}` : "a version not deployed",
    });
  }
  for (const [name, s] of Object.entries(state.stacks)) {
    if (s.breaker === "open") alerts.push(`${name}: circuit open after ${s.failures} failed deploys`);
  }
  return { deploys, held, alerts, state };
}

/** Takes a deploy's outcome into the Stack's backoff and circuit breaker. */
export function recordOutcome(s: StackState, ok: boolean, now: number, cfg: Config = DEFAULTS, random: () => number = Math.random): void {
  s.in_flight = "";
  if (ok) {
    Object.assign(s, { failures: 0, breaker: "closed", opened_at: 0, next_attempt_at: 0 });
    return;
  }
  s.failures++;
  // Jitter around the exponential delay: between half and one and a half of it.
  const delay = Math.min(cfg.backoff_base_ms * 2 ** (s.failures - 1), cfg.backoff_max_ms);
  s.next_attempt_at = now + Math.round(delay * (0.5 + random()));
  if (s.breaker === "half_open" || s.failures >= cfg.failure_threshold) {
    s.breaker = "open";
    s.opened_at = now;
  }
}
