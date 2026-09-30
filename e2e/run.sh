#!/usr/bin/env bash
# End-to-end test of the urbex CLI: runs the real binary through
# bootstrap, apply, release, deploy, promote, secrets, rollback, destroy
# and re-apply, against containers standing in for Proxmox LXCs. See
# e2e/README.md.
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
    grep -E '^(ok: [^[]|pushed |Deployed |Released |  [a-z]+ +(http|[0-9])|[a-z]+: .* -> |Nothing|Komodo server|Destroyed|Created |Set |Removed )' "$logfile" || true
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

if command -v go >/dev/null; then
  CGO_ENABLED=0 go build -o "$WORK/bin/urbex" ./cmd/urbex
else
  podman run --rm -v "$REPO:/src:z" -v urbex-e2e-gomod:/go/pkg/mod -w /src \
    docker.io/library/golang:1.26 sh -c 'CGO_ENABLED=0 go build -o e2e/.work/bin/urbex ./cmd/urbex'
fi

podman build -q -t urbex-e2e-machine --build-arg "PUBKEY=$(cat "$WORK/id_e2e.pub")" e2e/machine >/dev/null 2>&1
podman build -q -t urbex-e2e-runner e2e/runner >/dev/null 2>&1
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
step "apply staging: infrastructure, project Builds, environment folder - nothing to run yet" '
  urbex apply staging >/tmp/apply.log 2>&1 || { cat /tmp/apply.log; fail "apply staging failed"; }
  cat /tmp/apply.log
  grep -q "no version to run yet" /tmp/apply.log || fail "apply should say the new environment has no version"
  for f in compose.yaml config.env secrets.sops.env; do
    test -f /work/gitops/environments/staging/hello/api/$f || fail "missing environments/staging/hello/api/$f"
  done
  test ! -e /work/gitops/environments/staging/hello/api/version.env || fail "a new environment must have no version"
  [ "$(komodo_names Builds)" = "hello-api" ] || fail "unexpected builds: $(komodo_names Builds)"
  [ "$(komodo_names Stacks)" = "hello-api-staging" ] || fail "unexpected stacks: $(komodo_names Stacks)"
  urbex status staging | grep "version=none stack=down" || fail "status should show an empty environment"
  echo "ok: staging exists and is empty"'

step "release: a tag on main, built by Komodo; deploy staging: a commit to the GitOps repo" '
  urbex release
  expect_image hello-api 0.1.0 5
  urbex deploy staging
  expect_http http://$NET.210:8080/ "hello v1 from urbex (staging)" 5
  grep -q "^VERSION=0.1.0$" /work/gitops/environments/staging/hello/api/version.env || fail "version.env was not written"
  urbex status staging | grep "version=0.1.0 stack=running image=$NET.200:3000/urbex/hello-api:0.1.0" || fail "status does not show the running version"
  echo "ok: 0.1.0 released and running on staging"'

step "trunk development: a push to main deploys nothing; a tag pushed by hand is a release" '
  push_main v2
  unchanged_http http://$NET.210:8080/ "hello v1 from urbex (staging)" 20
  [ "$(gitea_api GET /packages/urbex/container/hello-api/0.1.1 | grep -c "\"version\"")" = 0 ] || fail "a push to main must not build a release"
  push_tag v0.2.0
  expect_image hello-api 0.2.0
  urbex deploy staging
  expect_http http://$NET.210:8080/ "hello v2 from urbex (staging)" 5'

echo hello-api-prod >>"$WORK/pve-containers.txt"
step "apply prod, then promote: prod runs the image staging ran" '
  urbex apply prod
  urbex promote
  expect_http http://$NET.211:8080/ "hello v2 from urbex (prod)" 5
  grep -q "^VERSION=0.2.0$" /work/gitops/environments/prod/hello/api/version.env || fail "prod version.env was not written"
  [ "$(komodo_names Builds)" = "hello-api" ] || fail "promoting must not create builds: $(komodo_names Builds)"
  urbex promote | grep "Nothing to promote" || fail "promote did not notice there is nothing left to promote"'

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
  sed -i "s/^GREETING_ENV=.*/GREETING_ENV=edited-by-hand/" environments/staging/hello/api/config.env
  git commit -qam "Change staging greeting" && git push -q "$(gitops_remote)" HEAD:main 2>/dev/null
  expect_http http://$NET.210:8080/ "hello v2 from urbex (edited-by-hand)" 90
  sleep 6
  echo "# note" >>README.md && git add README.md && git commit -qm "Unrelated" && git push -q "$(gitops_remote)" HEAD:main 2>/dev/null
  unchanged_http http://$NET.211:8080/ "hello v2 from urbex (prod)" 20'
step "the unrelated commit did not restart staging either" '
  started() { ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -q root@$NET.210 "docker inspect -f {{.State.StartedAt}} hello-api-staging-app-1"; }
  before=$(started); sleep 20; after=$(started)
  [ -n "$before" ] && [ "$before" = "$after" ] || fail "staging was restarted ($before -> $after)"
  echo "ok: staging untouched since $before"'

step "rollback: deploying an older release" '
  urbex deploy prod --version 0.1.0
  expect_http http://$NET.211:8080/ "hello v1 from urbex (prod)" 5
  urbex deploy prod --version 9.9.9 >/tmp/bad.log 2>&1 && fail "deploying a release that does not exist should fail"
  grep -q "has no image" /tmp/bad.log || { cat /tmp/bad.log; fail "unexpected error for a missing release"; }
  echo "ok: a missing release is refused"'

step "destroy staging: prod and the project Builds untouched" '
  urbex destroy staging
  [ "$(komodo_names Stacks)" = "hello-api-prod" ] || fail "unexpected stacks left: $(komodo_names Stacks)"
  [ "$(komodo_names Builds)" = "hello-api" ] || fail "the project build should stay while prod exists: $(komodo_names Builds)"
  test ! -e /work/gitops/environments/staging/hello || fail "the staging folder is still in the gitops repo"
  expect_http http://$NET.211:8080/ "hello v1 from urbex (prod)"
  echo "ok: staging is gone from komodo and the gitops repo"'

# What terraform does for real: the destroyed LXC is gone, and the next
# apply creates a new one at the next free address - with a Periphery
# identity Komodo has never seen under that name.
podman rm -f e2e-hello-api-staging >/dev/null
machine hello-api-staging 212
step "apply staging again on a new machine: it rejoins Komodo, then runs a release" '
  urbex apply staging
  urbex deploy staging
  expect_http http://$NET.212:8080/ "hello v2 from urbex (staging)" 5'

log "PASSED ($STEP steps)"
if $KEEP; then
  echo "Machines left running (podman ps --filter name=e2e-); the next run removes them."
else
  teardown
fi
