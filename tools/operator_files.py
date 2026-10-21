"""Constrain operator CLI file inputs to a separately configured local folder."""
import os
from pathlib import Path


def input_file(raw, root, maximum_bytes, private=False):
    """CLI arguments select files inside the trusted operator-owned root only.

    The root is application configuration, not a command-line argument. This
    protects against mistaken CLI paths; it is not a sandbox for the OS owner.
    """
    root = Path(root).expanduser().resolve(strict=True)
    if os.name != "nt" and root.stat().st_mode & 0o022:
        raise ValueError("operator folder must not be writable by group or others")
    candidate = Path(raw).expanduser()
    if not candidate.is_absolute():
        candidate = root / candidate
    relative = candidate.relative_to(root)  # rejects absolute paths outside root
    if ".." in relative.parts:
        raise ValueError("parent traversal is forbidden")
    resolved = candidate.resolve(strict=True)
    if not resolved.is_relative_to(root) or not resolved.is_file():
        raise ValueError("input must be a regular file inside the operator folder")
    metadata = resolved.stat()
    if metadata.st_size > maximum_bytes:
        raise ValueError("operator input exceeds its size budget")
    if private and os.name != "nt" and metadata.st_mode & 0o077:
        raise ValueError("private operator input must have mode 0600")
    return resolved
