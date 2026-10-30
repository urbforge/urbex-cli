#!/usr/bin/env bash
# PoC of dependencies between Komodo Stacks ("blocks") on a graph of six
# blocks, with Komodo's own constructs. It needs only podman. See README.md.
#
# usage: e2e/poc-deps/run.sh [--komodo-version 2.3.4] [--tests G1,G2,...]
#                            [--clean] [--keep]
#   --komodo-version  Komodo Core and Periphery version (default 2.3.4)
#   --tests           the tests to run (default all)
#   --clean           start from a new machine and new volumes (use it when
#                     the Komodo version changes)
#   --keep            leave the machine running at the end
set -euo pipefail

cd "$(dirname "$0")"
VERSION=2.3.4
TESTS=G1,G2,G3,G4,G5
CLEAN=false
KEEP=false
while [ $# -gt 0 ]; do
  case $1 in
    --komodo-version) VERSION=$2; shift ;;
    --tests) TESTS=$2; shift ;;
    --clean) CLEAN=true ;;
    --keep) KEEP=true ;;
    *) echo "unknown option $1" >&2; exit 1 ;;
  esac
  shift
done

command -v podman >/dev/null || { echo "needs podman" >&2; exit 1; }

NAME=poc-machine
IMAGE=poc-machine-image
RESULTS=$PWD/results/$VERSION

log() { printf '\n== %s\n' "$*"; }
inside() { podman exec -e "KOMODO_VERSION=$VERSION" "$NAME" "$@"; }

wait_docker() {
  for _ in $(seq 1 90); do
    podman exec "$NAME" docker info >/dev/null 2>&1 && return 0
    sleep 2
  done
  echo "docker did not start in the machine" >&2
  return 1
}

collect() {
  mkdir -p "$RESULTS"
  podman cp "$NAME:/poc/results/$VERSION/." "$RESULTS/" >/dev/null 2>&1 || true
  echo "results in $RESULTS"
}

cleanup() {
  collect
  if ! $KEEP; then
    podman rm -f "$NAME" >/dev/null 2>&1 || true
  fi
}

if $CLEAN; then
  podman rm -f "$NAME" >/dev/null 2>&1 || true
  podman volume rm -f poc-docker poc-containerd >/dev/null 2>&1 || true
fi

log "building the machine image (Debian 13, systemd, Docker)"
podman build -q -t "$IMAGE" machine >/dev/null

if ! podman container exists "$NAME"; then
  log "starting the machine"
  podman run -d --name "$NAME" --hostname poc --privileged --systemd=always \
    -v poc-docker:/var/lib/docker -v poc-containerd:/var/lib/containerd \
    "$IMAGE" >/dev/null
fi
podman start "$NAME" >/dev/null 2>&1 || true
trap cleanup EXIT
wait_docker

log "copying the PoC into the machine"
podman exec "$NAME" mkdir -p /poc
podman cp ./. "$NAME:/poc/"

log "Komodo $VERSION"
inside bash /poc/inside/setup.sh "$VERSION"
inside python3 /poc/inside/poc.py prepare

IFS=, read -r -a LIST <<<"$TESTS"
for t in "${LIST[@]}"; do
  log "test $t"
  inside python3 /poc/inside/poc.py run "$t"
  collect
done

log "summary"
inside python3 /poc/inside/poc.py summary
