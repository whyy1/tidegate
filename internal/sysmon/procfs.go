package sysmon

// Linux collector based on procfs, sysfs and cgroupfs. It has no build tag:
// every path is resolved under a configurable root, so tests can run it
// against fixture trees on any OS. Only sysmon_linux.go wires it up for
// production use.

import (
	"bytes"
	"errors"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// USER_HZ, the unit of CPU times in /proc/<pid>/stat. It is fixed at 100 by
// the Linux user-space ABI on all mainstream architectures.
const clkTck = 100

// Largest procfs file the collector will buffer.
const maxProcFile = 4 << 20

// cgroup v1 reports "no limit" as a huge number near 2^63.
const cgroupV1Unlimited = 1 << 62

// procFile re-reads one file from offset 0 through a descriptor that stays
// open, so steady-state reads cost a pread and no allocations.
type procFile struct {
	path string
	f    *os.File
	buf  []byte
	head bool // only the beginning of the file is needed
}

var errNoPath = errors.New("sysmon: file not configured")

func (p *procFile) init(path string, size int, head bool) {
	p.path, p.buf, p.head = path, make([]byte, size), head
}

func (p *procFile) setPath(path string) {
	if path != p.path {
		p.close()
		p.path = path
	}
	if p.buf == nil {
		p.buf = make([]byte, 4096)
	}
}

func (p *procFile) read() ([]byte, error) {
	if p.path == "" {
		return nil, errNoPath
	}
	if p.f == nil {
		f, err := os.Open(p.path)
		if err != nil {
			return nil, err
		}
		p.f = f
	}
	for {
		n, err := p.f.ReadAt(p.buf, 0)
		if err == io.EOF || (err == nil && p.head) {
			return p.buf[:n], nil
		}
		if err != nil {
			p.close()
			return nil, err
		}
		// The buffer is full and the file may be longer.
		if len(p.buf) >= maxProcFile {
			return p.buf[:n], nil
		}
		p.buf = make([]byte, 2*len(p.buf))
	}
}

func (p *procFile) close() {
	if p.f != nil {
		p.f.Close()
		p.f = nil
	}
}

// procCollector reads Linux statistics below root ("/" in production).
type procCollector struct {
	root     string
	numCPU   int
	pageSize uint64
	bits     uint8 // width of the kernel's "unsigned long" counters
	// selfCPU, when set, returns this process's CPU time in nanoseconds with
	// better resolution than /proc/self/stat.
	selfCPU func() (uint64, bool)

	stat, selfStat, meminfo, netDev, snmp, sock, sock6 procFile

	// cgroup state; a zero limit means none applies.
	memLimit   uint64
	memV2      bool
	cpuCores   float64
	cpuV2      bool
	cgMemUsage procFile
	cgMemStat  procFile
	cgCPU      procFile

	nics        []nicSlot
	lastRefresh time.Time
	needRefresh bool

	portStart, portCount int
	portOK               bool
}

func newProcCollector(root string) *procCollector {
	c := &procCollector{
		root:     root,
		numCPU:   runtime.NumCPU(),
		pageSize: uint64(os.Getpagesize()),
		bits:     64,
	}
	c.stat.init(c.p("/proc/stat"), 1024, true) // first line only
	c.selfStat.init(c.p("/proc/self/stat"), 1024, false)
	c.meminfo.init(c.p("/proc/meminfo"), 4096, false)
	c.netDev.init(c.p("/proc/net/dev"), 4096, false)
	c.snmp.init(c.p("/proc/net/snmp"), 8192, false)
	c.sock.init(c.p("/proc/net/sockstat"), 1024, false)
	c.sock6.init(c.p("/proc/net/sockstat6"), 1024, false)
	return c
}

// p maps an absolute Linux path to the collector's root.
func (c *procCollector) p(abs string) string {
	return filepath.Join(c.root, filepath.FromSlash(abs))
}

func (c *procCollector) close() {
	for _, f := range []*procFile{&c.stat, &c.selfStat, &c.meminfo, &c.netDev, &c.snmp, &c.sock, &c.sock6, &c.cgMemUsage, &c.cgMemStat, &c.cgCPU} {
		f.close()
	}
}

func (c *procCollector) read(r *reading) {
	since := r.at.Sub(c.lastRefresh)
	if c.lastRefresh.IsZero() || since >= refreshEvery || since < 0 || (c.needRefresh && since >= time.Second) {
		c.refresh(r.at)
	}
	r.nicBits, r.tcpSegBits, r.tcpRetBits = c.bits, c.bits, c.bits
	r.cores = float64(c.numCPU)
	if c.cpuCores > 0 && c.cpuCores < r.cores {
		r.cores = c.cpuCores
	}

	// Host CPU, always read so the host view is available if a cgroup
	// quota disappears.
	if b, err := c.stat.read(); err == nil {
		if busy, total, ok := parseProcStatCPU(b); ok {
			r.cpuBusy, r.cpuTotal = busy, total
		} else {
			r.missing |= missCPUMachine
		}
	} else {
		r.missing |= missCPUMachine
	}
	if c.cpuCores > 0 {
		r.cgCores = c.cpuCores
		if b, err := c.cgCPU.read(); err == nil {
			if c.cpuV2 {
				if v, ok := parseKeyedUint(b, "usage_usec"); ok {
					r.cgUsageNs, r.cgCPUOK = v*1000, true
				}
			} else if v, ok := parseUint(bytes.TrimSpace(b)); ok {
				r.cgUsageNs, r.cgCPUOK = v, true
			}
		}
	}

	// Process.
	var utime, stime, rss uint64
	selfOK := false
	if b, err := c.selfStat.read(); err == nil {
		utime, stime, rss, selfOK = parseSelfStat(b)
	}
	if selfOK {
		r.memProc = rss * c.pageSize
	} else {
		r.missing |= missMemProcess
	}
	switch {
	case c.selfCPU != nil:
		if ns, ok := c.selfCPU(); ok {
			r.procCPUNs = ns
		} else {
			r.missing |= missCPUProcess
		}
	case selfOK:
		r.procCPUNs = (utime + stime) * (1e9 / clkTck)
	default:
		r.missing |= missCPUProcess
	}

	c.readMemory(r)
	c.readNICs(r)

	// TCP.
	if b, err := c.snmp.read(); err == nil {
		if in, out, ret, est, ok := parseSNMPTcp(b); ok {
			r.tcpIn, r.tcpOut, r.tcpRetrans, r.tcpEstab = in, out, ret, est
		} else {
			r.missing |= missTCP
		}
	} else {
		r.missing |= missTCP
	}
	conns, connsOK := uint64(0), false
	if b, err := c.sock.read(); err == nil {
		if inuse, tw, ok := parseSockstat(b, "TCP"); ok {
			conns, connsOK = inuse+tw, true
		}
	}
	if b, err := c.sock6.read(); err == nil {
		if inuse, _, ok := parseSockstat(b, "TCP6"); ok {
			conns += inuse
		}
	}
	if connsOK {
		r.tcpConns = conns
	} else {
		r.missing |= missTCPConns
	}

	r.portStart, r.portCount = c.portStart, c.portCount
	if !c.portOK {
		r.missing |= missPorts
	}
}

func (c *procCollector) readMemory(r *reading) {
	if c.memLimit > 0 {
		if b, err := c.cgMemUsage.read(); err == nil {
			if usage, ok := parseUint(bytes.TrimSpace(b)); ok {
				var inactive uint64
				if s, err := c.cgMemStat.read(); err == nil {
					key := "total_inactive_file"
					if c.memV2 {
						key = "inactive_file"
					}
					inactive, _ = parseKeyedUint(s, key)
				}
				if inactive < usage {
					usage -= inactive
				}
				r.memUsed, r.memTotal, r.memCgroup = usage, c.memLimit, true
				return
			}
		}
		// Fall back to the host view if the cgroup files went away.
	}
	b, err := c.meminfo.read()
	if err != nil {
		r.missing |= missMemUsed | missMemTotal
		return
	}
	total, avail, ok := parseMeminfo(b)
	if !ok {
		r.missing |= missMemUsed | missMemTotal
		return
	}
	r.memTotal = total
	if avail <= total {
		r.memUsed = total - avail
	}
}

func (c *procCollector) readNICs(r *reading) {
	for i := range c.nics {
		c.nics[i].begin()
	}
	r.nics = c.nics
	b, err := c.netDev.read()
	if err != nil {
		r.missing |= missNICs | missNICRates
		return
	}
	parseNetDev(b, func(name []byte, rx, tx uint64) {
		c.slot(name).set(rx, tx)
	})
	r.nics = c.nics
}

// slot returns the slot for an interface name, creating it if needed.
func (c *procCollector) slot(name []byte) *nicSlot {
	for i := range c.nics {
		if c.nics[i].name == string(name) {
			return &c.nics[i]
		}
	}
	c.needRefresh = true
	n := string(name)
	c.nics = append(c.nics, nicSlot{name: n, loopback: n == "lo", up: true})
	return &c.nics[len(c.nics)-1]
}

// refresh re-reads slowly changing settings: interface flags and speeds,
// the port range and cgroup limits.
func (c *procCollector) refresh(now time.Time) {
	c.lastRefresh, c.needRefresh = now, false

	if b, err := os.ReadFile(c.p("/proc/sys/net/ipv4/ip_local_port_range")); err == nil {
		c.portStart, c.portCount, c.portOK = parsePortRange(b)
	} else {
		c.portOK = false
	}

	if entries, err := os.ReadDir(c.p("/sys/class/net")); err == nil {
		listed := make(map[string]bool, len(entries))
		for _, e := range entries {
			listed[e.Name()] = true
		}
		kept := c.nics[:0]
		have := make(map[string]bool, len(c.nics))
		for _, s := range c.nics {
			// Keep interfaces still listed in sysfs or still counted in
			// /proc/net/dev (sysfs may belong to another network namespace).
			if listed[s.name] || s.present {
				kept = append(kept, s)
				have[s.name] = true
			}
		}
		c.nics = kept
		for _, e := range entries {
			if !have[e.Name()] {
				c.nics = append(c.nics, nicSlot{name: e.Name()})
			}
		}
		for i := range c.nics {
			c.readSysfsNIC(&c.nics[i])
		}
	}
	sort.SliceStable(c.nics, func(i, j int) bool {
		a, b := c.nics[i], c.nics[j]
		if a.index != b.index {
			return a.index < b.index
		}
		return a.name < b.name
	})

	c.resolveCgroup()
}

// Interface flags from include/uapi/linux/if.h.
const (
	iffUp       = 0x1
	iffLoopback = 0x8
)

func (c *procCollector) readSysfsNIC(s *nicSlot) {
	dir := "/sys/class/net/" + s.name + "/"
	if b, err := os.ReadFile(c.p(dir + "flags")); err == nil {
		if f, err := strconv.ParseUint(strings.TrimSpace(string(b)), 0, 32); err == nil {
			s.loopback = f&iffLoopback != 0
			s.up = f&iffUp != 0
			// Administratively up is not enough: the operational state
			// must be up, or unknown for devices that do not report it
			// (loopback, many tunnels).
			if b, err := os.ReadFile(c.p(dir + "operstate")); err == nil && s.up {
				st := strings.TrimSpace(string(b))
				s.up = st == "up" || st == "unknown"
			}
		}
	} else {
		s.loopback, s.up = s.name == "lo", true
	}
	if b, err := os.ReadFile(c.p(dir + "ifindex")); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			s.index = v
		}
	}
	s.speed = sysfsLinkSpeed(c.root, s.name)
}

