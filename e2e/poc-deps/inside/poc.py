#!/usr/bin/env python3
"""Driver of the dependency PoC. It runs inside the machine, next to Komodo.

  poc.py prepare              wait for Komodo, note what Periphery has
  poc.py run T0 T1 ...        run tests; write /poc/results/<version>/
  poc.py after-reboot T       watch how the blocks come back after a reboot
  poc.py summary              rebuild summary.md from the results

Every test starts from nothing (the three Stacks destroyed and deleted),
creates the Stacks with the constructs the test is about, deploys them, and
watches the containers every second: when each one runs, when it is healthy,
how many times it restarted.
"""
import json
import os
import re
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

BASE = "http://localhost:9120"
ENV_FILE = "/opt/poc/komodo/compose.env"
VERSION = os.environ.get("KOMODO_VERSION", "unknown")
RESULTS = f"/poc/results/{VERSION}"
STACKS = ["poc-data", "poc-api", "poc-web"]
BLOCKS = {"poc-data": "data", "poc-api": "api", "poc-web": "web"}
# The containers every healthy run ends with.
HEALTHY = ["data/pg", "data/cache", "api/api", "web/assets", "web/web"]


def sh(cmd, check=True, timeout=120):
    r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    if check and r.returncode != 0:
        raise RuntimeError(f"{' '.join(cmd)}: {r.stderr.strip() or r.stdout.strip()}")
    return r.stdout


def read_env(path):
    out = {}
    for line in open(path):
        if "=" in line and not line.startswith("#"):
            k, v = line.rstrip("\n").split("=", 1)
            out[k] = v
    return out


# ---------------------------------------------------------------- Komodo API


