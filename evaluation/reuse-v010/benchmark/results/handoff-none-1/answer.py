def apply_event(ledger, event):
    """Return the ledger with an event applied at most once by event_id.

    The ledger is an iterable of event dictionaries. Existing events win
    on retries. Inputs are not mutated. The caller must persist the result
    atomically with any concurrent updates to provide durable deduplication.
    """
    event_id = event['event_id']
    event['value']  # Validate the required field before applying the event.
    result = [dict(entry) for entry in ledger]
    if not any(entry['event_id'] == event_id for entry in result):
        result.append(dict(event))
    return result