// sysfsLinkSpeed returns the link speed in bits/s from sysfs, or 0 when the
// driver does not report one (virtual devices, links that are down).
func sysfsLinkSpeed(root, name string) uint64 {
	b, err := os.ReadFile(filepath.Join(root, "sys", "class", "net", name, "speed"))
	if err != nil {
		return 0
	}
	return parseLinkSpeed(b)
}

// parseLinkSpeed converts the sysfs speed file (Mbit/s) to bits/s. Values
// that drivers use for "unknown" (-1, 0, 0xFFFFFFFF) and implausible values
// above 10 Tbit/s yield 0.
func parseLinkSpeed(b []byte) uint64 {
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || v <= 0 || v >= 10_000_000 {
		return 0
	}
	return uint64(v) * 1_000_000
}

// resolveCgroup finds the memory and CPU limits that apply to this process
// and points the cgroup usage files at the cgroup that sets them.
func (c *procCollector) resolveCgroup() {
	c.memLimit, c.cpuCores = 0, 0
	cg, err1 := os.ReadFile(c.p("/proc/self/cgroup"))
	mi, err2 := os.ReadFile(c.p("/proc/self/mountinfo"))
	if err1 != nil || err2 != nil {
		return
	}
	v2Path, hasV2Path, v1Paths := parseSelfCgroup(cg)
	v2Mount, hasV2Mount, v1Mounts := parseMountinfoCgroups(mi)

	var hostTotal uint64
	if b, err := os.ReadFile(c.p("/proc/meminfo")); err == nil {
		hostTotal, _, _ = parseMeminfo(b)
	}

	// Memory: a v1 memory hierarchy wins over the unified one (hybrid
	// systems mount cgroup2 without controllers).
	if m, ok := v1Mounts["memory"]; ok && v1Paths["memory"] != "" {
		dir := c.existingDir(cgroupDir(m, v1Paths["memory"]), m.mountpoint, "memory.usage_in_bytes")
		if dir != "" {
			limit, ok := c.v1MemoryLimit(dir)
			if ok && limit < cgroupV1Unlimited && (hostTotal == 0 || limit < hostTotal) {
				c.memLimit, c.memV2 = limit, false
				c.cgMemUsage.setPath(c.p(path.Join(dir, "memory.usage_in_bytes")))
				c.cgMemStat.setPath(c.p(path.Join(dir, "memory.stat")))
			}
		}
	} else if hasV2Mount && hasV2Path {
		dir := c.existingDir(cgroupDir(v2Mount, v2Path), v2Mount.mountpoint, "memory.current")
		if dir != "" {
			limit, at := c.minLimit(dir, v2Mount.mountpoint, func(d string) (float64, bool) {
				b, err := os.ReadFile(c.p(path.Join(d, "memory.max")))
				if err != nil {
					return 0, false
				}
				v, limited := parseCgroupMax(b)
				return float64(v), limited
			})
			if at != "" && (hostTotal == 0 || uint64(limit) < hostTotal) {
				c.memLimit, c.memV2 = uint64(limit), true
				c.cgMemUsage.setPath(c.p(path.Join(at, "memory.current")))
				c.cgMemStat.setPath(c.p(path.Join(at, "memory.stat")))
			}
		}
	}
	if c.memLimit == 0 {
		c.cgMemUsage.setPath("")
		c.cgMemStat.setPath("")
	}

	// CPU.
	if m, ok := v1Mounts["cpu"]; ok && v1Paths["cpu"] != "" {
		dir := c.existingDir(cgroupDir(m, v1Paths["cpu"]), m.mountpoint, "cpu.cfs_quota_us")
		if dir != "" {
			cores, at := c.minLimit(dir, m.mountpoint, func(d string) (float64, bool) {
				q, err1 := os.ReadFile(c.p(path.Join(d, "cpu.cfs_quota_us")))
				p, err2 := os.ReadFile(c.p(path.Join(d, "cpu.cfs_period_us")))
				if err1 != nil || err2 != nil {
					return 0, false
				}
				return parseCFSQuota(q, p)
			})
			if at != "" && cores < float64(c.numCPU) {
				usage := ""
				if am, ok := v1Mounts["cpuacct"]; ok {
					if am.mountpoint == m.mountpoint && c.fileExists(path.Join(at, "cpuacct.usage")) {
						usage = path.Join(at, "cpuacct.usage")
					} else if d := c.existingDir(cgroupDir(am, v1Paths["cpuacct"]), am.mountpoint, "cpuacct.usage"); d != "" {
						usage = path.Join(d, "cpuacct.usage")
					}
				}
				if usage != "" {
					c.cpuCores, c.cpuV2 = cores, false
					c.cgCPU.setPath(c.p(usage))
				}
			}
		}
	} else if hasV2Mount && hasV2Path {
		dir := c.existingDir(cgroupDir(v2Mount, v2Path), v2Mount.mountpoint, "cpu.stat")
		if dir != "" {
			cores, at := c.minLimit(dir, v2Mount.mountpoint, func(d string) (float64, bool) {
				b, err := os.ReadFile(c.p(path.Join(d, "cpu.max")))
				if err != nil {
					return 0, false
				}
				return parseCPUMax(b)
			})
			if at != "" && cores < float64(c.numCPU) && c.fileExists(path.Join(at, "cpu.stat")) {
				c.cpuCores, c.cpuV2 = cores, true
				c.cgCPU.setPath(c.p(path.Join(at, "cpu.stat")))
			}
		}
	}
	if c.cpuCores == 0 {
		c.cgCPU.setPath("")
	}
}

