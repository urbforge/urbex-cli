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
  # For the runner container: Linux, on podman's architecture.
  CGO_ENABLED=0 GOOS=linux GOARCH=$(podman info --format '{{.Host.Arch}}') go build -o "$WORK/bin/urbex" ./cmd/urbex
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

# A second stack, of a published image only, in a repo of its own.
mkdir -p "$WORK/site"
cp e2e/images/urbex.yaml "$WORK/site/"
git -C "$WORK/site" init -q -b main
git -C "$WORK/site" add -A
git -C "$WORK/site" -c user.email=e2e@urbex.test -c user.name=urbex-e2e commit -qm "site"

# A third stack, of applications linked together, in a repo of its own.
mkdir -p "$WORK/shop"
cp e2e/linked/urbex.yaml "$WORK/shop/"
git -C "$WORK/shop" init -q -b main
git -C "$WORK/shop" add -A
git -C "$WORK/shop" -c user.email=e2e@urbex.test -c user.name=urbex-e2e commit -qm "shop"

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
  network:
    cidr: $NET.0/24
    gateway: $NET.1
    baseHostOffset: 200
EOF
# On macOS the gateway's DNS is gvproxy's, which answers NXDOMAIN when
# the Mac's resolver fails for a moment: Technitium caches that for five
# minutes, and image pulls fail. Public resolvers answer for themselves.
if [ "$(uname -s)" = Darwin ]; then
  printf 'dns:\n  forwarders: [1.1.1.1, 9.9.9.9]\n' >>"$WORK/gitops/urbex.platform.yaml"
fi

step "bootstrap: base services and platform setup" '
  urbex bootstrap
  expect_status $GITEA/api/v1/version 200
  expect_status $KOMODO/ 200
  expect_status http://$NET.202:5380/ 200
  expect_status http://$NET.203:8080/ 302
  expect_status http://$NET.204:3000/api/health 200
  expect_status http://$NET.204:9090/-/ready 200
  # Loki answers ready as soon as its ring is: one look is enough, with
  # no wait that only starts at the first look (urbforge/urbex-cli#69).
  _deadline=$(( $(date +%s) + 180 ))
  until [ "$(curl -s -m 5 -o /tmp/loki-ready -w %{http_code} http://$NET.204:3100/ready)" = 200 ]; do
    grep -q "after being ready" /tmp/loki-ready && fail "Loki waits after its first /ready: $(cat /tmp/loki-ready)"
    [ "$(date +%s)" -ge "$_deadline" ] && fail "Loki /ready never answered 200: $(cat /tmp/loki-ready)"
    sleep 2
  done
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
  # Komodo adopted the bootstrap services as Stacks with their compose
  # files on the servers (ADR-0027); copies for review in the GitOps repo,
  # without secrets.
  for svc in technitium gitea komodo keycloak observability; do
    state=""
    for _ in $(seq 1 12); do
      state=$(komodo_stack_state urbex-$svc)
      [ "$state" = running ] && break
      sleep 5
    done
    [ "$state" = running ] || fail "komodo stack urbex-$svc should be running, is: ${state:-missing}"
    [ "$(komodo_tags Stack urbex-$svc)" = platform ] || fail "stack urbex-$svc tags: $(komodo_tags Stack urbex-$svc)"
    [ -f /work/gitops/platform/$svc/compose.yaml ] || fail "no $svc compose file in the gitops repo"
  done
  # Keycloak and the observability stack come from the GitOps repo, their
  # secrets encrypted there.
  # The telemetry agent of every LXC is a Stack of its own, kind agent.
  for host in urbex-gitea urbex-komodo urbex-technitium urbex-keycloak urbex-observability; do
    [ "$(komodo_stack_state urbex-telemetry-$host)" = running ] || fail "telemetry stack of $host: $(komodo_stack_state urbex-telemetry-$host)"
    [ "$(komodo_tags Stack urbex-telemetry-$host)" = agent ] || fail "telemetry stack of $host tags: $(komodo_tags Stack urbex-telemetry-$host)"
  done
  for svc in keycloak observability; do
    grep -q "^sops_" /work/gitops/platform/$svc/secrets.sops.env || fail "platform/$svc/secrets.sops.env is not sops-encrypted"
  done
  # No generated secret is in plaintext anywhere under platform/.
  for name in keycloakAdminPassword keycloakDbPassword grafanaAdminPassword technitiumAdminPassword komodoDbPassword komodoAdminPassword giteaAdminPassword; do
    value=$(cred $name)
    [ -n "$value" ] || fail "no $name in the credentials"
    ! grep -rqF "$value" /work/gitops/platform || fail "$name in plaintext in the gitops repo: $(grep -rlF "$value" /work/gitops/platform)"
  done
  echo "ok: platform is up"'

