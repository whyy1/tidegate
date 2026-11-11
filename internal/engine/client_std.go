package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"tidegate/internal/scenario"
)

// stdClient executes requests with net/http's Transport, tuned for load
// generation: HTTP/1.1 only, no proxy, no transparent decompression,
// counted connections, one Transport per virtual user (per_vu) or a few
// sharded Transports (shared).
type stdClient struct {
	eng     *Engine
	cfg     *scenario.HTTPConfig
	dial    *dialer
	shared  []*http.Transport
	origins sync.Map // "scheme://addr" -> *scenario.Origin
}

func newStdClient(e *Engine, d *dialer) *stdClient {
	c := &stdClient{eng: e, cfg: &e.plan.HTTP, dial: d}
	if c.cfg.ConnMode == scenario.ConnShared {
		for i := 0; i < e.shards; i++ {
			c.shared = append(c.shared, c.newTransport(1<<16))
		}
	}
	return c
}

func (c *stdClient) origin(scheme, addr string) (*scenario.Origin, error) {
	key := scheme + "://" + addr
	if o, ok := c.origins.Load(key); ok {
		return o.(*scenario.Origin), nil
	}
	o, err := scenario.NewOrigin(scheme, addr)
	if err != nil {
		return nil, err
	}
	c.origins.Store(key, o)
	return o, nil
}

func (c *stdClient) newTransport(maxIdle int) *http.Transport {
	return &http.Transport{
		Proxy: nil,
		// Dials use the engine's hard-stop context: net/http may finish a
		// dial in the background after the request that started it gave up.
		DialContext: func(_ context.Context, _, addr string) (net.Conn, error) {
			o, err := c.origin("http", addr)
			if err != nil {
				return nil, err
			}
			var t dialTiming
			return c.dial.dialTCP(c.eng.hardCtx, o, &t)
		},
		DialTLSContext: func(_ context.Context, _, addr string) (net.Conn, error) {
			o, err := c.origin("https", addr)
			if err != nil {
				return nil, err
			}
			var t dialTiming
			cc, err := c.dial.dialTCP(c.eng.hardCtx, o, &t)
			if err != nil {
				return nil, err
			}
			tc, err := c.dial.handshake(c.eng.hardCtx, cc, o, &t)
			if err != nil {
				return nil, err
			}
			return tc, nil
		},
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{}, // no HTTP/2
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          0,
		MaxIdleConnsPerHost:   maxIdle,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    true, // Accept-Encoding is added by the engine
		DisableKeepAlives:     !c.cfg.KeepAlive,
		ResponseHeaderTimeout: c.cfg.FirstByteTimeout,
		ExpectContinueTimeout: 0,
	}
}

func (c *stdClient) newVU(v *vu) vuClient {
	s := &stdVU{c: c, hdr: http.Header{}}
	if c.shared != nil {
		s.tr = c.shared[v.id%len(c.shared)]
	} else {
		s.tr = c.newTransport(4)
		s.own = true
	}
	s.trace = &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			s.gotConnAt = nanotime()
			s.reused = info.Reused
			s.conn = connOf(info.Conn)
			if s.conn == nil {
				return
			}
			// Bytes moved before this request (TLS handshake, leftovers)
			// are overhead; the request is written after GotConn returns.
			s.preSent, s.preRecv = s.conn.take()
			s.conn.requests++
			if m := c.cfg.MaxRequestsPerConn; m > 0 && s.conn.requests >= m && s.cur != nil {
				s.cur.Close = true
			}
		},
		GotFirstResponseByte: func() { s.firstByte = nanotime() },
	}
	s.baseCtx = httptrace.WithClientTrace(c.eng.hardCtx, s.trace)
	return s
}

func (c *stdClient) close() {
	for _, t := range c.shared {
		t.CloseIdleConnections()
	}
}

func connOf(nc net.Conn) *countConn {
	switch c := nc.(type) {
	case *countConn:
		return c
	case *tls.Conn:
		if cc, ok := c.NetConn().(*countConn); ok {
			return cc
		}
	}
	return nil
}

