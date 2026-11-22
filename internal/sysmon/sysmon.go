// Package sysmon samples the health of the machine that generates load:
// CPU, memory, network interface throughput, TCP retransmissions, ephemeral
// ports and Go runtime pauses. It lets the product tell whether the load
// generator itself, rather than the system under test, is the bottleneck.
//
// All values come from OS counters read in-process (no external commands).
// A sample costs well under a millisecond; in steady state the only heap
// allocation per sample is the NICs slice of the emitted Stats.
//
// Platform support: Windows and Linux are fully implemented. On other systems
// (macOS included) the package compiles and fills what it can cheaply read;
// everything else is zero and listed in Stats.Unavailable.
package sysmon

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Stats is one sample. Rates and per-interval counts cover the real time
// elapsed since the previous sample (ElapsedMS), not the nominal interval.
type Stats struct {
	// Time is when the sample was taken.
	Time time.Time `json:"time"`
	// ElapsedMS is the real time covered by rates and interval counts.
	ElapsedMS int64        `json:"elapsed_ms"`
	CPU       CPUStats     `json:"cpu"`
	Memory    MemoryStats  `json:"memory"`
	NICs      []NICStats   `json:"nics"`
	TCP       TCPStats     `json:"tcp"`
	Ports     PortStats    `json:"ports"`
	Runtime   RuntimeStats `json:"runtime"`
	// Unavailable names the fields that could not be read for this sample,
	// as JSON paths ("cpu.machine", "tcp", "nics.rates", ...). Those fields
	// are zero. The slice may be shared between samples; do not modify it.
	Unavailable []string `json:"unavailable,omitempty"`
}

// CPUStats reports utilisation as a fraction of capacity in [0, 1].
//
// Machine is the busy share of all logical CPUs of the host (busy means not
// idle and not waiting for I/O; time stolen by a hypervisor counts as busy).
// When the process runs in a Linux cgroup with a CPU quota, Machine is the
// cgroup's CPU usage divided by the quota instead, and Scope is "cgroup".
//
// Process is this process's CPU time (user plus kernel) divided by the
// elapsed time multiplied by Cores, so 1 means the process saturates every
// CPU it is allowed to use.
type CPUStats struct {
	Machine float64 `json:"machine"`
	Process float64 `json:"process"`
	// Cores is the CPU capacity used as the denominator of Process: the
	// logical CPUs this process may run on, reduced to the cgroup quota when
	// one is set. It can be fractional.
	Cores float64 `json:"cores"`
	// Scope is "host" or "cgroup".
	Scope string `json:"scope"`
}

