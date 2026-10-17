#!/usr/bin/env python3
"""Run `go test ./...` with the slow packages split into parallel processes.

internal/memory and internal/host/crew run their tests one after another (no t.Parallel) and
take most of the CI time. Every Postgres test gets its own database, so the same compiled test
binary can run disjoint `-test.run` groups side by side. All other packages run as one plain
`go test` at the same time.

  tools/ci_go_test.py -race -tags=integration        # checks.yml
  tools/ci_go_test.py --coverprofile go-coverage.out  # coverage.yml
"""

import argparse
import concurrent.futures
import os
import re
import subprocess
import sys
import tempfile

HEAVY = ["./internal/memory", "./internal/host/crew"]


def run(cmd, cwd=None, env=None):
    p = subprocess.run(cmd, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    return p.returncode, p.stdout


def chunks(names, size):
    return [names[i:i + size] for i in range(0, len(names), size)]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--coverprofile")
    ap.add_argument("--jobs", type=int, default=max(4, (os.cpu_count() or 4)))
    opts, goflags = ap.parse_known_args()
    tmp = tempfile.mkdtemp(prefix="ci-go-test-")
    cover = ["-covermode=atomic"] if opts.coverprofile else []

    pkgs = run(["go", "list", *[f for f in goflags if f.startswith("-tags")], "./..."])[1].split()
    module = run(["go", "list", "-m"])[1].strip()
    heavy = {module + p[1:]: p for p in HEAVY}
    rest = [p for p in pkgs if p not in heavy]
    rest_cmd = ["go", "test", *goflags, *cover, "-count=1"]
    if opts.coverprofile:
        rest_cmd.append("-coverprofile=" + os.path.join(tmp, "rest.out"))
    pool = concurrent.futures.ThreadPoolExecutor(opts.jobs + 1)
    rest_job = pool.submit(run, rest_cmd + rest)

    jobs, covdirs = [], []
    for i, rel in enumerate(HEAVY):
        binary = os.path.join(tmp, f"heavy{i}.test")
        build = ["go", "test", "-c", *goflags, *(["-cover", *cover] if cover else []), "-o", binary, rel]
        code, out = run(build)
        if code:
            print(out)
            return code
        names = [n for n in run([binary, "-test.list", "."], cwd=rel)[1].split()
                 if re.match(r"(Test|Fuzz|Example)", n)]
        covdir = os.path.join(tmp, f"cov{i}")
        os.mkdir(covdir)
        covdirs.append(covdir)
        # Small groups keep every worker busy until the end; one group is a few seconds.
        for group in chunks(names, max(1, len(names) // (opts.jobs * 4))):
            cmd = [binary, "-test.run", "^(" + "|".join(group) + ")$", "-test.count=1", "-test.timeout=10m"]
            env = None
            if cover:
                # The flag is what the test binary reads; the env var, as `go test` sets it,
                # covers the test binary when a test re-runs it as a helper process.
                cmd.append("-test.gocoverdir=" + covdir)
                env = dict(os.environ, GOCOVERDIR=covdir)
            jobs.append((rel, pool.submit(run, cmd, rel, env), cmd, env))

    failed = False
    for rel, job, cmd, env in jobs:
        code, out = job.result()
        if not code:
            continue
        # A test with a time budget can miss it only because the groups share the machine.
        # Rerun each failed test once, alone; it must pass then, and the rerun is reported.
        names = re.findall(r"^--- FAIL: (\w+)", out, re.M)
        alone = [run(cmd[:2] + ["^" + n + "$"] + cmd[3:], rel, env) for n in names]
        if names and all(c == 0 for c, _ in alone):
            print(f"::warning::{rel}: {', '.join(names)} failed under parallel load and passed alone\n{out}")
            continue
        failed = True
        print(f"--- FAIL group in {rel}\n{out}")
    print(*[f"heavy package {p}: {sum(1 for r, *_ in jobs if r == p)} groups" for p in HEAVY], sep="\n")
    code, out = rest_job.result()
    print(out, end="")
    failed = failed or code != 0

    if opts.coverprofile and not failed:
        heavy_out = os.path.join(tmp, "heavy.out")
        code, out = run(["go", "tool", "covdata", "textfmt", "-i=" + ",".join(covdirs), "-o", heavy_out])
        if code:
            print(out)
            return code
        with open(opts.coverprofile, "w") as dst:
            for n, src in enumerate([os.path.join(tmp, "rest.out"), heavy_out]):
                lines = open(src).read().splitlines(keepends=True)
                dst.writelines(lines if n == 0 else lines[1:])  # one "mode:" line
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
