package engine

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"tidegate/internal/scenario"
)

// fastClient is a minimal HTTP/1.1 client written for load generation.
// The virtual user's own goroutine writes the request and parses the
// response: no helper goroutines, no channels, no per-request allocations.
// It supports keep-alive, Content-Length, chunked and close-delimited
// bodies, TLS, the four timeouts and both connection modes.
type fastClient struct {
	eng  *Engine
	cfg  *scenario.HTTPConfig
	dial *dialer
	pool *connPool // shared connection mode only
}

func newFastClient(e *Engine, d *dialer) *fastClient {
	c := &fastClient{eng: e, cfg: &e.plan.HTTP, dial: d}
	if c.cfg.ConnMode == scenario.ConnShared {
		c.pool = newConnPool(e.shards)
	}
	return c
}

func (c *fastClient) newVU(v *vu) vuClient { return &fastVU{c: c, shard: v.id} }

func (c *fastClient) close() {
	if c.pool != nil {
		c.pool.closeAll()
	}
}

type fastConn struct {
	cc   *countConn
	nc   net.Conn // cc, or a TLS connection on top of it
	br   *bufio.Reader
	addr string
	tls  bool
}

func (fc *fastConn) matches(o *scenario.Origin) bool { return fc.addr == o.Addr && fc.tls == o.TLS }

type fastVU struct {
	c       *fastClient
	shard   int
	mu      sync.Mutex  // guards conns against abort
	conns   []*fastConn // per_vu: owned connections; shared: the one in use
	wbuf    []byte
	aborted atomic.Bool
}

const readBufSize = 4096

// acquire returns a connection to o, dialing if needed.
func (f *fastVU) acquire(o *scenario.Origin, r *hopResponse) (*fastConn, error) {
	if f.c.pool == nil {
		for _, fc := range f.conns {
			if fc.matches(o) {
				return fc, nil
			}
		}
	} else if fc := f.c.pool.get(f.shard, o); fc != nil {
		f.track(fc)
		return fc, nil
	}
	cc, nc, err := f.c.dial.dial(f.c.eng.hardCtx, o, &r.dial)
	if err != nil {
		return nil, err
	}
	r.newConn = true
	fc := &fastConn{cc: cc, nc: nc, br: bufio.NewReaderSize(nc, readBufSize), addr: o.Addr, tls: o.TLS}
	f.track(fc)
	if f.aborted.Load() {
		nc.Close()
		return nil, &hopError{phase: phaseDial, err: errAborted}
	}
	return fc, nil
}

var errAborted = errors.New("aborted")

func (f *fastVU) track(fc *fastConn) {
	f.mu.Lock()
	f.conns = append(f.conns, fc)
	f.mu.Unlock()
}

func (f *fastVU) untrack(fc *fastConn) {
	f.mu.Lock()
	for i, x := range f.conns {
		if x == fc {
			last := len(f.conns) - 1
			f.conns[i] = f.conns[last]
			f.conns[last] = nil
			f.conns = f.conns[:last]
			break
		}
	}
	f.mu.Unlock()
}

// release keeps a healthy connection for the next request.
func (f *fastVU) release(fc *fastConn) {
	if f.c.pool != nil {
		f.untrack(fc)
		f.c.pool.put(f.shard, fc)
	}
}

// discard closes a connection, booking its unattributed bytes as overhead.
func (f *fastVU) discard(fc *fastConn, r *hopResponse) {
	f.untrack(fc)
	s, rv := fc.cc.take()
	r.overSent += s
	r.overRecv += rv
	fc.nc.Close()
	fc.cc.Close()
}

func (f *fastVU) closeAll() {
	f.mu.Lock()
	conns := f.conns
	f.conns = nil
	f.mu.Unlock()
	for _, fc := range conns {
		fc.nc.Close()
		fc.cc.Close()
	}
}

// abort makes in-flight IO fail immediately. Safe to call concurrently.
func (f *fastVU) abort() {
	f.aborted.Store(true)
	past := time.Unix(1, 0)
	f.mu.Lock()
	for _, fc := range f.conns {
		fc.cc.SetDeadline(past)
	}
	f.mu.Unlock()
}

