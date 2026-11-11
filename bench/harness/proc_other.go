//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// procWatch on Unix reads /proc where available; peak memory comes from
// the exited process's rusage (maxRSS).
type procWatch struct{ pid int }

func watch(pid int) *procWatch                  { return &procWatch{pid: pid} }
func (w *procWatch) sample()                    {}
func (w *procWatch) peak() (ws, private uint64) { return 0, 0 }
func (w *procWatch) close()                     {}

func (w *procWatch) cpu() time.Duration {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", w.pid))
	if err != nil {
		return 0
	}
	s := string(data)
	if i := strings.LastIndexByte(s, ')'); i >= 0 {
		s = s[i+2:]
	}
	f := strings.Fields(s)
	if len(f) < 13 {
		return 0
	}
	ut, _ := strconv.ParseInt(f[11], 10, 64)
	st, _ := strconv.ParseInt(f[12], 10, 64)
	return time.Duration(ut+st) * 10 * time.Millisecond // USER_HZ is 100 on Linux
}

func maxRSS(ps *os.ProcessState) uint64 {
	if ru, ok := ps.SysUsage().(*syscall.Rusage); ok {
		return uint64(ru.Maxrss) * 1024
	}
	return 0
}

// timeWaitCount counts TIME_WAIT sockets from /proc/net/tcp{,6}.
func timeWaitCount() (int, error) {
	n := 0
	for _, p := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) > 3 && fields[3] == "06" {
				n++
			}
		}
		f.Close()
	}
	return n, nil
}

func defaultK6Paths() []string { return []string{"/usr/bin/k6", "/usr/local/bin/k6"} }

func machineInfo() string {
	cpu := "unknown CPU"
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "model name") {
				if _, v, ok := strings.Cut(line, ":"); ok {
					cpu = strings.TrimSpace(v)
					break
				}
			}
		}
	}
	return fmt.Sprintf("%s，%d 个逻辑处理器，%s", cpu, numCPU(), goVersion())
}
