// Package testserver is a small, fast HTTP/1.1 server used as the target
// of benchmarks and self tests. It implements just enough of HTTP/1.1
// (keep-alive, pipelining, Content-Length and chunked request bodies) to
// stay out of the way of the load generator.
//
// Routes:
//
//	/ping            fixed short text
//	/echo            returns the request body
//	/delay?ms=N      responds after N milliseconds
//	/status?code=N   responds with status N
//	/bytes?n=N       returns N bytes
//	/login           POST {"user":..,"pwd":..} -> JSON with a token
//	/items           GET with "Authorization: Bearer <token>" -> JSON items
//	/redirect?to=P   302 to P (default /ping)
package testserver

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxHeaderBytes = 64 << 10
	maxBodyBytes   = 16 << 20
	maxBytesRoute  = 256 << 20
	flushThreshold = 32 << 10
)

// Server is a running test server.
type Server struct {
	ln     net.Listener
	secret []byte
	wg     sync.WaitGroup
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed atomic.Bool
	reqs   atomic.Int64
	done   chan struct{}
}

// Listen creates a server listening on addr (for example ":18080" or
// "127.0.0.1:0").
func Listen(addr string) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return New(ln), nil
}

// New creates a server on an existing listener.
func New(ln net.Listener) *Server {
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	return &Server{ln: ln, secret: secret, conns: map[net.Conn]struct{}{}, done: make(chan struct{})}
}

// Addr returns the listening address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// URL returns the base URL, e.g. http://127.0.0.1:18080.
func (s *Server) URL() string {
	addr := s.ln.Addr().(*net.TCPAddr)
	host := addr.IP.String()
	if addr.IP.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(addr.Port))
}

// Requests returns the number of requests served so far (sampled per
// connection, so it can lag slightly behind).
func (s *Server) Requests() int64 { return s.reqs.Load() }

// Start serves in a background goroutine.
func (s *Server) Start() { go s.Serve() }

// Serve accepts connections until Close is called.
func (s *Server) Serve() error {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if s.closed.Load() {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return err
		}
		s.mu.Lock()
		if s.closed.Load() {
			s.mu.Unlock()
			c.Close()
			return nil
		}
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handle(c)
	}
}

// Close stops the server and closes all connections.
func (s *Server) Close() error {
	if !s.closed.Swap(true) {
		close(s.done)
	}
	err := s.ln.Close()
	s.mu.Lock()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return err
}

type request struct {
	method   []byte
	path     []byte
	query    []byte
	auth     []byte
	ctype    []byte
	body     []byte
	http10   bool
	close    bool
	chunked  bool
	length   int64
	expect   bool
	needBody bool
}

var errBadRequest = errors.New("bad request")

func (s *Server) handle(c net.Conn) {
	defer func() {
		c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		s.wg.Done()
	}()
	br := bufio.NewReaderSize(c, 4096)
	out := make([]byte, 0, 4096)
	var req request
	served := int64(0)
	defer func() { s.reqs.Add(served) }()
	for {
		err := readRequest(br, c, &req)
		if err != nil {
			if errors.Is(err, errBadRequest) {
				out = appendSimple(out, 400, "text/plain", []byte("bad request\n"), true)
				c.Write(out)
			}
			return
		}
		served++
		var keep bool
		out, keep = s.route(c, &req, out)
		if !keep {
			c.Write(out)
			return
		}
		if br.Buffered() == 0 || len(out) >= flushThreshold {
			if _, err := c.Write(out); err != nil {
				return
			}
			out = out[:0]
		}
	}
}

func readLine(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return nil, errBadRequest
		}
		return nil, err
	}
	line = line[:len(line)-1]
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	return line, nil
}

