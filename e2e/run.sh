#!/usr/bin/env bash
# End-to-end test of the urbex CLI: runs the real binary through
# bootstrap, apply, merge-triggered deploys, promote, deploy, destroy and
# re-apply, against containers standing in for Proxmox LXCs. See
# e2e/README.md.
#
# usage: e2e/run.sh [--keep]    (--keep leaves the machines running)
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
    grep -E '^(ok: [^[]|merged |Deployed |  [a-z]+ +http|Nothing|Opened|Merged|Komodo server|Destroyed|Created )' "$logfile" || true
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

if command -v go >/dev/null; then
  CGO_ENABLED=0 go build -o "$WORK/bin/urbex" ./cmd/urbex
else
  podman run --rm -v "$REPO:/src:z" -v urbex-e2e-gomod:/go/pkg/mod -w /src \
    docker.io/library/golang:1.26 sh -c 'CGO_ENABLED=0 go build -o e2e/.work/bin/urbex ./cmd/urbex'
fi

podman build -q -t urbex-e2e-machine --build-arg "PUBKEY=$(cat "$WORK/id_e2e.pub")" e2e/machine >/dev/null 2>&1
podman build -q -t urbex-e2e-runner e2e/runner >/dev/null 2>&1
podman network create --subnet "$NET.0/24" "$NETWORK" >/dev/null

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
  [ "$(komodo_names Servers)" = "urbex-komodo" ] || fail "unexpected komodo servers: $(komodo_names Servers)"
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
step "apply staging: infrastructure, Komodo pipeline, first deploy" '
  urbex apply staging
  expect_http http://$NET.210:8080/ "hello v1 from urbex (staging)"
  urbex status staging >/tmp/status.log
  cat /tmp/status.log
  grep -q "deployment=running image=$NET.200:3000/urbex/hello-api:0.0.1-staging" /tmp/status.log || fail "status does not show the running image"
  test -f /work/gitops/komodo/hello/staging.json || fail "komodo resources were not recorded in the gitops repo"
  echo "ok: staging is deployed and reported by status"'

step "merge a pull request into staging: deploys on its own" '
  merge_change staging feature-v2 v2
  expect_http http://$NET.210:8080/ "hello v2 from urbex (staging)" 180'

echo hello-api-prod >>"$WORK/pve-containers.txt"
step "apply prod" '
  urbex apply prod
  expect_http http://$NET.211:8080/ "hello v1 from urbex (prod)"'

step "promote --merge: staging reaches prod" '
  urbex promote --merge
  expect_http http://$NET.211:8080/ "hello v2 from urbex (prod)" 180
  urbex promote | grep "Nothing to promote" || fail "promote did not notice there is nothing left to promote"'

step "deploy staging after a manifest change" '
  git fetch -q "$(git_remote)" staging && git checkout -q -B staging FETCH_HEAD
  sed -i "s/GREETING_ENV: staging/GREETING_ENV: staging-reconfigured/" urbex.yaml
  urbex deploy staging
  expect_http http://$NET.210:8080/ "hello v2 from urbex (staging-reconfigured)"'

step "destroy staging: prod untouched" '
  urbex destroy staging
  [ "$(komodo_names Builds)" = "hello-api-prod" ] || fail "unexpected builds left: $(komodo_names Builds)"
  [ "$(komodo_names Deployments)" = "hello-api-prod" ] || fail "unexpected deployments left: $(komodo_names Deployments)"
  [ "$(komodo_names Procedures)" = "hello-prod" ] || fail "unexpected procedures left: $(komodo_names Procedures)"
  gitea_api GET /repos/urbex/hello/hooks | grep -q "hello-staging" && fail "the staging webhook is still there"
  test ! -e /work/gitops/komodo/hello/staging.json || fail "the staging record is still in the gitops repo"
  expect_http http://$NET.211:8080/ "hello v2 from urbex (prod)"
  echo "ok: staging is gone from komodo and gitea"'

# What terraform does for real: the destroyed LXC is gone, and the next
# apply creates a new one at the next free address - with a Periphery
# identity Komodo has never seen under that name.
podman rm -f e2e-hello-api-staging >/dev/null
machine hello-api-staging 212
step "apply staging again on a new machine: it rejoins Komodo" '
  urbex apply staging
  expect_http http://$NET.212:8080/ "hello v2 from urbex (staging-reconfigured)"'

log "PASSED ($STEP steps)"
if $KEEP; then
  echo "Machines left running (podman ps --filter name=e2e-); the next run removes them."
else
  teardown
fi
