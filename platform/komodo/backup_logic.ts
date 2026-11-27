// What the backup Action (backup.ts) decides, without Komodo or Proxmox:
// which machines it backs up, how, and the commands that dump the
// databases first, and the file layer's restic commands. ADR-0036
// (urbforge/urbex), urbforge/urbex-cli#121 and #122.
// urbex bootstrap puts this file, without its exports, in front of
// backup.ts; deno test platform/komodo tests it.

/** URBEX_BACKUP_CONFIG, written by urbex bootstrap from backups in urbex.platform.yaml. */
export interface BackupConfig {
  /** The Proxmox API, e.g. https://192.168.1.10:8006/api2/json. */
  api: string;
  /** proxmox.insecure: the node's certificate isn't checked. */
  insecure: boolean;
  node: string;
  /** The pool: only its machines are backed up. */
  pool: string;
  /** backups.proxmox.storage. */
  storage: string;
  /** Which machines: "platform", "projects", or both. */
  machines: string[];
  /** vzdump's mode, and overrides by hostname. */
  mode: string;
  modes: Record<string, string>;
  keep: { daily: number; weekly: number; monthly: number };
  /** The platform's machines' hostnames: every other machine of the pool is a project's. */
  platform_hosts: string[];
  /** Whether the Proxmox layer runs (backups.proxmox.storage). */
  proxmox: boolean;
  /** The file layer (backups.files), null when it doesn't run. */
  files: FilesConfig | null;
}

/** The file layer: restic in a Stack next to each machine's data, one repository. */
export interface FilesConfig {
  stacks: FileStack[];
  /** backups.files.keep: what restic forget keeps. */
  keep: { daily: number; weekly: number; monthly: number };
}

/** A backup Stack: restic, the host its snapshots are named after, what it copies. */
export interface FileStack {
  stack: string;
  host: string;
  paths: string[];
}

/** A machine as /cluster/resources?type=vm lists it. */
export interface Resource {
  vmid: number;
  name?: string;
  node?: string;
  pool?: string;
  template?: number;
  type?: string;
}

/**
 * The machines of the pool on the node that the configuration backs up,
 * by VMID. A machine whose pool isn't named - Proxmox names it only to a
 * token with Pool.Audit on the pool - is never backed up.
 */
export function selectMachines(resources: Resource[], cfg: BackupConfig): Resource[] {
  const platform = new Set(cfg.platform_hosts);
  return resources
    .filter((r) => r.pool === cfg.pool && r.node === cfg.node && Number(r.template ?? 0) !== 1)
    .filter((r) => cfg.machines.includes(platform.has(r.name ?? "") ? "platform" : "projects"))
    .sort((a, b) => a.vmid - b.vmid);
}

/** One vzdump run: the machines that share a mode. */
export interface Run {
  mode: string;
  vmids: number[];
  names: string[];
}

/** The machines grouped by their mode, snapshot first: one vzdump each. */
export function runs(machines: Resource[], cfg: BackupConfig): Run[] {
  const byMode = new Map<string, Run>();
  for (const m of machines) {
    const mode = cfg.modes[m.name ?? ""] ?? cfg.mode;
    if (!byMode.has(mode)) byMode.set(mode, { mode, vmids: [], names: [] });
    byMode.get(mode)!.vmids.push(m.vmid);
    byMode.get(mode)!.names.push(m.name ?? String(m.vmid));
  }
  const order = ["snapshot", "suspend", "stop"];
  return [...byMode.values()].sort((a, b) => order.indexOf(a.mode) - order.indexOf(b.mode));
}

/** vzdump's prune-backups. */
export function pruneBackups(keep: BackupConfig["keep"]): string {
  return `keep-daily=${keep.daily},keep-weekly=${keep.weekly},keep-monthly=${keep.monthly}`;
}

/**
 * The form of POST /nodes/{node}/vzdump for a run. A Proxmox Backup
 * Server compresses and deduplicates on its own; any other storage gets
 * zstd. No prune-backups: vzdump takes it only with Datastore.Allocate on
 * the storage - which may create and remove storages - so the retention
 * is applied after, machine by machine (prunePath), with what the backup
 * token has.
 */
export function vzdumpForm(run: Run, cfg: BackupConfig, storageType: string): Record<string, string> {
  const form: Record<string, string> = {
    vmid: run.vmids.join(","),
    storage: cfg.storage,
    mode: run.mode,
    "notes-template": "{{guestname}} - urbex",
  };
  if (storageType !== "pbs") form.compress = "zstd";
  return form;
}

/**
 * The path of a machine's prune on the storage: GET lists what the
 * retention keeps and removes, DELETE removes it. With a vmid, Proxmox
 * asks only Datastore.AllocateSpace on the storage and VM.Backup on the
 * machine - the backup token's - and considers only that machine's
 * backups.
 */
export function prunePath(cfg: BackupConfig, vmid: number): string {
  const q = new URLSearchParams({ vmid: String(vmid), "prune-backups": pruneBackups(cfg.keep) });
  return `/nodes/${cfg.node}/storage/${encodeURIComponent(cfg.storage)}/prunebackups?${q}`;
}

/** What a prune's listing removes: the volumes marked "remove". */
export function pruned(listing: { volid: string; mark: string }[]): string[] {
  return listing.filter((b) => b.mark === "remove").map((b) => b.volid).sort();
}

/** Whether a Proxmox task's exit status is a success: OK, or OK with warnings. */
export function taskSucceeded(exitStatus: string): boolean {
  return exitStatus === "OK" || exitStatus.startsWith("WARNINGS");
}

/** A database to dump before the backup: a compose service of a Stack. */
export interface Dump {
  stack: string;
  service: string;
  /** What it is, for the command and the report. */
  kind: "forgejo" | "postgres";
}

