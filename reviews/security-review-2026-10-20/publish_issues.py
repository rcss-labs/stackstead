#!/usr/bin/env python3
"""Preview or publish the approved review backlog, reusing stable issue markers.

Requires an authenticated GitHub CLI. Defaults to a read-only dry run. Publication
creates issues only, and never closes or edits an existing issue. A retry lists
GitHub again so a successful-but-interrupted creation is not duplicated.
"""

import argparse
import json
from pathlib import Path
import shlex
import subprocess
import sys


def gh(*args):
    result = subprocess.run(["gh", *args], capture_output=True, text=True, check=False)
    if result.returncode:
        raise RuntimeError(f"GitHub command failed (exit {result.returncode}); inspect GitHub before retrying.")
    return result.stdout.strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--dry-run", action="store_true", help="show exact planned commands; make no changes (default)")
    mode.add_argument("--publish", action="store_true", help="create the approved issues and save their URLs")
    args = parser.parse_args()
    folder = Path(__file__).resolve().parent
    manifest = json.loads((folder / "issues-manifest.json").read_text())
    repo = manifest["repository"]
    existing = json.loads(gh("issue", "list", "--repo", repo, "--state", "all", "--limit", "1000",
                             "--json", "number,title,body,url"))
    results = []
    for item in manifest["issues"]:
        body = (folder / item["body_file"]).resolve()
        if not body.is_relative_to(folder) or not body.is_file():
            raise RuntimeError(f"Invalid body file for {item['id']}")
        marker = f"<!-- stackstead-review:{manifest['review_date']}:{item['id']} -->"
        if marker not in body.read_text():
            raise RuntimeError(f"Missing stable marker for {item['id']}")
        found = next((issue for issue in existing if marker in (issue.get("body") or "")
                      or issue["title"] == item["title"]), None)
        if found:
            print(f"Reuse {item['id']}: {found['url']}", flush=True)
            result = {"id": item["id"], "aliases": item["aliases"], "title": found["title"],
                      "url": found["url"], "number": found["number"], "created": False}
        else:
            command = ["issue", "create", "--repo", repo, "--title", item["title"], "--body-file", str(body)]
            for label in item["labels"]:
                command.extend(["--label", label])
            print(shlex.join(["gh", *command]), flush=True)
            if not args.publish:
                continue
            url = gh(*command)
            if not url.startswith(f"https://github.com/{repo}/issues/"):
                raise RuntimeError(f"Unexpected creation response for {item['id']}; inspect GitHub before retrying.")
            number = int(url.rsplit("/", 1)[1])
            result = {"id": item["id"], "aliases": item["aliases"], "title": item["title"],
                      "url": url, "number": number, "created": True}
            existing.append({"title": item["title"], "body": marker, "url": url, "number": number})
            print(f"Created {item['id']}: {url}", flush=True)
        results.append(result)
        if args.publish:
            # Persist each confirmed creation, so partial runs remain inspectable.
            (folder / "github-issues.json").write_text(json.dumps(results, indent=2) + "\n")
    if not args.publish:
        print("Dry run only; no GitHub issues or local result files were created.")
        return 0
    lines = ["# Unified review issue index — 20 October 2026", "",
             "[Security assessment](../security-review-2026-10-20.md) · "
             "[Earlier codebase review](../codebase-review-2026-10-20.md)", "",
             f"{len(results)} backlog items published or reused by stable marker. "
             "SEC-07/F10 share one issue; SEC-08 reuses pre-existing #18. "
             "No finding is marked remediated by issue creation.", "",
             "| Finding | GitHub issue |", "| --- | --- |"]
    for result in results:
        identity = "/".join([result["id"], *result["aliases"]])
        lines.append(f"| {identity} | [#{result['number']} — {result['title']}]({result['url']}) |")
    for item in manifest["existing"]:
        lines.append(f"| {item['id']} (pre-existing) | [#{item['number']} — {item['title']}]({item['url']}) |")
    lines.extend(["", "Local issue bodies are under `issues/`; the manifest and confirmed URLs are stored "
                  "in `issues-manifest.json` and `github-issues.json`. "
                  "Re-running with `--dry-run` verifies reuse without posting anything.", ""])
    (folder / "issues.md").write_text("\n".join(lines))
    print(f"Confirmed {len(results)} planned items; wrote issues.md and github-issues.json.")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (RuntimeError, OSError, ValueError) as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
