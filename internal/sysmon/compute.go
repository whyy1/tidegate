package sysmon

import (
	"math"
	"runtime"
	"runtime/metrics"
	"time"
)

// collector reads raw cumulative counters from the operating system.
type collector interface {
	// read fills r with the current counters. It must not block for long
	// and should not allocate in steady state.
	read(r *reading)
	// close releases resources such as open files.
	close()
}

// How often collectors re-enumerate interfaces and re-read slowly changing
// settings (link speed, port range, cgroup limits).
const refreshEvery = 10 * time.Second

// Bits for reading.missing; see missingNames for the reported names.
const (
	missCPUMachine uint32 = 1 << iota
	missCPUProcess
	missMemUsed
	missMemTotal
	missMemProcess
	missNICs
	missNICRates
	missTCP
	missTCPConns
	missPorts
	missCount = iota
)

var missNameTable = [missCount]string{
	"cpu.machine",
	"cpu.process",
	"memory.used_bytes",
	"memory.total_bytes",
	"memory.process_bytes",
	"nics",
	"nics.rates",
	"tcp",
	"ports.tcp_conns",
	"ports.ephemeral",
}

func missingNames(mask uint32) []string {
	if mask == 0 {
		return nil
	}
	var names []string
	for i := 0; i < missCount; i++ {
		if mask&(1<<i) != 0 {
			names = append(names, missNameTable[i])
		}
	}
	return names
}

// reading is one snapshot of cumulative OS counters. Collectors set every
// field they can and flag the rest in missing.
type reading struct {
	at      time.Time
	missing uint32

	// Host CPU time in arbitrary units; only the ratio of deltas is used.
	cpuBusy, cpuTotal uint64
	// cgCores > 0 means a cgroup CPU quota applies; cgUsageNs is the
	// cgroup's cumulative CPU time.
	cgCores   float64
	cgUsageNs uint64
	cgCPUOK   bool
	// cores is the capacity used for the process share.
	cores     float64
	procCPUNs uint64

	memUsed, memTotal, memProc uint64
	memCgroup                  bool

	// nics is owned by the collector; slots keep their own previous values.
	nics    []nicSlot
	nicBits uint8

	tcpIn, tcpOut, tcpRetrans uint64
	tcpSegBits, tcpRetBits    uint8
	tcpEstab                  uint64
	tcpConns                  uint64

	portStart, portCount int

	goroutines           int
	gcCycles             uint64
	gcPauseNs, gcPauseMax uint64
}

// nicSlot tracks one interface across readings.
type nicSlot struct {
	name     string
	index    int
	luid     uint64 // Windows interface LUID
	up       bool
	loopback bool
	speed    uint64 // bits/s, 0 when unknown

	present bool // seen in the latest reading
	ok      bool // rx/tx hold counters from the latest reading
	prevOK  bool
	rx, tx  uint64
	prevRx  uint64
	prevTx  uint64
}

// begin shifts the current counters to the previous ones before a reading.
func (s *nicSlot) begin() {
	s.prevRx, s.prevTx, s.prevOK = s.rx, s.tx, s.ok
	s.ok, s.present = false, false
}

func (s *nicSlot) set(rx, tx uint64) {
	s.rx, s.tx, s.ok, s.present = rx, tx, true, true
}

// counterDelta returns cur-prev for a cumulative counter that is bits wide.
// A 32-bit counter that went down is assumed to have wrapped. A 64-bit
// counter that went down (or a 32-bit one whose previous value does not fit
// in 32 bits) was reset; ok is false and the interval has no rate.
func counterDelta(prev, cur uint64, bits uint8) (d uint64, ok bool) {
	if cur >= prev {
		return cur - prev, true
	}
	if bits == 32 && prev <= math.MaxUint32 && cur <= math.MaxUint32 {
		return uint64(uint32(cur) - uint32(prev)), true
	}
	return 0, false
}

