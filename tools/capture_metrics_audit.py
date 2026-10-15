#!/usr/bin/env python3
"""Summarize capture JSONL metadata without emitting session IDs or content."""
import argparse
from collections import Counter, defaultdict
from datetime import datetime
import json
from pathlib import Path

KNOWN_ERRORS = frozenset("""
evidence_missing tool_provenance_mismatch checkpoint_string_invalid schema_array_min_items
evidence_mismatch user_provenance_mismatch coverage_mismatch
session_batch_wait session_batch_parent_pending session_batch_parent_parked
session_archive_window_unavailable session_archive_window_incomplete
oversized_record_requires_artifact_reference session_capture_not_acknowledged
""".split())
EVENTS = frozenset(("session_processor_pending", "session_processor_parked"))
FIELDS = frozenset(("session_state", "check:passed", "check:failed",
                    "check:unknown", "check:not_run", "candidate:fact",
                    "candidate:preference", "candidate:correction",
                    "candidate:lesson", "candidate:failure_recovery"))


def timestamp(value):
    if not isinstance(value, str):
        raise ValueError("timestamp must be a string")
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("timestamp must include a timezone")
    return parsed


def summarize(lines, until=None):
    """Counts are log occurrences, never inferred candidate or unique-job counts."""
    cutoff = timestamp(until) if until else None
    groups = Counter()
    sessions = defaultdict(set)
    field_counts = Counter()
    malformed = invalid_time = 0
    for line in lines:
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except (ValueError, TypeError):
            malformed += 1
            continue
        if not isinstance(row, dict):
            malformed += 1
            continue
        event = row.get("event")
        if not isinstance(event, str) or (event not in EVENTS and event != "session_evidence_missing"):
            continue
        try:
            when = timestamp(row.get("ts"))
        except (ValueError, TypeError, OverflowError):
            invalid_time += 1
            continue
        if cutoff and when > cutoff:
            continue
        if event == "session_evidence_missing":
            field = row.get("field")
            field_counts[field if isinstance(field, str) and field in FIELDS else "other"] += 1
            continue
        error = row.get("error")
        error = error if isinstance(error, str) and error in KNOWN_ERRORS else "other"
        key = (event, error)
        groups[key] += 1
        session = row.get("session")
        if isinstance(session, str) and session:
            sessions[key].add(session)
    return {
        "count_unit": "log_occurrences",
        "unique_session_note": "Distinct logged identifiers may be truncated; these are not unique jobs or candidates.",
        "until": cutoff.isoformat() if cutoff else None,
        "errors": [
            {"event": event, "error": error, "occurrences": count,
             "distinct_logged_session_identifiers": len(sessions[(event, error)])}
            for (event, error), count in sorted(groups.items())
        ],
        "missing_evidence_fields": dict(sorted(field_counts.items())),
        "malformed_lines": malformed,
        "invalid_event_timestamps": invalid_time,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("metrics", type=Path)
    parser.add_argument("--until", help="Inclusive timezone-aware ISO timestamp")
    args = parser.parse_args()
    try:
        with args.metrics.open(encoding="utf-8") as stream:
            result = summarize(stream, args.until)
    except (OSError, UnicodeError, ValueError):
        parser.exit(2, "Cannot read metrics or parse the cutoff timestamp.\n")
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
