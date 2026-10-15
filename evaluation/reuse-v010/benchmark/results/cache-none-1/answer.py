def cache_ttl(ttl):
    """Return None for zero TTL; otherwise return the TTL in seconds."""
    return None if ttl == 0 else ttl
