#!/usr/bin/env python3
"""Draft a release's section of CHANGELOG.md from the merged pull requests
(urbforge/urbex-cli#156).

  changelog-draft.py --version 0.3.0 --repo urbforge/urbex-cli [--git DIR]
  changelog-draft.py --version 0.3.0 --prs prs.json        (tests)

It moves `## [Unreleased]` - what is under it stays, first - to
`## [0.3.0] - <date>`, under a placeholder for the intro, and adds the
pull requests merged since the previous release, grouped by their
Conventional Commit type:

  feat -> Added; fix -> Fixed; docs, perf, refactor, revert -> Changed;
  a `security` label or a CVE in the title -> Security;
  `!` after the type or a `breaking` label -> Breaking too;
  an `## Upgrading` section in the description or an `upgrading` label
  -> Upgrading too, with that section;
  ci, test, build, chore, style, the scopes of the team's tooling (e2e,
  ci, test, release) and the release pull requests (docs/release-v*) are
  left out, and listed in the report.

Each entry is the title, the number and the user-facing part of the
description: its "For the release notes:" line when it has one, else the
first paragraph of its `## Summary`. It is a draft: a person edits it in
the release pull request. The link references at the end are updated.

--report FILE writes what the pull request's description needs: the
previous tag, the pull requests in and out, whether there is an
Upgrading section.

Exit status: 0 done, 1 refused (the version exists, or isn't newer),
2 a usage error.
"""

import argparse
import datetime
import json
import re
import subprocess
import sys

VERSION = re.compile(r"^(\d+)\.(\d+)\.(\d+)$")
TITLE = re.compile(r"^(?P<type>[a-z]+)(?:\((?P<scope>[^)]*)\))?(?P<bang>!)?:\s*(?P<desc>.+?)(?:\s+\(#\d+\))?$")
SECTIONS = ["Breaking", "Upgrading", "Security", "Added", "Changed", "Fixed"]
# Scopes of the team's own tooling: left out like ci and test.
INTERNAL_SCOPES = {"e2e", "ci", "test", "tests", "release"}
BY_TYPE = {"feat": "Added", "fix": "Fixed", "docs": "Changed", "perf": "Changed", "refactor": "Changed", "revert": "Changed"}
MAX_SUMMARY = 400


def parse_version(s):
    m = VERSION.match(s.lstrip("v"))
    if not m:
        raise ValueError("not a release version X.Y.Z: %r" % s)
    return tuple(int(x) for x in m.groups())


def previous_tag(tags, version):
    """The newest vX.Y.Z tag older than version, without a suffix."""
    want = parse_version(version)
    older = []
    for t in tags:
        try:
            v = parse_version(t)
        except ValueError:
            continue
        if v < want:
            older.append((v, t))
    return max(older)[1] if older else None


def git(gitdir, *args):
    return subprocess.run(["git", "-C", gitdir] + list(args), check=True, capture_output=True, text=True).stdout


def merged_prs(gitdir, repo, prev):
    """The pull requests of the commits since prev on HEAD: squash merges
    end their title with (#N), merge commits say "Merge pull request #N"."""
    span = "%s..HEAD" % prev if prev else "HEAD"
    numbers = []
    for subject in git(gitdir, "log", "--first-parent", "--format=%s", span).splitlines():
        m = re.search(r"\(#(\d+)\)$", subject) or re.match(r"^Merge pull request #(\d+)", subject)
        if m and int(m.group(1)) not in numbers:
            numbers.append(int(m.group(1)))
    prs = []
    for n in reversed(numbers):  # oldest first
        out = subprocess.run(["gh", "api", "repos/%s/pulls/%d" % (repo, n)], check=True, capture_output=True, text=True).stdout
        pr = json.loads(out)
        prs.append({
            "number": n,
            "title": pr["title"],
            "body": pr.get("body") or "",
            "labels": [l["name"] for l in pr.get("labels", [])],
            "head": pr["head"]["ref"],
        })
    return prs


