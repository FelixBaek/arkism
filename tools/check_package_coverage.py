"""Enforce measured package coverage floors in addition to core-file coverage."""
import argparse
import json
from pathlib import Path

from check_core_coverage import BLOCK, parse_profile
from ci_inputs import input_roots, read_ci_text

ROOT = Path(__file__).resolve().parents[1]


def package_totals(text, packages):
    # Retain the core parser's rejection of malformed/duplicate blocks and
    # missing boundary files, even when they are outside a selected package.
    parse_profile(text)
    totals = {package: [0, 0] for package in packages}
    for line in text.splitlines()[1:]:
        match = BLOCK.fullmatch(line)
        filename, statements, count = match[1], int(match[6]), int(match[7])
        for package, values in totals.items():
            parent = filename.rsplit("/", 1)[0]
            if parent == package or parent.endswith("/" + package):
                values[0] += statements if count else 0
                values[1] += statements
    if any(total == 0 for _, total in totals.values()):
        raise ValueError("package coverage is missing or has no statements")
    return totals


def enforce(totals, floors):
    if not floors or set(totals) != set(floors):
        raise ValueError("coverage floor inventory is incomplete")
    failures = []
    for package, floor in floors.items():
        if type(floor) not in (int, float) or not 0 < floor <= 100:
            raise ValueError("invalid package coverage floor")
        covered, total = totals[package]
        if covered * 100 < total * floor:
            failures.append(package)
    return failures


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", type=Path)
    parser.add_argument("--floors", type=Path, default=ROOT / "config/package-coverage.json")
    args = parser.parse_args()
    try:
        floors = json.loads(read_ci_text(args.floors, input_roots(ROOT / "config"), 1 << 20))
        totals = package_totals(read_ci_text(args.profile, input_roots(ROOT)), floors)
        failures = enforce(totals, floors)
        for package, (covered, total) in totals.items():
            print(f"{package}: {covered}/{total} = {covered / total:.2%}; required {floors[package]}%")
        return int(bool(failures))
    except (OSError, ValueError, TypeError) as exc:
        parser.exit(1, f"Package coverage failed: {exc}\n")


if __name__ == "__main__":
    raise SystemExit(main())
