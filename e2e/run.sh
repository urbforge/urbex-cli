#!/usr/bin/env bash
# End-to-end test of the urbex CLI: runs the real binary through
# bootstrap, apply, continuous deployment of main to staging, release,
# promote, secrets, pinning, rollback, destroy and re-apply, against
# containers standing in for Proxmox LXCs. See e2e/README.md.
#
# usage: e2e/run.sh [--keep]    (--keep leaves the machines running)
#
# E2E_STOP_AFTER=N stops after step N and leaves the machines running -
# a platform to experiment on, or to look at the state a step left.
set -euo pipefail

cd "$(dirname "$0")/.."
REPO=$PWD
WORK=$REPO/e2e/.work
NETWORK=${E2E_NETWORK:-urbex-e2e}
NET=${E2E_NET:-10.89.77} # first three octets of the /24 the machines live in
KEEP=false
[ "${1:-}" = "--keep" ] && KEEP=true

command -v podman >/dev/null || { echo "e2e/run.sh needs podman" >&2; exit 1; }

BASE_MACHINES=(urbex-gitea urbex-komodo urbex-technitium urbex-keycloak urbex-observability)

log() { printf '\n== %s\n' "$*"; }

machine() { # machine <hostname> <last-octet>: (re)create a fake LXC
  local name=e2e-$1
  podman rm -f "$name" >/dev/null 2>&1 || true
  podman volume rm -f "$name-docker" "$name-containerd" >/dev/null 2>&1 || true
  # Docker's data lives on volumes: overlayfs can't nest on the
  # container's own overlay root.
  podman run -d --name "$name" --hostname "$1" --privileged --systemd=always \
    --network "$NETWORK" --ip "$NET.$2" \
    -v "$name-docker:/var/lib/docker" -v "$name-containerd:/var/lib/containerd" \
    urbex-e2e-machine >/dev/null
  # Technitium first, then the network's resolver - as Terraform sets up
  # a real LXC. Not with --dns: podman's resolver would forward to
  # Technitium, which forwards back to it.
  podman exec "$name" sh -c "printf 'nameserver $NET.202\nnameserver $NET.1\n' >/etc/resolv.conf"
}

teardown() {
  local names
  names=$(podman ps -aq --filter "name=^e2e-") || true
  [ -n "$names" ] && podman rm -f $names >/dev/null 2>&1 || true
  names=$(podman volume ls -q --filter "name=^e2e-") || true
  [ -n "$names" ] && podman volume rm -f $names >/dev/null 2>&1 || true
  podman network rm -f "$NETWORK" >/dev/null 2>&1 || true
}

# step <title> <commands>: run commands in the runner container, keeping
# the (long) output in a log that is only shown if the step fails.
STEP=0
step() {
  STEP=$((STEP + 1))
  local logfile
  logfile=$WORK/logs/$(printf '%02d' $STEP).log
  log "$1"
  if podman run --rm --network "$NETWORK" -e NET="$NET" \
    -v "$WORK:/work:z" -v "$REPO/e2e:/e2e:ro,z" \
    urbex-e2e-runner sh /e2e/inside.sh "$2" >"$logfile" 2>&1; then
    # The lines worth seeing: assertions and urbex's own summaries, not
    # Ansible's per-task "ok: [host]".
    grep -E '^(ok: [^[]|pushed |merged |Deployed |Released |Releasing |  [a-z]+ +(http|[0-9])|[a-z]+: .* -> |Nothing|Komodo server|Destroyed|Created |Set |Removed |staging )' "$logfile" || true
    if [ "${E2E_STOP_AFTER:-}" = "$STEP" ]; then
      log "Stopped after step $STEP as asked; machines left running (podman ps --filter name=e2e-)"
      exit 0
    fi
  else
    echo "--- step failed; last lines of $logfile:"
    tail -n 60 "$logfile"
    $KEEP || teardown
    exit 1
  fi
}

