//go:build !windows && !darwin

package capture

import "syscall"

func mtimeNs(st *syscall.Stat_t) int64 { return st.Mtim.Nano() }
func ctimeNs(st *syscall.Stat_t) int64 { return st.Ctim.Nano() }