type stdVU struct {
	c       *stdClient
	tr      *http.Transport
	own     bool
	hdr     http.Header
	body    bytes.Reader
	req     http.Request
	cur     *http.Request
	trace   *httptrace.ClientTrace
	baseCtx context.Context
	aborted atomic.Bool

	// Set by trace callbacks during RoundTrip (same goroutine, or ordered
	// before RoundTrip returns).
	conn      *countConn
	reused    bool
	gotConnAt int64
	firstByte int64
	preSent   int64
	preRecv   int64
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func (s *stdVU) closeAll() {
	if s.own {
		s.tr.CloseIdleConnections()
	}
}

// abort relies on the engine's hard-stop context, which every request
// context derives from; it only drops idle connections here.
func (s *stdVU) abort() {
	s.aborted.Store(true)
	if s.own {
		s.tr.CloseIdleConnections()
	}
}

func (s *stdVU) do(q *hopRequest, r *hopResponse) error {
	cfg := s.c.cfg
	req := &s.req
	*req = http.Request{Method: q.method, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: s.hdr}
	if q.url != nil {
		req.URL = q.url
	} else {
		req.URL = &url.URL{Scheme: q.origin.Scheme, Host: q.origin.Host, Opaque: string(q.uri)}
	}
	req.Host = q.origin.Host
	clear(s.hdr)
	for i := range q.headers {
		h := &q.headers[i]
		if h.canon == "Host" {
			req.Host = string(h.value)
			continue
		}
		if h.static != nil {
			s.hdr[h.canon] = h.static
		} else {
			s.hdr[h.canon] = []string{string(h.value)}
		}
	}
	if q.userAgent {
		s.hdr["User-Agent"] = userAgentValue
	}
	if q.gzip {
		s.hdr["Accept-Encoding"] = gzipValue
	}
	if q.contentType != "" && len(q.body) > 0 {
		s.hdr["Content-Type"] = []string{q.contentType}
	}
	if len(q.cookie) > 0 {
		s.hdr["Cookie"] = []string{string(q.cookie)}
	}
	if len(q.body) > 0 {
		s.body.Reset(q.body)
		req.Body = nopCloser{&s.body}
		req.ContentLength = int64(len(q.body))
	}
	req.Close = q.close

	ctx, cancel := context.WithTimeout(s.baseCtx, cfg.TotalTimeout)
	defer cancel()
	cur := req.WithContext(ctx)
	s.cur = cur
	s.conn, s.reused, s.firstByte, s.gotConnAt, s.preSent, s.preRecv = nil, false, 0, 0, 0, 0

	r.start = nanotime()
	res, err := s.tr.RoundTrip(cur)
	s.cur = nil
	if s.conn != nil && !s.reused {
		r.newConn = true
		r.dial = dialTiming{dns: s.conn.dns, connect: s.conn.connect, tls: s.conn.tlsDur}
	}
	r.overSent, r.overRecv = s.preSent, s.preRecv
	r.writeStart = s.gotConnAt
	if err != nil {
		r.end = nanotime()
		if s.conn != nil {
			r.sent, r.recv = s.conn.take()
		}
		var he *hopError
		if errors.As(err, &he) {
			return err // DNS, dial or TLS error from our dialer
		}
		if s.conn == nil {
			return &hopError{phase: phaseDial, err: err}
		}
		return &hopError{phase: phaseRead, err: err, noResponse: s.firstByte == 0}
	}
	r.firstByte = s.firstByte
	if r.firstByte == 0 {
		r.firstByte = nanotime()
	}
	r.status = res.StatusCode
	r.stdHeader = res.Header
	r.connClose = res.Close
	r.gzip = res.Header.Get("Content-Encoding") == "gzip"

	keep, limit := q.keepBody, q.bodyLimit
	if !keep && r.status >= 400 && q.errPrefix > 0 {
		keep, limit = true, int64(q.errPrefix)
	}
	err = readBody(res.Body, keep, limit, r)
	res.Body.Close()
	r.end = nanotime()
	if s.conn != nil {
		r.sent, r.recv = s.conn.take()
	}
	if err != nil {
		return &hopError{phase: phaseRead, err: err}
	}
	return nil
}

var (
	userAgentValue = []string{UserAgent}
	gzipValue      = []string{"gzip"}
)

func readBody(body io.Reader, keep bool, limit int64, r *hopResponse) error {
	if keep {
		for limit == 0 || int64(len(r.body)) < limit {
			want := 4096
			if limit > 0 {
				want = int(min(int64(want), limit-int64(len(r.body))))
			}
			old := len(r.body)
			r.body = growLen(r.body, want)
			n, err := body.Read(r.body[old:])
			r.body = r.body[:old+n]
			r.bodyLen += int64(n)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	n, err := io.Copy(io.Discard, body)
	r.bodyLen += n
	if keep && n > 0 {
		r.truncated = true
	}
	return err
}
