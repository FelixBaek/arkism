def apply_event(ledger, event):
    """Append new events in arrival order; ignore retries by event_id."""
    if not any(recorded["event_id"] == event["event_id"] for recorded in ledger):
        ledger.append(event)
    return ledger