# ---------------------------------------------------------------- setup
log "Preparing (work dir: $WORK)"
teardown
rm -rf "$WORK"
mkdir -p "$WORK/bin" "$WORK/home" "$WORK/gitops" "$WORK/logs"

ssh-keygen -q -t ed25519 -N "" -C urbex-e2e -f "$WORK/id_e2e"

# Versions come from mise.toml, like the developer's and the operator's.
pinned() { sed -n "s/^$1 = \"\(.*\)\"\$/\1/p" "$REPO/mise.toml"; }
if command -v go >/dev/null; then
  CGO_ENABLED=0 go build -o "$WORK/bin/urbex" ./cmd/urbex
else
  podman run --rm -v "$REPO:/src:z" -v urbex-e2e-gomod:/go/pkg/mod -w /src \
    "docker.io/library/golang:$(pinned go)" sh -c 'CGO_ENABLED=0 go build -o e2e/.work/bin/urbex ./cmd/urbex'
fi

podman build -q -t urbex-e2e-machine --build-arg "PUBKEY=$(cat "$WORK/id_e2e.pub")" e2e/machine >/dev/null 2>&1
podman build -q -t urbex-e2e-runner --build-arg "MISE_VERSION=$(pinned min_version)" \
  -f e2e/runner/Containerfile "$REPO" >/dev/null 2>&1
podman network create --subnet "$NET.0/24" "$NETWORK" >/dev/null
podman run --rm -v "$WORK:/work:z" urbex-e2e-runner age-keygen -o /work/age.key 2>/dev/null

# The app repo, as a developer has it locally: one commit on main.
cp -r e2e/app "$WORK/app"
git -C "$WORK/app" init -q -b main
git -C "$WORK/app" add -A
git -C "$WORK/app" -c user.email=e2e@urbex.test -c user.name=urbex-e2e commit -qm "hello v1"

# Machines at the addresses urbex allocates: base services from .200,
# project services from .210 in the order they are first applied.
octet=200
for m in "${BASE_MACHINES[@]}"; do
  machine "$m" $octet
  octet=$((octet + 1))
done
machine hello-api-staging 210
machine hello-api-prod 211

# ------------------------------------------------------------- scenario
step "bootstrap: scaffold urbex.platform.yaml" '
  urbex bootstrap
  test -f /work/gitops/urbex.platform.yaml'

cat >"$WORK/gitops/urbex.platform.yaml" <<EOF
domain: urbex.test
git:
  giteaUrl: http://$NET.200:3000
  org: urbex
proxmox:
  apiUrl: http://127.0.0.1:8006/api2/json
  node: pve
  storagePool: local-zfs
  lxcTemplate: local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst
  sshPublicKey: "$(cat "$WORK/id_e2e.pub")"
  vmIdBase: 9000
  pool: urbex
  keyctl: false
  network:
    cidr: $NET.0/24
    gateway: $NET.1
    baseHostOffset: 200
EOF

