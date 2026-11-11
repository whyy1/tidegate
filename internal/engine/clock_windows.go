//go:build windows

package engine

import (
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows the Go runtime's monotonic clock advances in steps of about
// 0.5 ms (it reads the interrupt time), which is coarser than a loopback
// request. Latencies are therefore measured with QueryPerformanceCounter,
// which has 100 ns resolution and costs about 55 ns per call.

var (
	qpcAddr  uintptr
	qpcFreq  int64
	qpcStart int64
)

func init() {
	k32 := windows.NewLazySystemDLL("kernel32.dll")
	proc := k32.NewProc("QueryPerformanceCounter")
	freq := k32.NewProc("QueryPerformanceFrequency")
	if proc.Find() != nil || freq.Find() != nil {
		panic("tidegate: QueryPerformanceCounter not available")
	}
	qpcAddr = proc.Addr()
	var f int64
	if ok, _, _ := syscall.SyscallN(freq.Addr(), uintptr(unsafe.Pointer(&f))); ok == 0 || f <= 0 {
		panic("tidegate: QueryPerformanceFrequency failed")
	}
	qpcFreq = f
	qpcStart = qpc()
}

func qpc() int64 {
	var v int64
	syscall.SyscallN(qpcAddr, uintptr(unsafe.Pointer(&v)))
	return v
}

// nanotime returns a high resolution monotonic time in nanoseconds.
func nanotime() int64 {
	d := qpc() - qpcStart
	if qpcFreq == 10_000_000 {
		return d * 100
	}
	// Split to avoid overflow for long runs.
	sec := d / qpcFreq
	rem := d % qpcFreq
	return sec*int64(time.Second) + rem*int64(time.Second)/qpcFreq
}
