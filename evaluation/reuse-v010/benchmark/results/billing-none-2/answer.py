def charge(cents, numerator, denominator):
    """Return the prorated charge, assuming nearest-cent, half-up rounding.

    No project billing policy was supplied. Uses exact integer arithmetic.
    """
    return (2 * cents * numerator + denominator) // (2 * denominator)