func (f *fastVU) do(q *hopRequest, r *hopResponse) error {
	cfg := f.c.cfg
	r.start = nanotime()
	for attempt := 0; ; attempt++ {
		fc, err := f.acquire(q.origin, r)
		if err != nil {
			r.end = nanotime()
			return err
		}
		reused := fc.cc.requests > 0
		s, rv := fc.cc.take() // bytes since the previous request: handshake, retries
		r.overSent += s
		r.overRecv += rv

		fc.cc.requests++
		closeAfter := q.close || !cfg.KeepAlive || (cfg.MaxRequestsPerConn > 0 && fc.cc.requests >= cfg.MaxRequestsPerConn)
		f.wbuf = appendRequest(f.wbuf[:0], q, closeAfter && !q.close)

		now := time.Now()
		fc.cc.SetDeadline(now.Add(cfg.FirstByteTimeout))
		if f.aborted.Load() {
			f.discard(fc, r)
			r.end = nanotime()
			return &hopError{phase: phaseWrite, err: errAborted}
		}
		r.writeStart = nanotime()
		if _, err = fc.nc.Write(f.wbuf); err == nil {
			err = f.readResponse(fc, q, r, now)
		}
		if err != nil {
			f.discard(fc, r)
			var he *hopError
			retry := reused && attempt == 0 && !f.aborted.Load()
			if errors.As(err, &he) {
				retry = retry && he.phase == phaseRead && he.noResponse && !isTimeout(he.err)
			} else {
				err = &hopError{phase: phaseWrite, err: err}
				retry = retry && !isTimeout(err)
			}
			if retry {
				// The server most likely closed an idle keep-alive
				// connection; it never saw this request.
				continue
			}
			r.end = nanotime()
			return err
		}
		r.end = nanotime()
		r.sent, r.recv = fc.cc.take()
		if closeAfter || r.connClose {
			r.connClose = true
			f.discard(fc, r)
		} else {
			f.release(fc)
		}
		return nil
	}
}

// UserAgent is sent when a step sets no User-Agent header.
var UserAgent = "Tidegate/0.1"

func appendRequest(b []byte, q *hopRequest, addClose bool) []byte {
	b = append(b, q.method...)
	b = append(b, ' ')
	if len(q.uri) == 0 {
		b = append(b, '/')
	} else {
		b = append(b, q.uri...)
	}
	b = append(b, " HTTP/1.1\r\n"...)
	userHost := false
	for i := range q.headers {
		h := &q.headers[i]
		if h.canon == "Host" {
			userHost = true
		}
		b = append(b, h.name...)
		b = append(b, ": "...)
		b = append(b, h.value...)
		b = append(b, "\r\n"...)
	}
	if !userHost {
		b = append(b, "Host: "...)
		b = append(b, q.origin.Host...)
		b = append(b, "\r\n"...)
	}
	if q.userAgent {
		b = append(b, "User-Agent: "...)
		b = append(b, UserAgent...)
		b = append(b, "\r\n"...)
	}
	if q.gzip {
		b = append(b, "Accept-Encoding: gzip\r\n"...)
	}
	if q.contentType != "" && len(q.body) > 0 {
		b = append(b, "Content-Type: "...)
		b = append(b, q.contentType...)
		b = append(b, "\r\n"...)
	}
	if len(q.cookie) > 0 {
		b = append(b, "Cookie: "...)
		b = append(b, q.cookie...)
		b = append(b, "\r\n"...)
	}
	if len(q.body) > 0 || q.method == "POST" || q.method == "PUT" || q.method == "PATCH" {
		b = append(b, "Content-Length: "...)
		b = strconv.AppendInt(b, int64(len(q.body)), 10)
		b = append(b, "\r\n"...)
	}
	if q.close || addClose {
		b = append(b, "Connection: close\r\n"...)
	}
	b = append(b, "\r\n"...)
	return append(b, q.body...)
}

const maxResponseHeader = 1 << 20

var errHeaderTooLarge = errors.New("response header larger than 1 MB")

// readLineInto appends one line without its line ending to r.arena and
// returns its start offset.
func readLineInto(br *bufio.Reader, r *hopResponse) (start int, err error) {
	start = len(r.arena)
	for {
		frag, err := br.ReadSlice('\n')
		r.arena = append(r.arena, frag...)
		if err == nil {
			break
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return start, err
		}
		if len(r.arena) > maxResponseHeader {
			return start, errHeaderTooLarge
		}
	}
	end := len(r.arena) - 1
	if end > start && r.arena[end-1] == '\r' {
		end--
	}
	r.arena = r.arena[:end]
	return start, nil
}

func trimOWS(b []byte, lo, hi int) (int, int) {
	for lo < hi && (b[lo] == ' ' || b[lo] == '\t') {
		lo++
	}
	for hi > lo && (b[hi-1] == ' ' || b[hi-1] == '\t') {
		hi--
	}
	return lo, hi
}

