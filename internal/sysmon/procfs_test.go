package sysmon

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProcCollectorFixtureCgroupV2(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "/etc/os-release", "PRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n")
	writeFixture(t, root, "/proc/self/cgroup", "0::/docker/abc\n")
	writeFixture(t, root, "/proc/self/mountinfo", "1 1 0:1 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n")
	writeFixture(t, root, "/sys/fs/cgroup/docker/abc/cpu.max", "200000 100000\n")
	writeFixture(t, root, "/sys/fs/cgroup/docker/abc/cpu.stat", "usage_usec 1000000\n")
	writeFixture(t, root, "/sys/fs/cgroup/docker/abc/memory.max", "1073741824\n")
	writeFixture(t, root, "/sys/fs/cgroup/docker/abc/memory.current", "536870912\n")
	writeFixture(t, root, "/sys/fs/cgroup/docker/abc/memory.stat", "inactive_file 67108864\n")

	writeFixture(t, root, "/proc/stat", "cpu  100 0 50 800 50 0 0 0\n")
	writeFixture(t, root, "/proc/self/stat", "123 (tide gate) R 1 1 1 0 -1 0 0 0 0 0 100 50 0 0 20 0 1 0 0 0 1024\n")
	writeFixture(t, root, "/proc/meminfo", "MemTotal:       8388608 kB\nMemAvailable:   4194304 kB\n")
	writeFixture(t, root, "/proc/net/dev", ""+
		"Inter-|   Receive                                                |  Transmit\n"+
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"+
		"    lo: 1000 10 0 0 0 0 0 0 1000 10 0 0 0 0 0 0\n"+
		"  eth0: 4294967000 100 0 0 0 0 0 0 10000 50 0 0 0 0 0 0\n")
	writeFixture(t, root, "/proc/net/snmp", ""+
		"Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets CurrEstab InSegs OutSegs RetransSegs InErrs OutRsts\n"+
		"Tcp: 1 200 120000 -1 10 5 0 0 4 1000 2000 3 0 0\n")
	writeFixture(t, root, "/proc/net/sockstat", "sockets: used 100\nTCP: inuse 12 orphan 0 tw 8 alloc 15 mem 1\n")
	writeFixture(t, root, "/proc/net/sockstat6", "TCP6: inuse 3\n")
	writeFixture(t, root, "/proc/sys/net/ipv4/ip_local_port_range", "32768\t60999\n")

	writeFixture(t, root, "/sys/class/net/lo/flags", "0x9\n")
	writeFixture(t, root, "/sys/class/net/lo/operstate", "unknown\n")
	writeFixture(t, root, "/sys/class/net/lo/ifindex", "1\n")
	writeFixture(t, root, "/sys/class/net/eth0/flags", "0x1003\n")
	writeFixture(t, root, "/sys/class/net/eth0/operstate", "up\n")
	writeFixture(t, root, "/sys/class/net/eth0/ifindex", "2\n")
	writeFixture(t, root, "/sys/class/net/eth0/speed", "10000\n")

	col := newProcCollector(root)
	col.numCPU = 8
	col.pageSize = 4096
	col.bits = 32 // verify 32-bit wraparound logic in counterDelta
	defer col.close()

	t0 := time.Unix(1700000000, 0)
	prev := reading{at: t0}
	col.read(&prev)

	// Advance counters by 1 second (including 32-bit RX wrap on eth0).
	writeFixture(t, root, "/sys/fs/cgroup/docker/abc/cpu.stat", "usage_usec 2000000\n")
	writeFixture(t, root, "/sys/fs/cgroup/docker/abc/memory.current", "603979776\n")
	writeFixture(t, root, "/proc/self/stat", "123 (tide gate) R 1 1 1 0 -1 0 0 0 0 0 150 100 0 0 20 0 1 0 0 0 2048\n")
	writeFixture(t, root, "/proc/net/dev", ""+
		"Inter-|   Receive                                                |  Transmit\n"+
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"+
		"    lo: 3000 20 0 0 0 0 0 0 3000 20 0 0 0 0 0 0\n"+
		"  eth0: 704 120 0 0 0 0 0 0 110000 100 0 0 0 0 0 0\n")
	writeFixture(t, root, "/proc/net/snmp", ""+
		"Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets CurrEstab InSegs OutSegs RetransSegs InErrs OutRsts\n"+
		"Tcp: 1 200 120000 -1 10 5 0 0 6 1500 2400 8 0 0\n")
	cur := reading{at: t0.Add(time.Second)}
	col.read(&cur)

	var s Stats
	if !compute(&prev, &cur, &s) {
		t.Fatal("compute failed")
	}
	if s.CPU.Scope != "cgroup" || s.CPU.Cores != 2 || math.Abs(s.CPU.Machine-0.5) > 0.01 {
		t.Fatalf("CPU = %+v", s.CPU)
	}
	// Process CPU delta = (50+50) ticks = 1s across 2 cores => 0.5
	if math.Abs(s.CPU.Process-0.5) > 0.01 {
		t.Fatalf("Process CPU = %v, want 0.5", s.CPU.Process)
	}
	if s.Memory.Scope != "cgroup" || s.Memory.TotalBytes != 1073741824 || s.Memory.ProcessBytes != 2048*4096 {
		t.Fatalf("Memory = %+v", s.Memory)
	}
	if len(s.NICs) != 2 || s.NICs[1].Name != "eth0" || s.NICs[1].LinkSpeedBitsPerSec != 10_000_000_000 {
		t.Fatalf("NICs = %+v", s.NICs)
	}
	// eth0 RX wrapped from 4294967000 to 704 over 32 bits: (2^32 - 4294967000) + 704 = 296 + 704 = 1000 bytes/s.
	if s.NICs[1].RxBytesPerSec != 1000 || s.NICs[1].TxBytesPerSec != 100000 {
		t.Fatalf("eth0 rates = rx:%d tx:%d", s.NICs[1].RxBytesPerSec, s.NICs[1].TxBytesPerSec)
	}
	if s.TCP.InSegs != 500 || s.TCP.OutSegs != 400 || s.TCP.RetransSegs != 5 || s.TCP.Established != 6 {
		t.Fatalf("TCP = %+v", s.TCP)
	}
	if s.Ports.EphemeralStart != 32768 || s.Ports.EphemeralCount != 28232 || s.Ports.TCPConns != 23 {
		t.Fatalf("Ports = %+v", s.Ports)
	}
	if got := osReleaseName([]byte("NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n")); got != "Ubuntu 24.04.1 LTS" {
		t.Fatalf("osReleaseName = %q", got)
	}
}
