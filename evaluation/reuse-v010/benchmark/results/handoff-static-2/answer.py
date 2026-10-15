def apply_event(ledger, event):
    """Append new events in arrival order; ignore already-recorded event IDs."""
    event_id = event["event_id"]
    if any(record["event_id"] == event_id for record in ledger):
        return ledger
    ledger.append(event)
    return ledger
