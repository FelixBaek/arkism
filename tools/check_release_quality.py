"""Require exact-commit correctness, Sonar, coverage and default-setup CodeQL."""
import argparse
import json
import os
import re
import subprocess
import sys
import time
from urllib.parse import urlencode
from urllib.request import HTTPRedirectHandler, Request, build_opener
from http.client import HTTPException


WORKFLOWS = ("checks.yml", "sonarcloud.yml", "coverage.yml", "codeql")
CODEQL_PATH = "dynamic/github-code-scanning/codeql"
PENDING = {"queued", "in_progress", "waiting", "pending", "requested"}
MAX_PAGES = 10
MAX_PAGE_BYTES = 5 * 1024 * 1024


class GateError(Exception):
    """The release cannot be verified; never include API bodies or stderr."""


class NoApiRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def github_auth(timeout):
    value = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")
    if not value:
        # Local verification can use the existing gh login. No request arguments
        # reach this fixed credential lookup, and its output is never logged.
        try:
            result = subprocess.run(["gh", "auth", "token", "--hostname", "github.com"],
                                    shell=False, capture_output=True, text=True,
                                    check=False, timeout=timeout)
        except (OSError, UnicodeError, subprocess.TimeoutExpired) as exc:
            raise GateError("GitHub credentials unavailable") from exc
        if result.returncode:
            raise GateError("GitHub credentials unavailable")
        value = result.stdout
    value = value.strip()
    if not value or any(char.isspace() for char in value):
        raise GateError("invalid GitHub credentials")
    return value


def request_page(url, auth, timeout):
    request = Request(url, method="GET", headers={
        "Authorization": "Bearer " + auth,
        "Accept": "application/vnd.github+json",
        "User-Agent": "arkism-release-quality",
    })
    try:
        with build_opener(NoApiRedirect()).open(request, timeout=timeout) as response:
            if response.status != 200:
                raise GateError("unexpected GitHub API status")
            body = response.read(MAX_PAGE_BYTES + 1)
        if len(body) > MAX_PAGE_BYTES:
            raise GateError("GitHub API page exceeds the size limit")
        return json.loads(body)
    except (OSError, ValueError, HTTPException) as exc:
        raise GateError("GitHub API request or response unavailable") from exc


def fetch_runs(repository, sha, workflow, timeout):
    validate_request(repository, sha, 0, 1)
    if workflow not in WORKFLOWS:
        raise GateError("unknown release quality workflow")
    deadline = time.monotonic() + timeout
    auth = github_auth(timeout)
    identifier = resolve_codeql(repository, auth, deadline) if workflow == "codeql" else workflow
    pages = fetch_pages(repository, sha, workflow, auth, deadline, identifier)
    return validate_run_inventory(pages, workflow)


def workflow_path(workflow):
    return CODEQL_PATH if workflow == "codeql" else f".github/workflows/{workflow}"


def workflow_event(workflow):
    return "dynamic" if workflow == "codeql" else "push"


def resolve_codeql(repository, auth, deadline):
    """Default-setup CodeQL has a dynamic path, not a repository YAML filename."""
    found, received, total = [], 0, None
    for number in range(1, MAX_PAGES + 1):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise GateError("CodeQL inventory request timed out")
        page = request_page(f"https://api.github.com/repos/{repository}/actions/workflows?per_page=100&page={number}", auth, remaining)
        total, items = codeql_inventory(page, total)
        for item in items:
            identifier = active_codeql(item)
            if identifier:
                found.append(identifier)
        received += len(items)
        if received >= total:
            if received != total or len(found) != 1:
                raise GateError("missing or ambiguous active CodeQL workflow")
            return found[0]
        if not items:
            break
    raise GateError("incomplete CodeQL workflow inventory")


def codeql_inventory(page, previous_total):
    if not isinstance(page, dict) or type(page.get("total_count")) is not int or not isinstance(page.get("workflows"), list):
        raise GateError("invalid CodeQL workflow inventory")
    total = page["total_count"]
    if total < 0 or (previous_total is not None and previous_total != total):
        raise GateError("CodeQL workflow inventory changed")
    return total, page["workflows"]


def active_codeql(item):
    if not isinstance(item, dict):
        raise GateError("invalid CodeQL workflow entry")
    if item.get("path") != CODEQL_PATH or item.get("state") != "active":
        return None
    if type(item.get("id")) is not int or item["id"] <= 0:
        raise GateError("invalid CodeQL workflow identifier")
    return str(item["id"])


