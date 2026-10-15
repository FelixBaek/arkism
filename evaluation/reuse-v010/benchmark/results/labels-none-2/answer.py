def normalize(label):
    """Trim surrounding whitespace and lowercase a string label."""
    if not isinstance(label, str):
        raise TypeError("label must be a string")
    return label.strip().lower()