func (c *procCollector) fileExists(abs string) bool {
	_, err := os.Stat(c.p(abs))
	return err == nil
}

// existingDir returns dir if it contains file, else fallback if that does,
// else "". Inside containers the path from /proc/self/cgroup may not exist
// in the container's view of cgroupfs, where the mount root is the
// container's own cgroup.
func (c *procCollector) existingDir(dir, fallback, file string) string {
	if c.fileExists(path.Join(dir, file)) {
		return dir
	}
	if fallback != dir && c.fileExists(path.Join(fallback, file)) {
		return fallback
	}
	return ""
}

// minLimit walks from dir up to stop (inclusive) and returns the smallest
// limit reported by get and the directory that sets it ("" if none does).
func (c *procCollector) minLimit(dir, stop string, get func(dir string) (float64, bool)) (float64, string) {
	best, at := math.Inf(1), ""
	for d := dir; ; d = path.Dir(d) {
		if v, ok := get(d); ok && v > 0 && v < best {
			best, at = v, d
		}
		if d == stop || d == "/" || d == "." || !strings.HasPrefix(d, stop) {
			break
		}
	}
	return best, at
}

func (c *procCollector) v1MemoryLimit(dir string) (uint64, bool) {
	// hierarchical_memory_limit already accounts for ancestors.
	if b, err := os.ReadFile(c.p(path.Join(dir, "memory.stat"))); err == nil {
		if v, ok := parseKeyedUint(b, "hierarchical_memory_limit"); ok {
			return v, true
		}
	}
	b, err := os.ReadFile(c.p(path.Join(dir, "memory.limit_in_bytes")))
	if err != nil {
		return 0, false
	}
	return parseUint(bytes.TrimSpace(b))
}

