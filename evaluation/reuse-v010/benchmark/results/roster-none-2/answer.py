def allowed(weekday):
    """Allow Monday through Friday, assuming a standard workweek policy."""
    return 0 <= weekday <= 4
