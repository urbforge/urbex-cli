#!/usr/bin/env bash
# The nightly full e2e on main (urbforge/urbex-cli#162): updates a clone
# of its own to origin/main, runs e2e/run.sh from scratch, and keeps the
# outcome and the logs under the results folder, one folder per run:
#
#   <results>/<YYYY-MM-DD-HHMM>/result   PASSED | FAILED step ... | RATE-LIMITED ...
#   <results>/<YYYY-MM-DD-HHMM>/logs/    every step's log
#   <results>/latest                     a link to the last run
#
# usage: e2e/nightly.sh [<clone dir>] [<results dir>]
#   defaults: ~/.cache/urbex-e2e-nightly/urbex-cli, ~/.cache/urbex-e2e-nightly/results
#
# It exits as run.sh does (0 passed, 1 failed, 3 rate-limited). The
# schedule, holding the session's nightly restart and telling the
# sessions are the caller's: the timer in urbforge/urbex-community.
set -uo pipefail

CLONE=${1:-$HOME/.cache/urbex-e2e-nightly/urbex-cli}
RESULTS=${2:-$HOME/.cache/urbex-e2e-nightly/results}
REMOTE=${E2E_NIGHTLY_REMOTE:-https://github.com/urbforge/urbex-cli.git}

if [ -d "$CLONE/.git" ]; then
  git -C "$CLONE" fetch -q origin main && git -C "$CLONE" checkout -q --detach origin/main || exit 1
else
  mkdir -p "$(dirname "$CLONE")" && git clone -q "$REMOTE" "$CLONE" && git -C "$CLONE" checkout -q --detach origin/main || exit 1
fi

RUN=$RESULTS/$(date +%Y-%m-%d-%H%M)
mkdir -p "$RUN"
cd "$CLONE" || exit 1
if command -v mise >/dev/null; then
  mise trust -q . >/dev/null 2>&1
  eval "$(mise env -s bash)"
fi
start=$(date +%s)
E2E_RESULT=$RUN/result e2e/run.sh >"$RUN/run.log" 2>&1
rc=$?
[ -s "$RUN/result" ] || echo "FAILED before any step: see run.log" >"$RUN/result"
echo "minutes $(( ($(date +%s) - start) / 60 ))" >>"$RUN/result"
cp -r e2e/.work/logs "$RUN/logs" 2>/dev/null
ln -sfn "$RUN" "$RESULTS/latest"
cat "$RUN/result"
exit $rc
