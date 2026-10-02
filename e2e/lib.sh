# Helpers for commands run inside the runner container (see inside.sh).
# NET is the first three octets of the test network.

GITEA="http://$NET.200:3000"
KOMODO="http://$NET.201:9120"

cred() { # cred <key>: a value from the generated credentials file
  sed -n "s/^$1: //p" /work/home/.urbex/credentials.yaml
}

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# expect_http <url> <substring> [timeout-seconds]: poll until the
# response contains the substring.
expect_http() {
  _deadline=$(( $(date +%s) + ${3:-120} ))
  while :; do
    _out=$(curl -s -m 5 "$1" || true)
    case "$_out" in *"$2"*) echo "ok: $1 -> $_out"; return 0 ;; esac
    [ "$(date +%s)" -ge "$_deadline" ] && fail "$1 never answered with '$2' (last: '$_out')"
    sleep 2
  done
}

# expect_status <url> <code> [timeout-seconds]
expect_status() {
  _deadline=$(( $(date +%s) + ${3:-120} ))
  while :; do
    _code=$(curl -s -m 5 -o /dev/null -w '%{http_code}' "$1" || true)
    [ "$_code" = "$2" ] && { echo "ok: $1 -> $_code"; return 0; }
    [ "$(date +%s)" -ge "$_deadline" ] && fail "$1 never answered $2 (last: $_code)"
    sleep 2
  done
}

gitea_api() { # gitea_api <method> <path> [json]
  curl -s -X "$1" "$GITEA/api/v1$2" -H "authorization: token $(cred giteaToken)" \
    -H 'content-type: application/json' ${3:+-d "$3"}
}

komodo_names() { # komodo_names <Servers|Builds|Deployments|Procedures>
  curl -s -X POST "$KOMODO/read/List$1" -H "x-api-key: $(cred komodoApiKey)" \
    -H "x-api-secret: $(cred komodoApiSecret)" -H 'content-type: application/json' -d '{}' |
    python3 -c 'import sys,json; print(" ".join(sorted(x["name"] for x in json.load(sys.stdin))))'
}

git_remote() { # the app repo's clone URL on Gitea, with credentials
  echo "http://urbex-admin:$(cred giteaToken)@$NET.200:3000/urbex/hello.git"
}

gitops_remote() { # the GitOps repo's clone URL on Gitea, with credentials
  echo "http://urbex-admin:$(cred giteaToken)@$NET.200:3000/urbex/gitops.git"
}

# push_main <new-version>: what a developer does on a trunk-based
# project, with no urbex involved - change the app (here, its version)
# and push to main.
push_main() {
  rm -rf /work/dev && git clone -q -b main "$(git_remote)" /work/dev
  (
    cd /work/dev
    sed -i "s/^const version = .*/const version = \"$1\"/" main.go
    git commit -qam "App $1"
    git push -q origin main 2>/dev/null
  )
  echo "pushed $1 to main"
}

# push_tag <tag>: release by hand - tag main's head and push the tag.
push_tag() {
  (
    cd /work/dev
    git tag -a "$1" -m "Release $1"
    git push -q origin "$1" 2>/dev/null
  )
  echo "pushed tag $1"
}

# expect_image <name> <tag> [timeout-seconds]: poll Gitea's registry.
expect_image() {
  _deadline=$(( $(date +%s) + ${3:-180} ))
  while :; do
    _res=$(gitea_api GET "/packages/urbex/container/$1/$2")
    case "$_res" in *'"version"'*) echo "ok: image $1:$2 is in the registry"; return 0 ;; esac
    [ "$(date +%s)" -ge "$_deadline" ] && fail "image $1:$2 never appeared in the registry"
    sleep 2
  done
}

# unchanged_http <url> <substring> <seconds>: the response must keep
# containing the substring for that long.
unchanged_http() {
  _deadline=$(( $(date +%s) + $3 ))
  while [ "$(date +%s)" -lt "$_deadline" ]; do
    _out=$(curl -s -m 5 "$1" || true)
    case "$_out" in *"$2"*) ;; *) fail "$1 changed to '$_out' (expected to keep '$2')" ;; esac
    sleep 3
  done
  echo "ok: $1 still -> $_out"
}

komodo_api() { # komodo_api <read|write|execute>/<Type> <json>
  curl -s -X POST "$KOMODO/$1" -H "x-api-key: $(cred komodoApiKey)" \
    -H "x-api-secret: $(cred komodoApiSecret)" -H 'content-type: application/json' -d "$2"
}

# loki_lines <logql> [text]: how many log lines Loki has for the query
# (in the last hour), containing text if given.
loki_lines() {
  curl -s -G "http://$NET.204:3100/loki/api/v1/query_range" \
    --data-urlencode "query=$1" --data-urlencode "limit=1000" |
    python3 -c '
import json, sys
text = sys.argv[1]
result = json.load(sys.stdin)["data"]["result"]
print(sum(1 for s in result for _, line in s["values"] if text in line))
' "${2:-}"
}

# prom_series <promql>: how many series Prometheus returns for the query.
prom_series() {
  curl -s -G "http://$NET.204:9090/api/v1/query" --data-urlencode "query=$1" |
    python3 -c 'import json, sys; print(len(json.load(sys.stdin)["data"]["result"]))'
}

# komodo_tags <Kind> <name>: the names of a Komodo resource's tags, sorted.
komodo_tags() {
  python3 - "$1" "$2" "$KOMODO" "$(cred komodoApiKey)" "$(cred komodoApiSecret)" <<'PY'
import json, sys, urllib.request
kind, name, url, key, secret = sys.argv[1:]
def call(op, body):
    req = urllib.request.Request(f"{url}/read/{op}", json.dumps(body).encode(),
        {"x-api-key": key, "x-api-secret": secret, "content-type": "application/json"})
    return json.load(urllib.request.urlopen(req))
tags = {t["_id"]["$oid"]: t["name"] for t in call("ListTags", {})}
for r in call("List" + kind + "s", {}):
    if r["name"] == name:
        print(" ".join(sorted(tags.get(t, t) for t in r.get("tags", []))))
PY
}

# keycloak_realm <realm>: the HTTP status of the realm's admin API (200 if it exists).
keycloak_realm() {
  token=$(curl -s -X POST "http://$NET.203:8080/realms/master/protocol/openid-connect/token" \
    -d grant_type=password -d client_id=admin-cli -d username=admin --data-urlencode "password=$(cred keycloakAdminPassword)" |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')
  curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" "http://$NET.203:8080/admin/realms/$1"
}
