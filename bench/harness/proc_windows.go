//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	k32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetProcessMemory = k32.NewProc("K32GetProcessMemoryInfo")
	iphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetTcpTable      = iphlpapi.NewProc("GetTcpTable")
)

type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

type procWatch struct {
	h        windows.Handle
	mu       sync.Mutex
	peakWS   uint64
	peakPriv uint64
}

// watch opens a query handle right after start; the handle keeps the
// counters readable after the process exits.
func watch(pid int) *procWatch {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		fmt.Fprintln(os.Stderr, "无法打开进程句柄：", err)
	}
	return &procWatch{h: h}
}

func (w *procWatch) sample() {
	if w.h == 0 {
		return
	}
	var c processMemoryCounters
	c.cb = uint32(unsafe.Sizeof(c))
	r, _, _ := syscall.SyscallN(procGetProcessMemory.Addr(), uintptr(w.h), uintptr(unsafe.Pointer(&c)), uintptr(c.cb))
	if r == 0 {
		return
	}
	w.mu.Lock()
	w.peakWS = max(w.peakWS, uint64(c.PeakWorkingSetSize))
	w.peakPriv = max(w.peakPriv, uint64(c.PeakPagefileUsage))
	w.mu.Unlock()
}

func (w *procWatch) peak() (ws, private uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.peakWS, w.peakPriv
}

func (w *procWatch) cpu() time.Duration {
	if w.h == 0 {
		return 0
	}
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(w.h, &c, &e, &k, &u) != nil {
		return 0
	}
	ticks := func(f windows.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }
	return time.Duration(ticks(k)+ticks(u)) * 100
}

func (w *procWatch) close() {
	if w.h != 0 {
		windows.CloseHandle(w.h)
	}
}

func maxRSS(*os.ProcessState) uint64 { return 0 }

// timeWaitCount counts IPv4 TCP connections in TIME_WAIT.
func timeWaitCount() (int, error) {
	var size uint32
	syscall.SyscallN(procGetTcpTable.Addr(), 0, uintptr(unsafe.Pointer(&size)), 0)
	for i := 0; i < 3; i++ {
		buf := make([]byte, size+4096)
		size = uint32(len(buf))
		r, _, _ := syscall.SyscallN(procGetTcpTable.Addr(), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
		if r == 122 { // ERROR_INSUFFICIENT_BUFFER
			continue
		}
		if r != 0 {
			return 0, fmt.Errorf("GetTcpTable: %d", r)
		}
		n := *(*uint32)(unsafe.Pointer(&buf[0]))
		count := 0
		for j := uint32(0); j < n; j++ {
			state := *(*uint32)(unsafe.Pointer(&buf[4+20*j]))
			if state == 11 { // MIB_TCP_STATE_TIME_WAIT
				count++
			}
		}
		return count, nil
	}
	return 0, fmt.Errorf("GetTcpTable: buffer too small")
}

func defaultK6Paths() []string {
	return []string{`C:\Program Files\k6\k6.exe`, os.ExpandEnv(`${LOCALAPPDATA}\Programs\k6\k6.exe`)}
}

func machineInfo() string {
	cpu := strings.TrimSpace(regValue(`HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "ProcessorNameString"))
	var mem struct {
		Length               uint32
		MemoryLoad           uint32
		TotalPhys            uint64
		AvailPhys            uint64
		TotalPageFile        uint64
		AvailPageFile        uint64
		TotalVirtual         uint64
		AvailVirtual         uint64
		AvailExtendedVirtual uint64
	}
	mem.Length = uint32(unsafe.Sizeof(mem))
	syscall.SyscallN(k32.NewProc("GlobalMemoryStatusEx").Addr(), uintptr(unsafe.Pointer(&mem)))
	v := windows.RtlGetVersion()
	return fmt.Sprintf("%s，%d 个逻辑处理器，内存 %.0f GB，Windows %d.%d.%d，%s",
		cpu, numCPU(), float64(mem.TotalPhys)/(1<<30), v.MajorVersion, v.MinorVersion, v.BuildNumber, goVersion())
}

func regValue(key, name string) string {
	k, err := openKey(key)
	if err != nil {
		return "未知 CPU"
	}
	defer windows.RegCloseKey(k)
	var buf [256]uint16
	n := uint32(len(buf) * 2)
	var typ uint32
	p, _ := windows.UTF16PtrFromString(name)
	if windows.RegQueryValueEx(k, p, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &n) != nil {
		return "未知 CPU"
	}
	return windows.UTF16ToString(buf[:])
}

func openKey(path string) (windows.Handle, error) {
	sub := strings.TrimPrefix(path, `HKLM\`)
	p, _ := windows.UTF16PtrFromString(sub)
	var k windows.Handle
	err := windows.RegOpenKeyEx(windows.HKEY_LOCAL_MACHINE, p, 0, windows.KEY_READ, &k)
	return k, err
}
