#!/usr/bin/env python3
"""Tests of changelog-draft.py (urbforge/urbex-cli#156). Run by 'mise run test'."""

import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True  # no __pycache__ next to the scripts
HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("draft", os.path.join(HERE, "changelog-draft.py"))
d = importlib.util.module_from_spec(spec)
spec.loader.exec_module(d)

CHANGELOG = """# Changelog

Intro of the file.

## [Unreleased]

### Plugin 0.2.4

- Already written for the next release.

## [0.2.0] - 2026-11-20

### Added

- An old feature.

## [0.1.0] - 2026-11-17

The first one.

[Unreleased]: https://github.com/o/r/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/o/r/releases/tag/v0.1.0
"""


def write(path, text):
    with open(path, "w") as f:
        f.write(text)


def read(path):
    with open(path) as f:
        return f.read()


def pr(n, title, body="", labels=(), head="feat/x"):
    return {"number": n, "title": title, "body": body, "labels": list(labels), "head": head}


PRS = [
    pr(1, "feat(apps): devices for an application (#1)", "Fixes o/r#9\n\n## Summary\n\n- **Manifest**:\n  - `devices:` on a service.\n- more\n\n## Why\n\nx"),
    pr(2, "fix(apply): refuse an uncommitted urbex.yaml", "## Summary\n\nApply refuses it before creating anything.\n\nMore detail."),
    pr(3, "docs: a recipe", "## Summary\n\nThe docs for o/r#5:\n\n- the cookbook recipe;\n- the reference."),
    pr(4, "feat(manifest)!: rename a field", "## Summary\n\nThe field is renamed.\n\n## Upgrading\n\nRename `a` to `b` in urbex.yaml."),
    pr(5, "fix: patch the image", "## Summary\n\nPatched.", labels=["security"]),
    pr(6, "ci: faster lint"),
    pr(7, "fix(e2e): a flaky step"),
    pr(8, "docs(changelog): the release of 0.3.0", head="docs/release-v0.3.0"),
    pr(9, "feat(backups): daily backups", "## Summary\n\nLong text.\n\nFor the release notes: *Added: daily backups of the machines.*"),
    pr(10, "Update README", "no type"),
]


class Draft(unittest.TestCase):
    def setUp(self):
        self.text, self.report = d.draft(CHANGELOG, "0.3.0", "2026-12-01", PRS, "https://github.com/o/r")

    def section(self, name):
        part = self.text.split("## [0.3.0] - 2026-12-01", 1)[1].split("## [0.2.0]", 1)[0]
        return part.split("### " + name + "\n", 1)[1].split("\n### ", 1)[0] if "### " + name + "\n" in part else ""

    def test_the_new_section_under_unreleased_keeps_what_was_there(self):
        self.assertIn("## [Unreleased]\n\n## [0.3.0] - 2026-12-01\n\n<!-- The intro", self.text)
        new = self.text.split("## [0.3.0]", 1)[1].split("## [0.2.0]", 1)[0]
        self.assertIn("### Plugin 0.2.4\n\n- Already written for the next release.", new)
        self.assertLess(new.index("### Plugin 0.2.4"), new.index("### Added"))
        self.assertEqual(self.text.count("Already written"), 1)

    def test_grouped_by_type(self):
        self.assertIn("- **Devices for an application** (#1): `devices:` on a service.", self.section("Added"))
        self.assertIn("- **Refuse an uncommitted urbex.yaml** (#2): Apply refuses it before creating anything.", self.section("Fixed"))
        self.assertIn("- **A recipe** (#3): The docs for o/r#5: the cookbook recipe;", self.section("Changed"))
        self.assertIn("(#5)", self.section("Security"))
        self.assertNotIn("(#5)", self.section("Fixed"))

    def test_breaking_and_upgrading(self):
        self.assertIn("- **Rename a field** (#4)", self.section("Breaking"))
        self.assertIn("- **Rename a field** (#4)", self.section("Added"))
        self.assertIn("- Rename `a` to `b` in urbex.yaml. (#4)", self.section("Upgrading"))
        self.assertTrue(self.report["upgrading"])
        new = self.text.split("## [0.3.0]", 1)[1]
        self.assertLess(new.index("### Breaking"), new.index("### Upgrading"))
        self.assertLess(new.index("### Upgrading"), new.index("### Added"))

    def test_the_release_notes_line_wins(self):
        self.assertIn("- **Daily backups** (#9): daily backups of the machines.", self.section("Added"))

    def test_left_out(self):
        out = {p["number"]: why for p, why in self.report["left_out"]}
        self.assertEqual(sorted(out), [6, 7, 8, 10])
        self.assertIn("the release pull request", out[8])
        for n in (6, 7, 8, 10):
            self.assertNotIn("(#%d)" % n, self.text)

    def test_links(self):
        tail = self.text.rstrip().split("\n")[-4:]
        self.assertEqual(tail, [
            "[Unreleased]: https://github.com/o/r/compare/v0.3.0...HEAD",
            "[0.3.0]: https://github.com/o/r/releases/tag/v0.3.0",
            "[0.2.0]: https://github.com/o/r/releases/tag/v0.2.0",
            "[0.1.0]: https://github.com/o/r/releases/tag/v0.1.0",
        ])

    def test_a_version_drafted_already_is_refused(self):
        with self.assertRaises(RuntimeError):
            d.draft(self.text, "0.3.0", "2026-12-01", PRS, "https://github.com/o/r")


class Helpers(unittest.TestCase):
    def test_previous_tag(self):
        tags = ["v0.1.0", "v0.2.0-rc.1", "v0.2.0", "v0.10.0", "junk"]
        self.assertEqual(d.previous_tag(tags, "0.3.0"), "v0.2.0")
        self.assertEqual(d.previous_tag(tags, "0.11.0"), "v0.10.0")
        self.assertIsNone(d.previous_tag(tags, "0.0.1"))

    def test_a_long_summary_is_cut_at_a_sentence(self):
        s = d.summary("## Summary\n\n" + "A sentence. " * 60)
        self.assertLessEqual(len(s), d.MAX_SUMMARY)
        self.assertTrue(s.endswith("."))


class Command(unittest.TestCase):
    def test_writes_the_file_and_the_report(self):
        with tempfile.TemporaryDirectory() as tmp:
            cl, prs, rep = (os.path.join(tmp, n) for n in ("CHANGELOG.md", "prs.json", "report.json"))
            write(cl, CHANGELOG)
            write(prs, json.dumps(PRS))
            run = lambda: subprocess.run([sys.executable, os.path.join(HERE, "changelog-draft.py"), "--version", "0.3.0",
                                          "--changelog", cl, "--prs", prs, "--report", rep], capture_output=True, text=True)
            r = run()
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn("## [0.3.0]", read(cl))
            self.assertEqual(len(json.loads(read(rep))["included"]), 6)
            r = run()
            self.assertEqual(r.returncode, 1, "a second run must refuse")
            self.assertIn("already", r.stderr)


if __name__ == "__main__":
    unittest.main(verbosity=1)
