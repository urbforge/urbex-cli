// Komodo Action "urbex-backup" - managed by urbex bootstrap; the
// Procedure urbex-backup runs it every day (backups.schedule). See
// backup_logic.ts, which bootstrap puts in front of this file, ADR-0036
// in urbforge/urbex and urbforge/urbex-cli#121.
//
// A run, in order:
//  1. dumps the databases into their volumes, so that the machines'
//     backups hold a consistent copy next to the live files: Komodo's
//     own (BackupCoreDatabase, into /etc/komodo/backups on its LXC),
//     Forgejo's SQLite and Keycloak's PostgreSQL, the projects'
//     PostgreSQL servers;
//  2. with the Proxmox layer, backs up the pool's machines with vzdump,
//     through the Proxmox API, with the backup token (VM.Backup on the
//     pool, Datastore.* on the storage: nothing else), to
//     backups.proxmox.storage, and waits for each task;
//  3. with the file layer (urbforge/urbex-cli#122), copies the forge's,
//     Komodo's and Keycloak's data with restic, from the backup Stack next
//     to each, into the platform's repository; then forgets the snapshots
//     beyond backups.files.keep, prunes on Sundays and checks a sample on
//     the 1st of the month.
// A failed dump doesn't stop the backups: the machine is still saved,
// with its last good dump. The run fails at the end, so Komodo shows it,
// and sends a line per failure to Loki, where Grafana alerts on them.
//
// Proxmox is called with curl, in Komodo Core's container: Deno's fetch
// can't skip the check of a self-signed certificate - Proxmox's default -
// that proxmox.insecure asks for. The token reaches curl on its stdin,
// never on its command line.

const CONFIG: BackupConfig = JSON.parse(String.raw`[[URBEX_BACKUP_CONFIG]]`);
const TOKEN = "[[URBEX_PROXMOX_BACKUP_TOKEN]]";
const LOKI_PUSH_URL = "[[URBEX_LOKI_PUSH_URL]]";
const LOKI_LABELS = { kind: "platform", service: "komodo", source: "urbex-backup" };
const CALL_TIMEOUT_MS = 60_000;
// A dump of a large database, a vzdump of many machines: long, but not
// forever.
const DUMP_TIMEOUT_MS = 60 * 60_000;
const TASK_TIMEOUT_MS = 6 * 60 * 60_000;
const TASK_POLL_MS = 10_000;

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

