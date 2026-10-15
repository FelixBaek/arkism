def normalize(label):
    """Normalize a string label by trimming whitespace and lowercasing it."""
    if not isinstance(label, str):
        raise TypeError("label must be a string")
    return label.strip().lower()
