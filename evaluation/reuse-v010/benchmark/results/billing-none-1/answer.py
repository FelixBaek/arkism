def charge(cents, numerator, denominator):
    """Return the proportional charge, assuming nearest-cent, half-up rounding."""
    return (2 * cents * numerator + denominator) // (2 * denominator)