/** The Komodo tag urbex apply gives the Stacks of the projects' PostgreSQL servers. */
export const DUMP_POSTGRES_TAG = "dump-postgres";

/** The platform's databases besides Komodo's own (BackupCoreDatabase). */
export const PLATFORM_DUMPS: Dump[] = [
  { stack: "urbex-gitea", service: "gitea", kind: "forgejo" },
  { stack: "urbex-keycloak", service: "postgres", kind: "postgres" },
];

/** The projects' PostgreSQL servers: the Stacks tagged dump-postgres, their service app. */
export function projectDumps(stacks: { name: string; tags: string[] }[], tagNames: Record<string, string>): Dump[] {
  return stacks
    .filter((s) => s.tags.some((id) => tagNames[id] === DUMP_POSTGRES_TAG))
    .map((s): Dump => ({ stack: s.name, service: "app", kind: "postgres" }))
    .sort((a, b) => a.stack.localeCompare(b.stack));
}

/**
 * Whether the platform's databases are dumped: when the Proxmox layer
 * backs up the platform's machines, or the file layer runs - it copies
 * those dumps.
 */
export function dumpsPlatform(cfg: BackupConfig): boolean {
  return (cfg.proxmox && cfg.machines.includes("platform")) || cfg.files !== null;
}

/**
 * The dumps of a configuration: the platform's (dumpsPlatform), the
 * projects' when the Proxmox layer backs up their machines.
 */
export function dumps(cfg: BackupConfig, projects: Dump[]): Dump[] {
  return [
    ...(dumpsPlatform(cfg) ? PLATFORM_DUMPS : []),
    ...(cfg.proxmox && cfg.machines.includes("projects") ? projects : []),
  ];
}

// The commands run in the database's container, through a Komodo
// terminal: one line each (the terminal's shell reads a line, busybox's
// at most 1024 bytes), in a subshell so that a failure ends it with its
// code, not the terminal's shell. Each dump is written aside, then
// renamed: a dump that is there is whole.

/**
 * A consistent copy of Forgejo's SQLite database, with SQLite's backup
 * command, into /data/urbex-dumps: on Forgejo's volume, next to what it
 * copies.
 */
export const FORGEJO_DUMP =
  `(set -e; D=/data/urbex-dumps; mkdir -p $D; sqlite3 /data/gitea/gitea.db ".backup '$D/gitea.db.tmp'"; ` +
  `mv $D/gitea.db.tmp $D/gitea.db; chown -R 1000:1000 $D; ls -l $D/gitea.db)`;

/**
 * pg_dump, in the custom format, of each database of a PostgreSQL
 * server, as its admin user over the local socket, into urbex-dumps next
 * to its data directory: on the server's volume for a project's (PGDATA
 * is <volume>/pgdata), on its own volume for Keycloak's.
 */
export const POSTGRES_DUMP =
  `(set -e; export PGPASSWORD="$POSTGRES_PASSWORD"; D="$(dirname "$PGDATA")/urbex-dumps"; mkdir -p "$D"; ` +
  `for db in $(psql -XAtq -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "select datname from pg_database where datallowconn and not datistemplate"); do ` +
  `pg_dump -Fc -U "$POSTGRES_USER" -d "$db" -f "$D/$db.dump.tmp"; mv "$D/$db.dump.tmp" "$D/$db.dump"; done; ls -l "$D")`;

/** The command that dumps d. */
export function dumpCommand(d: Dump): string {
  return d.kind === "forgejo" ? FORGEJO_DUMP : POSTGRES_DUMP;
}

// The file layer's commands, run in a backup Stack's restic container
// through a Komodo terminal: one line each, in a subshell, as the dumps.
// The repository, its password and the bucket's key are in the
// container's environment (RESTIC_*, AWS_*), never on a command line.

/** The tag of the platform's snapshots: the projects' get their own (#123). */
export const PLATFORM_SNAPSHOT_TAG = "platform";

/**
 * Backs up a Stack's paths, creating the repository the first time: one
 * per platform, shared by every machine - restic locks it, and the Action
 * runs one machine at a time.
 */
export function resticBackupCommand(s: FileStack): string {
  return `(set -e; restic cat config >/dev/null 2>&1 || restic init; ` +
    `restic backup --host ${s.host} --tag ${PLATFORM_SNAPSHOT_TAG} ${s.paths.join(" ")})`;
}

/** What one run does besides the backups: forget always, prune and check on some days. */
export interface Maintenance {
  prune: boolean;
  check: boolean;
}

/**
 * The maintenance of a run on date: restic forget every day; the prune,
 * which frees the forgotten snapshots' data and is the slow part, on
 * Sundays; restic check of a sample of the data on the 1st of the month
 * (ADR-0036, point 6). Days in UTC.
 */
export function maintenance(date: Date): Maintenance {
  return { prune: date.getUTCDay() === 0, check: date.getUTCDate() === 1 };
}

/**
 * restic forget with the retention, of the platform's snapshots, each
 * machine's apart (--group-by host); --prune on prune days. Run from one
 * Stack, it covers every machine: no RESTIC_HOST in the backup Stacks,
 * which would limit it to that Stack's host.
 */
export function resticForgetCommand(keep: FilesConfig["keep"], prune: boolean): string {
  return `(set -e; restic forget --tag ${PLATFORM_SNAPSHOT_TAG} --group-by host,tags ` +
    `--keep-daily ${keep.daily} --keep-weekly ${keep.weekly} --keep-monthly ${keep.monthly}${prune ? " --prune" : ""})`;
}

/** restic check, reading a tenth of the data: the whole over months, without a day's download. */
export const RESTIC_CHECK = "(set -e; restic check --read-data-subset=10%)";
