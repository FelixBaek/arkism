#!/usr/bin/env python3
"""Check an outgoing public branch, including intermediate commits, without printing content."""
import argparse
import os
import pathlib
import re
import subprocess
import sys

FORBIDDEN = {".private", ".gitignore", ".dockerignore", ".env.example"}


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], stderr=subprocess.PIPE)


def forbidden(path):
    return any(part in FORBIDDEN or part == ".env" or part.startswith(".env.")
               for part in path.split("/"))


def check(root, base, patterns, allow_public_blobs=()):
    findings = set()
    base_oid = git(root, "rev-parse", "--verify", base + "^{commit}").decode().strip()
    if subprocess.run(["git", "-C", str(root), "merge-base", "--is-ancestor",
                       base_oid, "HEAD"], capture_output=True).returncode:
        raise ValueError("public base must be an ancestor of HEAD; start from a public clone")
    allowed = set(allow_public_blobs)
    base_blobs = set()
    for entry in git(root, "ls-tree", "-r", "-z", base_oid).split(b"\0"):
        if entry:
            meta, _ = entry.split(b"\t", 1)
            _, kind, oid = meta.decode().split()
            if kind == "blob":
                base_blobs.add(oid)
    if not allowed.issubset(base_blobs):
        raise ValueError("allowed blobs must exist in the trusted public base")
    allowed_contents = {git(root, "cat-file", "blob", oid) for oid in allowed}
    checked = set()

    def content(data, label):
        if any(p.search(data.decode("utf-8", errors="replace")) for p in patterns):
            findings.add(label + ": denylist match (content withheld)")

    def blob(oid, label):
        if oid not in checked and oid not in allowed:
            content(git(root, "cat-file", "blob", oid), label)
            checked.add(oid)

    commits = git(root, "rev-list", base_oid + "..HEAD").decode().splitlines()
    # HEAD is checked even when nothing is outgoing. Each outgoing tree catches
    # files added then deleted, including commits reachable through merge parents.
    for commit in list(dict.fromkeys(commits + ["HEAD"])):
        content(git(root, "show", "-s", "--format=%B", commit), commit + ": commit message")
        for entry in git(root, "ls-tree", "-r", "-z", commit).split(b"\0"):
            if not entry:
                continue
            meta, name = entry.split(b"\t", 1)
            mode, kind, oid = meta.decode().split()
            path = os.fsdecode(name)
            label = commit + ":" + (
                "[private-path]" if any(p.search(path) for p in patterns) else path)
            if forbidden(path):
                findings.add(label + ": forbidden path")
            content(name, label + ": path")
            if kind == "blob":
                blob(oid, label)
            elif kind == "commit":
                findings.add(label + ": submodule requires separate public audit")
    for entry in git(root, "ls-files", "--stage", "-z").split(b"\0"):
        if not entry:
            continue
        meta, name = entry.split(b"\t", 1)
        mode, oid, stage = meta.decode().split()
        path = os.fsdecode(name)
        display = "[private-path]" if any(p.search(path) for p in patterns) else path
        if stage != "0":
            findings.add("index:" + display + ": unresolved merge")
        if forbidden(path):
            findings.add("index:" + display + ": forbidden path")
        content(name, "index:" + display + ": path")
        if mode == "160000":
            findings.add("index:" + display + ": submodule requires separate public audit")
            continue
        blob(oid, "index:" + display)
        file = root / path
        if file.is_symlink():
            data = os.fsencode(os.readlink(file))
            if data not in allowed_contents:
                content(data, "worktree:" + display)
        elif file.exists():
            data = file.read_bytes()
            if data not in allowed_contents:
                content(data, "worktree:" + display)
    return sorted(findings), len(commits), base_oid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True, help="verified public upstream commit or ref")
    parser.add_argument("--denylist", default=os.environ.get("ARKISM_PUBLIC_DENYLIST"),
                        help="external file: one Python regex per line, case-insensitive")
    parser.add_argument("--allow-public-blob", action="append", default=[],
                        help="reviewed exact blob hash present in trusted public base; repeatable")
    parser.add_argument("--repo", type=pathlib.Path, default=pathlib.Path.cwd())
    args = parser.parse_args()
    try:
        if not args.denylist:
            raise ValueError("--denylist or ARKISM_PUBLIC_DENYLIST is required")
        root = pathlib.Path(git(args.repo, "rev-parse", "--show-toplevel").decode().strip())
        denylist = pathlib.Path(args.denylist).resolve()
        if denylist.is_relative_to(root.resolve()):
            raise ValueError("denylist must live outside the repository")
        lines = [line for line in denylist.read_text().splitlines()
                 if line.strip() and not line.lstrip().startswith("#")]
        if not lines:
            raise ValueError("denylist must contain at least one pattern")
        patterns = [re.compile(line, re.IGNORECASE) for line in lines]
        findings, count, base = check(root, args.base, patterns, args.allow_public_blob)
        if findings:
            for finding in findings:
                print(finding)
            return 1
        print(f"public preflight clean: {count} outgoing commits; public base {base}")
        return 0
    except (OSError, ValueError, re.error, subprocess.CalledProcessError):
        # Git and regex diagnostics can contain private command arguments/patterns.
        print("public preflight could not run: verify repository, ancestor base, "
              "and external nonempty Python-regex denylist", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
