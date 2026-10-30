#!/usr/bin/env bash
# Runs inside the PoC machine: starts Komodo Core and Periphery. The graph
# tests write their blocks under /etc/komodo/poc/, where Periphery reads them.
# usage: setup.sh <komodo-version>
set -euo pipefail

VERSION=${1:?komodo version}
DIR=/opt/poc/komodo

docker network inspect pocnet >/dev/null 2>&1 || docker network create pocnet >/dev/null

mkdir -p "$DIR" /etc/komodo/backups /etc/komodo/poc
cp /poc/inside/komodo.compose.yaml "$DIR/compose.yaml"

# The credentials are made once per machine and stay in it: never in the
# repository.
if [ ! -f "$DIR/compose.env" ]; then
  rnd() { openssl rand -hex 16; }
  cat >"$DIR/compose.env" <<EOF
KOMODO_DATABASE_USERNAME=komodo
KOMODO_DATABASE_PASSWORD=$(rnd)
TZ=Etc/UTC

KOMODO_HOST=http://localhost:9120
KOMODO_TITLE=PoC
KOMODO_LOCAL_AUTH=true
KOMODO_INIT_ADMIN_USERNAME=admin
KOMODO_INIT_ADMIN_PASSWORD=$(rnd)
KOMODO_FIRST_SERVER_NAME=local
KOMODO_DISABLE_INIT_RESOURCES=true
KOMODO_DISABLE_USER_REGISTRATION=true
KOMODO_WEBHOOK_SECRET=$(rnd)
KOMODO_JWT_SECRET=$(rnd)
KOMODO_PERIPHERY_PUBLIC_KEY=file:/config/keys/periphery.pub

PERIPHERY_CORE_ADDRESS=ws://core:9120
PERIPHERY_CONNECT_AS=local
PERIPHERY_CORE_PUBLIC_KEYS=file:/config/keys/core.pub
PERIPHERY_ROOT_DIRECTORY=/etc/komodo
PERIPHERY_INCLUDE_DISK_MOUNTS=/etc/hostname
EOF
  chmod 600 "$DIR/compose.env"
fi

cd "$DIR"
# Core and Periphery must run the same version: a change of version
# recreates both.
KOMODO_VERSION=$VERSION docker compose --env-file compose.env up -d
echo "komodo $VERSION started"
