#!/usr/bin/env python3
"""Operator-controlled review intervals; no automatic or inferred human time."""
import argparse
from datetime import datetime, timezone
import fcntl
import json
import os
from private_storage import private_file


def timestamp(value):
    result = datetime.fromisoformat(value.replace('Z', '+00:00'))
    if result.tzinfo is None:
        raise ValueError('timestamp must include its timezone')
    return result


def start_event(previous, condition, candidate_id, reviewer):
    if previous:
        raise ValueError('review ID already exists; use a new ID for another interval')
    if condition not in ('none', 'static', 'arkism') or not candidate_id or not reviewer:
        raise ValueError('condition, candidate ID and reviewer label are required')
    return {'condition': condition, 'candidate_id': candidate_id, 'reviewer': reviewer}


def stop_event(previous, current, decision, evidence_ref):
    if len(previous) != 1 or previous[0]['event'] != 'start':
        raise ValueError('exactly one open start is required')
    if decision not in ('approved', 'rejected', 'needs_changes', 'abandoned') or not evidence_ref:
        raise ValueError('decision and evidence reference are required')
    start = previous[0]
    seconds = (current - timestamp(start['timestamp'])).total_seconds()
    if seconds < 0:
        raise ValueError('clock moved backwards; do not infer an elapsed time')
    return {'condition': start['condition'], 'candidate_id': start['candidate_id'],
            'reviewer': start['reviewer'], 'decision': decision, 'evidence_ref': evidence_ref,
            'review_interval_seconds': seconds, 'active_attention_seconds': None}


def record(action, review_id, *, condition=None, candidate_id=None,
           reviewer=None, decision=None, evidence_ref=None, now=None, storage_root=None):
    """Append a locked event. Clock/root injection is for API tests, not CLI input."""
    if not review_id or not review_id.strip():
        raise ValueError('review ID is required')
    current = now or datetime.now(timezone.utc)
    if current.tzinfo is None:
        raise ValueError('clock must include its timezone')
    with private_file('reviews', root=storage_root, create=True) as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        events = [json.loads(line) for line in stream if line.strip()]
        previous = [event for event in events if event['review_id'] == review_id]
        event = {'review_id': review_id, 'event': action, 'timestamp': current.isoformat(),
                 'measurement_type': 'operator_reported_wall_clock'}
        if action == 'start':
            event.update(start_event(previous, condition, candidate_id, reviewer))
        elif action == 'stop':
            event.update(stop_event(previous, current, decision, evidence_ref))
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
    parser.add_argument('--review-id', required=True)
    parser.add_argument('--condition', choices=['none', 'static', 'arkism'])
    parser.add_argument('--candidate-id')
    parser.add_argument('--reviewer', help='Non-sensitive reviewer label, not a credential')
    parser.add_argument('--decision', choices=['approved', 'rejected', 'needs_changes', 'abandoned'])
    parser.add_argument('--evidence-ref', help='Reference to the recorded decision, not its private contents')
    args = parser.parse_args()
    try:
        event = record(args.action, args.review_id, condition=args.condition,
                       candidate_id=args.candidate_id, reviewer=args.reviewer,
                       decision=args.decision, evidence_ref=args.evidence_ref)
    except (ValueError, OSError, KeyError) as error:
        parser.error(str(error))
    print(json.dumps(event, ensure_ascii=False))


if __name__ == '__main__':
    main()