// ---- Pure parsers. They take file contents and do not allocate. ----

func nextLine(b []byte) (line, rest []byte) {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[:i], b[i+1:]
	}
	return b, nil
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// nextField returns the next whitespace-separated field of b.
func nextField(b []byte) (field, rest []byte) {
	i := 0
	for i < len(b) && isSpace(b[i]) {
		i++
	}
	j := i
	for j < len(b) && !isSpace(b[j]) {
		j++
	}
	return b[i:j], b[j:]
}

func hasPrefix(b []byte, s string) bool {
	return len(b) >= len(s) && string(b[:len(s)]) == s
}

func parseUint(b []byte) (uint64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		d := uint64(c - '0')
		if n > (math.MaxUint64-d)/10 {
			return 0, false
		}
		n = n*10 + d
	}
	return n, true
}

func parseInt(b []byte) (int64, bool) {
	neg := len(b) > 0 && b[0] == '-'
	if neg {
		b = b[1:]
	}
	u, ok := parseUint(b)
	if !ok || u > math.MaxInt64 {
		return 0, false
	}
	if neg {
		return -int64(u), true
	}
	return int64(u), true
}

// parseProcStatCPU parses the aggregate "cpu" line of /proc/stat. busy
// covers user, nice, system, irq, softirq and steal; idle and iowait count
// as not busy. guest time is already included in user.
func parseProcStatCPU(b []byte) (busy, total uint64, ok bool) {
	line, _ := nextLine(b)
	f, rest := nextField(line)
	if string(f) != "cpu" {
		return 0, 0, false
	}
	var v [8]uint64
	n := 0
	for n < len(v) {
		f, rest = nextField(rest)
		if len(f) == 0 {
			break
		}
		x, ok := parseUint(f)
		if !ok {
			return 0, 0, false
		}
		v[n] = x
		n++
	}
	if n < 4 {
		return 0, 0, false
	}
	busy = v[0] + v[1] + v[2] + v[5] + v[6] + v[7]
	return busy, busy + v[3] + v[4], true
}

