#!/bin/bash
# Tests of notes.sh (urbforge/redemptor#54). Run by 'mise run test'.
set -u
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cat >"$tmp/CHANGELOG.md" <<'MD'
# Changelog

## [Unreleased]

- Something not released yet.

## [0.2.0] - 2026-12-01

### Added

- A feature.

## [0.1.0] - 2026-11-17

The first one.

## [0.0.9] - 2026-11-01

[0.2.0]: https://example.com/0.2.0
MD
fails=0
expect() { # expect NAME WANT GOT
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got [$3], want [$2]"; fails=$((fails + 1)); fi
}
expect "a version's section, without its heading" "$(printf '### Added\n\n- A feature.')" "$("$here/notes.sh" v0.2.0 "$tmp/CHANGELOG.md")"
expect "an rc takes its version's notes" "The first one." "$("$here/notes.sh" v0.1.0-rc.3 "$tmp/CHANGELOG.md")"
"$here/notes.sh" v0.3.0 "$tmp/CHANGELOG.md" >/dev/null 2>&1; expect "a version without a section fails" 1 $?
"$here/notes.sh" v0.0.9 "$tmp/CHANGELOG.md" >/dev/null 2>&1; expect "an empty section fails" 1 $?
"$here/notes.sh" v0.1.0 "$here/../../CHANGELOG.md" >/dev/null; expect "the repository's CHANGELOG.md has 0.1.0" 0 $?
[ $fails -eq 0 ] || { echo "$fails failed"; exit 1; }
