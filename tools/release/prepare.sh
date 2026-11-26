#!/bin/bash
# prepare.sh VERSION [--push]: the release pull requests of urbex-cli and
# urbex, on docs/release-vVERSION in both (urbforge/urbex-cli#156):
#
# - CHANGELOG.md of each: VERSION's section, drafted from the pull
#   requests merged since the previous tag (changelog-draft.py);
# - urbex's docs: moved to VERSION (scripts/bump-docs-version.sh);
# - when a changelog has an Upgrading section, whether docs/upgrading.md
#   has its "From A to B" section - the pull request says when it hasn't.
#
# Without --push it stops before pushing: the two branches are ready in
# the checkouts, and the pull requests' descriptions are printed. With
# --push it pushes them and opens the two pull requests as drafts; gh
# needs a token that may push to and open pull requests in both
# repositories (the release GitHub App's, in the workflow). Nothing is
# tagged or published.
#
#   DOCS_DIR=../urbex tools/release/prepare.sh 0.3.0
#
# DOCS_DIR is a checkout of urbforge/urbex (default: ../urbex, next to
# this one); DATE the release's date (default: today, UTC).
set -euo pipefail

usage() { echo "usage: $0 VERSION [--push]   (VERSION as 0.3.0)" >&2; exit 2; }
[ $# -ge 1 ] && [ $# -le 2 ] || usage
version=${1#v}
push=${2:-}
[ -z "$push" ] || [ "$push" = --push ] || usage
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "prepare.sh: $1 is not a release version X.Y.Z" >&2; exit 2; }

here=$(cd "$(dirname "$0")" && pwd)
cli=$(git -C "$here" rev-parse --show-toplevel)
docs=$(cd "${DOCS_DIR:-$cli/../urbex}" && pwd)
date=${DATE:-$(date -u +%F)}
branch=docs/release-v$version
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# The tools as they are now: the checkout moves to main's release branch.
cp "$here/changelog-draft.py" "$here/notes.sh" "$tmp/"
here=$tmp

# Both checkouts on a new branch from their main, refusing a version
# tagged or prepared already.
for dir in "$cli" "$docs"; do
  git -C "$dir" fetch -q --tags origin main
  if git -C "$dir" rev-parse -q --verify "refs/tags/v$version" >/dev/null; then
    echo "prepare.sh: v$version is tagged already in $(basename "$dir")" >&2; exit 1
  fi
  if git -C "$dir" ls-remote --exit-code --heads origin "$branch" >/dev/null; then
    echo "prepare.sh: $branch exists already on $(basename "$dir")'s origin: delete it, or finish that pull request" >&2; exit 1
  fi
  git -C "$dir" switch -q -C "$branch" origin/main
done

# The changelogs.
python3 "$here/changelog-draft.py" --version "$version" --date "$date" --repo urbforge/urbex-cli \
  --git "$cli" --changelog "$cli/CHANGELOG.md" --report "$tmp/cli.json"
python3 "$here/changelog-draft.py" --version "$version" --date "$date" --repo urbforge/urbex \
  --git "$docs" --changelog "$docs/CHANGELOG.md" --report "$tmp/docs.json"

# The docs: from the version they name today - one, or the check fails
# first - to the new one.
old=$(python3 "$docs/scripts/docs-version.py" list | sed 's/.*: //' | sort -u)
if [ "$(wc -l <<<"$old")" -ne 1 ]; then
  echo "prepare.sh: the docs name several versions ($(tr '\n' ' ' <<<"$old")): fix them on main first (scripts/docs-version.py check)" >&2; exit 1
fi
"$docs/scripts/bump-docs-version.sh" "$old" "$version" | tail -n 1

# The upgrading guide, when a changelog asks for upgrade steps.
prev=$(python3 -c 'import json,sys; print((json.load(open(sys.argv[1]))["previous"] or "").lstrip("v"))' "$tmp/cli.json")
upgrading=
for file in "$cli/CHANGELOG.md" "$docs/CHANGELOG.md"; do
  if "$here/notes.sh" "v$version" "$file" | grep -q '^### Upgrading'; then upgrading=yes; fi
done
guide_note=
if [ -n "$upgrading" ] && ! grep -qE "^#+ From ${prev//./\\.} to ${version//./\\.}\b" "$docs/docs/upgrading.md"; then
  guide_note="**docs/upgrading.md has no \"From $prev to $version\" section**, and a changelog has Upgrading steps: write it in this pull request."
fi

git -C "$cli" commit -q -am "docs(release): $version in the CHANGELOG"
git -C "$docs" commit -q -am "docs(release): $version in the CHANGELOG, and the docs install it"

# The pull requests' descriptions.
list() { # list REPORT KEY: the report's pull requests, one bullet each
  python3 -c 'import json,sys; xs = json.load(open(sys.argv[1]))[sys.argv[2]]; print("\n".join("- " + x for x in xs) or "None.")' "$1" "$2"
}
since=${prev:+v$prev}
since=${since:-the first commit}
body() { # body REPO REPORT COMPANION EXTRA
  cat <<EOF
The release of urbex $version, prepared by \`tools/release/prepare.sh\` (urbforge/urbex-cli#156). Companion: $3.

## Summary

- \`CHANGELOG.md\`: the section of $version, a **draft** from the pull requests merged since $since. Before review:
  - write the intro (the comment at its top);
  - edit the entries for users: what they get, what breaks, in plain words. Move lines between sections, merge or drop them;
  - Breaking and Upgrading first, when the release has them.
$4
## Pull requests in the draft

$(list "$2" included)

## Left out

$(list "$2" left_out)
${guide_note:+
## Upgrading guide

$guide_note
}
## Before merging

- [ ] The intro and the entries edited
- [ ] urbex-cli's and urbex's release pull requests agree
- [ ] The release checklist's lab runs (a from-zero run and an upgrade on a test node), and the nightly e2e on main green

Nothing is tagged by this pull request: the tags \`v$version\` follow its merge.
EOF
}
docs_extra="- The docs install $version: \`scripts/bump-docs-version.sh $old $version\` (the places \`scripts/docs-version.conf\` lists).
"
body urbex-cli "$tmp/cli.json" "the urbex pull request, \`$branch\`" "" >"$tmp/cli.md"
body urbex "$tmp/docs.json" "the urbex-cli pull request, \`$branch\`" "$docs_extra" >"$tmp/docs.md"

if [ "$push" != --push ]; then
  echo "== Dry run: the branches $branch are ready in $cli and $docs, not pushed."
  echo "== urbex-cli's pull request:"; cat "$tmp/cli.md"
  echo "== urbex's pull request:"; cat "$tmp/docs.md"
  exit 0
fi

git -C "$cli" push -q -u origin "$branch"
git -C "$docs" push -q -u origin "$branch"
title="docs(release): $version"
cli_url=$(gh pr create --repo urbforge/urbex-cli --draft --base main --head "$branch" --title "$title in the CHANGELOG" --body-file "$tmp/cli.md")
sed -i "s#the urbex-cli pull request, \`$branch\`#$cli_url#" "$tmp/docs.md"
docs_url=$(gh pr create --repo urbforge/urbex --draft --base main --head "$branch" --title "$title in the CHANGELOG, and the docs install it" --body-file "$tmp/docs.md")
sed -i "s#the urbex pull request, \`$branch\`#$docs_url#" "$tmp/cli.md"
gh pr edit "$cli_url" --body-file "$tmp/cli.md" >/dev/null || gh api -X PATCH "repos/urbforge/urbex-cli/pulls/${cli_url##*/}" -F "body=@$tmp/cli.md" >/dev/null
echo "Opened $cli_url and $docs_url"