// parseSelfStat parses /proc/<pid>/stat and returns utime and stime in clock
// ticks and the resident set in pages. The command name may contain spaces
// and parentheses, so fields are counted from the last ')'.
func parseSelfStat(b []byte) (utime, stime, rss uint64, ok bool) {
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, 0, 0, false
	}
	rest := b[i+1:]
	var got int
	for idx := 0; idx <= 21; idx++ { // idx 0 is field 3 (state)
		var f []byte
		f, rest = nextField(rest)
		if len(f) == 0 {
			return 0, 0, 0, false
		}
		switch idx {
		case 11: // field 14
			utime, ok = parseUint(f)
		case 12: // field 15
			stime, ok = parseUint(f)
		case 21: // field 24
			var v int64
			v, ok = parseInt(f)
			if v > 0 {
				rss = uint64(v)
			}
		default:
			continue
		}
		if !ok {
			return 0, 0, 0, false
		}
		got++
	}
	return utime, stime, rss, got == 3
}

// parseMeminfo returns MemTotal and MemAvailable in bytes. Kernels older
// than 3.14 lack MemAvailable; MemFree + Buffers + Cached approximates it.
func parseMeminfo(b []byte) (total, avail uint64, ok bool) {
	var free, buffers, cached uint64
	var haveTotal, haveAvail bool
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		c := bytes.IndexByte(line, ':')
		if c < 0 {
			continue
		}
		f, rest := nextField(line[c+1:])
		v, vok := parseUint(f)
		if !vok {
			continue
		}
		if unit, _ := nextField(rest); string(unit) == "kB" {
			v *= 1024
		}
		switch string(line[:c]) {
		case "MemTotal":
			total, haveTotal = v, true
		case "MemAvailable":
			avail, haveAvail = v, true
		case "MemFree":
			free = v
		case "Buffers":
			buffers = v
		case "Cached":
			cached = v
		}
	}
	if !haveTotal {
		return 0, 0, false
	}
	if !haveAvail {
		avail = free + buffers + cached
	}
	if avail > total {
		avail = total
	}
	return total, avail, true
}