func readRequest(br *bufio.Reader, c net.Conn, r *request) error {
	line, err := readLine(br)
	for err == nil && len(line) == 0 { // tolerate stray CRLF between requests
		line, err = readLine(br)
	}
	if err != nil {
		return err
	}
	sp1 := bytes.IndexByte(line, ' ')
	sp2 := bytes.LastIndexByte(line, ' ')
	if sp1 <= 0 || sp2 <= sp1 {
		return errBadRequest
	}
	proto := line[sp2+1:]
	if !bytes.HasPrefix(proto, []byte("HTTP/1.")) {
		return errBadRequest
	}
	r.method = append(r.method[:0], line[:sp1]...)
	target := line[sp1+1 : sp2]
	if q := bytes.IndexByte(target, '?'); q >= 0 {
		r.path = append(r.path[:0], target[:q]...)
		r.query = append(r.query[:0], target[q+1:]...)
	} else {
		r.path = append(r.path[:0], target...)
		r.query = r.query[:0]
	}
	r.http10 = bytes.Equal(proto, []byte("HTTP/1.0"))
	r.close = r.http10
	r.chunked, r.expect = false, false
	r.length = 0
	r.auth = r.auth[:0]
	r.ctype = r.ctype[:0]
	r.body = r.body[:0]
	total := len(line)
	for {
		line, err = readLine(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errBadRequest
			}
			return err
		}
		if len(line) == 0 {
			break
		}
		total += len(line)
		if total > maxHeaderBytes {
			return errBadRequest
		}
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			return errBadRequest
		}
		name := line[:colon]
		val := bytes.TrimSpace(line[colon+1:])
		switch {
		case eqFold(name, "content-length"):
			n, err := strconv.ParseInt(string(val), 10, 64)
			if err != nil || n < 0 {
				return errBadRequest
			}
			r.length = n
		case eqFold(name, "transfer-encoding"):
			r.chunked = bytes.Contains(bytes.ToLower(val), []byte("chunked"))
		case eqFold(name, "connection"):
			if eqFold(val, "close") {
				r.close = true
			} else if eqFold(val, "keep-alive") {
				r.close = false
			}
		case eqFold(name, "authorization"):
			r.auth = append(r.auth[:0], val...)
		case eqFold(name, "content-type"):
			r.ctype = append(r.ctype[:0], val...)
		case eqFold(name, "expect"):
			r.expect = eqFold(val, "100-continue")
		}
	}
	r.needBody = string(r.path) == "/echo" || string(r.path) == "/login"
	if r.expect && (r.length > 0 || r.chunked) {
		if _, err := c.Write([]byte("HTTP/1.1 100 Continue\r\n\r\n")); err != nil {
			return err
		}
	}
	if r.chunked {
		return readChunked(br, r)
	}
	if r.length > maxBodyBytes {
		return errBadRequest
	}
	if r.needBody {
		if cap(r.body) < int(r.length) {
			r.body = make([]byte, r.length)
		}
		r.body = r.body[:r.length]
		_, err := io.ReadFull(br, r.body)
		return err
	}
	_, err = br.Discard(int(r.length))
	return err
}

func readChunked(br *bufio.Reader, r *request) error {
	for {
		line, err := readLine(br)
		if err != nil {
			return err
		}
		if i := bytes.IndexByte(line, ';'); i >= 0 {
			line = line[:i]
		}
		n, err := strconv.ParseInt(string(bytes.TrimSpace(line)), 16, 64)
		if err != nil || n < 0 {
			return errBadRequest
		}
		if n == 0 {
			for { // trailers
				line, err := readLine(br)
				if err != nil {
					return err
				}
				if len(line) == 0 {
					return nil
				}
			}
		}
		if int64(len(r.body))+n > maxBodyBytes {
			return errBadRequest
		}
		if r.needBody {
			start := len(r.body)
			r.body = append(r.body, make([]byte, n)...)
			if _, err := io.ReadFull(br, r.body[start:]); err != nil {
				return err
			}
		} else if _, err := br.Discard(int(n)); err != nil {
			return err
		}
		if line, err := readLine(br); err != nil || len(line) != 0 {
			return errBadRequest
		}
	}
}

