package sysmon

import (
	"net"
	"os"
	"runtime"
	"time"
)

// HostInfo describes the machine once, for example when a load generator
// node registers with the controller.
type HostInfo struct {
	Hostname  string `json:"hostname"`
	OS        string `json:"os"` // runtime.GOOS
	OSVersion string `json:"os_version"`
	Arch      string `json:"arch"` // runtime.GOARCH
	// CPUCores is the number of logical CPUs this process may use.
	CPUCores int `json:"cpu_cores"`
	// CPULimitCores is the cgroup CPU quota in cores, 0 when there is none.
	CPULimitCores float64 `json:"cpu_limit_cores"`
	// MemoryTotalBytes is the cgroup memory limit when one is set
	// (MemoryScope "cgroup"), otherwise the physical memory ("host").
	MemoryTotalBytes   uint64    `json:"memory_total_bytes"`
	MemoryScope        string    `json:"memory_scope"`
	EphemeralPortStart int       `json:"ephemeral_port_start"`
	EphemeralPortCount int       `json:"ephemeral_port_count"`
	NICs               []NICInfo `json:"nics"`
}

// NICInfo describes one network interface. Name and Index match NICStats.
type NICInfo struct {
	Name     string `json:"name"`
	Index    int    `json:"index"`
	Up       bool   `json:"up"`
	Loopback bool   `json:"loopback"`
	// LinkSpeedBitsPerSec is the negotiated link speed, 0 when unknown or
	// for loopback interfaces.
	LinkSpeedBitsPerSec uint64   `json:"link_speed_bits_per_sec"`
	MAC                 string   `json:"mac,omitempty"`
	Addrs               []string `json:"addrs,omitempty"`
}

// Host returns a description of this machine. It takes a few milliseconds
// and is meant to be called once, not on the sampling path.
func Host() HostInfo {
	h := HostInfo{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		CPUCores:    runtime.NumCPU(),
		OSVersion:   osVersion(),
		MemoryScope: "host",
	}
	h.Hostname, _ = os.Hostname()

	col, err := newCollector()
	if err == nil {
		r := reading{at: time.Now()}
		col.read(&r)
		col.close()
		h.MemoryTotalBytes = r.memTotal
		if r.memCgroup {
			h.MemoryScope = "cgroup"
		}
		h.CPULimitCores = r.cgCores
		h.EphemeralPortStart, h.EphemeralPortCount = r.portStart, r.portCount
		for i := range r.nics {
			sl := &r.nics[i]
			if !sl.present {
				continue
			}
			ni := NICInfo{Name: sl.name, Index: sl.index, Up: sl.up, Loopback: sl.loopback, LinkSpeedBitsPerSec: sl.speed}
			if sl.loopback {
				ni.LinkSpeedBitsPerSec = 0
			}
			h.NICs = append(h.NICs, ni)
		}
	}

	// Hardware and IP addresses come from the standard library.
	ifs, err := net.Interfaces()
	if err != nil {
		return h
	}
	for i := range h.NICs {
		n := &h.NICs[i]
		for _, ifc := range ifs {
			if ifc.Name != n.Name {
				continue
			}
			if len(ifc.HardwareAddr) > 0 {
				n.MAC = ifc.HardwareAddr.String()
			}
			if addrs, err := ifc.Addrs(); err == nil {
				for _, a := range addrs {
					n.Addrs = append(n.Addrs, a.String())
				}
			}
			break
		}
	}
	return h
}
