//go:build windows

package engine

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// Winsock and Win32 error codes. Overlapped socket operations report some
// conditions as Win32 errors (for example ERROR_CONNECTION_REFUSED from
// ConnectEx), so both families are listed.

func isLocalErrno(e syscall.Errno) bool {
	switch e {
	case windows.WSAEADDRINUSE, // local port already in use
		windows.WSAEADDRNOTAVAIL, // no ephemeral port / address available
		windows.WSAENOBUFS,       // socket buffers or ports exhausted
		windows.WSAEMFILE,        // too many open sockets
		windows.ERROR_TOO_MANY_OPEN_FILES,
		windows.ERROR_NOT_ENOUGH_MEMORY,
		windows.ERROR_NO_SYSTEM_RESOURCES:
		return true
	}
	return false
}

func isRefusedErrno(e syscall.Errno) bool {
	return e == windows.WSAECONNREFUSED || e == windows.ERROR_CONNECTION_REFUSED
}

func isResetErrno(e syscall.Errno) bool {
	switch e {
	case windows.WSAECONNRESET, windows.WSAECONNABORTED, windows.WSAESHUTDOWN,
		windows.ERROR_NETNAME_DELETED, windows.ERROR_CONNECTION_ABORTED:
		return true
	}
	return false
}

func isTimeoutErrno(e syscall.Errno) bool {
	return e == windows.WSAETIMEDOUT || e == windows.ERROR_SEM_TIMEOUT
}