// compute fills s from two consecutive readings. It returns false when the
// readings do not span a positive amount of time.
func compute(prev, cur *reading, s *Stats) bool {
	elapsed := cur.at.Sub(prev.at)
	if elapsed <= 0 {
		return false
	}
	sec := elapsed.Seconds()
	ns := float64(elapsed.Nanoseconds())
	miss := cur.missing

	s.Time = cur.at
	s.ElapsedMS = elapsed.Milliseconds()

	// CPU.
	s.CPU.Cores = cur.cores
	s.CPU.Scope = "host"
	switch {
	case cur.cgCores > 0:
		s.CPU.Scope = "cgroup"
		if d, ok := counterDelta(prev.cgUsageNs, cur.cgUsageNs, 64); ok && cur.cgCPUOK && prev.cgCPUOK && prev.cgCores > 0 {
			s.CPU.Machine = ratio(float64(d), ns*cur.cgCores)
		} else {
			miss |= missCPUMachine
		}
	case cur.missing&missCPUMachine == 0 && prev.missing&missCPUMachine == 0:
		db, ok1 := counterDelta(prev.cpuBusy, cur.cpuBusy, 64)
		dt, ok2 := counterDelta(prev.cpuTotal, cur.cpuTotal, 64)
		if ok1 && ok2 && dt > 0 {
			s.CPU.Machine = ratio(float64(db), float64(dt))
		} else if !ok1 || !ok2 {
			miss |= missCPUMachine
		}
	default:
		miss |= missCPUMachine
	}
	if cur.missing&missCPUProcess == 0 && prev.missing&missCPUProcess == 0 && cur.cores > 0 {
		if d, ok := counterDelta(prev.procCPUNs, cur.procCPUNs, 64); ok {
			s.CPU.Process = ratio(float64(d), ns*cur.cores)
		} else {
			miss |= missCPUProcess
		}
	} else {
		miss |= missCPUProcess
	}

	// Memory.
	s.Memory = MemoryStats{
		UsedBytes:    cur.memUsed,
		TotalBytes:   cur.memTotal,
		ProcessBytes: cur.memProc,
		Scope:        "host",
	}
	if cur.memCgroup {
		s.Memory.Scope = "cgroup"
	}

	// Interfaces.
	n := 0
	for i := range cur.nics {
		if cur.nics[i].present {
			n++
		}
	}
	if n > 0 {
		s.NICs = make([]NICStats, 0, n)
	}
	for i := range cur.nics {
		sl := &cur.nics[i]
		if !sl.present {
			continue
		}
		st := NICStats{
			Name:                sl.name,
			Index:               sl.index,
			Up:                  sl.up,
			Loopback:            sl.loopback,
			LinkSpeedBitsPerSec: sl.speed,
			RxBytes:             sl.rx,
			TxBytes:             sl.tx,
		}
		if sl.loopback {
			st.LinkSpeedBitsPerSec = 0
		}
		if sl.ok && sl.prevOK {
			if d, ok := counterDelta(sl.prevRx, sl.rx, cur.nicBits); ok {
				st.RxBytesPerSec = uint64(math.Round(float64(d) / sec))
			}
			if d, ok := counterDelta(sl.prevTx, sl.tx, cur.nicBits); ok {
				st.TxBytesPerSec = uint64(math.Round(float64(d) / sec))
			}
		}
		s.NICs = append(s.NICs, st)
	}

	// TCP.
	if cur.missing&missTCP == 0 && prev.missing&missTCP == 0 {
		in, _ := counterDelta(prev.tcpIn, cur.tcpIn, cur.tcpSegBits)
		out, _ := counterDelta(prev.tcpOut, cur.tcpOut, cur.tcpSegBits)
		ret, _ := counterDelta(prev.tcpRetrans, cur.tcpRetrans, cur.tcpRetBits)
		s.TCP = TCPStats{
			InSegs:        in,
			OutSegs:       out,
			RetransSegs:   ret,
			OutSegsPerSec: round(float64(out)/sec, 100),
			RetransPerSec: round(float64(ret)/sec, 100),
		}
	} else {
		miss |= missTCP
	}
	s.TCP.Established = cur.tcpEstab

	s.Ports = PortStats{
		EphemeralStart: cur.portStart,
		EphemeralCount: cur.portCount,
		TCPConns:       cur.tcpConns,
	}

	s.Runtime = RuntimeStats{
		Goroutines:   cur.goroutines,
		GCCycles:     cur.gcCycles - prev.gcCycles,
		GCPauseNs:    cur.gcPauseNs,
		GCPauseMaxNs: cur.gcPauseMax,
	}

	cur.missing = miss
	return true
}

// ratio returns num/den clamped to [0, 1] and rounded to four decimals.
func ratio(num, den float64) float64 {
	if den <= 0 || num <= 0 {
		return 0
	}
	r := num / den
	if r > 1 {
		r = 1
	}
	return round(r, 1e4)
}

func round(x, scale float64) float64 {
	return math.Round(x*scale) / scale
}

// runtimeReader samples Go runtime metrics without stopping the world.
type runtimeReader struct {
	samples    [3]metrics.Sample
	prevCounts []uint64
	havePrev   bool
}

func (r *runtimeReader) init() {
	r.samples[0].Name = "/sched/goroutines:goroutines"
	r.samples[1].Name = "/gc/cycles/total:gc-cycles"
	r.samples[2].Name = "/sched/pauses/total/gc:seconds"
}

// read stores the goroutine count and cumulative GC cycles in out, plus the
// total and maximum GC pause since the previous call.
func (r *runtimeReader) read(out *reading) {
	metrics.Read(r.samples[:])

	if v := r.samples[0].Value; v.Kind() == metrics.KindUint64 {
		out.goroutines = int(v.Uint64())
	} else {
		out.goroutines = runtime.NumGoroutine()
	}
	if v := r.samples[1].Value; v.Kind() == metrics.KindUint64 {
		out.gcCycles = v.Uint64()
	}
	v := r.samples[2].Value
	if v.Kind() != metrics.KindFloat64Histogram {
		return
	}
	h := v.Float64Histogram()
	if len(r.prevCounts) != len(h.Counts) {
		r.prevCounts = make([]uint64, len(h.Counts))
		r.havePrev = false
	}
	if r.havePrev {
		var sum, max float64
		for i, c := range h.Counts {
			d := c - r.prevCounts[i]
			if d == 0 || c < r.prevCounts[i] {
				continue
			}
			lo, hi := h.Buckets[i], h.Buckets[i+1]
			mid := (lo + hi) / 2
			switch {
			case math.IsInf(lo, -1):
				mid, lo = hi, hi
			case math.IsInf(hi, 1):
				mid, hi = lo, lo
			}
			sum += mid * float64(d)
			if hi > max {
				max = hi
			}
		}
		out.gcPauseNs = uint64(sum * 1e9)
		out.gcPauseMax = uint64(max * 1e9)
	}
	copy(r.prevCounts, h.Counts)
	r.havePrev = true
}
