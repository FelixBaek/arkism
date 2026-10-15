def cache_ttl(ttl):
    """Return the expiration TTL, or None to bypass caching when TTL is zero."""
    return ttl if ttl > 0 else None
