//go:build !windows && !unix

package engine

import "syscall"

func isLocalErrno(syscall.Errno) bool   { return false }
func isRefusedErrno(syscall.Errno) bool { return false }
func isResetErrno(syscall.Errno) bool   { return false }
func isTimeoutErrno(syscall.Errno) bool { return false }