/** Sends the run's summary and its failures to Loki; a failure to send is only logged. */
async function report(summary: string, failures: string[]): Promise<void> {
  const ns = `${Date.now()}000000`;
  const streams = [{ stream: { ...LOKI_LABELS, level: "info" }, values: [[ns, summary]] }];
  if (failures.length > 0) streams.push({ stream: { ...LOKI_LABELS, level: "error" }, values: failures.map((f) => [ns, f]) });
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

/** Calls the Proxmox API and returns its data. */
async function pve(method: string, path: string, form?: Record<string, string>): Promise<any> {
  const args = ["-sS", "--max-time", "60", "--config", "-", "-X", method];
  if (CONFIG.insecure) args.push("-k");
  if (form) args.push("--data", new URLSearchParams(form).toString());
  args.push(CONFIG.api + path);
  const child = new Deno.Command("curl", { args, stdin: "piped", stdout: "piped", stderr: "piped" }).spawn();
  const writer = child.stdin.getWriter();
  await writer.write(new TextEncoder().encode(
    `write-out = "\\n%{http_code}"\nheader = "Authorization: PVEAPIToken=${TOKEN}"\n`,
  ));
  await writer.close();
  const out = await child.output();
  const text = new TextDecoder().decode(out.stdout);
  if (!out.success) throw new Error(`proxmox ${method} ${path}: ${new TextDecoder().decode(out.stderr).trim()}`);
  const cut = text.lastIndexOf("\n");
  const status = Number(text.slice(cut + 1));
  const body = text.slice(0, cut);
  if (status !== 200) throw new Error(`proxmox ${method} ${path}: ${status} ${body.trim()}`);
  return JSON.parse(body).data;
}

/** Waits for a Proxmox task and returns its exit status. */
async function waitTask(upid: string): Promise<string> {
  const started = Date.now();
  for (;;) {
    const st = await pve("GET", `/nodes/${CONFIG.node}/tasks/${encodeURIComponent(upid)}/status`);
    if (st?.status === "stopped") return String(st.exitstatus ?? "");
    if (Date.now() - started > TASK_TIMEOUT_MS) return `still running after ${TASK_TIMEOUT_MS / 3_600_000} h`;
    await new Promise((r) => setTimeout(r, TASK_POLL_MS));
  }
}

/** The last lines of a task's log, for the report. */
async function taskLog(upid: string): Promise<string> {
  try {
    const lines: any[] = await pve("GET", `/nodes/${CONFIG.node}/tasks/${encodeURIComponent(upid)}/log?limit=1000`);
    return lines.map((l) => l.t).filter((t) => /ERROR|error|WARN|failed/.test(t)).slice(-5).join(" | ");
  } catch (err) {
    return `(no log: ${message(err)})`;
  }
}

/** Runs a command in a Stack's service, through a Komodo terminal: its output and exit code. */
async function runIn(d: { stack: string; service: string }, command: string): Promise<{ output: string; code: string }> {
  const lines: string[] = [];
  let code = "";
  const terminal = `urbex-backup-${d.service}`;
  await deadline(
    komodo.execute_stack_service_terminal(
      { stack: d.stack, service: d.service, terminal, command, init: { command: "sh", recreate: "Always" as any } },
      { onLine: (l: string) => void lines.push(l), onFinish: (c: string) => void (code = c) },
    ),
    DUMP_TIMEOUT_MS,
    `dump in ${d.stack}`,
  );
  try {
    await komodo.write("DeleteTerminal", { target: { type: "Stack", params: { stack: d.stack, service: d.service } }, terminal });
  } catch { /* gone with its container, or never made */ }
  return { output: lines.join("\n").trim(), code: code.trim() };
}

/** Dumps the databases; returns the failures. */
async function dumpDatabases(log: (line: string) => void): Promise<string[]> {
  const failures: string[] = [];
  if (dumpsPlatform(CONFIG)) {
    try {
      const update: any = await deadline(komodo.execute_and_poll("BackupCoreDatabase", {}), DUMP_TIMEOUT_MS, "Komodo's database");
      if (update?.success === false) throw new Error("BackupCoreDatabase failed: see its update in Komodo");
      log("komodo: database backed up to /etc/komodo/backups");
    } catch (err) {
      failures.push(`komodo: database not dumped (${message(err)})`);
    }
  }
  const stacks: any[] = await deadline(komodo.read("ListStacks", {}), CALL_TIMEOUT_MS, "listing stacks");
  const tags: any[] = await deadline(komodo.read("ListTags", {}), CALL_TIMEOUT_MS, "listing tags");
  const tagNames: Record<string, string> = {};
  for (const t of tags) tagNames[t._id?.$oid ?? t.id ?? ""] = t.name;
  const states: Record<string, string> = {};
  for (const s of stacks) states[s.name] = String(s.info?.state ?? "");
  const projects = projectDumps(stacks.map((s) => ({ name: s.name, tags: s.tags ?? [] })), tagNames);
  for (const d of dumps(CONFIG, projects)) {
    if (states[d.stack] !== undefined && states[d.stack] !== "running") {
      failures.push(`${d.stack}: not dumped, the stack is ${states[d.stack] || "unknown"}`);
      continue;
    }
    try {
      const { output, code } = await runIn(d, dumpCommand(d));
      if (code !== "0") throw new Error(`exit ${code || "unknown"}: ${output.split("\n").slice(-3).join(" | ")}`);
      log(`${d.stack}: ${d.kind} dumped`);
    } catch (err) {
      failures.push(`${d.stack}: ${d.kind} not dumped (${message(err)})`);
    }
  }
  return failures;
}

/** Backs up the pool's machines with vzdump; returns the failures. */
async function backUpMachines(log: (line: string) => void): Promise<string[]> {
  const resources: Resource[] = await pve("GET", "/cluster/resources?type=vm");
  const machines = selectMachines(resources, CONFIG);
  if (machines.length === 0) return [`no machine of pool ${CONFIG.pool} on ${CONFIG.node} to back up: does the backup token have Pool.Audit and VM.Audit on the pool? Proxmox names a machine's pool only with Pool.Audit`];
  const storage = await pve("GET", `/nodes/${CONFIG.node}/storage/${encodeURIComponent(CONFIG.storage)}/status`);
  const failures: string[] = [];
  for (const run of runs(machines, CONFIG)) {
    try {
      const upid: string = await pve("POST", `/nodes/${CONFIG.node}/vzdump`, vzdumpForm(run, CONFIG, String(storage?.type ?? "")));
      const exit = await waitTask(upid);
      if (!taskSucceeded(exit)) throw new Error(`${exit}: ${await taskLog(upid)}`);
      log(`vzdump ${run.mode} of ${run.names.join(", ")} to ${CONFIG.storage}: ${exit}`);
    } catch (err) {
      failures.push(`vzdump ${run.mode} of ${run.names.join(", ")} to ${CONFIG.storage} failed (${message(err)})`);
    }
  }
  // The retention, machine by machine: only the backed-up machines'
  // backups, never another guest's.
  let removed = 0;
  for (const m of machines) {
    const path = prunePath(CONFIG, m.vmid);
    try {
      const gone = pruned(await pve("GET", path));
      if (gone.length === 0) continue;
      const upid = await pve("DELETE", path);
      if (typeof upid === "string" && upid.startsWith("UPID:")) {
        const exit = await waitTask(upid);
        if (!taskSucceeded(exit)) throw new Error(exit);
      }
      removed += gone.length;
      log(`${m.name}: ${gone.length} old backup(s) removed (${gone.join(", ")})`);
    } catch (err) {
      failures.push(`${m.name}: old backups not pruned (${message(err)})`);
    }
  }
  if (removed === 0) log(`retention ${pruneBackups(CONFIG.keep)}: nothing to remove`);
  return failures;
}

/** The file layer: restic in each backup Stack, then the retention; returns the failures. */
async function backUpFiles(files: FilesConfig, log: (line: string) => void): Promise<string[]> {
  const failures: string[] = [];
  let first: FileStack | undefined; // the first Stack that backed up
  for (const s of files.stacks) {
    try {
      const { output, code } = await runIn({ stack: s.stack, service: "restic" }, resticBackupCommand(s));
      if (code !== "0") throw new Error(`exit ${code || "unknown"}: ${output.split("\n").slice(-3).join(" | ")}`);
      log(`restic ${s.host}: ${resticSummary(output)}`);
      first ??= s;
    } catch (err) {
      failures.push(`restic ${s.host}: not backed up (${message(err)})`);
    }
  }
  // The retention, once for the repository, from the first Stack that
  // backed up: no snapshot is forgotten on a day nothing was saved.
  if (!first) return failures;
  const m = maintenance(new Date());
  const steps: [string, string][] = [[`forget${m.prune ? " and prune" : ""}`, resticForgetCommand(files.keep, m.prune)]];
  if (m.check) steps.push(["check", RESTIC_CHECK]);
  for (const [what, command] of steps) {
    try {
      const { output, code } = await runIn({ stack: first.stack, service: "restic" }, command);
      if (code !== "0") throw new Error(`exit ${code || "unknown"}: ${output.split("\n").slice(-3).join(" | ")}`);
      log(`restic ${what}: done`);
    } catch (err) {
      failures.push(`restic ${what} failed (${message(err)})`);
    }
  }
  return failures;
}

async function main(): Promise<void> {
  const lines: string[] = [];
  const log = (line: string) => {
    lines.push(line);
    console.log(line);
  };
  const started = Date.now();
  const failures: string[] = [];
  try {
    failures.push(...await dumpDatabases(log));
  } catch (err) {
    failures.push(`dumps: ${message(err)}`);
  }
  if (ARGS?.DUMPS_ONLY) {
    log("dumps only: no backups this run");
  } else {
    if (CONFIG.proxmox) {
      try {
        failures.push(...await backUpMachines(log));
      } catch (err) {
        failures.push(`proxmox: ${message(err)}`);
      }
    }
    if (CONFIG.files) {
      try {
        failures.push(...await backUpFiles(CONFIG.files, log));
      } catch (err) {
        failures.push(`files: ${message(err)}`);
      }
    }
  }
  for (const f of failures) console.log(`FAILED ${f}`);
  const took = Math.round((Date.now() - started) / 1000);
  await report(`backup ${failures.length === 0 ? "ok" : "failed"} in ${took} s: ${lines.join("; ")}`, failures);
  if (failures.length > 0) throw new Error(failures.join("; "));
}

await main();
