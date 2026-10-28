"""Run the public checkout's local checks without credentials or private docs."""
import os
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


def main():
    # The server's arkism.env (loaded by the ark hook wrapper) switches the live host to session-processor mode;
    # tests must see the code's defaults, not the operator's runtime switches.
    for key in ("ARKISM_SESSION_PROCESSOR", "ARKISM_HARNESS_TOKEN_FILE"):
        os.environ.pop(key, None)
    # Archive copies have no VCS metadata, including for binaries built by tests.
    os.environ["GOFLAGS"] = os.environ.get("GOFLAGS", "") + " -buildvcs=false"
    subprocess.run([sys.executable, "tools/check_roadmap_plan.py"], cwd=ROOT, check=True)
    subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", "tools",
                    "-p", "test_*.py"], cwd=ROOT, check=True)
    for directory in ("evaluation", "evaluation/reuse-v010/benchmark",
                      "evaluation/memory-harness-v1"):
        subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", directory,
                        "-p", "test_*.py"], cwd=ROOT, check=True)
    # Commits on this branch must say why, what was verified, and link research where a study screen changed.
    subprocess.run([sys.executable, "tools/check_work_record.py"], cwd=ROOT, check=True)
    subprocess.run(["go", "run", "./tools/complexity"], cwd=ROOT, check=True)
    subprocess.run(["go", "vet", "./..."], cwd=ROOT, check=True)
    subprocess.run(["go", "test", "-race", "./..."], cwd=ROOT, check=True)
    subprocess.run(["node", "--test", *sorted(str(path) for path in
                    (ROOT / "desktop/ui").glob("*.test.js"))], cwd=ROOT, check=True)
    print("Local checks passed.")


if __name__ == "__main__":
    main()