class Komodo:
    def __init__(self):
        env = read_env(ENV_FILE)
        self.user = env["KOMODO_INIT_ADMIN_USERNAME"]
        self.password = env["KOMODO_INIT_ADMIN_PASSWORD"]
        self.headers = None

    def _post(self, path, body, headers, timeout=60):
        req = urllib.request.Request(
            BASE + path,
            data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json", **headers},
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                data = r.read()
                return json.loads(data) if data else None
        except urllib.error.HTTPError as e:
            raise RuntimeError(f"komodo {path} {e.code}: {e.read().decode()[:600]}")

    def login(self):
        r = self._post("/auth/login/LoginLocalUser", {"username": self.user, "password": self.password}, {})
        jwt = r["data"]["jwt"]
        k = self._post(
            "/auth/manage/CreateApiKey",
            {"name": f"poc-{int(time.time())}", "expires": 0},
            {"Authorization": jwt},
        )
        self.headers = {"X-Api-Key": k["key"], "X-Api-Secret": k["secret"]}

    def call(self, kind, typ, params=None):
        return self._post(f"/{kind}/{typ}", params or {}, self.headers)

    def wait_ready(self, timeout=300):
        deadline = time.time() + timeout
        while True:
            try:
                self.login()
                break
            except Exception as e:  # core not up yet
                if time.time() > deadline:
                    raise RuntimeError(f"komodo core not ready: {e}")
                time.sleep(3)
        while True:
            servers = self.call("read", "ListServers")
            local = next((s for s in servers if s["name"] == "local"), None)
            if local and local["info"]["state"] == "Ok":
                return
            if local and local["info"].get("attempted_public_key"):
                self.call("write", "UpdateServerPublicKey", {"server": "local", "public_key": local["info"]["attempted_public_key"]})
            if time.time() > deadline:
                raise RuntimeError(f"komodo server local not connected: {local}")
            time.sleep(3)

    def exists(self, kind, name):
        return any(i["name"] == name for i in self.call("read", f"List{kind}s"))

    def ensure(self, kind, name, config):
        if self.exists(kind, name):
            self.call("write", f"Update{kind}", {"id": name, "config": config})
        else:
            self.call("write", f"Create{kind}", {"name": name, "config": config})

    def delete(self, kind, name):
        if self.exists(kind, name):
            self.call("write", f"Delete{kind}", {"id": name})

    def wait_update(self, update, timeout=900):
        uid = update["_id"]["$oid"]
        deadline = time.time() + timeout
        while True:
            u = self.call("read", "GetUpdate", {"id": uid})
            if u["status"] == "Complete":
                return u
            if time.time() > deadline:
                raise RuntimeError(f"update {uid} did not complete in {timeout}s")
            time.sleep(1)

    def execute(self, typ, params, timeout=900):
        t0 = time.time()
        u = self.wait_update(self.call("execute", typ, params), timeout)
        return {
            "type": typ,
            "params": params,
            "ok": bool(u.get("success")),
            "seconds": round(time.time() - t0, 1),
            "failure": failure(u),
        }


def failure(update):
    for log in update.get("logs", []):
        if not log.get("success", True):
            text = re.sub(r"<[^>]+>", "", (log.get("stderr") or "") + "\n" + (log.get("stdout") or ""))
            return f"{log.get('stage')}: {text.strip()[-1200:]}"
    return ""


# ---------------------------------------------------------------- Containers


def snapshot():
    """name -> state of every container of the PoC's blocks."""
    ids = sh(["docker", "ps", "-aq", "--filter", "label=com.docker.compose.project"]).split()
    if not ids:
        return {}
    out = {}
    for c in json.loads(sh(["docker", "inspect"] + ids)):
        labels = c["Config"].get("Labels") or {}
        project = labels.get("com.docker.compose.project", "")
        if not project.startswith("poc-"):
            continue
        st = c["State"]
        name = f"{project[4:]}/{labels.get('com.docker.compose.service')}"
        out[name] = {
            "container": c["Name"].lstrip("/"),
            "status": st["Status"],
            "health": (st.get("Health") or {}).get("Status", ""),
            "restarts": c["RestartCount"],
            "exit": st["ExitCode"],
        }
    return out


class Monitor(threading.Thread):
    """Notes every change of a container's state, once a second."""

    def __init__(self):
        super().__init__(daemon=True)
        self.t0 = time.time()
        self.events = []
        self.last = {}
        self.halt = threading.Event()

    def run(self):
        while not self.halt.is_set():
            try:
                for name, v in snapshot().items():
                    key = (v["status"], v["health"], v["restarts"], v["exit"])
                    if self.last.get(name) != key:
                        self.last[name] = key
                        self.events.append({"t": round(time.time() - self.t0, 1), "name": name, **v})
            except Exception as e:
                self.events.append({"t": round(time.time() - self.t0, 1), "name": "monitor", "error": str(e)})
            self.halt.wait(1)

    def first(self, name, **want):
        for e in self.events:
            if e.get("name") == name and all(e.get(k) == v for k, v in want.items()):
                return e["t"]
        return None


def converged():
    s = snapshot()
    ok = all(s.get(n, {}).get("health") == "healthy" for n in HEALTHY)
    m = s.get("api/migrate")
    return ok and (m is None or (m["status"] == "exited" and m["exit"] == 0))


def logs_count(word):
    n = 0
    for v in snapshot().values():
        r = subprocess.run(["docker", "logs", v["container"]], capture_output=True, text=True)
        n += (r.stdout + r.stderr).count(word)
    return n


def clean(k):
    for name in STACKS:
        if k.exists("Stack", name):
            try:
                k.execute("DestroyStack", {"stack": name}, 300)
            except Exception:
                pass
            k.delete("Stack", name)
    ids = sh(["docker", "ps", "-aq", "--filter", "label=com.docker.compose.project"]).split()
    for c in ids:
        proj = sh(["docker", "inspect", "-f", '{{index .Config.Labels "com.docker.compose.project"}}', c]).strip()
        if proj.startswith("poc-"):
            sh(["docker", "rm", "-f", c], check=False)
    for name in ["poc-order", "poc-wait-data", "poc-wait-api"]:
        k.delete("Procedure" if name == "poc-order" else "Action", name)


# ---------------------------------------------------------------- The tests


def stack_config(variant, name, **extra):
    return {
        "server_id": "local",
        "files_on_host": True,
        "run_directory": f"/etc/komodo/poc/{variant}/{BLOCKS[name]}",
        "file_paths": ["compose.yaml"],
        **extra,
    }


# What each consumer waits for, for the constructs that wait by themselves.
WAITS = {"poc-api": ["pg:5432"], "poc-web": ["api:8080", "cache:6379"]}


def waitfor(stack):
    return "/etc/komodo/poc/waitfor.sh " + " ".join(WAITS[stack])


def action_script(stack, services):
    return f"""
const STACK = {json.dumps(stack)};
const SERVICES = {json.dumps(services)};
const deadline = Date.now() + 240_000;
const healthy = (c: unknown) => /"healthy"/i.test(JSON.stringify(c));
for (const service of SERVICES) {{
  let shown = false;
  while (true) {{
    let c: unknown = undefined;
    try {{
      c = await komodo.read("InspectStackContainer", {{ stack: STACK, service }});
    }} catch (e) {{
      console.log(`${{STACK}}/${{service}}: ${{e}}`);
    }}
    if (c !== undefined && !shown) {{
      console.log(JSON.stringify(c).slice(0, 1500));
      shown = true;
    }}
    if (c !== undefined && healthy(c)) {{
      console.log(`${{STACK}}/${{service}} is healthy`);
      break;
    }}
    if (Date.now() > deadline) throw new Error(`${{STACK}}/${{service}} is not healthy in time`);
    await new Promise((r) => setTimeout(r, 2000));
  }}
}}
"""


def stage(name, *executions):
    return {
        "name": name,
        "enabled": True,
        "executions": [{"enabled": True, "execution": e} for e in executions],
    }


def deploy(stack):
    return {"type": "DeployStack", "params": {"stack": stack}}


def run_action(name):
    return {"type": "RunAction", "params": {"action": name}}


def serial_stages():
    return [
        stage("data", deploy("poc-data")),
        stage("api", deploy("poc-api")),
        stage("web", deploy("poc-web")),
    ]


def build_test(name, k):
    """Returns (description, how): how() deploys everything and returns results."""
    parallel = lambda: deploy_parallel(k)  # noqa: E731

    if name == "T0":
        for s in STACKS:
            k.ensure("Stack", s, stack_config("plain", s))
        return "Baseline: the three Stacks deployed at once, nothing to order them", parallel

    if name in ("T1", "T2"):
        extra = {"extra_args": ["--wait"]} if name == "T2" else {}
        for s in STACKS:
            k.ensure("Stack", s, stack_config("plain", s, **extra))
        k.ensure("Procedure", "poc-order", {"stages": serial_stages()})
        what = "Procedure with a stage per block" + (", each Stack deployed with `up -d --wait`" if extra else "")
        return what, lambda: [k.execute("RunProcedure", {"procedure": "poc-order"})]

    if name == "T3":
        for s in STACKS:
            extra = {"pre_deploy": {"path": ".", "command": waitfor(s)}} if s in WAITS else {}
            k.ensure("Stack", s, stack_config("plain", s, **extra))
        return "pre_deploy of each consumer Stack waits for its providers (TCP)", parallel

    if name == "T4":
        for s in STACKS:
            extra = {"compose_cmd_wrapper": f"sh -c '{waitfor(s)} && [[COMPOSE_COMMAND]]'"} if s in WAITS else {}
            k.ensure("Stack", s, stack_config("plain", s, **extra))
        return "compose_cmd_wrapper of each consumer Stack waits for its providers (TCP)", parallel

    if name in ("T5", "T5b"):
        extra = {"ignore_services": ["wait"]} if name == "T5" else {}
        for s in STACKS:
            k.ensure("Stack", s, stack_config("wait", s, **(extra if s in WAITS else {})))
        what = "a wait service inside each consumer block" + (", ignored by Komodo's Stack state" if extra else ", not ignored")
        return what, parallel

    if name == "T6":
        for s in STACKS:
            k.ensure("Stack", s, stack_config("plain", s))
        k.ensure("Action", "poc-wait-data", {"file_contents": action_script("poc-data", ["pg", "cache"])})
        k.ensure("Action", "poc-wait-api", {"file_contents": action_script("poc-api", ["api"])})
        k.ensure(
            "Procedure",
            "poc-order",
            {
                "stages": [
                    stage("data", deploy("poc-data")),
                    stage("wait-data", run_action("poc-wait-data")),
                    stage("api", deploy("poc-api")),
                    stage("wait-api", run_action("poc-wait-api")),
                    stage("web", deploy("poc-web")),
                ]
            },
        )
        return "Procedure with an Action between the stages that waits for the real container health", lambda: [
            k.execute("RunProcedure", {"procedure": "poc-order"})
        ]

    raise SystemExit(f"unknown test {name}")


def deploy_parallel(k):
    results = []
    threads = []

    def one(stack):
        try:
            results.append(k.execute("DeployStack", {"stack": stack}))
        except Exception as e:
            results.append({"type": "DeployStack", "params": {"stack": stack}, "ok": False, "seconds": 0, "failure": str(e)})

    for s in STACKS:
        t = threading.Thread(target=one, args=(s,))
        t.start()
        threads.append(t)
    for t in threads:
        t.join()
    return results


def stack_states(k):
    return {i["name"]: i["info"]["state"] for i in k.call("read", "ListStacks") if i["name"] in STACKS}


def run_test(name, k, converge_timeout):
    clean(k)
    time.sleep(2)
    description, how = build_test(name, k)
    print(f"\n== {name}: {description}", flush=True)
    mon = Monitor()
    mon.start()
    t0 = time.time()
    try:
        actions = how()
    except Exception as e:
        actions = [{"type": "error", "ok": False, "seconds": round(time.time() - t0, 1), "failure": str(e)}]
    returned = round(time.time() - t0, 1)
    deadline = time.time() + converge_timeout
    converged_at = None
    while time.time() < deadline:
        if converged():
            converged_at = round(time.time() - t0, 1)
            break
        time.sleep(2)
    time.sleep(2)
    mon.halt.set()
    mon.join()

    ev = mon.events
    pg_ok = mon.first("data/pg", health="healthy")
    cache_ok = mon.first("data/cache", health="healthy")
    api_ok = mon.first("api/api", health="healthy")
    # A one-shot can run and exit between two samples: its first exit counts as its start.
    starts = {n: min((t for t in (mon.first(n, status="running"), mon.first(n, status="exited")) if t is not None), default=None) for n in ["api/migrate", "api/api", "web/web"]}
    final = snapshot()
    result = {
        "test": name,
        "description": description,
        "komodo": VERSION,
        "actions": actions,
        "deploy_returned_after_s": returned,
        "converged_after_s": converged_at,
        "stack_states": stack_states(k),
        "postgres_healthy_at": pg_ok,
        "api_healthy_at": api_ok,
        "consumer_first_running_at": starts,
        # A consumer is ordered when none of its containers ran before what it needs was healthy.
        "api_after_postgres": all(t is not None and pg_ok is not None and t >= pg_ok - 1 for t in (starts["api/migrate"], starts["api/api"])),
        "web_after_api_and_cache": starts["web/web"] is not None and api_ok is not None and cache_ok is not None and starts["web/web"] >= max(api_ok, cache_ok) - 1,
        "restarts": {n: v["restarts"] for n, v in final.items()},
        "missing_dependency_lines": logs_count("missing"),
        "final": final,
        "events": ev,
    }
    os.makedirs(RESULTS, exist_ok=True)
    json.dump(result, open(f"{RESULTS}/{name}.json", "w"), indent=1)
    write_md(result)
    print(json.dumps({k_: v for k_, v in result.items() if k_ not in ("events", "final")}, indent=1), flush=True)
    return result


def write_md(r):
    lines = [f"# {r['test']}: {r['description']}", "", f"Komodo {r['komodo']}", ""]
    lines += [
        f"- deploy returned after {r['deploy_returned_after_s']} s; everything healthy after {r['converged_after_s']} s (None = never)",
        f"- postgres healthy at {r['postgres_healthy_at']} s, api healthy at {r['api_healthy_at']} s",
        f"- consumers first running at {r['consumer_first_running_at']}",
        f"- api after postgres: {r['api_after_postgres']}; web after api and cache: {r['web_after_api_and_cache']}",
        f"- lines saying a dependency was missing: {r['missing_dependency_lines']}; restarts: {r['restarts']}",
        f"- Komodo's Stack states: {r['stack_states']}",
        "",
        "## What Komodo ran",
        "",
    ]
    for a in r["actions"]:
        lines.append(f"- {a['type']} {json.dumps(a.get('params'))}: ok={a['ok']} in {a['seconds']} s")
        if a.get("failure"):
            lines += ["", "```", a["failure"], "```", ""]
    lines += ["", "## Timeline (seconds from the start)", "", "| t | container | status | health | restarts | exit |", "|---|---|---|---|---|---|"]
    for e in r["events"][:200]:
        lines.append(f"| {e.get('t')} | {e.get('name')} | {e.get('status', e.get('error', ''))} | {e.get('health', '')} | {e.get('restarts', '')} | {e.get('exit', '')} |")
    open(f"{RESULTS}/{r['test']}.md", "w").write("\n".join(lines) + "\n")


def write_summary():
    rows = []
    for f in sorted(os.listdir(RESULTS)) if os.path.isdir(RESULTS) else []:
        if f.endswith(".json") and not f.endswith(".reboot.json"):
            r = json.load(open(f"{RESULTS}/{f}"))
            ok = all(a["ok"] for a in r["actions"])
            reboot = ""
            rb = f"{RESULTS}/{r['test']}.reboot.json"
            if os.path.exists(rb):
                x = json.load(open(rb))
                reboot = f"{x['converged_after_s']} s, {x['consumer_restarts']} restarts"
            rows.append(
                f"| {r['test']} | {r['description']} | {ok} | {r['api_after_postgres']} | {r['web_after_api_and_cache']} | "
                f"{r['converged_after_s']} | {sum(v for k_, v in r['restarts'].items() if k_ in ('api/api', 'web/web'))} | "
                f"{r['missing_dependency_lines']} | {r['stack_states']} | {reboot} |"
            )
    head = (
        f"# PoC summary, Komodo {VERSION}\n\n"
        "| test | construct | Komodo ops ok | api after postgres | web after api+cache | healthy after s | consumer restarts | missing lines | Stack states | after reboot |\n"
        "|---|---|---|---|---|---|---|---|---|---|\n"
    )
    open(f"{RESULTS}/summary.md", "w").write(head + "\n".join(rows) + "\n")
    print(head + "\n".join(rows))


# ---------------------------------------------------------------- Commands


def prepare():
    k = Komodo()
    k.wait_ready()
    os.makedirs(RESULTS, exist_ok=True)
    tools = sh(
        [
            "docker", "exec", "poc-periphery", "sh", "-c",
            "for t in sh bash curl wget nc python3 docker git sops; do printf '%s: ' $t; command -v $t || echo MISSING; done; "
            "docker compose version; docker version --format 'server {{.Server.Version}}'; cat /etc/os-release | head -2",
        ],
        check=False,
    )
    open(f"{RESULTS}/periphery-tools.txt", "w").write(tools)
    print(tools)
    try:
        print("core:", k.call("read", "GetVersion"))
    except Exception as e:
        print("core version:", e)


def run(tests):
    k = Komodo()
    k.wait_ready()
    for t in tests:
        run_test(t, k, 90 if t == "T0" else 300)
    write_summary()


def after_reboot(name):
    deadline = time.time() + 300
    t0 = time.time()
    while True:
        try:
            sh(["docker", "info"], timeout=20)
            break
        except Exception:
            if time.time() > deadline:
                raise
            time.sleep(2)
    mon = Monitor()
    mon.start()
    converged_at = None
    while time.time() < deadline:
        try:
            if converged():
                converged_at = round(time.time() - t0, 1)
                break
        except Exception:
            pass
        time.sleep(2)
    time.sleep(2)
    mon.halt.set()
    mon.join()
    final = snapshot()
    out = {
        "test": name,
        "converged_after_s": converged_at,
        "consumer_restarts": sum(v["restarts"] for n, v in final.items() if n in ("api/api", "web/web")),
        "final": final,
        "events": mon.events,
    }
    json.dump(out, open(f"{RESULTS}/{name}.reboot.json", "w"), indent=1)
    print(json.dumps({k_: v for k_, v in out.items() if k_ != "events"}, indent=1))
    write_summary()


if __name__ == "__main__":
    cmd, args = (sys.argv[1], sys.argv[2:]) if len(sys.argv) > 1 else ("", [])
    if cmd == "prepare":
        prepare()
    elif cmd == "run":
        run(args)
    elif cmd == "after-reboot":
        after_reboot(args[0])
    elif cmd == "summary":
        write_summary()
    else:
        sys.exit(__doc__)
