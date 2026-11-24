// What the backup Action (backup.ts) decides, without Komodo or Proxmox:
// which machines it backs up, how, and the commands that dump the
// databases first. ADR-0036 (urbforge/urbex), urbforge/urbex-cli#121.
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

/** The machines of the pool on the node that the configuration backs up, by VMID. */
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
 * zstd.
 */
export function vzdumpForm(run: Run, cfg: BackupConfig, storageType: string): Record<string, string> {
  const form: Record<string, string> = {
    vmid: run.vmids.join(","),
    storage: cfg.storage,
    mode: run.mode,
    "prune-backups": pruneBackups(cfg.keep),
    "notes-template": "{{guestname}} - urbex",
  };
  if (storageType !== "pbs") form.compress = "zstd";
  return form;
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
 * The dumps of a configuration: the platform's when it backs up the
 * platform's machines, the projects' when the projects'.
 */
export function dumps(cfg: BackupConfig, projects: Dump[]): Dump[] {
  return [
    ...(cfg.machines.includes("platform") ? PLATFORM_DUMPS : []),
    ...(cfg.machines.includes("projects") ? projects : []),
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