# From here on Proxmox "has" the base services, as after a real apply.
printf '%s\n' "${BASE_MACHINES[@]}" >"$WORK/pve-containers.txt"
step "bootstrap again: idempotent" '
  urbex bootstrap >/tmp/again.log 2>&1 || { cat /tmp/again.log; fail "second bootstrap failed"; }
  cat /tmp/again.log
  if grep -E "changed=[1-9]" /tmp/again.log; then fail "a second bootstrap changed something"; fi
  # Its progress, phase by phase: the template is not on the fake
  # Proxmox, and there is no Cloudflare.
  urbex status --progress | tee /tmp/progress.txt
  grep -q "^urbex bootstrap: done in" /tmp/progress.txt || fail "status --progress should show the bootstrap done"
  grep -q "✓ Install Gitea and Komodo" /tmp/progress.txt || fail "status --progress should show the parallel play done"
  grep -q "skipped: no Cloudflare" /tmp/progress.txt || fail "status --progress should show Cloudflare skipped"
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
  urbex status --progress | tee /tmp/progress.txt
  grep -q "^urbex apply staging (hello): done in" /tmp/progress.txt && grep -q "✓ Build main" /tmp/progress.txt || fail "status --progress should show the apply done, main built"
  [ "$(komodo_tags Stack hello-api-staging)" = "app hello staging" ] || fail "stack tags: $(komodo_tags Stack hello-api-staging)"
  [ "$(komodo_tags Build hello-api)" = "app hello" ] || fail "build tags: $(komodo_tags Build hello-api)"
  [ "$(keycloak_realm hello-staging)" = 200 ] || fail "no Keycloak realm for the project in staging"
  [ "$(technitium_record api.staging.hello.urbex.urbex.test)" = "$NET.210" ] || fail "no internal dns record for staging api"
  urbex status staging | grep -F "dns=api.staging.hello.urbex.urbex.test" | grep -qv "(missing)\|(points to" || fail "status should show the staging api internal dns name"
  # Only the issuer may be an address: without Cloudflare it is what the
  # users browsers reach Keycloak at, not how a service reaches it.
  ! grep -rnE "([0-9]{1,3}[.]){3}[0-9]{1,3}" /work/gitops/environments | grep -v KEYCLOAK_ISSUER || fail "the environments in the gitops repo hold addresses, not names"
  grep -q "image: git.urbex.urbex.test:3000/urbex/hello-api" /work/gitops/environments/staging/hello/api/compose.yaml || fail "the image should come from the registry by name"
  urbex status | grep -F "Gitea" | grep -F "http://git.urbex.urbex.test:3000" | grep -F "http://$NET.200:3000" | grep -qv "(missing)\|(points to" || fail "status should show gitea by internal name and LAN address, with its record"
  echo "ok: staging follows main"'

step "a push to main is deployed to staging, with no command" '
  push_main v2
  expect_http http://$NET.210:8080/ "hello v2 from urbex (staging)" 240
  git -C /work/gitops pull -q "$(gitops_remote)" main 2>/dev/null
  git -C /work/gitops log -1 --format=%s | grep -q "hello main@" || fail "the deploy is not a commit to the gitops repo"'