step "bootstrap: base services and platform setup" '
  urbex bootstrap
  expect_status $GITEA/api/v1/version 200
  expect_status $KOMODO/ 200
  expect_status http://$NET.202:5380/ 200
  expect_status http://$NET.203:8080/ 302
  expect_status http://$NET.204:3000/api/health 200
  expect_status http://$NET.204:9090/-/ready 200
  expect_status http://$NET.204:3100/ready 200 180
  [ "$(stat -c %a /work/home/.urbex/credentials.yaml)" = 600 ] || fail "credentials file is not mode 600"
  [ -n "$(cred komodoOnboardingKey)" ] || fail "no onboarding key was generated"
  [ -n "$(cred technitiumToken)" ] || fail "no technitium api token was generated"
  curl -s "$TECHNITIUM/api/zones/list?token=$(cred technitiumToken)" | grep -q '"urbex.urbex.test"' || fail "technitium has no urbex.urbex.test zone"
  [ "$(technitium_record git.urbex.urbex.test)" = "$NET.200" ] || fail "no internal dns record for gitea"
  [ "$(technitium_record auth.urbex.urbex.test)" = "$NET.203" ] || fail "no internal dns record for keycloak"
  [ "$(technitium_resolve loki.urbex.urbex.test)" = "$NET.204" ] || fail "technitium does not resolve loki by its internal name"
  # The first forwarded lookups can time out while Technitium warms up.
  for _ in $(seq 10); do [ -n "$(technitium_resolve deb.debian.org)" ] && break; sleep 3; done
  [ -n "$(technitium_resolve deb.debian.org)" ] || fail "technitium does not resolve public names through its forwarders"
  servers="urbex-gitea urbex-keycloak urbex-komodo urbex-observability urbex-technitium"
  [ "$(komodo_names Servers)" = "$servers" ] || fail "unexpected komodo servers: $(komodo_names Servers)"
  for name in $servers; do
    [ "$(komodo_tags Server $name)" = platform ] || fail "komodo server $name should be tagged platform, has: $(komodo_tags Server $name)"
  done
  git -C /work/gitops log --oneline | grep -q "urbex bootstrap" || fail "gitops repo was not committed"
  echo "ok: platform is up"'

# From here on Proxmox "has" the base services, as after a real apply.
printf '%s\n' "${BASE_MACHINES[@]}" >"$WORK/pve-containers.txt"
step "bootstrap again: idempotent" '
  urbex bootstrap >/tmp/again.log 2>&1 || { cat /tmp/again.log; fail "second bootstrap failed"; }
  cat /tmp/again.log
  if grep -E "changed=[1-9]" /tmp/again.log; then fail "a second bootstrap changed something"; fi
  echo "ok: second bootstrap changed nothing"'

echo hello-api-staging >>"$WORK/pve-containers.txt"
step "apply staging: it follows main, and runs main's head right away" '
  urbex apply staging >/tmp/apply.log 2>&1 || { cat /tmp/apply.log; fail "apply staging failed"; }
  cat /tmp/apply.log
  grep -q "staging follows main" /tmp/apply.log || fail "apply should say staging follows main"
  expect_http http://$NET.210:8080/ "hello v1 from urbex (staging)" 5
  for f in compose.yaml config.env secrets.sops.env version.env; do
    test -f /work/gitops/environments/staging/hello/api/$f || fail "missing environments/staging/hello/api/$f"
  done
  grep -q "^TRACK=main$" /work/gitops/environments/staging/hello/api/version.env || fail "staging does not follow main"
  [ "$(komodo_names Builds)" = "hello-api" ] || fail "unexpected builds: $(komodo_names Builds)"
  urbex status staging | grep -E "version=[0-9a-f]{7}\(follows-main\) stack=running" || fail "status should show staging following main"
  [ "$(komodo_tags Stack hello-api-staging)" = "app hello staging" ] || fail "stack tags: $(komodo_tags Stack hello-api-staging)"
  [ "$(komodo_tags Build hello-api)" = "app hello" ] || fail "build tags: $(komodo_tags Build hello-api)"
  [ "$(keycloak_realm hello-staging)" = 200 ] || fail "no Keycloak realm for the project in staging"
  [ "$(technitium_record api.staging.hello.urbex.urbex.test)" = "$NET.210" ] || fail "no internal dns record for staging api"
  urbex status staging | grep -F "dns=api.staging.hello.urbex.urbex.test" | grep -qv "(missing)\|(points to" || fail "status should show the staging api internal dns name"
  ! grep -rnE "([0-9]{1,3}[.]){3}[0-9]{1,3}" /work/gitops/environments || fail "the environments in the gitops repo hold addresses, not names"
  grep -q "image: git.urbex.urbex.test:3000/urbex/hello-api" /work/gitops/environments/staging/hello/api/compose.yaml || fail "the image should come from the registry by name"
  urbex status | grep -F "Gitea" | grep -F "http://git.urbex.urbex.test:3000" | grep -F "http://$NET.200:3000" | grep -qv "(missing)\|(points to" || fail "status should show gitea by internal name and LAN address, with its record"
  echo "ok: staging follows main"'

