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

# merge_change <base-branch> <feature-branch> <new-version>: what a
# developer does, with no urbex involved - branch off, change the app's
# version, push, open a pull request on Gitea, merge it.
merge_change() {
  rm -rf /work/dev && git clone -q -b "$1" "$(git_remote)" /work/dev
  (
    cd /work/dev
    git checkout -q -b "$2"
    sed -i "s/^const version = .*/const version = \"$3\"/" main.go
    git commit -qam "Release $3"
    git push -q origin "$2" 2>/dev/null
  )
  _pr=$(gitea_api POST /repos/urbex/hello/pulls "{\"head\":\"$2\",\"base\":\"$1\",\"title\":\"$3\"}" |
    python3 -c 'import sys,json; print(json.load(sys.stdin)["number"])')
  # Gitea needs a moment to compute mergeability.
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
    _res=$(gitea_api POST "/repos/urbex/hello/pulls/$_pr/merge" '{"Do":"merge"}')
    case "$_res" in *"try again"*) sleep 2 ;; *) break ;; esac
  done
  [ -z "$_res" ] || fail "merging pull request #$_pr: $_res"
  echo "merged pull request #$_pr ($2 -> $1)"
}