// parseNetDev calls fn for every interface in /proc/net/dev with its
// received and transmitted byte counters.
func parseNetDev(b []byte, fn func(name []byte, rx, tx uint64)) {
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		c := bytes.IndexByte(line, ':')
		if c < 0 { // header lines
			continue
		}
		name := bytes.TrimSpace(line[:c])
		rest := line[c+1:]
		var rx, tx uint64
		good := len(name) > 0
		for k := 0; k < 9 && good; k++ {
			var f []byte
			f, rest = nextField(rest)
			v, ok := parseUint(f)
			if !ok {
				good = false
				break
			}
			switch k {
			case 0:
				rx = v
			case 8:
				tx = v
			}
		}
		if good {
			fn(name, rx, tx)
		}
	}
}

// parseSNMPTcp extracts TCP counters from /proc/net/snmp, where a header
// line of names is followed by a line of values.
func parseSNMPTcp(b []byte) (inSegs, outSegs, retrans, estab uint64, ok bool) {
	var hdr []byte
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		if !hasPrefix(line, "Tcp:") {
			continue
		}
		if hdr == nil {
			hdr = line[4:]
			continue
		}
		vals := line[4:]
		found := 0
		for {
			var k, v []byte
			k, hdr = nextField(hdr)
			v, vals = nextField(vals)
			if len(k) == 0 || len(v) == 0 {
				break
			}
			var dst *uint64
			switch string(k) {
			case "InSegs":
				dst = &inSegs
			case "OutSegs":
				dst = &outSegs
			case "RetransSegs":
				dst = &retrans
			case "CurrEstab":
				dst = &estab
			default:
				continue
			}
			x, xok := parseUint(v)
			if !xok {
				return 0, 0, 0, 0, false
			}
			*dst = x
			found++
		}
		return inSegs, outSegs, retrans, estab, found == 4
	}
	return 0, 0, 0, 0, false
}