// MemoryStats reports physical memory. Inside a Linux cgroup with a memory
// limit, TotalBytes is the limit, UsedBytes is the cgroup's usage minus
// reclaimable page cache (the working set) and Scope is "cgroup".
type MemoryStats struct {
	UsedBytes  uint64 `json:"used_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
	// ProcessBytes is this process's resident set (Linux) or working set
	// (Windows).
	ProcessBytes uint64 `json:"process_bytes"`
	// Scope is "host" or "cgroup".
	Scope string `json:"scope"`
}

// NICStats describes one network interface.
type NICStats struct {
	Name     string `json:"name"`
	Index    int    `json:"index"`
	Up       bool   `json:"up"`
	Loopback bool   `json:"loopback"`
	// LinkSpeedBitsPerSec is the negotiated link speed, 0 when unknown and
	// always 0 for loopback interfaces (they have no bandwidth limit).
	LinkSpeedBitsPerSec uint64 `json:"link_speed_bits_per_sec"`
	RxBytesPerSec       uint64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec       uint64 `json:"tx_bytes_per_sec"`
	// RxBytes and TxBytes are the raw cumulative OS counters.
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
}

// TCPStats holds system-wide TCP counters (IPv4 and IPv6 combined).
// InSegs, OutSegs and RetransSegs are counts during the interval.
type TCPStats struct {
	InSegs        uint64  `json:"in_segs"`
	OutSegs       uint64  `json:"out_segs"`
	RetransSegs   uint64  `json:"retrans_segs"`
	OutSegsPerSec float64 `json:"out_segs_per_sec"`
	RetransPerSec float64 `json:"retrans_per_sec"`
	// Established is the current number of connections in ESTABLISHED or
	// CLOSE_WAIT state.
	Established uint64 `json:"established"`
}

// PortStats describes ephemeral (client) port usage.
type PortStats struct {
	// EphemeralStart and EphemeralCount describe the system's dynamic port
	// range for outgoing TCP connections.
	EphemeralStart int `json:"ephemeral_start"`
	EphemeralCount int `json:"ephemeral_count"`
	// TCPConns is the number of TCP sockets on the system in any state,
	// TIME_WAIT and listeners included. It is an upper bound for ephemeral
	// ports in use; per-process counts are not collected because they are
	// not cheap to read.
	TCPConns uint64 `json:"tcp_conns"`
}

// RuntimeStats reports the Go runtime of this process.
type RuntimeStats struct {
	Goroutines int `json:"goroutines"`
	// GCCycles is the number of GC cycles completed during the interval.
	GCCycles uint64 `json:"gc_cycles"`
	// GCPauseNs is the total stop-the-world GC pause time during the
	// interval and GCPauseMaxNs the longest single pause. Both come from the
	// runtime's pause histogram, whose buckets are up to 25% wide: the total
	// uses bucket midpoints (within about 12%), the maximum the bucket's
	// upper bound.
	GCPauseNs    uint64 `json:"gc_pause_ns"`
	GCPauseMaxNs uint64 `json:"gc_pause_max_ns"`
}

// Options configures a Monitor.
type Options struct {
	// Interval between samples. Zero means one second; values below 10 ms
	// are rejected.
	Interval time.Duration
}

// Monitor samples system statistics periodically. Create one with New, run
// it with Start and read samples from Stats or Latest. A Monitor is
// single-use: once its context is done it cannot be restarted.
type Monitor struct {
	interval time.Duration
	col      collector
	rt       runtimeReader

	prev, cur reading
	hasPrev   bool
	missMask  uint32
	missNames []string

	out   chan Stats
	start sync.Once

	mu     sync.Mutex
	latest Stats
}

// New creates a Monitor. It does not start sampling.
func New(opts Options) (*Monitor, error) {
	iv := opts.Interval
	if iv == 0 {
		iv = time.Second
	}
	if iv < 10*time.Millisecond {
		return nil, errors.New("sysmon: interval must be at least 10ms")
	}
	col, err := newCollector()
	if err != nil {
		return nil, err
	}
	m := &Monitor{
		interval: iv,
		col:      col,
		out:      make(chan Stats, 1),
	}
	m.rt.init()
	return m, nil
}

// Start launches the sampling goroutine and returns immediately. A baseline
// reading is taken at once; the first Stats is produced one interval later,
// so every emitted sample has valid rates. Sampling stops and the Stats
// channel is closed when ctx is done. Calls after the first are no-ops.
func (m *Monitor) Start(ctx context.Context) {
	m.start.Do(func() { go m.run(ctx) })
}

// Stats returns the channel that receives one Stats per interval. It holds
// at most one value: when the consumer falls behind, older samples are
// dropped and sampling never blocks. The channel is closed when sampling
// stops.
func (m *Monitor) Stats() <-chan Stats {
	return m.out
}

// Latest returns the most recent sample, or a zero Stats (Time.IsZero())
// when none has been produced yet.
func (m *Monitor) Latest() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.latest
}

func (m *Monitor) run(ctx context.Context) {
	defer close(m.out)
	defer m.col.close()

	m.sample(time.Now())
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			// Ticks delivered late in a burst (after the machine was
			// suspended, for example) would produce rates over a tiny
			// window; skip them.
			if now.Sub(m.prev.at) < m.interval/2 {
				continue
			}
			if s, ok := m.sample(time.Now()); ok {
				m.publish(s)
			}
		}
	}
}

func (m *Monitor) publish(s Stats) {
	m.mu.Lock()
	m.latest = s
	m.mu.Unlock()
	for {
		select {
		case m.out <- s:
			return
		default:
		}
		// Full: drop the stale value so the newest one gets in.
		select {
		case <-m.out:
		default:
		}
	}
}

// sample takes one reading and, if a previous reading exists, computes a
// Stats from the two. It is called only from the sampling goroutine (and
// from tests and benchmarks).
func (m *Monitor) sample(now time.Time) (Stats, bool) {
	m.cur = reading{at: now}
	m.col.read(&m.cur)
	m.rt.read(&m.cur)
	if !m.hasPrev {
		m.prev = m.cur
		m.hasPrev = true
		return Stats{}, false
	}
	var s Stats
	ok := compute(&m.prev, &m.cur, &s)
	m.prev = m.cur
	if !ok {
		return Stats{}, false
	}
	mask := m.cur.missing
	if mask != m.missMask || (mask != 0 && m.missNames == nil) {
		m.missMask = mask
		m.missNames = missingNames(mask)
	}
	s.Unavailable = m.missNames
	return s, true
}
