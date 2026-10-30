#!/bin/sh
# waitfor.sh host:port ...  waits until each host:port accepts a TCP
# connection, and gives up after four minutes. It runs wherever Komodo runs
# a command for a Stack (Periphery's container), so it only assumes what is
# there: bash, nc or python3, whichever it finds.
deadline=$(( $(date +%s) + 240 ))

probe() {
  if command -v bash >/dev/null 2>&1; then
    bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null
  elif command -v nc >/dev/null 2>&1; then
    nc -w 2 "$1" "$2" </dev/null >/dev/null 2>&1
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c "import socket,sys; socket.create_connection((sys.argv[1], int(sys.argv[2])), 2).close()" "$1" "$2" 2>/dev/null
  else
    echo "waitfor: no bash, nc or python3 here" >&2
    exit 2
  fi
}

for hp in "$@"; do
  host=${hp%:*}
  port=${hp##*:}
  until probe "$host" "$port"; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "waitfor: gave up waiting for $hp"
      exit 1
    fi
    sleep 2
  done
  echo "waitfor: $hp is up"
done
