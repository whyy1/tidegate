//go:build unix

package engine

import (
	"syscall"

	"tidegate/internal/metrics"
)

func platformClassifyCases() []struct {
	err  error
	want metrics.ErrorKind
} {
	return []struct {
		err  error
		want metrics.ErrorKind
	}{
		{&hopError{phase: phaseDial, err: sysErr("dial", syscall.EADDRNOTAVAIL)}, metrics.ErrLocal},
		{&hopError{phase: phaseDial, err: sysErr("dial", syscall.EMFILE)}, metrics.ErrLocal},
		{&hopError{phase: phaseDial, err: sysErr("dial", syscall.ENFILE)}, metrics.ErrLocal},
		{&hopError{phase: phaseDial, err: sysErr("dial", syscall.ECONNREFUSED)}, metrics.ErrConnectRefused},
		{&hopError{phase: phaseDial, err: sysErr("dial", syscall.ETIMEDOUT)}, metrics.ErrConnectTimeout},
		{&hopError{phase: phaseRead, err: sysErr("read", syscall.ECONNRESET)}, metrics.ErrReset},
		{&hopError{phase: phaseWrite, err: sysErr("write", syscall.EPIPE)}, metrics.ErrReset},
	}
}