// parseSockstat reads "inuse" and "tw" from the line for proto ("TCP" in
// /proc/net/sockstat, "TCP6" in /proc/net/sockstat6).
func parseSockstat(b []byte, proto string) (inuse, tw uint64, ok bool) {
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		if !hasPrefix(line, proto) || len(line) <= len(proto) || line[len(proto)] != ':' {
			continue
		}
		rest := line[len(proto)+1:]
		for {
			var k, v []byte
			k, rest = nextField(rest)
			v, rest = nextField(rest)
			if len(k) == 0 || len(v) == 0 {
				break
			}
			x, xok := parseUint(v)
			if !xok {
				continue
			}
			switch string(k) {
			case "inuse":
				inuse, ok = x, true
			case "tw":
				tw = x
			}
		}
		return inuse, tw, ok
	}
	return 0, 0, false
}

// parsePortRange parses ip_local_port_range ("32768\t60999").
func parsePortRange(b []byte) (start, count int, ok bool) {
	f1, rest := nextField(b)
	f2, _ := nextField(rest)
	lo, ok1 := parseUint(f1)
	hi, ok2 := parseUint(f2)
	if !ok1 || !ok2 || lo == 0 || hi > 65535 || lo > hi {
		return 0, 0, false
	}
	return int(lo), int(hi - lo + 1), true
}

// parseKeyedUint finds "key value" in files such as memory.stat and
// cpu.stat.
func parseKeyedUint(b []byte, key string) (uint64, bool) {
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		k, rest := nextField(line)
		if string(k) != key {
			continue
		}
		v, _ := nextField(rest)
		return parseUint(v)
	}
	return 0, false
}

// parseCgroupMax parses a cgroup v2 limit such as memory.max: a number, or
// "max" for no limit.
func parseCgroupMax(b []byte) (v uint64, limited bool) {
	f, _ := nextField(b)
	if string(f) == "max" {
		return 0, false
	}
	v, ok := parseUint(f)
	return v, ok
}

// parseCPUMax parses cgroup v2 cpu.max ("$QUOTA $PERIOD" or "max $PERIOD")
// into cores.
func parseCPUMax(b []byte) (cores float64, limited bool) {
	q, rest := nextField(b)
	p, _ := nextField(rest)
	if string(q) == "max" {
		return 0, false
	}
	quota, ok1 := parseUint(q)
	period, ok2 := parseUint(p)
	if !ok1 || !ok2 || quota == 0 || period == 0 {
		return 0, false
	}
	return float64(quota) / float64(period), true
}

