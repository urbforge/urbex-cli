// deno test platform/komodo - the reconciler's decisions (reconcile_logic.ts).
import {
  type Config,
  decide,
  DEFAULTS,
  emptyState,
  type Graph,
  type Observation,
  parseState,
  recordOutcome,
  type State,
} from "./reconcile_logic.ts";

function assert(cond: unknown, msg: string): asserts cond {
  if (!cond) throw new Error(msg);
}
function eq<T>(got: T, want: T, what: string) {
  assert(JSON.stringify(got) === JSON.stringify(want), `${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
}

// db <- api <- web, and web uses db too: the e2e's stack shop.
const graph: Graph = {
  project: "shop",
  env: "prod",
  stacks: {
    "shop-db-prod": { uses: [], ready_within: 60, wait: 0 },
    "shop-api-prod": { uses: ["shop-db-prod"], ready_within: 60, wait: 60 },
    "shop-web-prod": { uses: ["shop-api-prod", "shop-db-prod"], ready_within: 60, wait: 120 },
  },
};

const up = (server: string, o: Partial<Observation> = {}): Observation => ({
  state: "running", health: "healthy", restarts: 0, busy: false, pending: false,
  stopped_by_operator: false, server, ...o,
});
const all = (o: Record<string, Partial<Observation>> = {}) => ({
  "shop-db-prod": up("db", o["shop-db-prod"]),
  "shop-api-prod": up("api", o["shop-api-prod"]),
  "shop-web-prod": up("web", o["shop-web-prod"]),
});

/** Runs decide `runs` times with the same view, as the Action every minute. */
function settle(seen: Record<string, Observation>, runs = 2, state: State = emptyState(), cfg: Config = DEFAULTS) {
  let d = decide(state, [graph], seen, 0, cfg);
  for (let i = 1; i < runs; i++) d = decide(d.state, [graph], seen, i * 60_000, cfg);
  return d;
}

Deno.test("all healthy: nothing to do", () => {
  const d = settle(all());
  eq(d.deploys, [], "deploys");
  eq(d.alerts, [], "alerts");
});

Deno.test("hysteresis: a consumer seen down once is left alone, twice is deployed", () => {
  const seen = all({ "shop-api-prod": { state: "created", health: "none" } });
  // The first run doesn't know db ready either: two runs for both.
  eq(decide(emptyState(), [graph], seen, 0).deploys, [], "after one run");
  const d = settle(seen);
  eq(d.deploys.map((x) => x.stack), ["shop-api-prod"], "after two runs");
  eq(d.deploys[0].if_changed, false, "a down consumer is deployed, changed or not");
});

Deno.test("a consumer waits while what it uses isn't ready", () => {
  const d = settle(all({ "shop-db-prod": { state: "exited", health: "none" }, "shop-api-prod": { state: "created", health: "none" } }));
  eq(d.deploys, [], "deploys");
  eq(d.held["shop-api-prod"], "waiting for shop-db-prod", "why api is held");
});

Deno.test("a version not deployed (the preflight stopped it) is deployed if changed once the provider is back", () => {
  const d = settle(all({ "shop-api-prod": { pending: true } }));
  eq(d.deploys, [{ stack: "shop-api-prod", if_changed: true, reason: "a version not deployed" }], "deploys");
});

Deno.test("crash loop: a provider whose restarts grow is not ready, with an alert, until it stays put", () => {
  let d = settle(all({ "shop-api-prod": { state: "created", health: "none" } }), 1);
  d = decide(d.state, [graph], all({ "shop-db-prod": { restarts: 3 }, "shop-api-prod": { state: "created", health: "none" } }), 60_000);
  eq(d.deploys, [], "deploys while db crash loops");
  assert(d.alerts.some((a) => a.includes("shop-db-prod is restarting")), `alerts: ${d.alerts}`);
  // Two runs without a new restart: stable again, then ready after two more.
  let state = d.state;
  for (let i = 2; i < 6; i++) {
    d = decide(state, [graph], all({ "shop-db-prod": { restarts: 3 }, "shop-api-prod": { state: "created", health: "none" } }), i * 60_000);
    state = d.state;
  }
  eq(d.deploys.map((x) => x.stack), ["shop-api-prod"], "deploys once db is stable and ready");
  eq(d.alerts, [], "no alert once stable");
});

Deno.test("a recreated provider (restart count back to 0) is not a crash loop", () => {
  let d = settle(all({ "shop-db-prod": { restarts: 4 } }));
  d = decide(d.state, [graph], all({ "shop-db-prod": { restarts: 0 } }), 120_000);
  eq(d.state.stacks["shop-db-prod"].unstable, false, "unstable");
});

Deno.test("backoff: a failed deploy waits, longer each time, with jitter", () => {
  const s = settle(all()).state.stacks["shop-api-prod"];
  const cfg = { ...DEFAULTS, failure_threshold: 99 };
  recordOutcome(s, false, 0, cfg, () => 0.5);
  eq(s.next_attempt_at, 60_000, "first delay, jitter at the middle");
  recordOutcome(s, false, 0, cfg, () => 0.5);
  eq(s.next_attempt_at, 120_000, "second delay doubles");
  recordOutcome(s, false, 0, cfg, () => 0);
  eq(s.next_attempt_at, 120_000, "third delay (240 s) at the lowest jitter is half");
  for (let i = 0; i < 10; i++) recordOutcome(s, false, 0, cfg, () => 0.5);
  eq(s.next_attempt_at, DEFAULTS.backoff_max_ms, "capped");

  const seen = all({ "shop-api-prod": { state: "created", health: "none" } });
  const state = settle(seen).state;
  recordOutcome(state.stacks["shop-api-prod"], false, 120_000, cfg, () => 0.5);
  const d = decide(state, [graph], seen, 150_000, cfg);
  eq(d.deploys, [], "deploys while backing off");
  assert(d.held["shop-api-prod"].startsWith("backing off"), d.held["shop-api-prod"]);
});

Deno.test("circuit breaker: opens after the threshold, half opens after the cooldown, closes on success", () => {
  const seen = all({ "shop-api-prod": { state: "created", health: "none" } });
  const cfg = { ...DEFAULTS, failure_threshold: 3 };
  const state = settle(seen, 2, emptyState(), cfg).state;
  const s = state.stacks["shop-api-prod"];
  for (let i = 0; i < 3; i++) recordOutcome(s, false, 0, cfg, () => 0.5);
  eq(s.breaker, "open", "after three failures");

  let d = decide(state, [graph], seen, cfg.cooldown_ms - 1, cfg);
  eq(d.deploys, [], "deploys while open");
  assert(d.alerts.some((a) => a.includes("circuit open after 3 failed deploys")), `alerts: ${d.alerts}`);

  d = decide(d.state, [graph], seen, cfg.cooldown_ms + 1, cfg);
  eq(d.deploys.map((x) => x.stack), ["shop-api-prod"], "one try once the cooldown is over");
  eq(d.state.stacks["shop-api-prod"].breaker, "half_open", "half open");

  // The try fails: open again, for another cooldown.
  recordOutcome(d.state.stacks["shop-api-prod"], false, cfg.cooldown_ms + 1, cfg, () => 0.5);
  eq(d.state.stacks["shop-api-prod"].breaker, "open", "a failed try opens it again");

  // The next try works: closed, and nothing remembered.
  d = decide(d.state, [graph], seen, 3 * cfg.cooldown_ms, cfg);
  recordOutcome(d.state.stacks["shop-api-prod"], true, 3 * cfg.cooldown_ms, cfg);
  eq(d.state.stacks["shop-api-prod"].breaker, "closed", "closed");
  eq(d.state.stacks["shop-api-prod"].failures, 0, "failures");
});

Deno.test("retry budget and bulkhead: at most so many deploys a run, one per server", () => {
  const seen = all({ "shop-api-prod": { state: "created", health: "none", server: "s1" }, "shop-web-prod": { state: "created", health: "none", server: "s1" } });
  // web waits for api anyway; take api's dependency away to see the bulkhead.
  const g: Graph = { ...graph, stacks: { ...graph.stacks, "shop-web-prod": { uses: ["shop-db-prod"], ready_within: 60, wait: 60 } } };
  let d = decide(emptyState(), [g], seen, 0);
  d = decide(d.state, [g], seen, 60_000);
  eq(d.deploys.map((x) => x.stack), ["shop-api-prod"], "one per server");
  assert(d.held["shop-web-prod"].includes("already has a deploy"), d.held["shop-web-prod"]);

  d = decide(emptyState(), [g], seen, 0, { ...DEFAULTS, max_deploys_per_run: 1, max_deploys_per_server: 9 });
  d = decide(d.state, [g], seen, 60_000, { ...DEFAULTS, max_deploys_per_run: 1, max_deploys_per_server: 9 });
  eq(d.deploys.length, 1, "budget of one");
  assert(d.held["shop-web-prod"].includes("budget"), d.held["shop-web-prod"]);
});

Deno.test("fail safe: busy, stopped by an operator, or not observed is left alone", () => {
  for (const [o, why] of [
    [{ state: "created", health: "none", busy: true }, "a deploy is in progress"],
    [{ state: "exited", health: "none", stopped_by_operator: true }, "stopped by an operator"],
  ] as [Partial<Observation>, string][]) {
    const d = settle(all({ "shop-api-prod": o }));
    eq(d.deploys, [], `deploys (${why})`);
    eq(d.held["shop-api-prod"], why, "why");
  }
  let crash = settle(all(), 1);
  crash = decide(crash.state, [graph], all({ "shop-api-prod": { state: "restarting", health: "none", restarts: 2 } }), 60_000);
  crash = decide(crash.state, [graph], all({ "shop-api-prod": { state: "restarting", health: "none", restarts: 4 } }), 120_000);
  eq(crash.deploys, [], "deploys of a consumer in a crash loop of its own");
  eq(crash.held["shop-api-prod"], "restarting by itself (crash loop)", "why");

  const flying = settle(all({ "shop-api-prod": { state: "created", health: "none" } }));
  flying.state.stacks["shop-api-prod"].in_flight = "update-1";
  const again = decide(flying.state, [graph], all({ "shop-api-prod": { state: "created", health: "none" } }), 180_000);
  eq(again.deploys, [], "deploys while the last one is in flight");
  recordOutcome(again.state.stacks["shop-api-prod"], true, 200_000);
  eq(again.state.stacks["shop-api-prod"].in_flight, "", "an outcome lands the deploy");

  const seen: Record<string, Observation | undefined> = all();
  seen["shop-api-prod"] = undefined;
  const d = settle(seen as Record<string, Observation>);
  eq(d.held["shop-api-prod"], "not observed", "unobserved");
});

Deno.test("a steady state doesn't change from run to run, so it isn't written again", () => {
  const a = settle(all(), 3).state;
  const b = decide(a, [graph], all(), 10 * 60_000).state;
  eq(b.stacks, a.stacks, "stacks after one more run");
});

Deno.test("the state survives the variable, and anything unreadable starts over", () => {
  const s = settle(all({ "shop-api-prod": { state: "created", health: "none" } })).state;
  eq(parseState(JSON.stringify(s)), s, "round trip");
  eq(parseState("not json"), emptyState(), "garbage");
  eq(parseState(undefined), emptyState(), "missing");
});
