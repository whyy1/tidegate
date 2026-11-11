package engine

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"tidegate/internal/scenario"
)

// reqHeader is a rendered request header; value points into a VU buffer.
type reqHeader struct {
	name   string // as written by the user
	canon  string
	value  []byte
	static []string // pre-built value for net/http when the template is static
}

// hopRequest is one HTTP request on the wire (one hop of a redirect chain).
// All byte slices point into buffers owned by the virtual user.
type hopRequest struct {
	method      string
	origin      *scenario.Origin
	uri         []byte   // request target, e.g. /items?id=1
	url         *url.URL // parsed URL when known; built lazily otherwise
	headers     []reqHeader
	body        []byte
	contentType string // added when the user set no Content-Type
	userAgent   bool   // add the default User-Agent
	gzip        bool   // add Accept-Encoding: gzip
	cookie      []byte // Cookie header from the jar
	close       bool   // ask the server to close the connection
	head        bool   // HEAD request: no response body
	keepBody    bool   // retain the response body for extractors/assertions
	bodyLimit   int64  // retained body cap (0 = unlimited)
	errPrefix   int    // retain this many body bytes of 4xx/5xx responses for samples
}

// rawHeader is a response header as offsets into hopResponse.arena.
type rawHeader struct {
	name, value [2]int32
}

// hopResponse receives the result of one hop.
type hopResponse struct {
	status    int
	connClose bool
	gzip      bool

	// Response headers: either raw (fast client) or a net/http header map.
	arena     []byte
	raw       []rawHeader
	stdHeader http.Header

	body      []byte // retained body (keepBody or error prefix)
	truncated bool   // body was cut at the retention limit
	bodyLen   int64  // body bytes received (after transfer decoding)

	// Timeline (nanotime).
	start      int64 // before acquiring a connection
	writeStart int64 // connection ready, request about to be written
	firstByte  int64
	end        int64

	newConn bool
	dial    dialTiming

	sent, recv         int64 // bytes of this request on the wire
	overSent, overRecv int64 // bytes not attributable to it (handshakes, retries)
}

func (r *hopResponse) reset() {
	r.status, r.connClose, r.gzip = 0, false, false
	r.arena = r.arena[:0]
	r.raw = r.raw[:0]
	r.stdHeader = nil
	r.body = r.body[:0]
	r.truncated = false
	r.bodyLen = 0
	r.start, r.writeStart, r.firstByte, r.end = 0, 0, 0, 0
	r.newConn = false
	r.dial = dialTiming{}
	r.sent, r.recv, r.overSent, r.overRecv = 0, 0, 0, 0
}

func (r *hopResponse) rawName(h *rawHeader) []byte  { return r.arena[h.name[0]:h.name[1]] }
func (r *hopResponse) rawValue(h *rawHeader) []byte { return r.arena[h.value[0]:h.value[1]] }

// header returns the first value of a header (case-insensitive name).
func (r *hopResponse) header(name string) ([]byte, bool) {
	if r.stdHeader != nil {
		if v := r.stdHeader.Values(name); len(v) > 0 {
			return []byte(v[0]), true
		}
		return nil, false
	}
	for i := range r.raw {
		h := &r.raw[i]
		if equalFoldBytes(r.rawName(h), name) {
			return r.rawValue(h), true
		}
	}
	return nil, false
}

// eachHeader calls fn for every response header.
func (r *hopResponse) eachHeader(fn func(name string, value []byte)) {
	if r.stdHeader != nil {
		for k, vs := range r.stdHeader {
			for _, v := range vs {
				fn(k, []byte(v))
			}
		}
		return
	}
	for i := range r.raw {
		h := &r.raw[i]
		fn(string(r.rawName(h)), r.rawValue(h))
	}
}

func equalFoldBytes(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		x, y := b[i], s[i]
		if x == y {
			continue
		}
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// fullURL formats the request URL for samples and redirects.
func (q *hopRequest) fullURL() string {
	var b strings.Builder
	b.WriteString(q.origin.Scheme)
	b.WriteString("://")
	b.WriteString(q.origin.Host)
	b.Write(q.uri)
	return b.String()
}

// parsedURL returns the request URL as *url.URL, parsing it if needed.
func (q *hopRequest) parsedURL() *url.URL {
	if q.url == nil {
		u, err := url.Parse(q.fullURL())
		if err != nil {
			u = &url.URL{Scheme: q.origin.Scheme, Host: q.origin.Host, Path: "/"}
		}
		q.url = u
	}
	return q.url
}

func durOf(from, to int64) time.Duration {
	if to <= from || from == 0 {
		return 0
	}
	return time.Duration(to - from)
}