step "a push to main is deployed to staging, with no command" '
  push_main v2
  expect_http http://$NET.210:8080/ "hello v2 from urbex (staging)" 240
  git -C /work/gitops pull -q "$(gitops_remote)" main 2>/dev/null
  git -C /work/gitops log -1 --format=%s | grep -q "hello main@" || fail "the deploy is not a commit to the gitops repo"'

step "logs and resource metrics reach the observability stack, tagged platform or app" '
  for _ in $(seq 1 30); do
    curl -s -o /dev/null http://$NET.210:8080/
    found=$(loki_lines "{kind=\"app\", project=\"hello\", service=\"api\", env=\"staging\"}" hello-app)
    [ "$found" -gt 0 ] && break
    sleep 5
  done
  [ "$found" -gt 0 ] || fail "no log line of hello api (staging) in Loki"
  others=$(loki_lines "{host=\"hello-api-staging\", container!~\"hello-api-staging-.*\"}")
  [ "$others" = 0 ] || fail "only the Stack containers should be shipped, found $others other lines"
  for svc in gitea komodo keycloak observability; do
    [ "$(loki_lines "{kind=\"platform\", service=\"$svc\"}")" -gt 0 ] || fail "no log line of the platform service $svc in Loki"
  done
  for _ in $(seq 1 12); do
    app=$(prom_series "node_memory_MemTotal_bytes{kind=\"app\", project=\"hello\", service=\"api\", env=\"staging\"}")
    platform=$(prom_series "node_memory_MemTotal_bytes{kind=\"platform\"}")
    [ "$app" -gt 0 ] && [ "$platform" -ge 5 ] && break
    sleep 5
  done
  [ "$app" -gt 0 ] || fail "no resource metrics of hello api (staging) in Prometheus"
  [ "$platform" -ge 5 ] || fail "resource metrics of only $platform platform LXCs in Prometheus"
  pass=$(cred grafanaAdminPassword)
  for uid in datasources/uid/urbex-loki datasources/uid/urbex-prometheus dashboards/uid/urbex-logs dashboards/uid/urbex-resources; do
    curl -sf -u "admin:$pass" http://$NET.204:3000/api/$uid >/dev/null || fail "Grafana lacks $uid"
  done
  echo "ok: $found app log lines, platform logs and metrics of $platform base LXCs; Grafana has both dashboards"'

step "Keycloak: a realm per environment, a test user per role, an endpoint for members only" '
  urbex users staging | tee /tmp/users.log
  pass=$(awk "\$1 == \"test-member\" {print \$2; exit}" /tmp/users.log)
  [ -n "$pass" ] || fail "no password for test-member"
  code=$(curl -s -o /dev/null -w "%{http_code}" http://$NET.210:8080/member)
  [ "$code" = 401 ] || fail "/member without a token: $code, want 401"
  token=$(curl -s http://$NET.203:8080/realms/hello-staging/protocol/openid-connect/token \
    -d grant_type=password -d client_id=urbex-test -d username=test-member --data-urlencode "password=$pass" |
    python3 -c "import json,sys; print(json.load(sys.stdin)[\"access_token\"])")
  curl -s -H "Authorization: Bearer $token" http://$NET.210:8080/member | tee /tmp/member.txt
  grep -q "hello member test-member" /tmp/member.txt || fail "test-member was not let in"
  forged="${token%.*}.AAAA"
  code=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $forged" http://$NET.210:8080/member)
  [ "$code" = 401 ] || fail "/member with a forged token: $code, want 401"
  echo "ok: 401 without a token or with a forged one, 200 for test-member"'

step "release: the commit staging runs, tagged - nothing rebuilt" '
  runs=$(komodo_api read/ListUpdates "{}" | python3 -c "import sys,json;print(sum(1 for u in json.load(sys.stdin)[\"updates\"] if u[\"operation\"]==\"RunBuild\"))")
  urbex release >/tmp/release.log 2>&1 || { cat /tmp/release.log; fail "release failed"; }
  cat /tmp/release.log
  grep -q "from the commit staging runs" /tmp/release.log || fail "release should release what staging runs"
  expect_image hello-api 0.1.0 5
  after=$(komodo_api read/ListUpdates "{}" | python3 -c "import sys,json;print(sum(1 for u in json.load(sys.stdin)[\"updates\"] if u[\"operation\"]==\"RunBuild\"))")
  [ "$runs" = "$after" ] || fail "a release must not rebuild ($runs builds before, $after after)"
  echo "ok: 0.1.0 is the image staging runs, retagged"'

step "a tag pushed by hand is a release too" '
  push_main v3
  expect_http http://$NET.210:8080/ "hello v3 from urbex (staging)" 240
  push_tag v0.2.0
  expect_image hello-api 0.2.0'

echo hello-api-prod >>"$WORK/pve-containers.txt"
step "apply prod: empty until promoted; promote: the release staging runs" '
  urbex apply prod >/tmp/apply.log 2>&1 || { cat /tmp/apply.log; fail "apply prod failed"; }
  grep -q "no version to run yet" /tmp/apply.log || fail "a new prod must start empty"
  urbex promote
  expect_http http://$NET.211:8080/ "hello v3 from urbex (prod)" 5
  grep -q "^VERSION=0.2.0$" /work/gitops/environments/prod/hello/api/version.env || fail "prod should run release 0.2.0"
  if grep -q TRACK /work/gitops/environments/prod/hello/api/version.env; then fail "prod must not follow main"; fi
  [ "$(keycloak_realm hello-prod)" = 200 ] || fail "no Keycloak realm for prod"
  [ "$(technitium_record api.hello.urbex.urbex.test)" = "$NET.211" ] || fail "no internal dns record for prod api"
  urbex users prod | grep -q "No test users in prod" || fail "prod must have no test users"
  urbex promote | grep "Nothing to promote" || fail "promote did not notice there is nothing left to promote"'

step "promote refuses what was never released" '
  push_main v4
  expect_http http://$NET.210:8080/ "hello v4 from urbex (staging)" 240
  urbex promote >/tmp/promote.log 2>&1 && fail "promoting an unreleased commit should fail"
  grep -q "not a release" /tmp/promote.log || { cat /tmp/promote.log; fail "unexpected promote error"; }
  expect_http http://$NET.211:8080/ "hello v3 from urbex (prod)" 5'

step "secrets: encrypted in the GitOps repo, decrypted only on the LXC" '
  printf %s "s3cr3t-e2e" | urbex secret set staging api API_TOKEN
  expect_http http://$NET.210:8080/secret "token=s3cr3t-e2e" 90
  [ "$(urbex secret list staging api)" = "API_TOKEN" ] || fail "secret list does not show API_TOKEN"
  if grep -rq "s3cr3t-e2e" /work/gitops; then fail "the secret is in the GitOps repo in plaintext"; fi
  grep -q "^sops_" /work/gitops/environments/staging/hello/api/secrets.sops.env || fail "the secrets file is not sops-encrypted"
  expect_http http://$NET.211:8080/secret "token=" 5
  urbex secret unset staging api API_TOKEN
  for _ in $(seq 1 45); do [ "$(curl -s http://$NET.210:8080/secret)" = "token=" ] && break; sleep 2; done
  [ "$(curl -s http://$NET.210:8080/secret)" = "token=" ] || fail "the secret is still set after unset"
  echo "ok: secret set, delivered, listed, and removed"'

# Pushes made by hand are spaced out: Komodo caches a repo for a few
# seconds after pulling it, and a push landing in that window is only
# picked up by the next one or by the 15-minute reconcile. The urbex
# commands deal with this themselves.
step "configuration edited by hand in the GitOps repo; an unrelated commit restarts nothing" '
  sleep 6
  cd /work/gitops
  git pull -q "$(gitops_remote)" main 2>/dev/null
  sed -i "s/^GREETING_ENV=.*/GREETING_ENV=edited-by-hand/" environments/staging/hello/api/config.env
  git commit -qam "Change staging greeting" && git push -q "$(gitops_remote)" HEAD:main 2>/dev/null
  expect_http http://$NET.210:8080/ "hello v4 from urbex (edited-by-hand)" 90
  sleep 6
  echo "# note" >>README.md && git add README.md && git commit -qm "Unrelated" && git push -q "$(gitops_remote)" HEAD:main 2>/dev/null
  unchanged_http http://$NET.211:8080/ "hello v3 from urbex (prod)" 20'
step "the unrelated commit did not restart staging either" '
  started() { ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -q root@$NET.210 "docker inspect -f {{.State.StartedAt}} hello-api-staging-app-1"; }
  before=$(started); sleep 20; after=$(started)
  [ -n "$before" ] && [ "$before" = "$after" ] || fail "staging was restarted ($before -> $after)"
  echo "ok: staging untouched since $before"'

step "pin staging to a release: main no longer moves it; --follow-main resumes" '
  urbex deploy staging --version 0.1.0 | tee /tmp/pin.log
  grep -q "no longer follows main" /tmp/pin.log || fail "deploy should say staging stopped following main"
  expect_http http://$NET.210:8080/ "hello v2 from urbex" 5
  push_main v5
  unchanged_http http://$NET.210:8080/ "hello v2 from urbex" 45
  urbex deploy staging --follow-main
  expect_http http://$NET.210:8080/ "hello v5 from urbex" 5'

step "rollback: deploying an older release" '
  urbex deploy prod --version 0.1.0
  expect_http http://$NET.211:8080/ "hello v2 from urbex (prod)" 5
  urbex deploy prod --version 9.9.9 >/tmp/bad.log 2>&1 && fail "deploying a release that does not exist should fail"
  grep -q "has no image" /tmp/bad.log || { cat /tmp/bad.log; fail "unexpected error for a missing release"; }
  echo "ok: a missing release is refused"'

step "destroy staging: prod and the project Builds untouched" '
  urbex destroy staging
  [ "$(komodo_names Stacks)" = "hello-api-prod" ] || fail "unexpected stacks left: $(komodo_names Stacks)"
  [ "$(komodo_names Builds)" = "hello-api" ] || fail "the project build should stay while prod exists: $(komodo_names Builds)"
  test ! -e /work/gitops/environments/staging/hello || fail "the staging folder is still in the gitops repo"
  expect_http http://$NET.211:8080/ "hello v2 from urbex (prod)"
  [ -z "$(technitium_record api.staging.hello.urbex.urbex.test)" ] || fail "internal dns record for staging api was not removed"
  echo "ok: staging is gone from komodo, the gitops repo, and technitium"'

# What terraform does for real: the destroyed LXC is gone, and the next
# apply creates a new one at the next free address - with a Periphery
# identity Komodo has never seen under that name.
podman rm -f e2e-hello-api-staging >/dev/null
machine hello-api-staging 212
step "apply staging again on a new machine: it rejoins Komodo and follows main" '
  urbex apply staging
  expect_http http://$NET.212:8080/ "hello v5 from urbex (staging)" 5
  [ "$(technitium_record api.staging.hello.urbex.urbex.test)" = "$NET.212" ] || fail "internal dns record was not re-created at the new address"'

log "PASSED ($STEP steps)"
if $KEEP; then
  echo "Machines left running (podman ps --filter name=e2e-); the next run removes them."
else
  teardown
fi
