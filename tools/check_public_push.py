#!/usr/bin/env python3
"""Audit outgoing Git content; unknown secrets still require human review.

Never expose matched data or patterns. Only use a base verified against public
upstream; this tool cannot establish trust in arbitrary local history.
"""
import argparse
import os
import pathlib
import re
import subprocess
import sys

FORBIDDEN = {".private", ".gitignore", ".dockerignore", ".env.example"}
GIT_FLAGS = {"--verify", "--end-of-options", "--show-toplevel", "--is-ancestor",
             "-r", "-z", "--stage", "-s", "--format=%B%x00%an%x00%ae%x00%cn%x00%ce"}
SAFE_ARGUMENT = re.compile(r"[A-Za-z0-9_][A-Za-z0-9_./{}^:-]*")


def git(root, *args):
    """Run fixed Git operations with validated arguments, never option-like refs."""
    if any(arg not in GIT_FLAGS and not SAFE_ARGUMENT.fullmatch(arg) for arg in args):
        raise ValueError("invalid Git argument")
    return subprocess.check_output(["git", "-C", str(pathlib.Path(root).resolve()), *args],
                                   stderr=subprocess.PIPE)


def forbidden(path):
    """Reject private storage and locally managed ignore/environment files."""
    return any(part in FORBIDDEN or part == ".env" or part.startswith(".env.")
               for part in path.split("/"))


def entries(data):
    """Decode NUL-delimited Git metadata without interpreting filenames as options."""
    for entry in data.split(b"\0"):
        if entry:
            meta, name = entry.split(b"\t", 1)
            yield meta.decode().split(), name


class PublicAudit:
    """Keep content caches separate from always-enforced path and metadata checks."""

    def __init__(self, root, patterns, allowed, identities):
        self.identities = set(identities)
        self.root = root
        self.patterns = patterns
        self.allowed = allowed
        self.allowed_contents = {git(root, "cat-file", "blob", oid) for oid in allowed}
        self.checked = set()
        self.findings = set()

    def matches(self, text):
        """Match only the external, operator-controlled denylist."""
        return any(pattern.search(text) for pattern in self.patterns)

    def content(self, data, label):
        """Record a redacted finding without retaining the matching content."""
        if self.matches(data.decode("utf-8", errors="replace")):
            self.findings.add(label + ": denylist match (content withheld)")

    def path(self, name, prefix):
        """Return a display-safe label and independently enforce path restrictions."""
        path = os.fsdecode(name)
        display = "[private-path]" if self.matches(path) else path
        label = prefix + display
        if forbidden(path):
            self.findings.add(label + ": forbidden path")
        self.content(name, label + ": path")
        return path, display, label

    def blob(self, oid, label):
        """Scan each distinct blob unless its exact public bytes were reviewed."""
        if oid not in self.checked and oid not in self.allowed:
            self.content(git(self.root, "cat-file", "blob", oid), label)
            self.checked.add(oid)

    def commit(self, commit):
        """Audit messages and author/committer identities, then the complete tree."""
        fields = git(self.root, "show", "-s", "--format=%B%x00%an%x00%ae%x00%cn%x00%ce",
                     commit).rstrip(b"\n").split(b"\0")
        self.content(fields[0], commit + ": commit message")
        for role, pair in (("author", fields[1:3]), ("committer", fields[3:5])):
            identity = tuple(value.decode("utf-8", errors="replace") for value in pair)
            if identity not in self.identities:
                self.content(b"\n".join(pair), commit + ": commit identities (" + role + ")")
        for (_, kind, oid), name in entries(git(self.root, "ls-tree", "-r", "-z", commit)):
            _, _, label = self.path(name, commit + ":")
            if kind == "blob":
                self.blob(oid, label)
            elif kind == "commit":
                self.findings.add(label + ": submodule requires separate public audit")

    def worktree(self, path, display):
        """Inspect tracked working bytes, reading symlink targets without following them."""
        file = self.root / path
        if file.is_symlink():
            data = os.fsencode(os.readlink(file))
        elif file.exists():
            data = file.read_bytes()
        else:
            return
        if data not in self.allowed_contents:
            self.content(data, "worktree:" + display)

    def index(self):
        """Inspect staged content even when the working copy has been cleaned."""
        for (mode, oid, stage), name in entries(git(self.root, "ls-files", "--stage", "-z")):
            path, display, label = self.path(name, "index:")
            if stage != "0":
                self.findings.add(label + ": unresolved merge")
            if mode == "160000":
                self.findings.add(label + ": submodule requires separate public audit")
                continue
            self.blob(oid, label)
            self.worktree(path, display)


