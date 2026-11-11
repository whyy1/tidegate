package engine

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"tidegate/internal/scenario"
)

// countConn wraps a TCP connection and counts the bytes actually read and
// written on the socket. It sits below TLS, so it sees encrypted bytes
// including handshakes. Counters are atomic: net/http reads and writes on
// its own goroutines while the virtual user attributes bytes.
type countConn struct {
	net.Conn
	eng      *Engine
	sent     atomic.Int64
	recv     atomic.Int64
	markSent atomic.Int64 // bytes already attributed
	markRecv atomic.Int64
	closed   atomic.Bool

	// Set once when dialing; read by the connection's user.
	dns, connect, tlsDur time.Duration
	tls                  bool
	// requests counts requests sent on this connection (owner only).
	requests int
}

func (c *countConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.recv.Add(int64(n))
	}
	return n, err
}

func (c *countConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.sent.Add(int64(n))
	}
	return n, err
}

func takeDelta(counter, mark *atomic.Int64) int64 {
	for {
		m, v := mark.Load(), counter.Load()
		if v <= m {
			return 0
		}
		if mark.CompareAndSwap(m, v) {
			return v - m
		}
	}
}

// take returns the bytes moved since the previous take. Each byte is
// returned exactly once even if take races with Close.
func (c *countConn) take() (sent, recv int64) {
	return takeDelta(&c.sent, &c.markSent), takeDelta(&c.recv, &c.markRecv)
}

// Close closes the connection once and books unattributed bytes (for
// example a TLS close_notify) as overhead.
func (c *countConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	if s, r := c.take(); s != 0 || r != 0 {
		c.eng.lateOverheadSent.Add(s)
		c.eng.lateOverheadRecv.Add(r)
	}
	c.eng.activeConns.Add(-1)
	return c.Conn.Close()
}

// dnsEntry is a resolved name. Readers wait on ready.
type dnsEntry struct {
	ready chan struct{}
	addrs []string // "ip:port", preferred family first
	alt   []string // other address family, used only when all preferred fail
	err   error
	at    time.Time
	next  atomic.Uint32
}

// dnsCache resolves each host once (or every refresh interval) and hands
// out its addresses round robin. Lookups are lock free after the first.
type dnsCache struct {
	refresh time.Duration
	mu      sync.Mutex
	entries atomic.Pointer[map[string]*dnsEntry]
}

func newDNSCache(refresh time.Duration) *dnsCache {
	c := &dnsCache{refresh: refresh}
	m := map[string]*dnsEntry{}
	c.entries.Store(&m)
	return c
}

const dnsErrorTTL = time.Second

func (c *dnsCache) fresh(e *dnsEntry) bool {
	select {
	case <-e.ready:
	default:
		return true // resolution in progress
	}
	age := time.Since(e.at)
	if e.err != nil {
		return age < dnsErrorTTL
	}
	return c.refresh <= 0 || age < c.refresh
}

