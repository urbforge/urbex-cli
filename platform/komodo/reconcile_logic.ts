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
//     `ready_runs` runs; its consumers aren't deployed meanwhile;
//   - exponential backoff with jitter between the deploys of a Stack
//     that fail;
//   - a circuit breaker per Stack: after `failure_threshold` failed
//     deploys in a row it opens and nothing is tried for `cooldown_ms`;
//     then one deploy is tried (half open) and closes it, or opens it
//     again;
//   - a retry budget per run, and a bulkhead per server: a reboot doesn't
//     turn into a storm of deploys;
//   - fail safe: a Stack it couldn't observe, a Stack being deployed,
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
  restarts: number;
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
  /** RestartCount at the last run; -1 before the first. */
  restarts: number;
  ready_runs: number;
  unready_runs: number;
  unstable: boolean;
  stable_runs: number;
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
    restarts: -1, ready_runs: 0, unready_runs: 0, unstable: false, stable_runs: 0, in_flight: "",
  };
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

function healthy(o: Observation): boolean {
  return o.state === "running" && (o.health === "healthy" || o.health === "none");
}

/** Takes this run's observations into the state: hysteresis, crash loops. */
export function observe(prev: State, graphs: Graph[], seen: Record<string, Observation | undefined>, cfg: Config = DEFAULTS): { state: State; alerts: string[] } {
  const state: State = { ...prev, stacks: {} };
  const alerts: string[] = [];
  for (const g of graphs) {
    for (const name of Object.keys(g.stacks)) {
      const s: StackState = { ...freshStack(), ...(prev.stacks[name] ?? {}) };
      const o = seen[name];
      state.stacks[name] = s;
      if (!o) continue; // not observed: nothing learnt
      // A new container starts from 0: only a count that grows is a restart.
      // The counters stop at what they are compared with: a state that
      // doesn't change isn't written again (reconcile.ts).
      if (s.restarts >= 0 && o.restarts > s.restarts) {
        s.unstable = true;
        s.stable_runs = 0;
      } else if (s.unstable) {
        s.stable_runs = Math.min(s.stable_runs + 1, cfg.ready_runs);
        if (s.stable_runs >= cfg.ready_runs) s.unstable = false;
      }
      if (!s.unstable) s.stable_runs = 0;
      s.restarts = o.restarts;
      if (s.unstable) alerts.push(`${name} is restarting (crash loop): what uses it waits`);
      if (healthy(o) && !s.unstable) {
        s.ready_runs = Math.min(s.ready_runs + 1, cfg.ready_runs);
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
  const { state, alerts } = observe(prev, graphs, seen, cfg);
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
      if (s.breaker !== "closed" && healthy(o)) Object.assign(s, { breaker: "closed", failures: 0, next_attempt_at: 0 });
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
      held[name] = "restarting by itself (crash loop)";
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