def check(root, base, patterns, allow_public_blobs=(), allow_public_identities=()):
    """Check all outgoing history plus HEAD/index/worktree against a trusted base."""
    if not SAFE_ARGUMENT.fullmatch(base):
        raise ValueError("invalid public base")
    base_oid = git(root, "rev-parse", "--verify", "--end-of-options",
                   base + "^{commit}").decode().strip()
    try:
        git(root, "merge-base", "--is-ancestor", base_oid, "HEAD")
    except subprocess.CalledProcessError as error:
        raise ValueError("public base must be an ancestor of HEAD") from error
    allowed = set(allow_public_blobs)
    base_blobs = {oid for (_, kind, oid), _ in entries(git(root, "ls-tree", "-r", "-z", base_oid))
                  if kind == "blob"}
    if not allowed.issubset(base_blobs):
        raise ValueError("allowed blobs must exist in the trusted public base")
    audit = PublicAudit(root, patterns, allowed, map(tuple, allow_public_identities))
    commits = git(root, "rev-list", base_oid + "..HEAD").decode().splitlines()
    for commit in dict.fromkeys(commits + ["HEAD"]):
        audit.commit(commit)
    audit.index()
    return sorted(audit.findings), len(commits), base_oid


def read_patterns(filename, root):
    """Require the private rules to remain outside the public repository."""
    if not filename:
        raise ValueError("external denylist is required")
    denylist = pathlib.Path(filename).resolve()
    if denylist.is_relative_to(root.resolve()):
        raise ValueError("denylist must live outside the repository")
    lines = [line for line in denylist.read_text().splitlines()
             if line.strip() and not line.lstrip().startswith("#")]
    if not lines:
        raise ValueError("denylist must contain at least one pattern")
    return [re.compile(line, re.IGNORECASE) for line in lines]


def main():
    """Print safe diagnostics; Git errors and regex errors may contain private values."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True, help="verified public upstream commit or ref")
    parser.add_argument("--denylist", default=os.environ.get("ARKISM_PUBLIC_DENYLIST"),
                        help="external file: one Python regex per line, case-insensitive")
    parser.add_argument("--allow-public-blob", action="append", default=[],
                        help="reviewed exact blob hash present in trusted public base; repeatable")
    parser.add_argument("--allow-public-identity", nargs=2, action="append", default=[],
                        metavar=("NAME", "EMAIL"), help="verified exact public identity pair")
    parser.add_argument("--repo", type=pathlib.Path, default=pathlib.Path.cwd())
    args = parser.parse_args()
    try:
        root = pathlib.Path(git(args.repo, "rev-parse", "--show-toplevel").decode().strip())
        patterns = read_patterns(args.denylist, root)
        findings, count, base = check(root, args.base, patterns, args.allow_public_blob,
                                      args.allow_public_identity)
        if findings:
            print("\n".join(findings))
            return 1
        print(f"public preflight clean: {count} outgoing commits; public base {base}")
        return 0
    except (OSError, ValueError, re.error, subprocess.CalledProcessError):
        print("public preflight could not run: verify repository, ancestor base, "
              "and external nonempty Python-regex denylist", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