// parseCFSQuota parses cgroup v1 cpu.cfs_quota_us and cpu.cfs_period_us
// into cores; a quota of -1 means no limit.
func parseCFSQuota(quota, period []byte) (cores float64, limited bool) {
	q, ok1 := parseInt(bytes.TrimSpace(quota))
	p, ok2 := parseInt(bytes.TrimSpace(period))
	if !ok1 || !ok2 || q <= 0 || p <= 0 {
		return 0, false
	}
	return float64(q) / float64(p), true
}

// osReleaseName returns PRETTY_NAME from an os-release file.
func osReleaseName(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// kernelCounterBits returns the width of the kernel's "unsigned long"
// counters for a uname machine string.
func kernelCounterBits(machine string) uint8 {
	if strings.Contains(machine, "64") || machine == "s390x" || machine == "alpha" {
		return 64
	}
	if machine == "" {
		return 64
	}
	return 32
}

// ---- cgroup discovery (refresh path; may allocate). ----

type cgMount struct {
	mountpoint string // absolute path where the hierarchy is mounted
	root       string // cgroup path of the mount's root within the hierarchy
}

// parseSelfCgroup parses /proc/self/cgroup. It returns the cgroup v2 path
// (from the "0::" line) and the v1 path of each controller.
func parseSelfCgroup(b []byte) (v2 string, hasV2 bool, v1 map[string]string) {
	v1 = map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" && parts[1] == "" {
			v2, hasV2 = parts[2], true
			continue
		}
		for _, ctl := range strings.Split(parts[1], ",") {
			if ctl != "" && !strings.HasPrefix(ctl, "name=") {
				v1[ctl] = parts[2]
			}
		}
	}
	return v2, hasV2, v1
}

// parseMountinfoCgroups finds cgroup mounts in /proc/self/mountinfo: the
// cgroup2 mount and, for cgroup v1, one mount per controller.
func parseMountinfoCgroups(b []byte) (v2 cgMount, hasV2 bool, v1 map[string]cgMount) {
	v1 = map[string]cgMount{}
	for _, line := range strings.Split(string(b), "\n") {
		fs := strings.Fields(line)
		sep := -1
		for i := 6; i < len(fs); i++ {
			if fs[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || len(fs) < sep+3 {
			continue
		}
		m := cgMount{mountpoint: unescapeMount(fs[4]), root: unescapeMount(fs[3])}
		switch fs[sep+1] {
		case "cgroup2":
			if !hasV2 {
				v2, hasV2 = m, true
			}
		case "cgroup":
			if len(fs) < sep+4 {
				continue
			}
			for _, opt := range strings.Split(fs[sep+3], ",") {
				switch opt {
				case "memory", "cpu", "cpuacct":
					if _, dup := v1[opt]; !dup {
						v1[opt] = m
					}
				}
			}
		}
	}
	return v2, hasV2, v1
}

// unescapeMount decodes the octal escapes (\040 for space and so on) used
// in mountinfo paths.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				sb.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// cgroupDir maps a cgroup path from /proc/self/cgroup to a directory under
// the mount. When the mount's root is a sub-cgroup (common in containers),
// that prefix is removed. Paths that cannot be mapped resolve to the mount
// point itself.
func cgroupDir(m cgMount, p string) string {
	if p == "" || strings.Contains(p, "/..") {
		return m.mountpoint
	}
	rel := p
	if m.root != "/" && m.root != "" {
		switch {
		case p == m.root:
			rel = "/"
		case strings.HasPrefix(p, m.root+"/"):
			rel = p[len(m.root):]
		default:
			return m.mountpoint
		}
	}
	return path.Join(m.mountpoint, rel)
}
