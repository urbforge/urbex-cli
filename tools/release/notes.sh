#!/bin/bash
# notes.sh TAG [CHANGELOG]: the release notes of TAG (v0.2.0, v0.2.0-rc.1)
# from CHANGELOG.md - the section "## [0.2.0] ..." without its heading -
# for the GitHub release. An -rc takes the notes of its version. Fails,
# saying so, when the version has no section or an empty one: a release
# needs its notes (urbforge/redemptor#54).
set -euo pipefail
tag=${1:?usage: notes.sh TAG [CHANGELOG]}
file=${2:-CHANGELOG.md}
version=${tag#v}
version=${version%%-*}
notes=$(awk -v v="$version" '
  /^## \[/ { if (on) exit; on = (index($0, "## [" v "]") == 1); next }
  /^\[[^]]+\]: / { next }
  on { print }
' "$file" | sed -e '/./,$!d' | sed -e ':a' -e '/^\n*$/{$d;N;ba' -e '}')
if [ -z "$notes" ]; then
  echo "notes.sh: $file has no notes for $version: add a '## [$version] - <date>' section (move the Unreleased lines there)" >&2
  exit 1
fi
printf '%s\n' "$notes"