step "logs and resource metrics reach the observability stack, tagged platform, app or agent" '
  for _ in $(seq 1 30); do
    curl -s -o /dev/null http://$NET.210:8080/
    found=$(loki_lines "{kind=\"app\", project=\"hello\", service=\"api\", env=\"staging\"}" hello-app)
    [ "$found" -gt 0 ] && break
    sleep 5
  done
  [ "$found" -gt 0 ] || fail "no log line of hello api (staging) in Loki"
  others=$(loki_lines "{host=\"hello-api-staging\", kind=\"app\", container!~\"hello-api-staging-.*\"}")
  [ "$others" = 0 ] || fail "only the Stack containers are the app, found $others app lines from others"
  agents=$(loki_lines "{host=\"hello-api-staging\", kind=\"agent\"}")
  [ "$agents" -gt 0 ] || fail "no log line of the agents (Periphery, Alloy) of hello api (staging), as agent"
  for svc in gitea komodo keycloak grafana; do
    [ "$(loki_lines "{kind=\"platform\", service=\"$svc\"}")" -gt 0 ] || fail "no log line of the platform service $svc in Loki"
  done
  for _ in $(seq 1 12); do
    app=$(prom_series "node_memory_MemTotal_bytes{kind=\"app\", project=\"hello\", service=\"api\", env=\"staging\"}")
    platform=$(prom_series "node_memory_MemTotal_bytes{kind=\"platform\"}")
    container=$(prom_series "container_memory_working_set_bytes{kind=\"app\", project=\"hello\", service=\"api\", env=\"staging\"}")
    agent=$(prom_series "container_memory_working_set_bytes{kind=\"agent\", host=\"hello-api-staging\"}")
    [ "$app" -gt 0 ] && [ "$platform" -ge 5 ] && [ "$container" -gt 0 ] && [ "$agent" -gt 0 ] && break
    sleep 5
  done
  [ "$app" -gt 0 ] || fail "no resource metrics of hello api (staging) in Prometheus"
  [ "$platform" -ge 5 ] || fail "resource metrics of only $platform platform LXCs in Prometheus"
  [ "$container" -gt 0 ] || fail "no per-container metrics of hello api (staging), as app"
  [ "$agent" -gt 0 ] || fail "no per-container metrics of the agents of hello api (staging), as agent"
  nameless=$(prom_series "container_memory_working_set_bytes{container=\"\"}")
  [ "$nameless" = 0 ] || fail "$nameless per-container series without a container: the LXC'"'"'s own cgroup must be dropped"
  pass=$(cred grafanaAdminPassword)
  for uid in datasources/uid/urbex-loki datasources/uid/urbex-prometheus dashboards/uid/urbex-logs dashboards/uid/urbex-resources; do
    curl -sf -u "admin:$pass" http://$NET.204:3000/api/$uid >/dev/null || fail "Grafana lacks $uid"
  done
  echo "ok: $found app log lines, $agents agent lines, platform logs and metrics of $platform base LXCs, per-container metrics; Grafana has both dashboards"'

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
  # Set from inside the GitOps repo: sops must not trip on its .sops.yaml.
  (cd /work/gitops && printf %s "s3cr3t-e2e" | urbex secret set --file /work/app/urbex.yaml staging api API_TOKEN)
  expect_http http://$NET.210:8080/secret "token=s3cr3t-e2e" 90
  [ "$(urbex secret list staging api)" = "API_TOKEN" ] || fail "secret list does not show API_TOKEN"
  if grep -rq "s3cr3t-e2e" /work/gitops; then fail "the secret is in the GitOps repo in plaintext"; fi
  grep -q "^sops_" /work/gitops/environments/staging/hello/api/secrets.sops.env || fail "the secrets file is not sops-encrypted"
  expect_http http://$NET.211:8080/secret "token=" 5
  urbex secret unset staging api API_TOKEN
  for _ in $(seq 1 45); do [ "$(curl -s http://$NET.210:8080/secret)" = "token=" ] && break; sleep 2; done
  [ "$(curl -s http://$NET.210:8080/secret)" = "token=" ] || fail "the secret is still set after unset"
  echo "ok: secret set (from inside the GitOps repo), delivered, listed, and removed"'

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
  [ "$(komodo_names Stacks)" = "hello-api-prod urbex-gitea urbex-keycloak urbex-komodo urbex-observability urbex-technitium urbex-telemetry-hello-api-prod urbex-telemetry-urbex-gitea urbex-telemetry-urbex-keycloak urbex-telemetry-urbex-komodo urbex-telemetry-urbex-observability urbex-telemetry-urbex-technitium" ] || fail "unexpected stacks left (prod and the platform'"'"'s expected): $(komodo_names Stacks)"
  [ "$(komodo_names Builds)" = "hello-api" ] || fail "the project build should stay while prod exists: $(komodo_names Builds)"
  test ! -e /work/gitops/environments/staging/hello || fail "the staging folder is still in the gitops repo"
  expect_http http://$NET.211:8080/ "hello v2 from urbex (prod)"
  [ -z "$(technitium_record api.staging.hello.urbex.urbex.test)" ] || fail "internal dns record for staging api was not removed"
  echo "ok: staging is gone from komodo, the gitops repo, and technitium"'

# What terraform does for real: the destroyed LXC is gone, and the next
# apply creates a new one - at the address destroy freed, the lowest free
# one - with a Periphery identity Komodo has never seen under that name.
podman rm -f e2e-hello-api-staging >/dev/null
machine hello-api-staging 210
step "apply staging again on a new machine: it rejoins Komodo and follows main" '
  urbex apply staging
  expect_http http://$NET.210:8080/ "hello v5 from urbex (staging)" 5
  [ "$(technitium_record api.staging.hello.urbex.urbex.test)" = "$NET.210" ] || fail "internal dns record was not re-created"'

machine site-web-prod 212
echo site-web-prod >>"$WORK/pve-containers.txt"
step "an application from a published image: pinned to a digest, updated, one environment" '
  cd /work/site
  urbex apply staging >/tmp/staging.log 2>&1 && fail "a prod-only stack has no staging"
  grep -q "has no staging environment" /tmp/staging.log || { cat /tmp/staging.log; fail "unexpected apply staging error"; }
  urbex apply prod >/tmp/apply.log 2>&1 || { cat /tmp/apply.log; fail "apply prod failed"; }
  cat /tmp/apply.log
  expect_http http://$NET.212/ "Welcome to nginx" 5
  grep -qE "^VERSION=1[.]29@sha256:[0-9a-f]{64}$" /work/gitops/environments/prod/site/web/version.env || fail "version.env should pin the tag to its digest"
  grep -q "image: docker.io/library/nginx:\${VERSION}" /work/gitops/environments/prod/site/web/compose.yaml || fail "the stack should pull the published image"
  [ "$(komodo_names Builds)" = "hello-api" ] || fail "an application from an image needs no Build: $(komodo_names Builds)"
  [ "$(technitium_record web.site.urbex.urbex.test)" = "$NET.212" ] || fail "no internal dns record for site web"
  urbex deploy prod --update | tee /tmp/update.log
  grep -q "already runs docker.io/library/nginx:1.29" /tmp/update.log || fail "an unchanged tag should change nothing"
  urbex deploy prod --app web --version 1.28 | tee /tmp/version.log
  grep -qE "^VERSION=1[.]28@sha256:[0-9a-f]{64}$" /work/gitops/environments/prod/site/web/version.env || fail "deploy --version should pin the other tag"
  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -q root@$NET.212 "docker inspect -f {{.Config.Image}} site-web-prod-app-1" | grep -q "nginx:1.28@sha256:" || fail "the container does not run the pinned 1.28 digest"
  expect_http http://$NET.212/ "Welcome to nginx" 5
  urbex promote >/tmp/promote.log 2>&1 && fail "promote should not apply to a prod-only stack"
  grep -q "only prod" /tmp/promote.log || { cat /tmp/promote.log; fail "unexpected promote error"; }
  urbex release >/tmp/release.log 2>&1 && fail "release should not apply to a prod-only stack"
  grep -q "only prod" /tmp/release.log || { cat /tmp/release.log; fail "unexpected release error"; }
  echo "ok: site runs nginx from its published image, pinned, updated by tag"'

