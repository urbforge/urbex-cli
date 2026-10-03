"""Stub of the one Proxmox API call urbex makes itself: listing a node's
containers. It answers with the names in /work/pve-containers.txt, which
the scenario keeps in step with the machines that exist."""
import http.server
import json


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        try:
            with open("/work/pve-containers.txt") as f:
                names = [line.strip() for line in f if line.strip()]
        except FileNotFoundError:
            names = []
        body = json.dumps({"data": [{"name": n} for n in names]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


http.server.HTTPServer(("127.0.0.1", 8006), Handler).serve_forever()
