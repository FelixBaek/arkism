#!/usr/bin/env python3
"""Read-only planned result-row coverage; no task or measurement validation."""
import argparse
import json
from pathlib import Path

import run as benchmark


def condition_coverage(planned, recorded):
    groups = {}
    for condition in benchmark.CONDITIONS:
        expected = {cell for cell in planned if cell[1] == condition}
        observed = expected & recorded
        groups[condition] = {
            'scheduled_cells': len(expected), 'recorded_cells': len(observed),
            'missing_cell_count': len(expected - recorded),
        }
    return groups


def audit(results):
    """Reuse the current protocol gates without creating or changing any files."""
    results = Path(results)
    schedule = benchmark.frozen_schedule()
    _, recorded = benchmark.resume_results(results, schedule)
    if not benchmark.validate_resume_spec(results, schedule, bool(recorded)):
        raise ValueError('completion audit requires an existing frozen benchmark spec')
    planned = [(task['id'], condition, rep) for task, condition, rep in schedule]
    missing = [cell for cell in planned if cell not in recorded]
    return {
        'schema_version': 1,
        'scheduled_cells': len(planned), 'recorded_cells': len(recorded),
        'missing_cell_count': len(missing),
        'missing_cells': [{'task_id': task, 'condition': condition, 'replicate': rep}
                          for task, condition, rep in missing],
        'coverage_complete': bool(planned) and not missing,
        'conditions': condition_coverage(planned, recorded),
        'limitations': [
            'Coverage means planned cells have result rows accepted by the current identity gates.',
            'Failed, timed-out and missing-outcome rows count as recorded; outcomes are not validated.',
            'Coverage does not establish usage, provenance, attempts, billing, human review or whole-experiment completion.',
        ],
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', choices=tuple(benchmark.OUTPUT_LOCATIONS), required=True)
    parser.add_argument('--require-complete', action='store_true',
                        help='Exit nonzero when planned result rows are missing; no quality judgment.')
    args = parser.parse_args(argv)
    try:
        report = audit(benchmark.OUTPUT_LOCATIONS[args.out])
    except (ValueError, OSError) as error:
        parser.error(str(error))
    print(json.dumps(report, indent=2))
    return int(args.require_complete and not report['coverage_complete'])


if __name__ == '__main__':
    raise SystemExit(main())