# What Proxmox does for a mount point, on the machine standing in for the
# LXC: a folder that is a mount point, with the lost+found of an ext4 one.
podman exec e2e-site-web-prod sh -c 'mkdir -p /srv/volumes/web/data/lost+found && mount --bind /srv/volumes/web/data /srv/volumes/web/data'
step "volumes: a named volume is a mount point, kept across redeploys; destroy asks for --delete-data" '
  cd /work/site
  site_volume() { # site_volume <size>: the site stack, with a volume of that size
    cat >urbex.yaml <<YAML
apiVersion: urbex/v1
project: site
environments: [prod]
services:
  - name: web
    image: docker.io/library/nginx
    version: "1.29"
    port: 80
    volumes:
      - { name: data, path: /data, size: $1 }
YAML
  }
  lxc() { ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -q root@$NET.212 "$@"; }
  mounts() { cat /work/pve-lxc-*.json; }

  site_volume 2Gi
  urbex apply prod >/tmp/volume.log 2>&1 || { cat /tmp/volume.log; fail "apply prod with a volume failed"; }
  grep -q "web: volume data, 2 GB, mounted at /data (mp0)" /tmp/volume.log || { cat /tmp/volume.log; fail "apply should say it mounted the volume"; }
  [ "$(ls /work/pve-lxc-*.json | wc -l)" = 1 ] || fail "only the LXC with a volume should have mount points"
  mounts | grep -q "mp=/srv/volumes/web/data,backup=1,size=2G" || fail "the volume should be a mount point of 2G with backup=1: $(mounts)"
  grep -q "      - data:/data" /work/gitops/environments/prod/site/web/compose.yaml && grep -q "device: /srv/volumes/web/data" /work/gitops/environments/prod/site/web/compose.yaml || fail "the stack should have a Docker volume on the folder of the mount point"
  [ "$(lxc docker volume inspect -f "{{.Options.device}}" site-web-prod_data)" = /srv/volumes/web/data ] || fail "the Docker volume is not on the folder of the mount point"
  lxc docker inspect -f \"{{range .Mounts}}{{.Name}}:{{.Destination}} {{end}}\" site-web-prod-app-1 | grep -q "site-web-prod_data:/data" || fail "the container does not have the volume"
  lxc test ! -e /srv/volumes/web/data/lost+found || fail "the empty lost+found of the new volume should have been removed, for Docker to fill the volume"
  lxc "docker exec site-web-prod-app-1 sh -c \"echo kept >/data/keep\""

  urbex apply prod >/tmp/again.log 2>&1 || { cat /tmp/again.log; fail "apply prod again failed"; }
  if grep -q "mounted at" /tmp/again.log; then fail "a re-apply should not add the volume again"; fi
  urbex deploy prod --app web --version 1.29
  lxc docker exec site-web-prod-app-1 cat /data/keep | grep -q kept || fail "the volume lost its data when the container was replaced"

  site_volume 3Gi
  urbex apply prod >/tmp/grow.log 2>&1 || { cat /tmp/grow.log; fail "apply prod with a bigger volume failed"; }
  grep -q "volume data grown from 2 to 3 GB" /tmp/grow.log || { cat /tmp/grow.log; fail "apply should say it grew the volume"; }
  mounts | grep -q "size=3G" || fail "the volume should be 3G now: $(mounts)"
  site_volume 1Gi
  urbex apply prod >/tmp/shrink.log 2>&1 && fail "a smaller volume should be refused"
  grep -q "can.t shrink" /tmp/shrink.log || { cat /tmp/shrink.log; fail "unexpected error for a smaller volume"; }

  urbex destroy prod >/tmp/destroy.log 2>&1 && fail "destroy should refuse an environment with named volumes"
  grep -q "web/data" /tmp/destroy.log && grep -q -- "--delete-data" /tmp/destroy.log || { cat /tmp/destroy.log; fail "destroy should name the volume and the flag"; }
  [ "$(komodo_stack_state site-web-prod)" = running ] || fail "a refused destroy should leave the stack alone"
  urbex destroy prod --delete-data
  [ -z "$(komodo_stack_state site-web-prod)" ] || fail "the stack should be gone after destroy --delete-data"
  echo "ok: the volume was mounted, kept, grown, never shrunk, and deleted only with --delete-data"'

