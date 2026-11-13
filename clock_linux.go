package main

import (
	"syscall"
	"unsafe"
)

// monoNs is CLOCK_MONOTONIC in nanoseconds, the clock SurfaceFlinger stamps frames with.
func monoNs() int64 {
	var ts syscall.Timespec
	syscall.Syscall(syscall.SYS_CLOCK_GETTIME, 1 /* CLOCK_MONOTONIC */, uintptr(unsafe.Pointer(&ts)), 0)
	return ts.Sec*1e9 + ts.Nsec
}
