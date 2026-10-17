#!/usr/bin/env python3
"""Run `go test ./...` with the slow packages split into parallel processes.

internal/memory and internal/host/crew run their tests one after another (no t.Parallel) and
take most of the CI time. Every Postgres test gets its own database, so the same compiled test
binary can run disjoint `-test.run` groups side by side. All other packages run as one plain
`go test` at the same time.

  tools/ci_go_test.py --race --integration   # checks.yml
  tools/ci_go_test.py --coverage              # coverage.yml, writes go-coverage.out
"""

import argparse
import concurrent.futures
import os
import re
import subprocess
import sys
import tempfile

HEAVY = ["./internal/memory", "./internal/host/crew"]
COVERAGE_FILE = "go-coverage.out"
# Tests with a wall-clock budget that can miss it only because the groups share the machine.
# They get one rerun alone, after every other group has finished; nothing else is rerun.
RERUNNABLE = {
    "TestPostgresCodeGraphArkism",  # impact query under 5s
    "TestSystemOneFailuresRecordOnce",  # timeout subtest
    "TestSessionArchiveZcodeLongMetadata", "TestPostgresSessionArchiveZcodeLongMetadata",  # 15s search deadline
    "TestPostgresSessionArchiveSQLiteLargeMessage",  # 10s read deadline
    "TestSkillComparisonRunner", "TestPostgresSkillComparisonRunner",  # adapter timeout
    "TestPostgresAutomaticWorkWriterExactRetryAndConflict",  # waits up to 30s for hook processes
    "TestCLIRunBoundedOutputTimeoutAndFailures",  # 4s budget per case
}


def run(cmd, cwd=None, env=None):
    p = subprocess.run(cmd, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    return p.returncode, p.stdout


def checked(cmd, cwd=None):
    code, out = run(cmd, cwd)
    if code:
        print(out)
        raise SystemExit(code)
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--race", action="store_true")
    ap.add_argument("--integration", action="store_true", help="build with -tags=integration")
    ap.add_argument("--coverage", action="store_true", help="write " + COVERAGE_FILE)
    # Both Go test jobs share the self-hosted runners' 10-CPU slice; more workers only add contention.
    ap.add_argument("--jobs", type=int, default=min(6, os.cpu_count() or 4))
    opts = ap.parse_args()
    tags = ["-tags=integration"] if opts.integration else []
    goflags = (["-race"] if opts.race else []) + tags
    jobs = max(1, opts.jobs)
    with tempfile.TemporaryDirectory(prefix="ci-go-test-") as tmp, \
            concurrent.futures.ThreadPoolExecutor(jobs + 1) as pool:
        rest = start_rest(goflags, tags, opts.coverage, tmp, pool)
        groups = [g for i, rel in enumerate(HEAVY)
                  for g in start_groups(rel, i, goflags, opts.coverage, jobs, tmp, pool)]
        failed = report(groups, rest)
        if opts.coverage and not failed:
            merge_coverage(tmp)
    return 1 if failed else 0


def start_rest(goflags, tags, coverage, tmp, pool):
    pkgs = checked(["go", "list", *tags, "./..."]).split()
    module = checked(["go", "list", "-m"]).strip()
    heavy = {module + p[1:] for p in HEAVY}
    cmd = ["go", "test", *goflags, "-count=1"]
    if coverage:
        cmd += ["-covermode=atomic", "-coverprofile=" + os.path.join(tmp, "rest.out")]
    return pool.submit(run, cmd + [p for p in pkgs if p not in heavy])


def start_groups(rel, i, goflags, coverage, jobs, tmp, pool):
    binary = os.path.join(tmp, f"heavy{i}.test")
    cover = ["-cover", "-covermode=atomic"] if coverage else []
    checked(["go", "test", "-c", *goflags, *cover, "-o", binary, rel])
    names = [n for n in checked([binary, "-test.list", "."], cwd=rel).split()
             if re.match(r"(Test|Fuzz|Example)", n)]
    if not names:
        print(f"::error::{rel}: no tests listed")
        raise SystemExit(1)
    covdir = os.path.join(tmp, f"cov{i}")
    os.mkdir(covdir)
    env = dict(os.environ, GOCOVERDIR=covdir) if coverage else None
    size = max(1, len(names) // (jobs * 4))  # small groups keep every worker busy until the end
    groups = []
    for start in range(0, len(names), size):
        cmd = [binary, "-test.run", "^(" + "|".join(names[start:start + size]) + ")$",
               "-test.count=1", "-test.timeout=10m"]
        if coverage:
            # The flag is what the test binary reads; the env var, as `go test` sets it,
            # covers the test binary when a test re-runs it as a helper process.
            cmd.append("-test.gocoverdir=" + covdir)
        groups.append((rel, pool.submit(run, cmd, rel, env), cmd, env))
    return groups


def report(groups, rest):
    results = [(rel, *job.result(), cmd, env) for rel, job, cmd, env in groups]
    rest_code, rest_out = rest.result()
    for p in HEAVY:
        print(f"heavy package {p}: {sum(1 for r, *_ in results if r == p)} groups")
    print(rest_out, end="")
    failed = rest_code != 0
    for rel, code, out, cmd, env in results:
        if code and not passes_alone(rel, out, cmd, env):
            failed = True
            print(f"--- FAIL group in {rel}\n{out}")
    return failed


def passes_alone(rel, out, cmd, env):
    names = re.findall(r"^--- FAIL: (\w+)", out, re.M)
    if not names or not set(names) <= RERUNNABLE or "DATA RACE" in out or "race detected" in out:
        return False
    if any(run(cmd[:2] + ["^" + n + "$"] + cmd[3:], rel, env)[0] for n in names):
        return False
    print(f"::warning::{rel}: {', '.join(names)} missed a time budget under parallel load and passed alone\n{out}")
    return True


def merge_coverage(tmp):
    heavy_out = os.path.join(tmp, "heavy.out")
    covdirs = ",".join(os.path.join(tmp, f"cov{i}") for i in range(len(HEAVY)))
    checked(["go", "tool", "covdata", "textfmt", "-i=" + covdirs, "-o", heavy_out])
    with open(COVERAGE_FILE, "w") as dst:
        for n, src in enumerate([os.path.join(tmp, "rest.out"), heavy_out]):
            with open(src) as f:
                lines = f.readlines()
            dst.writelines(lines if n == 0 else lines[1:])  # one "mode:" line


if __name__ == "__main__":
    sys.exit(main())
