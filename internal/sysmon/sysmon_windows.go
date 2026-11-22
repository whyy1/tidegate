//go:build windows

package sysmon

import (
	"fmt"
	"math"
	"net/netip"
	"runtime"
	"sort"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	ifFlagFilter         = 0x02
	ifTypeLoopback       = 24
	ifOperStatusUp       = 1
	defaultPortStart     = 49152
	defaultPortCount     = 16384
)

var (
	modKernel32              = windows.NewLazySystemDLL("kernel32.dll")
	modPSAPI                 = windows.NewLazySystemDLL("psapi.dll")
	modIPHlpAPI              = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetSystemTimes       = modKernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = modKernel32.NewProc("GlobalMemoryStatusEx")
	procK32GetProcMemInfo    = modKernel32.NewProc("K32GetProcessMemoryInfo")
	procPSAPIGetProcMemInfo  = modPSAPI.NewProc("GetProcessMemoryInfo")
	procGetTcpStatisticsEx2  = modIPHlpAPI.NewProc("GetTcpStatisticsEx2")
)

type memoryStatusEx struct {
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

type mibTCPStats2 struct {
	RtoAlgorithm uint32
	RtoMin       uint32
	RtoMax       uint32
	MaxConn      uint32
	ActiveOpens  uint32
	PassiveOpens uint32
	AttemptFails uint32
	EstabResets  uint32
	CurrEstab    uint32
	_            uint32
	InSegs       uint64
	OutSegs      uint64
	RetransSegs  uint32
	InErrs       uint32
	OutRsts      uint32
	NumConns     uint32
	_            uint32
}

type winCollector struct {
	numCPU      int
	nics        []nicSlot
	lastRefresh time.Time
	portStart   int
	portCount   int
}

func newCollector() (collector, error) {
	return &winCollector{
		numCPU:    runtime.NumCPU(),
		portStart: defaultPortStart,
		portCount: defaultPortCount,
	}, nil
}

func (c *winCollector) close() {}

func ftToUint64(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

func (c *winCollector) read(r *reading) {
	since := r.at.Sub(c.lastRefresh)
	refresh := c.lastRefresh.IsZero() || since >= refreshEvery || since < 0
	if refresh {
		c.lastRefresh = r.at
		c.refreshPorts()
	}

	r.cores = float64(c.numCPU)
	r.nicBits = 64
	r.tcpSegBits = 64
	r.tcpRetBits = 32
	r.portStart, r.portCount = c.portStart, c.portCount

	// Host CPU: on Windows, kernelTime includes idleTime.
	if procGetSystemTimes.Find() == nil {
		var idle, kernel, user windows.Filetime
		ok, _, _ := procGetSystemTimes.Call(
			uintptr(unsafe.Pointer(&idle)),
			uintptr(unsafe.Pointer(&kernel)),
			uintptr(unsafe.Pointer(&user)),
		)
		if ok != 0 {
			i := ftToUint64(idle)
			k := ftToUint64(kernel)
			u := ftToUint64(user)
			r.cpuTotal = k + u
			if k >= i {
				r.cpuBusy = (k - i) + u
			} else {
				r.cpuBusy = u
			}
		} else {
			r.missing |= missCPUMachine
		}
	} else {
		r.missing |= missCPUMachine
	}

	// Process CPU (FILETIME is in 100-ns units).
	var create, exit, pKernel, pUser windows.Filetime
	if windows.GetProcessTimes(windows.CurrentProcess(), &create, &exit, &pKernel, &pUser) == nil {
		r.procCPUNs = (ftToUint64(pKernel) + ftToUint64(pUser)) * 100
	} else {
		r.missing |= missCPUProcess
	}

	// Host memory.
	if procGlobalMemoryStatusEx.Find() == nil {
		var ms memoryStatusEx
		ms.Length = uint32(unsafe.Sizeof(ms))
		ok, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
		if ok != 0 {
			r.memTotal = ms.TotalPhys
			if ms.AvailPhys <= ms.TotalPhys {
				r.memUsed = ms.TotalPhys - ms.AvailPhys
			}
		} else {
			r.missing |= missMemUsed | missMemTotal
		}
	} else {
		r.missing |= missMemUsed | missMemTotal
	}

	// Process memory.
	memProc := procK32GetProcMemInfo
	if memProc.Find() != nil {
		memProc = procPSAPIGetProcMemInfo
	}
	if memProc.Find() == nil {
		var pmc processMemoryCounters
		pmc.cb = uint32(unsafe.Sizeof(pmc))
		ok, _, _ := memProc.Call(
			uintptr(windows.CurrentProcess()),
			uintptr(unsafe.Pointer(&pmc)),
			uintptr(pmc.cb),
		)
		if ok != 0 {
			r.memProc = uint64(pmc.WorkingSetSize)
		} else {
			r.missing |= missMemProcess
		}
	} else {
		r.missing |= missMemProcess
	}

	c.readNICs(r, refresh)
	c.readTCP(r)
}

func (c *winCollector) refreshPorts() {
	c.portStart, c.portCount = defaultPortStart, defaultPortCount
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\Tcpip\Parameters`, registry.QUERY_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if maxPort, _, err := k.GetIntegerValue("MaxUserPort"); err == nil && maxPort > 1024 && maxPort <= 65535 {
		if int(maxPort) >= c.portStart {
			c.portCount = int(maxPort) - c.portStart + 1
		}
	}
}

func (c *winCollector) readNICs(r *reading, refresh bool) {
	for i := range c.nics {
		c.nics[i].begin()
	}
	var tab *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfEntryNormal, &tab); err != nil {
		r.nics = c.nics
		r.missing |= missNICs | missNICRates
		return
	}
	defer windows.FreeMibTable(unsafe.Pointer(tab))

	if tab.NumEntries > 0 {
		rows := unsafe.Slice(&tab.Table[0], tab.NumEntries)
		for i := range rows {
			row := &rows[i]
			if row.InterfaceIndex == 0 || row.InterfaceAndOperStatusFlags&ifFlagFilter != 0 {
				continue
			}
			// Avoid duplicate entries sharing the same interface index.
			if c.hasPresentIndex(int(row.InterfaceIndex)) {
				continue
			}
			sl := c.slotByLUID(row.InterfaceLuid)
			if sl == nil {
				c.nics = append(c.nics, nicSlot{luid: row.InterfaceLuid})
				sl = &c.nics[len(c.nics)-1]
				refresh = true
			}
			if refresh || sl.name == "" {
				sl.index = int(row.InterfaceIndex)
				sl.name = windows.UTF16ToString(row.Alias[:])
				sl.loopback = row.Type == ifTypeLoopback
				sl.up = row.OperStatus == ifOperStatusUp
				sl.speed = row.TransmitLinkSpeed
				if sl.loopback || sl.speed == math.MaxUint64 {
					sl.speed = 0
				}
			}
			sl.set(row.InOctets, row.OutOctets)
		}
	}
	if refresh {
		kept := c.nics[:0]
		for _, sl := range c.nics {
			if sl.present {
				kept = append(kept, sl)
			}
		}
		c.nics = kept
		sort.SliceStable(c.nics, func(i, j int) bool {
			if c.nics[i].index != c.nics[j].index {
				return c.nics[i].index < c.nics[j].index
			}
			return c.nics[i].name < c.nics[j].name
		})
	}
	r.nics = c.nics
}

func (c *winCollector) hasPresentIndex(idx int) bool {
	for i := range c.nics {
		if c.nics[i].present && c.nics[i].index == idx {
			return true
		}
	}
	return false
}

func (c *winCollector) slotByLUID(luid uint64) *nicSlot {
	for i := range c.nics {
		if c.nics[i].luid == luid {
			return &c.nics[i]
		}
	}
	return nil
}

func (c *winCollector) readTCP(r *reading) {
	if procGetTcpStatisticsEx2.Find() != nil {
		r.missing |= missTCP | missTCPConns
		return
	}
	var gotAny bool
	for _, family := range []uintptr{windows.AF_INET, windows.AF_INET6} {
		var st mibTCPStats2
		rc, _, _ := procGetTcpStatisticsEx2.Call(uintptr(unsafe.Pointer(&st)), family)
		if rc != 0 {
			continue
		}
		gotAny = true
		r.tcpIn += st.InSegs
		r.tcpOut += st.OutSegs
		r.tcpRetrans += uint64(st.RetransSegs)
		r.tcpEstab += uint64(st.CurrEstab)
		r.tcpConns += uint64(st.NumConns)
	}
	if !gotAny {
		r.missing |= missTCP | missTCPConns
	}
}

func osVersion() string {
	v := windows.RtlGetVersion()
	if v == nil {
		return "Windows"
	}
	if v.MajorVersion == 10 && v.BuildNumber >= 22000 {
		return fmt.Sprintf("Windows 11 (build %d)", v.BuildNumber)
	}
	if v.MajorVersion == 10 {
		return fmt.Sprintf("Windows 10 (build %d)", v.BuildNumber)
	}
	return fmt.Sprintf("Windows %d.%d (build %d)", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}

func routeInterface(ip, local netip.Addr) (idx int, name string, speed uint64, loopback bool, err error) {
	idx, name, loopback, err = routeBySource(local)
	if err != nil {
		return 0, "", 0, false, err
	}
	if !loopback && idx > 0 {
		var row windows.MibIfRow2
		row.InterfaceIndex = uint32(idx)
		if windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row) == nil {
			if row.TransmitLinkSpeed != math.MaxUint64 {
				speed = row.TransmitLinkSpeed
			}
		}
	}
	return idx, name, speed, loopback, nil
}
