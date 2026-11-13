//go:build !linux

package main

import "time"

var started = time.Now()

// monoNs stands in for the tablet's monotonic clock so the control law's tests build on the Mac.
func monoNs() int64 { return time.Since(started).Nanoseconds() }
