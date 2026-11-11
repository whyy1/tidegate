//go:build unix

package engine

import "syscall"

func isLocalErrno(e syscall.Errno) bool {
	switch e {
	case syscall.EADDRNOTAVAIL, // ephemeral ports exhausted
		syscall.EADDRINUSE,
		syscall.EMFILE, // per-process descriptor limit
		syscall.ENFILE, // system-wide descriptor limit
		syscall.ENOBUFS,
		syscall.ENOMEM:
		return true
	}
	return false
}

func isRefusedErrno(e syscall.Errno) bool { return e == syscall.ECONNREFUSED }

func isResetErrno(e syscall.Errno) bool {
	return e == syscall.ECONNRESET || e == syscall.ECONNABORTED || e == syscall.EPIPE
}

func isTimeoutErrno(e syscall.Errno) bool { return e == syscall.ETIMEDOUT }
