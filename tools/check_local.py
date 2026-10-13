"""Run the public checkout's local checks without credentials or private docs."""
import os
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


def main():
    # Archive copies have no VCS metadata, including for binaries built by tests.
    os.environ["GOFLAGS"] = os.environ.get("GOFLAGS", "") + " -buildvcs=false"
    subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", "tools",
                    "-p", "test_*.py"], cwd=ROOT, check=True)
    subprocess.run(["go", "vet", "./..."], cwd=ROOT, check=True)
    subprocess.run(["go", "test", "-race", "./..."], cwd=ROOT, check=True)
    subprocess.run(["node", "--test", *sorted(str(path) for path in
                    (ROOT / "desktop/ui").glob("*.test.js"))], cwd=ROOT, check=True)
    print("Local checks passed.")


if __name__ == "__main__":
    main()
