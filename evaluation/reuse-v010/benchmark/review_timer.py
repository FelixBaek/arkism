#!/usr/bin/env python3
"""Operator-controlled review intervals; no automatic or inferred human time."""
import argparse
from datetime import datetime, timezone
import fcntl
import json
import os
import stat
from pathlib import Path


def timestamp(value):
    result = datetime.fromisoformat(value.replace('Z', '+00:00'))
    if result.tzinfo is None:
        raise ValueError('timestamp must include its timezone')
    return result


def record(path, action, review_id, *, condition=None, candidate_id=None,
           reviewer=None, decision=None, evidence_ref=None, now=None):
    """Append one locked, durable event. now is injectable for deterministic tests."""
    if not review_id or not review_id.strip():
        raise ValueError('review ID is required')
    current = now or datetime.now(timezone.utc)
    if current.tzinfo is None:
        raise ValueError('clock must include its timezone')
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    with os.fdopen(descriptor, 'r+', encoding='utf-8') as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid()
                or stat.S_IMODE(info.st_mode) != 0o600 or info.st_nlink != 1):
            raise ValueError('ledger must be a private 0600 regular file owned by this user with no extra links')
        fcntl.flock(stream, fcntl.LOCK_EX)
        events = [json.loads(line) for line in stream if line.strip()]
        previous = [event for event in events if event['review_id'] == review_id]
        event = {'review_id': review_id, 'event': action,
                 'timestamp': current.isoformat(),
                 'measurement_type': 'operator_reported_wall_clock'}
        if action == 'start':
            if previous:
                raise ValueError('review ID already exists; use a new ID for another interval')
            if condition not in ('none', 'static', 'arkism') or not candidate_id or not reviewer:
                raise ValueError('condition, candidate ID and reviewer label are required')
            event.update(condition=condition, candidate_id=candidate_id, reviewer=reviewer)
        elif action == 'stop':
            if len(previous) != 1 or previous[0]['event'] != 'start':
                raise ValueError('exactly one open start is required')
            if decision not in ('approved', 'rejected', 'needs_changes', 'abandoned') or not evidence_ref:
                raise ValueError('decision and evidence reference are required')
            start = previous[0]
            seconds = (current - timestamp(start['timestamp'])).total_seconds()
            if seconds < 0:
                raise ValueError('clock moved backwards; do not infer an elapsed time')
            event.update(condition=start['condition'], candidate_id=start['candidate_id'],
                         reviewer=start['reviewer'], decision=decision,
                         evidence_ref=evidence_ref, review_interval_seconds=seconds,
                         active_attention_seconds=None)
        else:
            raise ValueError('action must be start or stop')
        stream.seek(0, os.SEEK_END)
        stream.write(json.dumps(event, ensure_ascii=False) + '\n')
        stream.flush()
        os.fsync(stream.fileno())
        return event


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['start', 'stop'])
    parser.add_argument('--ledger', type=Path, required=True)
    parser.add_argument('--review-id', required=True)
    parser.add_argument('--condition', choices=['none', 'static', 'arkism'])
    parser.add_argument('--candidate-id')
    parser.add_argument('--reviewer', help='Non-sensitive reviewer label, not a credential')
    parser.add_argument('--decision', choices=['approved', 'rejected', 'needs_changes', 'abandoned'])
    parser.add_argument('--evidence-ref', help='Reference to the recorded decision, not its private contents')
    args = parser.parse_args()
    try:
        event = record(args.ledger, args.action, args.review_id, condition=args.condition,
                       candidate_id=args.candidate_id, reviewer=args.reviewer,
                       decision=args.decision, evidence_ref=args.evidence_ref)
    except (ValueError, OSError, KeyError) as error:
        parser.error(str(error))
    print(json.dumps(event, ensure_ascii=False))


if __name__ == '__main__':
    main()