def section(body, name):
    """The text of the body's `## name` section, or ""."""
    m = re.search(r"^##\s+%s\s*$(.*?)(?=^##\s|\Z)" % re.escape(name), body, re.M | re.S | re.I)
    return m.group(1).strip() if m else ""


def one_line(text):
    text = re.sub(r"\s+", " ", text).strip()
    if len(text) > MAX_SUMMARY:
        cut = text[:MAX_SUMMARY]
        stop = max(cut.rfind(". "), cut.rfind("; "))
        text = cut[: stop + 1] if stop > MAX_SUMMARY // 2 else cut.rstrip() + "..."
    return text


def summary(body):
    """The user-facing part of a description."""
    m = re.search(r"for the release notes[^:]*:\s*(.+)", body, re.I)
    if m:
        line = m.group(1).strip().strip("*_ ")
        # "*Added: daily backups ...*" -> "daily backups ..."
        line = re.sub(r"^(Added|Changed|Fixed|Security|Breaking|Upgrading)\s*:\s*", "", line, flags=re.I)
        return one_line(line)
    text = section(body, "Summary") or body
    intro = ""
    for para in re.split(r"\n\s*\n", text):
        para = para.strip()
        if not para or re.match(r"^(Fixes|Closes|Part of|Depends on|Refs?)\b", para, re.I):
            continue
        # The text before a list, or its first item when that text only
        # introduces it ("**Manifest**:", "`e2e/run.sh`:").
        items = [i.strip() for i in re.split(r"(?:^|\n)\s*[-*] ", para)]
        while len(items) > 1 and (not items[0] or is_intro(items[0])):
            items.pop(0)
        if is_intro(items[0]) and not intro:
            # "The docs for #152:", its list in the next paragraph.
            intro = items[0]
            continue
        return one_line((intro + " " + items[0]).strip())
    return one_line(intro)


def is_intro(text):
    """A short line that only introduces what follows it."""
    return text.endswith(":") and len(text) < 60


def classify(pr):
    """(sections, entry) for a pull request, or (None, reason) when left out."""
    if re.match(r"^docs/release-v", pr.get("head", "")):
        return None, "the release pull request"
    m = TITLE.match(pr["title"].strip())
    if not m:
        return None, "not a Conventional Commit title"
    labels = {l.lower() for l in pr.get("labels", [])}
    kind = m.group("type")
    if (m.group("scope") or "") in INTERNAL_SCOPES and "security" not in labels:
        return None, "%s(%s): the team's tooling, not for users" % (kind, m.group("scope"))
    sections = []
    if "security" in labels or re.search(r"\bCVE-\d{4}-\d+", pr["title"]):
        sections.append("Security")
    elif kind in BY_TYPE:
        sections.append(BY_TYPE[kind])
    else:
        return None, "%s: not for users" % kind
    if m.group("bang") or "breaking" in labels:
        sections.insert(0, "Breaking")
    text = summary(pr.get("body", ""))
    desc = m.group("desc").strip()
    desc = desc[:1].upper() + desc[1:]
    entry = "- **%s** (#%d)%s" % (desc, pr["number"], ": " + text if text else "")
    upgrading = section(pr.get("body", ""), "Upgrading")
    extra = {}
    if upgrading or "upgrading" in labels:
        extra["Upgrading"] = "- %s (#%d)" % (one_line(upgrading) if upgrading else m.group("desc").strip() + " - write the steps", pr["number"])
    return (sections, entry, extra), None