func (f *fastVU) readResponse(fc *fastConn, q *hopRequest, r *hopResponse, sent time.Time) error {
	br := fc.br
	if _, err := br.Peek(1); err != nil {
		return &hopError{phase: phaseRead, err: err, noResponse: true}
	}
	r.firstByte = nanotime()
	cfg := f.c.cfg
	if cfg.TotalTimeout > cfg.FirstByteTimeout {
		fc.cc.SetReadDeadline(sent.Add(cfg.TotalTimeout))
		if f.aborted.Load() {
			return &hopError{phase: phaseRead, err: errAborted}
		}
	}
	var (
		contentLength int64 = -1
		chunked       bool
		http10        bool
	)
	for {
		r.arena, r.raw = r.arena[:0], r.raw[:0]
		start, err := readLineInto(br, r)
		if err != nil {
			return &hopError{phase: phaseRead, err: err}
		}
		line := r.arena[start:]
		if len(line) < 12 || !bytes.HasPrefix(line, []byte("HTTP/1.")) || line[8] != ' ' {
			return &hopError{phase: phaseRead, err: fmt.Errorf("malformed status line %q", truncateBytes(line, 64))}
		}
		http10 = line[7] == '0'
		code := 0
		for _, c := range line[9:12] {
			if c < '0' || c > '9' {
				return &hopError{phase: phaseRead, err: fmt.Errorf("malformed status code in %q", truncateBytes(line, 64))}
			}
			code = code*10 + int(c-'0')
		}
		r.status = code
		r.arena = r.arena[:0] // the status line is not kept
		r.connClose = http10
		contentLength, chunked = -1, false
		for {
			start, err := readLineInto(br, r)
			if err != nil {
				return &hopError{phase: phaseRead, err: err}
			}
			if len(r.arena) == start {
				break
			}
			if len(r.arena) > maxResponseHeader {
				return &hopError{phase: phaseRead, err: errHeaderTooLarge}
			}
			line := r.arena[start:]
			colon := bytes.IndexByte(line, ':')
			if colon <= 0 {
				continue // tolerate junk lines
			}
			nlo, nhi := trimOWS(r.arena, start, start+colon)
			vlo, vhi := trimOWS(r.arena, start+colon+1, len(r.arena))
			h := rawHeader{name: [2]int32{int32(nlo), int32(nhi)}, value: [2]int32{int32(vlo), int32(vhi)}}
			r.raw = append(r.raw, h)
			name, val := r.arena[nlo:nhi], r.arena[vlo:vhi]
			switch len(name) {
			case 14:
				if equalFoldBytes(name, "content-length") {
					n, err := strconv.ParseInt(string(val), 10, 64)
					if err != nil || n < 0 {
						return &hopError{phase: phaseRead, err: fmt.Errorf("invalid Content-Length %q", val)}
					}
					contentLength = n
				}
			case 17:
				if equalFoldBytes(name, "transfer-encoding") {
					chunked = bytes.Contains(bytes.ToLower(val), []byte("chunked"))
				}
			case 10:
				if equalFoldBytes(name, "connection") {
					if equalFoldBytes(val, "close") {
						r.connClose = true
					} else if equalFoldBytes(val, "keep-alive") {
						r.connClose = false
					}
				}
			case 16:
				if equalFoldBytes(name, "content-encoding") {
					r.gzip = equalFoldBytes(val, "gzip")
				}
			}
		}
		if r.status >= 100 && r.status < 200 && r.status != 101 {
			continue // interim response
		}
		break
	}

	keep := q.keepBody
	limit := q.bodyLimit
	if !keep && r.status >= 400 && q.errPrefix > 0 {
		keep, limit = true, int64(q.errPrefix)
	}
	var err error
	switch {
	case q.head || r.status == 204 || r.status == 304 || r.status < 200:
	case chunked:
		err = f.readChunked(fc, keep, limit, r)
	case contentLength >= 0:
		err = f.consume(fc, contentLength, keep, limit, r)
	default:
		r.connClose = true
		err = f.consume(fc, -1, keep, limit, r)
	}
	if err != nil {
		return &hopError{phase: phaseRead, err: err}
	}
	return nil
}

