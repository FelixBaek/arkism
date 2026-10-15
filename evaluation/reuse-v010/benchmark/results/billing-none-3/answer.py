def charge(cents, numerator, denominator):
    """Return cents * numerator / denominator, rounded to nearest, ties up.

    Assumes half-up rounding because no billing policy was supplied.
    """
    quotient, remainder = divmod(cents * numerator, denominator)
    return quotient + int(2 * remainder >= denominator)