def draft(changelog, version, date, prs, repo_url):
    """The new CHANGELOG.md text and a report."""
    if re.search(r"^## \[%s\]" % re.escape(version), changelog, re.M):
        raise RuntimeError("CHANGELOG.md has a section for %s already" % version)
    m = re.search(r"^## \[Unreleased\]\s*$", changelog, re.M)
    if not m:
        raise RuntimeError("CHANGELOG.md has no '## [Unreleased]' heading")
    nxt = re.search(r"^## \[|^\[[^]]+\]: ", changelog[m.end():], re.M)
    end = m.end() + (nxt.start() if nxt else len(changelog) - m.end())
    unreleased = changelog[m.end():end].strip()

    entries = {s: [] for s in SECTIONS}
    included, left_out = [], []
    for pr in prs:
        got, why = classify(pr)
        if not got:
            left_out.append((pr, why))
            continue
        sections, entry, extra = got
        for s in sections:
            entries[s].append(entry)
        for s, e in extra.items():
            entries[s].append(e)
        included.append(pr)

    out = ["## [Unreleased]", "", "## [%s] - %s" % (version, date), "",
           "<!-- The intro: a paragraph for users - what this release brings, and what to read first (Breaking, Upgrading). Then edit the draft below. -->", ""]
    if unreleased:
        out += [unreleased, ""]
    for s in SECTIONS:
        if entries[s]:
            out += ["### " + s, ""] + entries[s] + [""]
    text = changelog[: m.start()] + "\n".join(out) + "\n" + changelog[end:].lstrip("\n")
    text = links(text, version, repo_url)
    return text, {"included": included, "left_out": left_out, "upgrading": bool(entries["Upgrading"])}


def links(text, version, repo_url):
    """The link references: Unreleased compares from the new tag, and every
    version heading has its release's link."""
    if not repo_url:
        return text
    versions = re.findall(r"^## \[(\d+\.\d+\.\d+)\]", text, re.M)
    lines = text.rstrip("\n").split("\n")
    refs = {}
    while lines and re.match(r"^\[[^]]+\]: ", lines[-1]):
        k, _, v = lines.pop().partition("]: ")
        refs[k[1:]] = v
    while lines and not lines[-1].strip():
        lines.pop()
    out = ["[Unreleased]: %s/compare/v%s...HEAD" % (repo_url, version)]
    for v in versions:
        out.append("[%s]: %s" % (v, refs.get(v, "%s/releases/tag/v%s" % (repo_url, v))))
    return "\n".join(lines) + "\n\n" + "\n".join(out) + "\n"


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--version", required=True)
    p.add_argument("--date", default=datetime.date.today().isoformat())
    p.add_argument("--changelog", default="CHANGELOG.md")
    p.add_argument("--repo", help="owner/name, for the pull requests and the links")
    p.add_argument("--git", default=".", help="the repository's checkout")
    p.add_argument("--prs", help="a JSON list of pull requests instead of git and gh (tests)")
    p.add_argument("--previous", help="the previous tag, instead of the newest older one")
    p.add_argument("--report", help="write the report, as JSON, here")
    a = p.parse_args(argv)
    try:
        parse_version(a.version)
    except ValueError as err:
        p.error(str(err))
    version = a.version.lstrip("v")

    prev = a.previous
    if a.prs:
        with open(a.prs) as f:
            prs = json.load(f)
    else:
        if not a.repo:
            p.error("--repo is needed without --prs")
        tags = git(a.git, "tag", "--list", "v*").split()
        if "v" + version in tags:
            print("v%s is tagged already" % version, file=sys.stderr)
            return 1
        prev = prev or previous_tag(tags, version)
        prs = merged_prs(a.git, a.repo, prev)

    with open(a.changelog) as f:
        changelog = f.read()
    repo_url = "https://github.com/%s" % a.repo if a.repo else None
    try:
        text, report = draft(changelog, version, a.date, prs, repo_url)
    except RuntimeError as err:
        print("changelog-draft: %s" % err, file=sys.stderr)
        return 1
    with open(a.changelog, "w") as f:
        f.write(text)
    if a.report:
        with open(a.report, "w") as f:
            json.dump({
                "previous": prev,
                "included": ["#%d %s" % (pr["number"], pr["title"]) for pr in report["included"]],
                "left_out": ["#%d %s (%s)" % (pr["number"], pr["title"], why) for pr, why in report["left_out"]],
                "upgrading": report["upgrading"],
            }, f, indent=1)
    print("CHANGELOG.md: %s drafted from %d pull requests (%d left out)" % (version, len(report["included"]), len(report["left_out"])))
    return 0


if __name__ == "__main__":
    sys.exit(main())
