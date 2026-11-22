package auth

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterGrowthAndReset(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(RateLimitConfig{}, clk.Now)
	const key = "admin"

	for i := 1; i <= DefaultFreeAttempts; i++ {
		l.fail(key)
		if d := l.retryAfter(key); d != 0 {
			t.Fatalf("failure %d within free attempts blocked for %v", i, d)
		}
	}
	// Each further failure doubles the block: 1s, 2s, 4s, 8s.
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		l.fail(key)
		if d := l.retryAfter(key); d != want {
			t.Fatalf("step %d: retryAfter = %v, want %v", i, d, want)
		}
		clk.Advance(want / 2)
		if d := l.retryAfter(key); d != want/2 {
			t.Fatalf("step %d: retryAfter after half the wait = %v, want %v", i, d, want/2)
		}
		clk.Advance(want / 2)
		if d := l.retryAfter(key); d != 0 {
			t.Fatalf("step %d: still blocked after the wait: %v", i, d)
		}
	}

	l.reset(key)
	l.fail(key)
	if d := l.retryAfter(key); d != 0 {
		t.Fatalf("after reset the first failure must be free, blocked for %v", d)
	}
}

func TestLimiterCapsAtMaxDelay(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(RateLimitConfig{FreeAttempts: 1, BaseDelay: time.Second, MaxDelay: 10 * time.Second}, clk.Now)
	for range 50 {
		l.fail("k")
	}
	if d := l.retryAfter("k"); d != 10*time.Second {
		t.Fatalf("retryAfter = %v, want the 10s cap", d)
	}
}

func TestLimiterForgetsAfterQuietPeriod(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(RateLimitConfig{ForgetAfter: time.Hour}, clk.Now)
	for range DefaultFreeAttempts + 2 {
		l.fail("k")
	}
	if l.retryAfter("k") == 0 {
		t.Fatal("expected a block")
	}
	clk.Advance(time.Hour)
	if d := l.retryAfter("k"); d != 0 {
		t.Fatalf("still blocked after the quiet period: %v", d)
	}
	if n := l.size(); n != 0 {
		t.Fatalf("stale entry kept, size = %d", n)
	}
	l.fail("k")
	if d := l.retryAfter("k"); d != 0 {
		t.Fatalf("counter not reset after the quiet period, blocked %v", d)
	}
}

func TestLimiterBoundedSize(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(RateLimitConfig{MaxEntries: 3}, clk.Now)
	for i := range 10 {
		l.fail(fmt.Sprintf("k%d", i))
		clk.Advance(time.Second)
	}
	if n := l.size(); n != 3 {
		t.Fatalf("size = %d, want 3", n)
	}
	// The most recent keys survive.
	for i := 7; i < 10; i++ {
		if _, ok := l.entries[fmt.Sprintf("k%d", i)]; !ok {
			t.Fatalf("k%d evicted, want the oldest keys evicted first", i)
		}
	}
	// Stale entries are preferred for eviction and pruned on demand.
	clk.Advance(2 * time.Hour)
	l.prune()
	if n := l.size(); n != 0 {
		t.Fatalf("prune left %d entries", n)
	}
}

func TestAddressKey(t *testing.T) {
	cases := map[string]string{
		"192.0.2.1:1234":             "192.0.2.1",
		"[::ffff:192.0.2.1]:80":      "192.0.2.1",
		"[2001:db8:1:2:3:4:5:6]:443": "2001:db8:1:2::/64",
		"[2001:db8:1:2:ffff::1]:443": "2001:db8:1:2::/64",
		"[fe80::1%eth0]:22":          "fe80::/64",
		"192.0.2.9":                  "192.0.2.9",
		"not-an-ip":                  "not-an-ip",
	}
	for in, want := range cases {
		if got := addressKey(in); got != want {
			t.Errorf("addressKey(%q) = %q, want %q", in, got, want)
		}
	}
	if got := clientIP("[2001:db8::1]:443"); got != "2001:db8::1" {
		t.Errorf("clientIP = %q", got)
	}
}

func TestClip(t *testing.T) {
	if got := clip("潮汐", 4); got != "潮" {
		t.Fatalf("clip split a UTF-8 sequence: %q", got)
	}
	if got := clip("abc", 10); got != "abc" {
		t.Fatalf("clip = %q", got)
	}
}
