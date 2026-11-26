"""Shared file protection and human label input for the local evaluation tools."""
import os


def restrict_private_file(file):
    if os.name != "nt":
        os.fchmod(file.fileno(), 0o600)
        return
    # chmod on Windows changes the read-only flag, not the file's access list.
    import ctypes
    from ctypes import wintypes

    advapi = ctypes.WinDLL("advapi32", use_last_error=True)
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    descriptor = wintypes.LPVOID()
    convert = advapi.ConvertStringSecurityDescriptorToSecurityDescriptorW
    convert.argtypes = [wintypes.LPCWSTR, wintypes.DWORD, ctypes.POINTER(wintypes.LPVOID), ctypes.POINTER(wintypes.DWORD)]
    if not convert("D:P(A;;FA;;;OW)(A;;FA;;;SY)", 1, ctypes.byref(descriptor), None):
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        set_security = advapi.SetFileSecurityW
        set_security.argtypes = [wintypes.LPCWSTR, wintypes.DWORD, wintypes.LPVOID]
        if not set_security(str(file.name), 0x80000004, descriptor):  # protected DACL
            raise ctypes.WinError(ctypes.get_last_error())
    finally:
        kernel.LocalFree.argtypes = [wintypes.LPVOID]
        kernel.LocalFree(descriptor)


def parse_answer(answer, shown):
    """Rank numbers or literal IDs typed by the labeler; None for skip/quit, [] for 'nothing should come back'."""
    answer = answer.strip()
    if answer in ("s", "q"):
        return None
    if answer == "-":
        return []
    # Compare against the ranks actually displayed. A numeric memory ID can be
    # longer than Python's integer conversion limit.
    ranks = {str(i): memory_id for i, memory_id in enumerate(shown, 1)}
    ids = []
    for token in answer.replace(",", " ").split():
        rank = token
        if shown and token.isdecimal():
            digits = token if token.isascii() else "".join(str(int(digit)) for digit in token)
            rank = digits.lstrip("0")
        ids.append(ranks.get(rank, token))
    return list(dict.fromkeys(ids)) if ids else None
