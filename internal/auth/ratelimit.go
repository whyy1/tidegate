package auth

import (
	"container/list"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// RateLimitConfig controls the failure-based login limiter. After
// FreeAttempts consecutive failures for a key, each further failure blocks
// the key for BaseDelay, then twice as long, and so on up to MaxDelay. A key
// is forgotten once it is not blocked and has had no failure for
// ForgetAfter. A successful login clears the keys it used.
type RateLimitConfig struct {
	FreeAttempts int           // default 3
	BaseDelay    time.Duration // default 1s
	MaxDelay     time.Duration // default 15m
	ForgetAfter  time.Duration // default 1h
	MaxEntries   int           // per key space (usernames, addresses); default 10000
}

// Rate limit defaults.
const (
	DefaultFreeAttempts    = 3
	DefaultBaseDelay       = time.Second
	DefaultMaxDelay        = 15 * time.Minute
	DefaultForgetAfter     = time.Hour
	DefaultMaxLimitEntries = 10000
)

func (c RateLimitConfig) withDefaults() RateLimitConfig {
	if c.FreeAttempts <= 0 {
		c.FreeAttempts = DefaultFreeAttempts
	}
	if c.BaseDelay <= 0 {
		c.BaseDelay = DefaultBaseDelay
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = DefaultMaxDelay
	}
	if c.MaxDelay < c.BaseDelay {
		c.MaxDelay = c.BaseDelay
	}
	if c.ForgetAfter <= 0 {
		c.ForgetAfter = DefaultForgetAfter
	}
	if c.MaxEntries <= 0 {
		c.MaxEntries = DefaultMaxLimitEntries
	}
	return c
}

// limiter tracks failures per key in memory. Entries are kept in a list
// ordered by last failure (front is most recent) so stale entries are found
// at the back and the map never grows past MaxEntries.
type limiter struct {
	mu      sync.Mutex
	cfg     RateLimitConfig
	now     func() time.Time
	entries map[string]*list.Element
	order   *list.List
}

type limitEntry struct {
	key          string
	failures     int
	lastFailure  time.Time
	blockedUntil time.Time
}

func newLimiter(cfg RateLimitConfig, now func() time.Time) *limiter {
	return &limiter{
		cfg:     cfg.withDefaults(),
		now:     now,
		entries: make(map[string]*list.Element),
		order:   list.New(),
	}
}

// retryAfter returns how long key is still blocked, or 0.
func (l *limiter) retryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.entries[key]
	if !ok {
		return 0
	}
	e := el.Value.(*limitEntry)
	now := l.now()
	if l.stale(e, now) {
		l.remove(el)
		return 0
	}
	if now.Before(e.blockedUntil) {
		return e.blockedUntil.Sub(now)
	}
	return 0
}

// fail records a failure for key and extends its block.
func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var e *limitEntry
	if el, ok := l.entries[key]; ok {
		e = el.Value.(*limitEntry)
		if l.stale(e, now) {
			e.failures = 0
		}
		l.order.MoveToFront(el)
	} else {
		if len(l.entries) >= l.cfg.MaxEntries {
			l.evict(now)
		}
		e = &limitEntry{key: key}
		l.entries[key] = l.order.PushFront(e)
	}
	e.failures++
	e.lastFailure = now
	if d := l.delay(e.failures); d > 0 {
		e.blockedUntil = now.Add(d)
	}
}

// reset forgets key.
func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.entries[key]; ok {
		l.remove(el)
	}
}

// prune drops every stale entry.
func (l *limiter) prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dropStale(l.now())
}

func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// delay returns the block duration after the given number of consecutive
// failures.
func (l *limiter) delay(failures int) time.Duration {
	n := failures - l.cfg.FreeAttempts
	if n <= 0 {
		return 0
	}
	d := l.cfg.BaseDelay
	for i := 1; i < n; i++ {
		d *= 2
		if d >= l.cfg.MaxDelay {
			return l.cfg.MaxDelay
		}
	}
	return min(d, l.cfg.MaxDelay)
}

func (l *limiter) stale(e *limitEntry, now time.Time) bool {
	return !now.Before(e.blockedUntil) && now.Sub(e.lastFailure) >= l.cfg.ForgetAfter
}

func (l *limiter) remove(el *list.Element) {
	delete(l.entries, el.Value.(*limitEntry).key)
	l.order.Remove(el)
}

// dropStale removes stale entries from the back of the list. Entries are
// ordered by last failure, so it stops at the first live entry it meets
// whose last failure is recent enough.
func (l *limiter) dropStale(now time.Time) {
	for el := l.order.Back(); el != nil; {
		prev := el.Prev()
		e := el.Value.(*limitEntry)
		if l.stale(e, now) {
			l.remove(el)
		} else if now.Sub(e.lastFailure) < l.cfg.ForgetAfter {
			break
		}
		el = prev
	}
}

// evict makes room for one entry: stale entries first, then the entry with
// the oldest failure.
func (l *limiter) evict(now time.Time) {
	l.dropStale(now)
	for len(l.entries) >= l.cfg.MaxEntries {
		back := l.order.Back()
		if back == nil {
			return
		}
		l.remove(back)
	}
}

// maxKeyBytes bounds the memory used by one limiter key.
const maxKeyBytes = 128

// usernameKey folds a login name into a limiter key.
func usernameKey(username string) string {
	return clip(strings.ToLower(strings.TrimSpace(username)), maxKeyBytes)
}

// addressKey folds a remote address into a limiter key. IPv6 clients are
// grouped by /64 because a single host usually controls a whole /64.
func addressKey(remoteAddr string) string {
	host := hostOnly(remoteAddr)
	a, err := netip.ParseAddr(host)
	if err != nil {
		return clip(host, maxKeyBytes)
	}
	a = a.Unmap().WithZone("")
	if a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}

// clientIP returns the IP part of a remote address for display and storage.
func clientIP(remoteAddr string) string {
	host := hostOnly(remoteAddr)
	if a, err := netip.ParseAddr(host); err == nil {
		return a.Unmap().WithZone("").String()
	}
	return clip(host, 64)
}

func hostOnly(remoteAddr string) string {
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return h
	}
	return strings.Trim(remoteAddr, "[]")
}

// clip truncates s to at most n bytes without splitting a UTF-8 sequence.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && cut < len(s) && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}
