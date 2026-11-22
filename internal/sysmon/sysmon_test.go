package sysmon

import (
	"context"
	"io"
	"net"
	"runtime"
	"testing"
	"time"
)

func TestHostInfo(t *testing.T) {
	h := Host()
	if h.OS != runtime.GOOS || h.Arch != runtime.GOARCH || h.CPUCores <= 0 {
		t.Fatalf("unexpected HostInfo: %+v", h)
	}
	if h.OSVersion == "" {
		t.Fatal("empty OSVersion")
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "linux" {
		if h.MemoryTotalBytes == 0 || len(h.NICs) == 0 || h.EphemeralPortCount <= 0 {
			t.Fatalf("expected populated host metrics on %s: %+v", runtime.GOOS, h)
		}
	}
}

func TestMonitorSamplingAndLoopbackTraffic(t *testing.T) {
	m, err := New(Options{Interval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	var s Stats
	select {
	case s = <-m.Stats():
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first sample")
	}
	if s.Time.IsZero() || s.ElapsedMS <= 0 || s.CPU.Cores <= 0 || s.Runtime.Goroutines <= 0 {
		t.Fatalf("unexpected sample: %+v", s)
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "linux" {
		if len(s.Unavailable) > 0 {
			t.Fatalf("unexpected unavailable metrics on %s: %v", runtime.GOOS, s.Unavailable)
		}
		if s.Memory.TotalBytes == 0 || s.Memory.ProcessBytes == 0 {
			t.Fatalf("unexpected memory stats: %+v", s.Memory)
		}
	}

	// Generate loopback traffic and confirm Latest() updates.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			io.Copy(io.Discard, c)
			c.Close()
		}
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 256*1024)
	_, _ = conn.Write(payload)
	_ = conn.Close()

	select {
	case <-m.Stats():
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for second sample")
	}
	latest := m.Latest()
	if latest.Time.IsZero() {
		t.Fatal("Latest() is zero after receiving stats")
	}
}

func TestEgressInterfaceLoopback(t *testing.T) {
	eg, err := EgressInterface("http://127.0.0.1:18080/ping")
	if err != nil {
		t.Fatal(err)
	}
	if !eg.Loopback || eg.IP != "127.0.0.1" || eg.LinkSpeedBitsPerSec != 0 {
		t.Fatalf("unexpected loopback egress: %+v", eg)
	}
}
