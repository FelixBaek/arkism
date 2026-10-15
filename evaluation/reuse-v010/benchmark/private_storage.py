"""Fixed measurement filenames inside a private, owned directory (Linux)."""
from contextlib import contextmanager
import os
from pathlib import Path
import pwd
import stat

FILES = {'reviews': 'reviews.jsonl', 'charges': 'charges.jsonl', 'audit': 'measurement-audit.json'}


def check_owned(info, mode, directory=False):
    expected_type = stat.S_ISDIR if directory else stat.S_ISREG
    if (not expected_type(info.st_mode) or info.st_uid != os.getuid()
            or stat.S_IMODE(info.st_mode) != mode):
        raise ValueError('measurement storage must be private and owned by the current user')
    if not directory and info.st_nlink != 1:
        raise ValueError('measurement file must not have additional hard links')


@contextmanager
def private_file(kind, *, root=None, create=False):
    # root is an API-only fixture seam, never a command-line path. passwd avoids
    # taking a storage path from HOME or another user-controlled environment.
    name = FILES[kind]
    directory = Path(root) if root is not None else Path(pwd.getpwuid(os.getuid()).pw_dir) / '.arkism-measurements'
    directory.mkdir(mode=0o700, exist_ok=True)
    root_fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        check_owned(os.fstat(root_fd), 0o700, directory=True)
        flags = os.O_RDWR if create else os.O_RDONLY
        if create:
            flags |= os.O_CREAT
        descriptor = os.open(name, flags | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600, dir_fd=root_fd)
        with os.fdopen(descriptor, 'r+' if create else 'r', encoding='utf-8') as stream:
            check_owned(os.fstat(stream.fileno()), 0o600)
            yield stream
    finally:
        os.close(root_fd)
