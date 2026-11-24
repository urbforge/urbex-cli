// deno test platform/komodo - the backup Action's decisions (backup_logic.ts).
import {
  type BackupConfig,
  dumpCommand,
  dumps,
  FORGEJO_DUMP,
  POSTGRES_DUMP,
  projectDumps,
  pruned,
  pruneBackups,
  prunePath,
  type Resource,
  runs,
  selectMachines,
  taskSucceeded,
  vzdumpForm,
} from "./backup_logic.ts";

function assert(cond: unknown, msg: string): asserts cond {
  if (!cond) throw new Error(msg);
}
function eq<T>(got: T, want: T, what: string) {
  assert(JSON.stringify(got) === JSON.stringify(want), `${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
}

const cfg = (o: Partial<BackupConfig> = {}): BackupConfig => ({
  api: "https://pve:8006/api2/json",
  insecure: true,
  node: "pve",
  pool: "urbex",
  storage: "pbs",
  machines: ["platform", "projects"],
  mode: "snapshot",
  modes: {},
  keep: { daily: 7, weekly: 4, monthly: 6 },
  platform_hosts: ["urbex-gitea", "urbex-komodo", "urbex-keycloak"],
  ...o,
});

const resources: Resource[] = [
  { vmid: 9003, name: "urbex-keycloak", node: "pve", pool: "urbex", type: "lxc" },
  { vmid: 9001, name: "urbex-gitea", node: "pve", pool: "urbex", type: "lxc" },
  { vmid: 9020, name: "shop-db-prod", node: "pve", pool: "urbex", type: "lxc" },
  { vmid: 9021, name: "shop-api-prod", node: "pve", pool: "urbex", type: "qemu" },
  // Not urbex's: another pool, no pool, a template, another node.
  { vmid: 100, name: "pbs", node: "pve", type: "lxc" },
  { vmid: 200, name: "nas", node: "pve", pool: "other", type: "qemu" },
  { vmid: 9090, name: "urbex-template", node: "pve", pool: "urbex", template: 1, type: "lxc" },
  { vmid: 9030, name: "shop-web-prod", node: "pve2", pool: "urbex", type: "lxc" },
];

Deno.test("only the pool's machines on the node, never a template", () => {
  eq(selectMachines(resources, cfg()).map((m) => m.vmid), [9001, 9003, 9020, 9021], "vmids");
});

Deno.test("machines: platform or projects", () => {
  eq(selectMachines(resources, cfg({ machines: ["platform"] })).map((m) => m.name), ["urbex-gitea", "urbex-keycloak"], "platform");
  eq(selectMachines(resources, cfg({ machines: ["projects"] })).map((m) => m.name), ["shop-db-prod", "shop-api-prod"], "projects");
});

Deno.test("one vzdump per mode, snapshot first; modes override by hostname", () => {
  const c = cfg({ modes: { "shop-db-prod": "stop", "urbex-gitea": "suspend" } });
  eq(runs(selectMachines(resources, c), c), [
    { mode: "snapshot", vmids: [9003, 9021], names: ["urbex-keycloak", "shop-api-prod"] },
    { mode: "suspend", vmids: [9001], names: ["urbex-gitea"] },
    { mode: "stop", vmids: [9020], names: ["shop-db-prod"] },
  ], "runs");
});

Deno.test("vzdump's form: retention, zstd but on a PBS", () => {
  const run = { mode: "snapshot", vmids: [9001, 9003], names: [] };
  eq(pruneBackups({ daily: 7, weekly: 4, monthly: 6 }), "keep-daily=7,keep-weekly=4,keep-monthly=6", "prune");
  const pbs = vzdumpForm(run, cfg(), "pbs");
  eq(pbs.vmid, "9001,9003", "vmid");
  eq(pbs.storage, "pbs", "storage");
  eq(pbs["prune-backups"], undefined, "no prune-backups: it needs Datastore.Allocate");
  eq(pbs.compress, undefined, "no compress on a PBS");
  eq(vzdumpForm(run, cfg(), "nfs").compress, "zstd", "compress elsewhere");
});

Deno.test("a task with warnings succeeded; an error didn't", () => {
  assert(taskSucceeded("OK"), "OK");
  assert(taskSucceeded("WARNINGS: 1"), "warnings");
  assert(!taskSucceeded("job errors"), "errors");
  assert(!taskSucceeded(""), "empty");
});

Deno.test("dumps: the platform's, and the Stacks tagged dump-postgres", () => {
  const tagNames = { t1: "app", t2: "shop", t3: "prod", t4: "dump-postgres" };
  const stacks = [
    { name: "shop-db-prod", tags: ["t1", "t2", "t3", "t4"] },
    { name: "shop-api-prod", tags: ["t1", "t2", "t3"] },
  ];
  const projects = projectDumps(stacks, tagNames);
  eq(projects, [{ stack: "shop-db-prod", service: "app", kind: "postgres" }], "projects");
  eq(dumps(cfg(), projects).map((d) => d.stack), ["urbex-gitea", "urbex-keycloak", "shop-db-prod"], "both");
  eq(dumps(cfg({ machines: ["projects"] }), projects).map((d) => d.stack), ["shop-db-prod"], "projects only");
  eq(dumps(cfg({ machines: ["platform"] }), projects).map((d) => d.stack), ["urbex-gitea", "urbex-keycloak"], "platform only");
});

Deno.test("the dump commands fit a terminal's line and fail as a whole", () => {
  for (const c of [FORGEJO_DUMP, POSTGRES_DUMP]) {
    assert(c.length < 900, `too long for a terminal line: ${c.length}`);
    assert(!c.includes("\n"), "one line");
    assert(c.startsWith("(set -e;") && c.endsWith(")"), "a subshell that stops at the first failure");
  }
  eq(dumpCommand({ stack: "urbex-gitea", service: "gitea", kind: "forgejo" }), FORGEJO_DUMP, "forgejo");
  eq(dumpCommand({ stack: "x", service: "app", kind: "postgres" }), POSTGRES_DUMP, "postgres");
});

Deno.test("the retention, one machine at a time, with what the token has", () => {
  eq(prunePath(cfg(), 9001), "/nodes/pve/storage/pbs/prunebackups?vmid=9001&prune-backups=keep-daily%3D7%2Ckeep-weekly%3D4%2Ckeep-monthly%3D6", "path");
  eq(pruned([
    { volid: "pbs:backup/ct/9001/2026-11-20T01:00:00Z", mark: "remove" },
    { volid: "pbs:backup/ct/9001/2026-11-24T01:00:00Z", mark: "keep" },
    { volid: "pbs:backup/ct/9001/2026-11-01T01:00:00Z", mark: "protected" },
  ]), ["pbs:backup/ct/9001/2026-11-20T01:00:00Z"], "only what is marked remove");
});
