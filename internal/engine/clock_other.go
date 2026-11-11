//go:build !windows

package engine

import "time"

var clockStart = time.Now()

// nanotime returns a high resolution monotonic time in nanoseconds. On
// Linux and macOS the runtime clock already has nanosecond resolution.
func nanotime() int64 { return int64(time.Since(clockStart)) }
