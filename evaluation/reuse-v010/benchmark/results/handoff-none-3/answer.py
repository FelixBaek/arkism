def apply_event(ledger, event):
    """Return the ledger with an event applied once per event_id.

    The ledger is an iterable of event dictionaries. Replaying an event
    with the same ID and value is a no-op; conflicting reuse of an ID
    raises ValueError. Inputs are not mutated.
    """
    event_id = event['event_id']
    value = event['value']
    result = [dict(entry) for entry in ledger]
    found = False
    for entry in result:
        if entry['event_id'] == event_id:
            if entry['value'] != value:
                raise ValueError('An event_id cannot be reused with a different value')
            found = True
    if not found:
        result.append(dict(event))
    return result
