//go:build unix

package memory

import "syscall"

// processCPUSeconds is this process's user plus system CPU time.
func processCPUSeconds() (float64, bool) {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0, false
	}
	return float64(ru.Utime.Sec+ru.Stime.Sec) + float64(ru.Utime.Usec+ru.Stime.Usec)/1e6, true
}
