"""Deterministic attempt locations and atomic, non-replacing Linux moves.

No execution or cleanup: callers retain every byte, including on failure.
Unsupported hosts reject the new archive protocol before any native dispatch.
"""
import ctypes
import errno
import hashlib
import os
import sys


def relative_output(namespace,job_id,attempt_id):
    key=hashlib.sha256((job_id+'\0'+attempt_id).encode('utf8')).hexdigest()
    return namespace+'/a'+key+'-native'


def _rename_function():
    if not sys.platform.startswith('linux'):return None
    library=ctypes.CDLL(None,use_errno=True)
    function=getattr(library,'renameat2',None)
    if function is not None:
        function.argtypes=[ctypes.c_int,ctypes.c_char_p,ctypes.c_int,ctypes.c_char_p,ctypes.c_uint]
        function.restype=ctypes.c_int
    return function


def supported():
    return _rename_function() is not None


def move_no_replace(source_fd,source_name,destination_fd,destination_name):
    function=_rename_function()
    if function is None:raise OSError(errno.ENOTSUP,'atomic archive unsupported')
    # RENAME_NOREPLACE is atomic even if another writer creates the target
    # after the caller's path/identity checks. Never fall back to os.rename.
    if function(source_fd,os.fsencode(source_name),destination_fd,os.fsencode(destination_name),1)!=0:
        number=ctypes.get_errno()
        raise OSError(number,os.strerror(number))
