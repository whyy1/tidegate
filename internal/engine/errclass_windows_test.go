//go:build windows

package engine

import (
	"golang.org/x/sys/windows"

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
		{&hopError{phase: phaseDial, err: sysErr("dial", windows.WSAEADDRINUSE)}, metrics.ErrLocal},
		{&hopError{phase: phaseDial, err: sysErr("dial", windows.WSAEADDRNOTAVAIL)}, metrics.ErrLocal},
		{&hopError{phase: phaseDial, err: sysErr("dial", windows.WSAENOBUFS)}, metrics.ErrLocal},
		{&hopError{phase: phaseDial, err: sysErr("dial", windows.WSAEMFILE)}, metrics.ErrLocal},
		{&hopError{phase: phaseDial, err: sysErr("dial", windows.ERROR_CONNECTION_REFUSED)}, metrics.ErrConnectRefused},
		{&hopError{phase: phaseDial, err: sysErr("dial", windows.WSAECONNREFUSED)}, metrics.ErrConnectRefused},
		{&hopError{phase: phaseDial, err: sysErr("dial", windows.WSAETIMEDOUT)}, metrics.ErrConnectTimeout},
		{&hopError{phase: phaseRead, err: sysErr("wsarecv", windows.WSAECONNRESET)}, metrics.ErrReset},
		{&hopError{phase: phaseRead, err: sysErr("wsarecv", windows.ERROR_NETNAME_DELETED)}, metrics.ErrReset},
		{&hopError{phase: phaseWrite, err: sysErr("wsasend", windows.WSAECONNABORTED)}, metrics.ErrReset},
	}
}
