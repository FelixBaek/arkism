"""Bound CI validation inputs to repository and operator-configured roots.

The temporary root comes from runner configuration, never a CLI argument.
This is an input boundary, not a sandbox against the OS account that owns it.
"""
import os
from pathlib import Path
import stat
import tempfile


def input_roots(repository_root):
    temporary = os.environ.get("ARKISM_CI_INPUT_ROOT") or os.environ.get("RUNNER_TEMP") or tempfile.gettempdir()
    return (Path(repository_root).resolve(), Path(temporary).resolve())


def read_ci_text(raw, roots, limit=64 << 20):
    selected = Path(raw).expanduser()
    if ".." in selected.parts:
        raise ValueError("parent traversal in CI input")
    selected = selected.resolve(strict=True)
    if not any(selected.is_relative_to(Path(root).resolve()) for root in roots):
        raise ValueError("CI input is outside configured roots")
    if any(part == ".git" or part == ".env" or part.startswith(".env.") for part in selected.parts):
        raise ValueError("private CI input is forbidden")
    if not selected.is_file():
        raise ValueError("CI input must be a regular file")
    flags = os.O_RDONLY | getattr(os, "O_BINARY", 0) | getattr(os, "O_NONBLOCK", 0) | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(selected, flags)
    with os.fdopen(descriptor, "rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            raise ValueError("CI input must be a bounded regular file")
        data = source.read(limit + 1)
    if len(data) > limit:
        raise ValueError("CI input exceeds its size budget")
    return data.decode("utf-8-sig")