// lookup returns the entry for o, resolving it if needed. resolved is true
// when this call performed the lookup.
func (c *dnsCache) lookup(ctx context.Context, o *scenario.Origin) (e *dnsEntry, resolved bool, err error) {
	if e = (*c.entries.Load())[o.Addr]; e == nil || !c.fresh(e) {
		c.mu.Lock()
		cur := *c.entries.Load()
		if e = cur[o.Addr]; e == nil || !c.fresh(e) {
			e = &dnsEntry{ready: make(chan struct{})}
			next := make(map[string]*dnsEntry, len(cur)+1)
			for k, v := range cur {
				next[k] = v
			}
			next[o.Addr] = e
			c.entries.Store(&next)
			resolved = true
		}
		c.mu.Unlock()
		if resolved {
			resolved = c.resolve(ctx, o, e)
		}
	}
	select {
	case <-e.ready:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	return e, resolved, e.err
}

// resolve fills e. It reports whether a name lookup happened (false for
// IP literals).
func (c *dnsCache) resolve(ctx context.Context, o *scenario.Origin, e *dnsEntry) bool {
	defer func() { e.at = time.Now(); close(e.ready) }()
	if ip, err := netip.ParseAddr(o.HostName); err == nil {
		e.addrs = []string{netip.AddrPortFrom(ip, mustPort(o.Port)).String()}
		return false
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", o.HostName)
	if err != nil {
		e.err = err
		return true
	}
	// Prefer IPv4: "localhost" resolves to ::1 first on Windows while
	// most test targets listen on IPv4 only, and a refused connect to a
	// closed loopback port takes about two seconds there.
	for _, ip := range ips {
		ap := netip.AddrPortFrom(ip.Unmap(), mustPort(o.Port)).String()
		if ip.Unmap().Is4() {
			e.addrs = append(e.addrs, ap)
		} else {
			e.alt = append(e.alt, ap)
		}
	}
	if len(e.addrs) == 0 {
		e.addrs, e.alt = e.alt, nil
	}
	if len(e.addrs) == 0 {
		e.err = &net.DNSError{Err: "no addresses", Name: o.HostName, IsNotFound: true}
	}
	return true
}

func mustPort(p string) uint16 {
	var n uint16
	for i := 0; i < len(p); i++ {
		n = n*10 + uint16(p[i]-'0')
	}
	return n
}

// dialTiming receives the duration of the connection setup phases.
type dialTiming struct {
	dns, connect, tls time.Duration
}

// dialer opens counted connections, optionally with TLS on top.
type dialer struct {
	eng        *Engine
	nd         net.Dialer
	dns        *dnsCache
	tlsBase    *tls.Config
	tlsTimeout time.Duration
	tlsByHost  sync.Map // host name -> *tls.Config
}

func newDialer(e *Engine, cfg *scenario.HTTPConfig) *dialer {
	return &dialer{
		eng: e,
		nd:  net.Dialer{Timeout: cfg.ConnectTimeout, KeepAlive: 30 * time.Second},
		dns: newDNSCache(cfg.DNSRefresh),
		tlsBase: &tls.Config{
			InsecureSkipVerify: cfg.InsecureSkipVerify,
			NextProtos:         []string{"http/1.1"}, // never negotiate HTTP/2
			ClientSessionCache: tls.NewLRUClientSessionCache(1024),
		},
		tlsTimeout: cfg.TLSTimeout,
	}
}

func (d *dialer) tlsConfig(host string) *tls.Config {
	if c, ok := d.tlsByHost.Load(host); ok {
		return c.(*tls.Config)
	}
	c := d.tlsBase.Clone()
	c.ServerName = host
	c.ClientSessionCache = d.tlsBase.ClientSessionCache
	actual, _ := d.tlsByHost.LoadOrStore(host, c)
	return actual.(*tls.Config)
}

// dialTCP resolves (from cache) and connects, trying the next address on
// failure. The returned connection is counted.
func (d *dialer) dialTCP(ctx context.Context, o *scenario.Origin, t *dialTiming) (*countConn, error) {
	t0 := nanotime()
	e, resolved, err := d.dns.lookup(ctx, o)
	if resolved {
		t.dns = time.Duration(nanotime() - t0)
	}
	if err != nil {
		return nil, &hopError{phase: phaseDNS, err: err}
	}
	t1 := nanotime()
	n := len(e.addrs)
	start := int(e.next.Add(1) % uint32(n))
	var c net.Conn
	for i := 0; i < n+len(e.alt); i++ {
		addr := ""
		if i < n {
			addr = e.addrs[(start+i)%n]
		} else {
			addr = e.alt[i-n]
		}
		c, err = d.nd.DialContext(ctx, "tcp", addr)
		if err == nil || ctx.Err() != nil || isTimeout(err) {
			break // a timeout is not retried: it would multiply the connect timeout
		}
	}
	t.connect = time.Duration(nanotime() - t1)
	if err != nil {
		return nil, &hopError{phase: phaseDial, err: err}
	}
	d.eng.activeConns.Add(1)
	cc := &countConn{Conn: c, eng: d.eng, dns: t.dns, connect: t.connect}
	return cc, nil
}

// handshake runs a TLS client handshake over cc with the TLS timeout.
func (d *dialer) handshake(ctx context.Context, cc *countConn, o *scenario.Origin, t *dialTiming) (*tls.Conn, error) {
	t0 := nanotime()
	tc := tls.Client(cc, d.tlsConfig(o.HostName))
	if d.tlsTimeout > 0 {
		cc.SetDeadline(time.Now().Add(d.tlsTimeout))
	}
	stop := context.AfterFunc(ctx, func() { cc.SetDeadline(time.Unix(1, 0)) })
	err := tc.Handshake()
	stop()
	t.tls = time.Duration(nanotime() - t0)
	cc.tlsDur, cc.tls = t.tls, true
	if err != nil {
		cc.Close()
		if isTimeout(err) && ctx.Err() == nil {
			err = errTLSHandshakeTimeout
		}
		return nil, &hopError{phase: phaseTLS, err: err}
	}
	cc.SetDeadline(time.Time{})
	return tc, nil
}

// dial opens a connection to o, with TLS for https.
func (d *dialer) dial(ctx context.Context, o *scenario.Origin, t *dialTiming) (*countConn, net.Conn, error) {
	cc, err := d.dialTCP(ctx, o, t)
	if err != nil {
		return nil, nil, err
	}
	if !o.TLS {
		return cc, cc, nil
	}
	tc, err := d.handshake(ctx, cc, o, t)
	if err != nil {
		return nil, nil, err
	}
	return cc, tc, nil
}