func eqFold[T []byte | string](a T, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(b); i++ {
		c := a[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != b[i] {
			return false
		}
	}
	return true
}

func queryInt(q []byte, key string, def int64) int64 {
	for len(q) > 0 {
		var kv []byte
		if i := bytes.IndexByte(q, '&'); i >= 0 {
			kv, q = q[:i], q[i+1:]
		} else {
			kv, q = q, nil
		}
		if k, v, ok := bytes.Cut(kv, []byte("=")); ok && string(k) == key {
			if n, err := strconv.ParseInt(string(v), 10, 64); err == nil {
				return n
			}
			return def
		}
	}
	return def
}

func queryString(q []byte, key string) string {
	for _, kv := range bytes.Split(q, []byte("&")) {
		if k, v, ok := bytes.Cut(kv, []byte("=")); ok && string(k) == key {
			return string(v)
		}
	}
	return ""
}

func statusLine(out []byte, code int) []byte {
	out = append(out, "HTTP/1.1 "...)
	out = strconv.AppendInt(out, int64(code), 10)
	out = append(out, ' ')
	text := http.StatusText(code)
	if text == "" {
		text = "Status"
	}
	out = append(out, text...)
	return append(out, "\r\n"...)
}

func appendSimple(out []byte, code int, ctype string, body []byte, close bool) []byte {
	out = statusLine(out, code)
	if ctype != "" {
		out = append(out, "Content-Type: "...)
		out = append(out, ctype...)
		out = append(out, "\r\n"...)
	}
	if code != http.StatusNoContent && code != http.StatusNotModified {
		out = append(out, "Content-Length: "...)
		out = strconv.AppendInt(out, int64(len(body)), 10)
		out = append(out, "\r\n"...)
	}
	if close {
		out = append(out, "Connection: close\r\n"...)
	}
	out = append(out, "\r\n"...)
	if code != http.StatusNoContent && code != http.StatusNotModified {
		out = append(out, body...)
	}
	return out
}

var (
	pongResponse      = []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 4\r\n\r\npong")
	pongResponseClose = []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 4\r\nConnection: close\r\n\r\npong")
	unauthorizedBody  = []byte(`{"code":401,"msg":"unauthorized"}`)
	zeroBytes         = make([]byte, 64<<10)
)

func init() {
	for i := range zeroBytes {
		zeroBytes[i] = 'a' + byte(i%26)
	}
}

// route writes the response for r into out. keep is false when the
// connection must be closed afterwards.
func (s *Server) route(c net.Conn, r *request, out []byte) ([]byte, bool) {
	keep := !r.close
	switch string(r.path) {
	case "/ping":
		if keep {
			return append(out, pongResponse...), true
		}
		return append(out, pongResponseClose...), false
	case "/echo":
		ct := "application/octet-stream"
		if len(r.ctype) > 0 {
			ct = string(r.ctype)
		}
		return appendSimple(out, 200, ct, r.body, !keep), keep
	case "/delay":
		ms := queryInt(r.query, "ms", 0)
		if ms < 0 || ms > 600_000 {
			return appendSimple(out, 400, "text/plain", []byte("ms must be 0-600000\n"), !keep), keep
		}
		// Flush earlier pipelined responses before sleeping.
		if len(out) > 0 {
			if _, err := c.Write(out); err != nil {
				return out[:0], false
			}
			out = out[:0]
		}
		t := time.NewTimer(time.Duration(ms) * time.Millisecond)
		select {
		case <-t.C:
		case <-s.done:
			t.Stop()
			return out, false
		}
		return appendSimple(out, 200, "text/plain", []byte("ok\n"), !keep), keep
	case "/status":
		code := queryInt(r.query, "code", 200)
		if code < 200 || code > 599 {
			return appendSimple(out, 400, "text/plain", []byte("code must be 200-599\n"), !keep), keep
		}
		return appendSimple(out, int(code), "text/plain", []byte("status "+strconv.FormatInt(code, 10)+"\n"), !keep), keep
	case "/bytes":
		n := queryInt(r.query, "n", 0)
		if n < 0 || n > maxBytesRoute {
			return appendSimple(out, 400, "text/plain", []byte("n must be 0-268435456\n"), !keep), keep
		}
		out = statusLine(out, 200)
		out = append(out, "Content-Type: application/octet-stream\r\nContent-Length: "...)
		out = strconv.AppendInt(out, n, 10)
		out = append(out, "\r\n"...)
		if !keep {
			out = append(out, "Connection: close\r\n"...)
		}
		out = append(out, "\r\n"...)
		for n > 0 {
			k := min(n, int64(len(zeroBytes)))
			out = append(out, zeroBytes[:k]...)
			n -= k
			if len(out) >= flushThreshold {
				if _, err := c.Write(out); err != nil {
					return out[:0], false
				}
				out = out[:0]
			}
		}
		return out, keep
	case "/login":
		if string(r.method) != "POST" {
			return appendSimple(out, 405, "application/json", []byte(`{"code":405,"msg":"use POST"}`), !keep), keep
		}
		var cred struct {
			User string `json:"user"`
			Pwd  string `json:"pwd"`
		}
		if err := json.Unmarshal(r.body, &cred); err != nil {
			return appendSimple(out, 400, "application/json", []byte(`{"code":400,"msg":"invalid json"}`), !keep), keep
		}
		if cred.User == "" || cred.Pwd == "" || cred.Pwd == "wrong" {
			return appendSimple(out, 401, "application/json", []byte(`{"code":401,"msg":"invalid credentials"}`), !keep), keep
		}
		resp, _ := json.Marshal(map[string]any{
			"code": 0, "msg": "ok",
			"data": map[string]string{"user": cred.User, "token": s.token(cred.User)},
		})
		return appendSimple(out, 200, "application/json", resp, !keep), keep
	case "/items":
		user, ok := s.verify(r.auth)
		if !ok {
			return appendSimple(out, 401, "application/json", unauthorizedBody, !keep), keep
		}
		u, _ := json.Marshal(user)
		body := []byte(`{"code":0,"data":{"user":` + string(u) +
			`,"items":[{"id":1,"name":"apple"},{"id":2,"name":"banana"},{"id":3,"name":"cherry"}]}}`)
		return appendSimple(out, 200, "application/json", body, !keep), keep
	case "/redirect":
		to := queryString(r.query, "to")
		if to == "" || to[0] != '/' {
			to = "/ping"
		}
		out = statusLine(out, 302)
		out = append(out, "Location: "...)
		out = append(out, to...)
		out = append(out, "\r\nContent-Length: 0\r\n"...)
		if !keep {
			out = append(out, "Connection: close\r\n"...)
		}
		return append(out, "\r\n"...), keep
	}
	return appendSimple(out, 404, "text/plain", []byte("not found\n"), !keep), keep
}

func (s *Server) mac(user string) string {
	h := fnv.New64a()
	h.Write(s.secret)
	h.Write([]byte(user))
	return hex.EncodeToString(h.Sum(nil))
}

// token returns "<mac>.<hex user>"; it can be verified without state.
func (s *Server) token(user string) string {
	return s.mac(user) + "." + hex.EncodeToString([]byte(user))
}

func (s *Server) verify(auth []byte) (string, bool) {
	tok, ok := bytes.CutPrefix(auth, []byte("Bearer "))
	if !ok {
		return "", false
	}
	mac, hexUser, ok := bytes.Cut(tok, []byte("."))
	if !ok {
		return "", false
	}
	user, err := hex.DecodeString(string(hexUser))
	if err != nil || len(user) == 0 {
		return "", false
	}
	if s.mac(string(user)) != string(mac) {
		return "", false
	}
	return string(user), true
}
