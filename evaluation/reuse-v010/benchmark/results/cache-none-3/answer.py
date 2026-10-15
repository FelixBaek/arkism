def cache_ttl(ttl):
    """Return None to bypass caching for zero, otherwise return the TTL."""
    if not isinstance(ttl, int) or isinstance(ttl, bool):
        raise TypeError("ttl must be a nonnegative integer")
    if ttl < 0:
        raise ValueError("ttl must be nonnegative")
    return ttl if ttl > 0 else None