# site's LXC went with destroy --delete-data, and its address (.212) with
# it. One machine per application, at the addresses apply allocates in
# the manifest's order, the lowest free first.
podman rm -f e2e-site-web-prod >/dev/null
podman volume rm -f e2e-site-web-prod-docker e2e-site-web-prod-containerd >/dev/null 2>&1 || true
machine shop-db-prod 212
machine shop-api-prod 213
machine shop-web-prod 214
printf '%s\n' shop-db-prod shop-api-prod shop-web-prod >>"$WORK/pve-containers.txt"
# The PostgreSQL's volume, a mount point as Proxmox makes it (see the
# volumes step).
podman exec e2e-shop-db-prod sh -c 'mkdir -p /srv/volumes/db/data/lost+found && mount --bind /srv/volumes/db/data /srv/volumes/db/data'
step "linked applications: each starts once what it uses is ready, with what it needs to reach it" '
  cd /work/shop
  urbex apply prod >/tmp/apply.log 2>&1 || { cat /tmp/apply.log; fail "apply prod of the linked stack failed"; }
  cat /tmp/apply.log
  on() { ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -q root@$NET.$1 "$2"; }
  # first <octet> <container> <event>: when the container first did it, in
  # ns. Docker events, not inspect: every deploy of the Stack runs the
  # one-shot wait again (it exits at once), and inspect keeps only the last.
  first() { on $1 "docker events --since 2h --until $(date +%s) --filter container=$2 --filter event=$3 --format {{.TimeNano}}" | head -n 1; }
  later() { [ -n "$1" ] && [ -n "$2" ] && [ "$1" -ge "$2" ]; }
  dir=/work/gitops/environments/prod/shop

  # What the PoC measured, for each consumer: its wait saw each provider
  # ready and exited 0, the application started only then, and never
  # restarted.
  consumer() { # consumer <octet> <service> <provider>...
    octet=$1 svc=$2; shift 2
    for p in "$@"; do
      on $octet "docker logs shop-$svc-prod-wait-1" 2>&1 | grep -q "wait: $p is ready" || fail "$svc: the wait did not see $p ready: $(on $octet "docker logs shop-$svc-prod-wait-1" 2>&1)"
    done
    [ "$(on $octet "docker inspect -f {{.State.ExitCode}} shop-$svc-prod-wait-1")" = 0 ] || fail "$svc: the wait did not exit 0"
    later "$(first $octet shop-$svc-prod-app-1 start)" "$(first $octet shop-$svc-prod-wait-1 die)" || fail "$svc started before its wait was done"
    [ "$(on $octet "docker inspect -f {{.RestartCount}} shop-$svc-prod-app-1")" = 0 ] || fail "$svc restarted"
    [ "$(komodo_stack_state shop-$svc-prod)" = running ] || fail "$svc: Komodo shows the Stack $(komodo_stack_state shop-$svc-prod), not running (ignore_services)"
  }
  consumer 213 api db
  consumer 214 web api db
  [ "$(komodo_stack_state shop-db-prod)" = running ] || fail "db: Komodo shows the Stack $(komodo_stack_state shop-db-prod)"
  later "$(first 213 shop-api-prod-app-1 start)" "$(first 212 shop-db-prod-app-1 start)" || fail "api started before db"
  later "$(first 214 shop-web-prod-app-1 start)" "$(first 213 shop-api-prod-app-1 start)" || fail "web started before api"

  # The probes: the PostgreSQL takes connections, the api answers its
  # healthcheck (a port that answers is not enough); a budget per level.
  grep -q "host=db.shop.urbex.urbex.test; probe() { PGPASSWORD=\"\$\$URBEX_PASSWORD_DB\" PGCONNECT_TIMEOUT=2 psql -X -w -q -A -t -h \"\$\$1\" -p 5432 -U api -d shop -c .select 1. >/dev/null 2>&1; }" $dir/api/compose.yaml || fail "api should wait for db by logging in with its role: $(grep wait_for $dir/api/compose.yaml)"
  grep -q "host=api.shop.urbex.urbex.test; probe() { wget -q --spider -T 2 --header \"Host: api.shop.urbex.urbex.test:80\" \"http://\$\$1:80/\"; }" $dir/web/compose.yaml || fail "web should probe the healthcheck of api: $(grep wait_for $dir/web/compose.yaml)"
  grep -q "+ 110 ))" $dir/api/compose.yaml && grep -q "+ 190 ))" $dir/web/compose.yaml || fail "the wait should be the ready_within along the chain (db 90 s, api 60 s) plus the stable window per level"
  if grep -q wait_for $dir/db/compose.yaml; then fail "db uses nothing and should not wait"; fi

  # What the consumers get to reach what they use.
  [ "$(on 213 "docker exec shop-api-prod-app-1 printenv PGHOST")" = db.shop.urbex.urbex.test ] || fail "api has no PGHOST"
  on 213 "docker exec shop-api-prod-app-1 printenv DATABASE_URL" | grep -q "^postgres://api:.*@db.shop.urbex.urbex.test:5432/shop$" || fail "api has no DATABASE_URL"
  [ "$(on 214 "docker exec shop-web-prod-app-1 printenv API_ADDRESS")" = http://api.shop.urbex.urbex.test:80 ] || fail "the reference to api.url did not resolve"
  [ "$(on 214 "docker exec shop-web-prod-app-1 printenv API_URL")" = http://api.shop.urbex.urbex.test:80 ] || fail "web has no API_URL"
  if grep -rq "$(on 213 "docker exec shop-api-prod-app-1 printenv PGPASSWORD")" $dir; then fail "a generated password is in plaintext in the GitOps repo"; fi

  # Each consumer has its own role and schema on db (ADR-0030): it logs
  # in with its DATABASE_URL, writes and reads in its schema without
  # naming it, and can touch neither the other one'"'"'s schema nor public.
  sql() { # sql <octet> <service> <statement>: as the service, from its LXC
    on $1 "docker run --rm --network host docker.io/library/postgres:17-alpine psql \"\$(docker exec shop-$2-prod-app-1 printenv DATABASE_URL)\" -X -q -A -t -v ON_ERROR_STOP=1 -c \"$3\"" 2>&1
  }
  sql 213 api "CREATE TABLE notes (v text); INSERT INTO notes VALUES ('"'"'from api'"'"')" || fail "api cannot write in its schema"
  sql 214 web "CREATE TABLE notes (v text); INSERT INTO notes VALUES ('"'"'from web'"'"')" || fail "web cannot write in its schema"
  [ "$(sql 213 api "SELECT v FROM notes")" = "from api" ] || fail "api reads $(sql 213 api "SELECT v FROM notes"), not its own row"
  [ "$(sql 213 api "SELECT current_schema()")" = api ] || fail "api'"'"'s search_path is not its schema"
  sql 213 api "SELECT v FROM web.notes" | grep -q "permission denied" || fail "api can read web'"'"'s schema"
  sql 214 web "CREATE TABLE public.t (v int)" | grep -q "permission denied" || fail "web can create in public"
  # The admin, password and all, is no application'"'"'s.
  if on 213 "docker exec shop-api-prod-app-1 printenv" | grep -q "^POSTGRES_PASSWORD="; then fail "api has the server'"'"'s password"; fi
  urbex status prod >/tmp/status.log 2>&1
  grep -q "role api *schema api *used by api" /tmp/status.log && grep -q "role web *schema web *used by web" /tmp/status.log || fail "status should list the roles and schemas of db: $(cat /tmp/status.log)"
  if grep -q "$(on 213 "docker exec shop-api-prod-app-1 printenv PGPASSWORD")" /tmp/status.log; then fail "status prints a password"; fi
  urbex status prod --output json | tr -d " \n" | grep -qF "{\"role\":\"api\",\"schema\":\"api\",\"use\":\"used\",\"usedBy\":\"api\"}" || fail "status --output json should list the roles of db: $(urbex status prod --output json)"
  urbex secret list prod api | grep -q "^DATABASE_URL  (generated by urbex)$" || fail "secret list should mark DATABASE_URL as generated: $(urbex secret list prod api)"
  urbex secret set prod api PGPASSWORD=x >/tmp/secret.log 2>&1 && fail "secret set should refuse a generated key"

  # A second apply changes nothing on the server, and keeps the data.
  urbex apply prod >/tmp/apply2.log 2>&1 || { cat /tmp/apply2.log; fail "the second apply of the linked stack failed"; }
  if grep -Eq "^db: (created|set|took|gave)" /tmp/apply2.log; then fail "the second apply changed db: $(grep "^db: " /tmp/apply2.log)"; fi
  [ "$(sql 213 api "SELECT v FROM notes")" = "from api" ] || fail "the second apply lost api'"'"'s data"

  # web stops using db: its role and schema stay, with their data, named
  # as unused - until --delete-data.
  cp urbex.yaml /tmp/shop-urbex.yaml
  sed -i "s/uses: \[api, db\]/uses: [api]/" urbex.yaml
  urbex apply prod >/tmp/apply3.log 2>&1 || { cat /tmp/apply3.log; fail "the apply without web on db failed"; }
  grep -q "no application uses the schema and role web any more: kept" /tmp/apply3.log || fail "apply should name web'"'"'s unused schema: $(cat /tmp/apply3.log)"
  urbex status prod | grep -q "role web *schema web *unused" || fail "status should show web'"'"'s schema unused: $(urbex status prod)"
  [ "$(on 212 "docker exec shop-db-prod-app-1 psql -U urbex -d shop -X -A -t -c \"SELECT v FROM web.notes\"")" = "from web" ] || fail "web'"'"'s data should be kept"
  cp /tmp/shop-urbex.yaml urbex.yaml
  urbex apply prod >/tmp/apply4.log 2>&1 || { cat /tmp/apply4.log; fail "the apply with web back on db failed"; }
  [ "$(sql 214 web "SELECT v FROM notes")" = "from web" ] || fail "web back on db should find its data"

  # A provider with no way to tell it is ready is refused: without its
  # healthcheck, api could only be probed on its port.
  rm -rf /tmp/noprobe && cp -r /work/shop /tmp/noprobe && sed -i "/healthcheck: \//d" /tmp/noprobe/urbex.yaml
  (cd /tmp/noprobe && urbex apply prod) >/tmp/noprobe.log 2>&1 && fail "a provider without a healthcheck should be refused"
  grep -q "uses api, which has neither an interface nor a healthcheck" /tmp/noprobe.log || fail "the refusal should name api: $(cat /tmp/noprobe.log)"
  [ "$(komodo_stack_state shop-web-prod)" = running ] || fail "a refused apply should leave the stacks alone"

  # The healthchecks reach Prometheus, for dashboards and alerts.
  for _ in $(seq 1 12); do
    healthy=$(prom_series "container_health_state{kind=\"app\", project=\"shop\", service=~\"db|api\", env=\"prod\"} == 1")
    [ "$healthy" = 2 ] && break
    sleep 5
  done
  [ "$healthy" = 2 ] || fail "db and api should be healthy in Prometheus, $healthy are"
  echo "ok: db, then api, then web, each once what it uses was ready, with no restart; a provider without a healthcheck is refused"'

step "the reconciler: a change waits for what it needs, a late provider is handled" '
  on() { ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -q root@$NET.$1 "$2"; }
  app() { on $1 "docker inspect -f {{.State.Status}}/{{.Id}}/{{.RestartCount}} shop-$2-prod-app-1" 2>/dev/null; }
  expect_running() { # expect_running <octet> <service> <timeout-seconds>
    _end=$(( $(date +%s) + $3 ))
    until app $1 $2 | grep -q "^running/"; do
      [ "$(date +%s)" -ge "$_end" ] && fail "$2 did not come back by itself: $(app $1 $2)"
      sleep 10
    done
  }
  cd /work/shop
  urbex status prod | grep -q "Reconciler: running, last heard from .* ago\.$" || fail "the reconciler should have run lately: $(urbex status prod | grep -A2 Reconciler)"
  # Its lines reach Loki, and Grafana alerts on them: when they stop
  # coming, and on what it holds back.
  [ "$(loki_lines "{kind=\"platform\", service=\"komodo\", source=\"urbex-reconcile\"}")" -gt 0 ] || fail "no line of the reconciler in Loki"
  for uid in urbex-reconcile-not-running urbex-reconcile-errors; do
    curl -sf -u "admin:$(cred grafanaAdminPassword)" http://$NET.204:3000/api/v1/provisioning/alert-rules/$uid >/dev/null || fail "Grafana lacks the alert rule $uid"
  done

  # 1. A change to api while db is down: the preflight keeps the running
  # api (compose would have replaced it with one waiting for db), and the
  # reconciler deploys the change once db is back.
  before=$(app 213 api)
  on 212 "docker stop shop-db-prod-app-1" >/dev/null
  sleep 6
  cd /work/gitops && git pull -q "$(gitops_remote)" main 2>/dev/null
  echo "RECONCILE_MARK=1" >>environments/prod/shop/api/config.env
  git commit -qam "Change api while db is down" && git push -q "$(gitops_remote)" HEAD:main 2>/dev/null
  # Longer than the preflight waits for db (110 s), from the push on.
  for _ in $(seq 1 30); do
    [ "$(app 213 api)" = "$before" ] || fail "api was touched while db was down: $before -> $(app 213 api)"
    sleep 5
  done
  on 212 "docker start shop-db-prod-app-1" >/dev/null
  # The reconciler deploys it within a minute or two, unless the GitOps
  # Procedure (every quarter of an hour) happens to run first: either way
  # the change lands.
  _deadline=$(( $(date +%s) + 360 ))
  until [ "$(on 213 "docker exec shop-api-prod-app-1 printenv RECONCILE_MARK" 2>/dev/null)" = 1 ]; do
    [ "$(date +%s)" -ge "$_deadline" ] && fail "the change to api was not deployed once db was back; the reconciler: $(reconciler_runs | head -n 3)"
    sleep 10
  done

  # 2. api gone while db is down; db back later than api would wait: the
  # reconciler brings api back, through its wait, once db is ready. Only
  # its runs from now on count: the change above logged the same success.
  since=$(date +%s)000
  on 212 "docker stop shop-db-prod-app-1" >/dev/null
  on 213 "docker rm -f shop-api-prod-app-1" >/dev/null
  sleep 150
  [ -z "$(app 213 api)" ] || fail "api should stay down while db is: $(app 213 api)"
  reconciler_runs | head -n 1 | grep -q "shop-api-prod: waiting for shop-db-prod" || fail "the reconciler should hold api for db: $(reconciler_runs | head -n 1)"
  on 212 "docker start shop-db-prod-app-1" >/dev/null
  expect_running 213 api 360
  expect_reconciler "shop-api-prod: missing - DeployStack started" 60 "$since"
  expect_reconciler "shop-api-prod: the deploy started earlier succeeded" 240 "$since"
  [ "$(app 213 api | cut -d/ -f3)" = 0 ] || fail "api restarted: $(app 213 api)"

  # What the reboot, next, must not recreate.
  on 213 "docker inspect -f {{.Id}} shop-api-prod-app-1" >/work/api-before-reboot
  on 214 "docker inspect -f {{.Id}} shop-web-prod-app-1" >/work/web-before-reboot
  # The runs the next step looks at: from the reboot on.
  date +%s000 >/work/reboot-at
  echo "ok: a change waited for db and a late db was handled, with no deploy on what was not ready"'

# The node of shop reboots: its LXCs are down for a while - long enough
# for the reconciler to run twice without reaching their Periphery - then
# start again, with Docker, Periphery and the applications. One restart
# is no crash loop, and a Stack on an unreachable server is left alone.
podman stop -t 10 e2e-shop-db-prod e2e-shop-api-prod e2e-shop-web-prod >/dev/null
sleep 150
podman start e2e-shop-db-prod e2e-shop-api-prod e2e-shop-web-prod >/dev/null
for m in db api web; do
  podman exec e2e-shop-$m-prod sh -c "printf 'nameserver $NET.202\nnameserver $NET.1\n' >/etc/resolv.conf"
done
podman exec e2e-shop-db-prod sh -c 'mount --bind /srv/volumes/db/data /srv/volumes/db/data'

step "the reconciler: a reboot of the node is no crash loop, a crash loop is" '
  on() { ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -q root@$NET.$1 "$2"; }
  app() { on $1 "docker inspect -f {{.State.Status}}/{{.Id}}/{{.RestartCount}} shop-$2-prod-app-1" 2>/dev/null; }
  expect_running() { # expect_running <octet> <service> <timeout-seconds>
    _end=$(( $(date +%s) + $3 ))
    until app $1 $2 | grep -q "^running/"; do
      [ "$(date +%s)" -ge "$_end" ] && fail "$2 did not come back by itself: $(app $1 $2)"
      sleep 10
    done
  }
  cd /work/shop

  # 3. After the reboot, the applications come back by themselves: the
  # runs while their servers were unreachable leave them alone, and the
  # runs that see them back do not fail, call nothing a crash loop and
  # deploy nothing - api and web are the containers they were.
  _deadline=$(( $(date +%s) + 180 ))
  until [ "$(on 212 "docker inspect -f {{.State.Health.Status}} shop-db-prod-app-1" 2>/dev/null)" = healthy ]; do
    [ "$(date +%s)" -ge "$_deadline" ] && fail "db did not come back healthy after the reboot: $(app 212 db)"
    sleep 5
  done
  expect_running 213 api 60
  expect_running 214 web 60
  # Five runs from then: the ones that see them back, and well past the
  # two they stay unstable for; and the runs while they were down, all
  # from the reboot on (the last run before it may still deploy).
  sleep 300
  runs=$(reconciler_runs "$(cat /work/reboot-at)")
  if echo "$runs" | grep -q "^FAILED"; then fail "a run failed after a reboot: $runs"; fi
  if echo "$runs" | grep -q "crash loop"; then fail "a reboot was called a crash loop: $runs"; fi
  if echo "$runs" | grep -q "DeployStack"; then fail "a reboot should deploy nothing: $runs"; fi
  echo "$runs" | grep -q "its server is unreachable: left alone" || fail "the runs during the reboot should leave the Stacks alone: $runs"
  if urbex status prod | grep -q "crash loop"; then fail "urbex status calls a reboot a crash loop: $(urbex status prod | grep -A3 Reconciler)"; fi
  [ "$(on 213 "docker inspect -f {{.Id}} shop-api-prod-app-1")" = "$(cat /work/api-before-reboot)" ] || fail "api was recreated after the reboot"
  [ "$(on 214 "docker inspect -f {{.Id}} shop-web-prod-app-1")" = "$(cat /work/web-before-reboot)" ] || fail "web was recreated after the reboot"

  # 4. api in a crash loop: web, gone, is not deployed on it, and the runs
  # fail - Komodo shows it; once api stays up, web comes back.
  on 214 "docker rm -f shop-web-prod-app-1" >/dev/null
  on 213 "for i in \$(seq 1 18); do docker exec shop-api-prod-app-1 kill 1 2>/dev/null; sleep 10; done" &
  killer=$!
  expect_reconciler "shop-api-prod is restarting (crash loop): what uses it waits" 240
  reconciler_runs | grep "crash loop" | head -n 1 | grep -q "^FAILED" || fail "a run that sees a crash loop should fail"
  [ -z "$(app 214 web)" ] || fail "web should not be deployed on a crash-looping api: $(app 214 web)"
  wait $killer
  expect_running 214 web 480
  echo "ok: a reboot of the node alerted and deployed nothing; a crash-looping api held web back and failed the runs"'

step "the reconciler: an environment destroyed is no longer looked after, its graph read by name" '
  cd /work/shop
  [ -n "$(komodo_variable URBEX_GRAPH_SHOP_PROD)" ] || fail "the graph of shop prod should be in Komodo"
  urbex destroy prod --delete-data >/tmp/destroy.log 2>&1 || { cat /tmp/destroy.log; fail "destroy prod of the linked stack failed"; }
  [ -z "$(komodo_variable URBEX_GRAPH_SHOP_PROD)" ] || fail "destroy should remove the graph of shop prod"
  # The next run reads no graph: hello prod, still there, has no links,
  # so the variable it asks for by name does not exist.
  _deadline=$(( $(date +%s) + 180 ))
  until reconciler_runs | head -n 1 | grep -qF "0 environment(s) with links: nothing to do."; do
    [ "$(date +%s)" -ge "$_deadline" ] && fail "the reconciler still looks after shop prod: $(reconciler_runs | head -n 1)"
    sleep 10
  done
  latest=$(reconciler_runs | head -n 1)
  echo "$latest" | grep -q "^ok " || fail "the run after the destroy failed: $latest"
  if echo "$latest" | grep -q "shop-"; then fail "the run after the destroy still names shop: $latest"; fi
  echo "ok: the graph went with the environment, and the reconciler found no other"'

log "PASSED ($STEP steps)"
if $KEEP; then
  echo "Machines left running (podman ps --filter name=e2e-); the next run removes them."
else
  teardown
fi
