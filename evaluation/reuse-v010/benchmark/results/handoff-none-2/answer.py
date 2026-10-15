def apply_event(ledger, event):
    """Return the ledger with the event applied once, keyed by event_id.

    Retried events preserve the first recorded value. The input ledger and
    event dictionaries are not modified.
    """
    event_id = event["event_id"]
    value = event["value"]
    result = [dict(entry) for entry in ledger]
    if not any(entry["event_id"] == event_id for entry in result):
        result.append({**event, "event_id": event_id, "value": value})
    return result
