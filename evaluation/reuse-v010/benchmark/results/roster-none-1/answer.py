def allowed(weekday):
    """Assume a Monday–Friday schedule; no project policy was supplied."""
    if not isinstance(weekday, int) or isinstance(weekday, bool):
        raise TypeError("weekday must be an integer")
    if not 0 <= weekday <= 6:
        raise ValueError("weekday must be between 0 and 6")
    return weekday < 5
