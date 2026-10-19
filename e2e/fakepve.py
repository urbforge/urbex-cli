"""Stub of the Proxmox API calls urbex makes itself: listing a node's
containers (it answers with the names in /work/pve-containers.txt, which
the scenario keeps in step with the machines that exist), and an LXC's
mount points: its configuration, new mount points, resizes and pending
changes (kept in /work/pve-lxc-<vmid>.json). New mount points apply at
once, as on a running LXC that can hotplug them: there is nothing to
restart."""
import http.server
import json
import os
import re
import urllib.parse

LXC = re.compile(r"^/api2/json/nodes/[^/]+/lxc/(\d+)/(config|resize|pending)$")


def load(vmid):
    try:
        with open(f"/work/pve-lxc-{vmid}.json") as f:
            return json.load(f)
    except FileNotFoundError:
        return {}


def save(vmid, config):
    with open(f"/work/pve-lxc-{vmid}.json", "w") as f:
        json.dump(config, f)


class Handler(http.server.BaseHTTPRequestHandler):
    def reply(self, data):
        body = json.dumps({"data": data}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body)

    def form(self):
        length = int(self.headers.get("Content-Length") or 0)
        return {k: v[0] for k, v in urllib.parse.parse_qs(self.rfile.read(length).decode()).items()}

    def do_GET(self):
        m = LXC.match(self.path)
        if m and m.group(2) == "config":
            return self.reply(load(m.group(1)))
        if m and m.group(2) == "pending":
            return self.reply([])
        try:
            with open("/work/pve-containers.txt") as f:
                names = [line.strip() for line in f if line.strip()]
        except FileNotFoundError:
            names = []
        self.reply([{"name": n} for n in names])

    def do_PUT(self):
        m = LXC.match(self.path)
        if not m:
            return self.reply(None)
        vmid, what = m.group(1), m.group(2)
        form, config = self.form(), load(m.group(1))
        if what == "config":
            for key, value in form.items():
                # "local-zfs:5,mp=/srv/volumes/web/data,backup=1" becomes the
                # volume Proxmox allocates, with its size.
                storage, _, rest = value.partition(":")
                size, _, options = rest.partition(",")
                config[key] = f"{storage}:vm-{vmid}-disk-{key[2:]},{options},size={size}G"
        else:
            disk, size = form["disk"], form["size"]
            config[disk] = re.sub(r"size=\d+G", f"size={size}", config[disk])
        save(vmid, config)
        self.reply(None)

    def log_message(self, *args):
        pass


http.server.HTTPServer(("127.0.0.1", 8006), Handler).serve_forever()
