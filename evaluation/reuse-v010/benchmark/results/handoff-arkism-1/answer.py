def apply_event(ledger, event):
    """Append a new event in arrival order; ignore retries by event_id."""
    event_id = event["event_id"]
    if not any(record["event_id"] == event_id for record in ledger):
        ledger.append(event)
    return ledger