func truncateBytes(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

var scratchPool = sync.Pool{New: func() any { b := make([]byte, 64<<10); return &b }}

// consume reads n body bytes (n < 0: until EOF), keeping up to limit of
// them in r.body when keep is set (limit 0 = no limit).
func (f *fastVU) consume(fc *fastConn, n int64, keep bool, limit int64, r *hopResponse) error {
	br := fc.br
	untilEOF := n < 0
	for untilEOF || n > 0 {
		if keep && (limit == 0 || int64(len(r.body)) < limit) {
			want := int64(readBufSize)
			if !untilEOF {
				want = n
			}
			if limit > 0 {
				want = min(want, limit-int64(len(r.body)))
			}
			old := len(r.body)
			r.body = growLen(r.body, int(want))
			got, err := io.ReadFull(br, r.body[old:])
			r.body = r.body[:old+got]
			r.bodyLen += int64(got)
			if !untilEOF {
				n -= int64(got)
			}
			if err != nil {
				if untilEOF && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
					return nil
				}
				return eofIsUnexpected(err)
			}
			continue
		}
		if keep {
			r.truncated = true
		}
		// Discard: drain the read buffer, then read big chunks straight
		// from the connection so large bodies need few system calls.
		if b := br.Buffered(); b > 0 {
			k := b
			if !untilEOF && int64(k) > n {
				k = int(n)
			}
			br.Discard(k)
			r.bodyLen += int64(k)
			if !untilEOF {
				n -= int64(k)
			}
			continue
		}
		if !untilEOF && n <= readBufSize {
			k, err := br.Discard(int(n))
			r.bodyLen += int64(k)
			n -= int64(k)
			if err != nil {
				return eofIsUnexpected(err)
			}
			continue
		}
		bp := scratchPool.Get().(*[]byte)
		buf := *bp
		for untilEOF || n > 0 {
			chunk := buf
			if !untilEOF && int64(len(chunk)) > n {
				chunk = chunk[:n]
			}
			k, err := fc.nc.Read(chunk)
			r.bodyLen += int64(k)
			if !untilEOF {
				n -= int64(k)
			}
			if err != nil {
				scratchPool.Put(bp)
				if untilEOF && errors.Is(err, io.EOF) {
					return nil
				}
				return eofIsUnexpected(err)
			}
		}
		scratchPool.Put(bp)
	}
	return nil
}

func eofIsUnexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func growLen(b []byte, n int) []byte {
	if cap(b)-len(b) < n {
		nb := make([]byte, len(b), max(2*cap(b), len(b)+n))
		copy(nb, b)
		b = nb
	}
	return b[:len(b)+n]
}

func (f *fastVU) readChunked(fc *fastConn, keep bool, limit int64, r *hopResponse) error {
	br := fc.br
	for {
		line, err := br.ReadSlice('\n')
		if err != nil {
			return eofIsUnexpected(err)
		}
		if i := bytes.IndexByte(line, ';'); i >= 0 {
			line = line[:i]
		}
		size, err := strconv.ParseInt(string(bytes.TrimSpace(line)), 16, 64)
		if err != nil || size < 0 {
			return fmt.Errorf("invalid chunk size %q", bytes.TrimSpace(line))
		}
		if size == 0 {
			for { // trailers
				line, err := br.ReadSlice('\n')
				if err != nil {
					return eofIsUnexpected(err)
				}
				if len(bytes.TrimSpace(line)) == 0 {
					return nil
				}
			}
		}
		if err := f.consume(fc, size, keep, limit, r); err != nil {
			return err
		}
		if line, err := br.ReadSlice('\n'); err != nil {
			return eofIsUnexpected(err)
		} else if len(bytes.TrimSpace(line)) != 0 {
			return errors.New("missing CRLF after chunk")
		}
	}
}

// connPool shares idle connections between virtual users (conn_mode:
// shared). It is split into shards so that no single lock is hot; a
// virtual user looks in its own shard first and then steals from two
// neighbours before dialing.
type connPool struct {
	shards []poolShard
	closed atomic.Bool
}

type poolShard struct {
	mu   sync.Mutex
	idle []*fastConn
	_    [40]byte
}

func newConnPool(n int) *connPool { return &connPool{shards: make([]poolShard, max(n, 1))} }

func (p *connPool) get(shard int, o *scenario.Origin) *fastConn {
	n := len(p.shards)
	for i := 0; i < min(3, n); i++ {
		s := &p.shards[(shard+i)%n]
		s.mu.Lock()
		for j := len(s.idle) - 1; j >= 0; j-- {
			if fc := s.idle[j]; fc.matches(o) {
				s.idle = append(s.idle[:j], s.idle[j+1:]...)
				s.mu.Unlock()
				return fc
			}
		}
		s.mu.Unlock()
	}
	return nil
}

func (p *connPool) put(shard int, fc *fastConn) {
	if p.closed.Load() {
		fc.nc.Close()
		fc.cc.Close()
		return
	}
	s := &p.shards[shard%len(p.shards)]
	s.mu.Lock()
	s.idle = append(s.idle, fc)
	s.mu.Unlock()
}

func (p *connPool) closeAll() {
	p.closed.Store(true)
	for i := range p.shards {
		s := &p.shards[i]
		s.mu.Lock()
		idle := s.idle
		s.idle = nil
		s.mu.Unlock()
		for _, fc := range idle {
			fc.nc.Close()
			fc.cc.Close()
		}
	}
}
