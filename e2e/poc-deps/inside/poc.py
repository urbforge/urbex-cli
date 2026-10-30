#!/usr/bin/env python3
"""Driver of the dependency PoC. It runs inside the machine, next to Komodo.

  poc.py prepare           wait for Komodo, note what Periphery has
  poc.py run G1 G2 ...     run the graph tests (see graph.py)
  poc.py summary           rebuild summary.md from the results

Every test starts from nothing (its Stacks destroyed and deleted), creates
the Stacks with the construct the test is about, deploys them, and watches
the containers every second: when each one runs, when it is healthy, how many
times it restarted.
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


def logs_count(word):
    n = 0
    for v in snapshot().values():
        r = subprocess.run(["docker", "logs", v["container"]], capture_output=True, text=True)
        n += (r.stdout + r.stderr).count(word)
    return n


def clean(k):
    """Destroys the PoC's Stacks and removes what is left of their containers."""
    for i in k.call("read", "ListStacks"):
        if i["name"].startswith("poc-"):
            try:
                k.execute("DestroyStack", {"stack": i["name"]}, 300)
            except Exception:
                pass
            k.delete("Stack", i["name"])
    for c in sh(["docker", "ps", "-aq", "--filter", "label=com.docker.compose.project"]).split():
        proj = sh(["docker", "inspect", "-f", '{{index .Config.Labels "com.docker.compose.project"}}', c]).strip()
        if proj.startswith("poc-"):
            sh(["docker", "rm", "-f", c], check=False)


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
    import graph

    k = Komodo()
    k.wait_ready()
    for t in tests:
        graph.run_test(t, k)
    write_summary()


def write_summary():
    import graph

    text = graph.write_summary()
    os.makedirs(RESULTS, exist_ok=True)
    open(f"{RESULTS}/summary.md", "w").write(text)
    print(text)


if __name__ == "__main__":
    cmd, args = (sys.argv[1], sys.argv[2:]) if len(sys.argv) > 1 else ("", [])
    if cmd == "prepare":
        prepare()
    elif cmd == "run":
        run(args)
    elif cmd == "summary":
        write_summary()
    else:
        sys.exit(__doc__)
