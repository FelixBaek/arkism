package capture

import "syscall"

// macOS names the stat times differently from Linux.
func mtimeNs(st *syscall.Stat_t) int64 { return syscall.TimespecToNsec(st.Mtimespec) }
func ctimeNs(st *syscall.Stat_t) int64 { return syscall.TimespecToNsec(st.Ctimespec) }