def fetch_pages(repository, sha, workflow, auth, deadline, identifier=None):
    pages, received = [], 0
    for number in range(1, MAX_PAGES + 1):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise GateError("GitHub API request timed out")
        query = urlencode({"head_sha": sha, "event": workflow_event(workflow), "branch": "main",
                           "per_page": 100, "page": number})
        url = f"https://api.github.com/repos/{repository}/actions/workflows/{identifier or workflow}/runs?{query}"
        page = request_page(url, auth, remaining)
        total, items = page_inventory(page, workflow)
        pages.append(page)
        received += len(items)
        if received >= total:
            return pages
        if not items:
            raise GateError("incomplete GitHub run inventory")
    raise GateError("GitHub run inventory exceeds the page limit")


def page_inventory(page, workflow):
    if not isinstance(page, dict):
        raise GateError(f"{workflow}: invalid GitHub API page")
    count, items = page.get("total_count"), page.get("workflow_runs")
    if type(count) is not int or count < 0 or not isinstance(items, list):
        raise GateError(f"{workflow}: invalid GitHub run inventory")
    return count, items


def run_identifier(run, workflow):
    if not isinstance(run, dict) or type(run.get("id")) is not int or run["id"] <= 0:
        raise GateError(f"{workflow}: invalid GitHub run identifier")
    return run["id"]


def validate_run_inventory(pages, workflow):
    if not isinstance(pages, list) or not pages:
        raise GateError(f"{workflow}: missing GitHub API pages")
    runs, total = [], None
    for page in pages:
        count, items = page_inventory(page, workflow)
        if total is not None and total != count:
            raise GateError(f"{workflow}: GitHub run inventory changed during pagination")
        total = count
        runs.extend(items)
    if len(runs) != total:
        raise GateError(f"{workflow}: incomplete GitHub run inventory")
    ids = [run_identifier(run, workflow) for run in runs]
    if len(set(ids)) != len(ids):
        raise GateError(f"{workflow}: duplicate GitHub run identifier")
    return runs


def latest_run(runs, repository, sha, workflow):
    matching = []
    for run in runs:
        if (run.get("head_sha") != sha or run.get("event") != workflow_event(workflow)
                or run.get("head_branch") != "main"
                or run.get("path") != workflow_path(workflow)):
            continue
        for field in ("repository", "head_repository"):
            value = run.get(field)
            if not isinstance(value, dict) or value.get("full_name") != repository:
                break
        else:
            if type(run.get("run_attempt")) is not int or run["run_attempt"] <= 0:
                raise GateError(f"{workflow}: missing run attempt")
            matching.append(run)
    return max(matching, key=lambda run: (run["id"], run["run_attempt"]), default=None)


def validate_request(repository, sha, timeout_seconds, poll_seconds):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*", repository):
        raise GateError("expected an owner/repository name")
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise GateError("expected a full lowercase commit SHA")
    if not 0 <= timeout_seconds <= 1800 or not 1 <= poll_seconds <= 60:
        raise GateError("invalid wait limits")


def quality_evidence(run, workflow):
    if run is None:
        return None
    status = run.get("status")
    if not isinstance(status, str):
        raise GateError(f"{workflow}: invalid run status")
    if status in PENDING:
        return None
    if status != "completed":
        raise GateError(f"{workflow}: unknown run status")
    if run.get("conclusion") != "success":
        raise GateError(f"{workflow}: latest run did not succeed")
    return {"workflow": workflow, "run_id": run["id"], "attempt": run["run_attempt"]}


def read_quality_once(repository, sha, deadline, single_check):
    pending, verified = [], []
    for workflow in WORKFLOWS:
        remaining = deadline - time.monotonic()
        # A zero wait still performs one bounded API check per workflow.
        request_timeout = 30 if single_check else min(30, max(0.1, remaining))
        runs = fetch_runs(repository, sha, workflow, request_timeout)
        evidence = quality_evidence(latest_run(runs, repository, sha, workflow), workflow)
        if evidence is None:
            pending.append(workflow)
        else:
            verified.append(evidence)
    return pending, verified


def verify(repository, sha, timeout_seconds=1800, poll_seconds=20):
    validate_request(repository, sha, timeout_seconds, poll_seconds)
    deadline = time.monotonic() + timeout_seconds
    while True:
        pending, verified = read_quality_once(repository, sha, deadline, timeout_seconds == 0)
        if not pending:
            return {"status": "verified", "commit": sha, "workflows": verified}
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise GateError("timed out waiting for exact-commit quality gates")
        time.sleep(min(poll_seconds, remaining))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--timeout-seconds", type=int, default=1800)
    parser.add_argument("--poll-seconds", type=int, default=20)
    args = parser.parse_args()
    try:
        result = verify(args.repository, args.sha, args.timeout_seconds, args.poll_seconds)
    except GateError as exc:
        print(f"Release quality gate blocked: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
