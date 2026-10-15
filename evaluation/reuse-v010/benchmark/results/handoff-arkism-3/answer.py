def apply_event(ledger, event):
    """Append an event once by event_id, preserving arrival order."""
    if not any(record["event_id"] == event["event_id"] for record in ledger):
        ledger.append(dict(event))
    return ledger
