package engine

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
	"tidegate/internal/testserver"
)

var clients = []ClientKind{ClientFast, ClientStd}

func startTestServer(t testing.TB) *testserver.Server {
	t.Helper()
	s, err := testserver.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	t.Cleanup(func() { s.Close() })
	return s
}

// compile builds a plan from YAML with BASE replaced by base.
func compile(t testing.TB, src, base string, files map[string]string) *scenario.Plan {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(dir+"/"+name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	def, err := scenario.Parse([]byte(strings.ReplaceAll(src, "BASE", base)), scenario.FormatYAML)
	if err != nil {
		t.Fatal(err)
	}
	p, err := scenario.Compile(def, scenario.CompileOptions{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type runResult struct {
	snaps   []*metrics.Snapshot
	summary *metrics.Snapshot
	elapsed time.Duration
}

func run(t testing.TB, p *scenario.Plan, opts Options) runResult {
	t.Helper()
	return runCtx(t, context.Background(), p, opts)
}

func runCtx(t testing.TB, ctx context.Context, p *scenario.Plan, opts Options) runResult {
	t.Helper()
	e, err := New(p, opts)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	errc := make(chan error, 1)
	go func() { errc <- e.Run(ctx) }()
	var res runResult
	for s := range e.Snapshots() {
		res.snaps = append(res.snaps, s)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	res.elapsed = time.Since(start)
	res.summary = e.Summary()
	if res.summary == nil {
		t.Fatal("no summary")
	}
	return res
}

// checkConsistent verifies that per-second snapshots add up to the summary.
func checkConsistent(t *testing.T, r runResult) {
	t.Helper()
	var req, errs, it int64
	for i, s := range r.snaps {
		if s.Seq != int64(i) {
			t.Fatalf("snapshot %d has seq %d", i, s.Seq)
		}
		req += s.Total.Requests
		errs += s.Total.ErrorCount()
		it += s.Iterations
	}
	if req != r.summary.Total.Requests || errs != r.summary.Total.ErrorCount() || it != r.summary.Iterations {
		t.Fatalf("snapshots (req=%d err=%d it=%d) != summary (req=%d err=%d it=%d)",
			req, errs, it, r.summary.Total.Requests, r.summary.Total.ErrorCount(), r.summary.Iterations)
	}
	if r.summary.Total.Latency.Count() != r.summary.Total.Requests {
		t.Fatalf("histogram count %d != requests %d", r.summary.Total.Latency.Count(), r.summary.Total.Requests)
	}
}

func TestProfile(t *testing.T) {
	p := newProfile([]scenario.StageSpec{
		{Duration: 10 * time.Second, Target: 100},
		{Duration: 0, Target: 50},
		{Duration: 10 * time.Second, Target: 50},
	}, 1)
	for _, c := range []struct {
		at   time.Duration
		want float64
	}{{0, 0}, {5 * time.Second, 50}, {9999 * time.Millisecond, 99.99}, {10 * time.Second, 50}, {15 * time.Second, 50}} {
		got, done := p.targetAt(c.at)
		if done || math.Abs(got-c.want) > 1e-6 {
			t.Errorf("targetAt(%v) = %v %v, want %v", c.at, got, done, c.want)
		}
	}
	if _, done := p.targetAt(20 * time.Second); !done {
		t.Error("profile should end at 20s")
	}
	// Ramp 0 -> 100/s over 10s: 500 iterations by 10s, then 50/s.
	if c := p.cumAt(10 * time.Second); math.Abs(c-500) > 1e-6 {
		t.Errorf("cumAt(10s) = %v", c)
	}
	if c := p.cumAt(20 * time.Second); math.Abs(c-1000) > 1e-6 {
		t.Errorf("cumAt(20s) = %v", c)
	}
	for _, k := range []float64{0, 1, 125, 499, 500, 750, 999} {
		at, ok := p.timeOf(k)
		if !ok {
			t.Fatalf("timeOf(%v) not reached", k)
		}
		if c := p.cumAt(at); math.Abs(c-k) > 1e-3 {
			t.Errorf("timeOf(%v) = %v, cumAt = %v", k, at, c)
		}
	}
	if _, ok := p.timeOf(1001); ok {
		t.Error("iteration beyond the profile should not be scheduled")
	}
	// Ramp 0 -> 100 over 10s: iteration 125 at t = sqrt(2*125/10) = 5s.
	if at, _ := p.timeOf(125); (at - 5*time.Second).Abs() > time.Millisecond {
		t.Errorf("timeOf(125) = %v, want 5s", at)
	}
	q := p.withOverride(5*time.Second, 10)
	if v, _ := q.targetAt(4 * time.Second); math.Abs(v-40) > 1e-6 {
		t.Errorf("override changed the past: %v", v)
	}
	if v, _ := q.targetAt(7 * time.Second); v != 10 {
		t.Errorf("override target = %v", v)
	}
	if c := q.cumAt(15 * time.Second); math.Abs(c-(125+100)) > 1e-6 {
		t.Errorf("override cum = %v", c)
	}
	half := newProfile([]scenario.StageSpec{{Duration: time.Second, Target: 100}}, 0.5)
	if v, _ := half.targetAt(999 * time.Millisecond); v > 50 {
		t.Errorf("load share not applied: %v", v)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want metrics.ErrorKind
	}{
		{&hopError{phase: phaseDNS, err: errors.New("x")}, metrics.ErrDNS},
		{&net.DNSError{Err: "no such host", Name: "x"}, metrics.ErrDNS},
		{&hopError{phase: phaseDial, err: os.ErrDeadlineExceeded}, metrics.ErrConnectTimeout},
		{&hopError{phase: phaseRead, err: os.ErrDeadlineExceeded}, metrics.ErrTimeout},
		{&hopError{phase: phaseRead, err: context.DeadlineExceeded}, metrics.ErrTimeout},
		{&hopError{phase: phaseTLS, err: errTLSHandshakeTimeout}, metrics.ErrTLS},
		{&hopError{phase: phaseTLS, err: io.EOF}, metrics.ErrTLS},
		{&hopError{phase: phaseRead, err: io.ErrUnexpectedEOF}, metrics.ErrReset},
		{&hopError{phase: phaseRead, err: errors.New("weird")}, metrics.ErrOther},
		{fmt.Errorf("wrapped: %w", &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}), metrics.ErrConnectTimeout},
	}
	cases = append(cases, platformClassifyCases()...)
	for i, c := range cases {
		if got := classify(c.err); got != c.want {
			t.Errorf("case %d (%v): got %v, want %v", i, c.err, got, c.want)
		}
	}
}

// sysErr builds the error shape returned by the net package.
func sysErr(op string, errno syscall.Errno) error {
	return &net.OpError{Op: op, Net: "tcp", Err: os.NewSyscallError("connect", errno)}
}

func TestClassifyRealErrors(t *testing.T) {
	// A closed port on loopback: connection refused.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	_, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err == nil {
		t.Skip("closed port accepted a connection")
	}
	if got := classify(&hopError{phase: phaseDial, err: err}); got != metrics.ErrConnectRefused {
		t.Fatalf("refused dial (%v) classified as %v", err, got)
	}

	// A peer that resets the connection.
	ln, _ = net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.(*net.TCPConn).SetLinger(0)
			io.ReadFull(c, make([]byte, 1))
			c.Close()
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("x"))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = c.Read(make([]byte, 10))
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := classify(&hopError{phase: phaseRead, err: err}); got != metrics.ErrReset {
		t.Fatalf("reset (%v) classified as %v", err, got)
	}
}

const pingScenario = `
name: ping
steps:
  - id: ping
    request:
      url: BASE/ping
    assert:
      - type: body_contains
        value: pong
load:
  mode: vus
  stages:
    - {duration: 0s, target: 8}
    - {duration: 1500ms, target: 8}
  graceful_stop: 2s
`

func TestClosedModel(t *testing.T) {
	srv := startTestServer(t)
	for _, client := range clients {
		t.Run(string(client), func(t *testing.T) {
			p := compile(t, pingScenario, srv.URL(), nil)
			r := run(t, p, Options{Client: client})
			checkConsistent(t, r)
			s := r.summary
			if s.Total.Requests < 100 {
				t.Fatalf("only %d requests", s.Total.Requests)
			}
			if s.Total.ErrorCount() != 0 || s.Total.Status[200] != s.Total.Requests {
				t.Fatalf("errors=%v status=%v samples=%+v", s.Total.Errors, s.Total.Status, s.Samples)
			}
			if s.ActiveVUs != 8 || s.ActiveConns < 8 {
				t.Fatalf("peak vus=%d conns=%d", s.ActiveVUs, s.ActiveConns)
			}
			if s.Total.PhaseCount[metrics.PhaseConnect] != 8 {
				t.Fatalf("connections opened = %d, want 8 (keep-alive)", s.Total.PhaseCount[metrics.PhaseConnect])
			}
			// GET /ping: request is ~80 bytes, response 68 bytes.
			perReq := float64(s.Total.BytesReceived) / float64(s.Total.Requests)
			if perReq < 60 || perReq > 80 {
				t.Fatalf("received %.1f bytes per request", perReq)
			}
			if s.Total.BytesSent < s.Total.Requests*50 {
				t.Fatalf("sent %d bytes for %d requests", s.Total.BytesSent, s.Total.Requests)
			}
			if r.elapsed > 4*time.Second || len(r.snaps) < 2 || len(r.snaps) > 3 {
				t.Fatalf("elapsed %v with %d snapshots", r.elapsed, len(r.snaps))
			}
			if st := s.Total.Latency.Stats(); st.P50 <= 0 || st.P50 > 50 {
				t.Fatalf("p50 = %vms", st.P50)
			}
		})
	}
}

func TestOpenModel(t *testing.T) {
	srv := startTestServer(t)
	src := `
steps:
  - id: slow
    request: {url: "BASE/delay?ms=50"}
load:
  mode: rate
  max_vus: MAXVUS
  stages:
    - {duration: 0s, target: 200}
    - {duration: 2s, target: 200}
  graceful_stop: 2s
`
	for _, client := range clients {
		t.Run(string(client), func(t *testing.T) {
			// 200 iterations/s of 50ms need about 10 concurrent users.
			p := compile(t, strings.Replace(src, "MAXVUS", "100", 1), srv.URL(), nil)
			r := run(t, p, Options{Client: client})
			checkConsistent(t, r)
			s := r.summary
			if s.Total.Requests < 360 || s.Total.Requests > 420 {
				t.Fatalf("requests = %d, want about 400", s.Total.Requests)
			}
			if s.DroppedIterations != 0 {
				t.Fatalf("dropped = %d", s.DroppedIterations)
			}
			if s.SchedDelay == nil || s.SchedDelay.Count() != s.Total.Requests {
				t.Fatalf("sched delay count = %v", s.SchedDelay)
			}
			if st := s.Total.Latency.Stats(); st.P50 < 49 || st.P50 > 70 {
				t.Fatalf("p50 = %vms, want about 50ms", st.P50)
			}
			if s.ActiveVUs > 40 {
				t.Fatalf("used %d VUs for 200/s x 50ms", s.ActiveVUs)
			}
			if r.snaps[0].Target != 200 {
				t.Fatalf("target = %v", r.snaps[0].Target)
			}

			// With only 2 VUs, 200/s of 50ms requests must drop most iterations.
			p = compile(t, strings.Replace(src, "MAXVUS", "2", 1), srv.URL(), nil)
			r = run(t, p, Options{Client: client})
			s = r.summary
			if s.DroppedIterations < 250 || s.Total.Requests > 100 {
				t.Fatalf("dropped = %d, requests = %d", s.DroppedIterations, s.Total.Requests)
			}
		})
	}
}

// Latency in the open model counts from the planned start: when the only
// virtual user is busy, queued work is not hidden.
func TestOpenModelMeasuresFromPlannedTime(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	p := compile(t, `
steps:
  - id: a
    request: {url: BASE/}
    think: {type: fixed, min: 300ms}
load:
  mode: rate
  max_vus: 1
  stages: [{duration: 0s, target: 2}, {duration: 1100ms, target: 2}]
`, srv.URL, nil)
	r := run(t, p, Options{})
	s := r.summary
	// Iterations at 0, 500ms, 1000ms: each 300ms long, so the single VU is
	// free in time and nothing is dropped.
	if s.Total.Requests != 3 || s.DroppedIterations != 0 {
		t.Fatalf("requests=%d dropped=%d", s.Total.Requests, s.DroppedIterations)
	}
	if st := s.SchedDelay.Stats(); st.Max > 20 {
		t.Fatalf("scheduling delay max = %vms", st.Max)
	}
}

func TestRedirectsCookiesAndSamples(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc", Path: "/"})
		http.Redirect(w, r, "/landing", http.StatusFound)
	})
	mux.HandleFunc("/landing", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("sid")
		if err != nil || c.Value != "abc" {
			http.Error(w, "no cookie", 403)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		io.WriteString(gz, `{"ok":true,"user":"zed"}`)
		gz.Close()
	})
	mux.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "secret=1")
		w.WriteHeader(503)
		io.WriteString(w, strings.Repeat("e", 3000))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := compile(t, `
steps:
  - id: login
    request: {url: BASE/start}
    extract: [{name: user, from: jsonpath, expr: $.user}]
    assert: [{type: jsonpath, expr: $.ok, equals: true}]
  - id: fail
    request:
      url: BASE/fail
      headers: {Authorization: "Bearer {{user}}", X-User: "{{user}}"}
load:
  stages: [{duration: 0s, target: 1}, {duration: 300ms, target: 1}]
`, srv.URL, nil)
	for _, client := range clients {
		t.Run(string(client), func(t *testing.T) {
			r := run(t, p, Options{Client: client})
			s := r.summary
			login := s.Steps[0]
			// Every iteration: one 302 hop and one 200 hop.
			if d := login.Status[302] - login.Status[200]; login.Status[302] == 0 || d < 0 || d > 1 || login.ErrorCount() != 0 {
				t.Fatalf("login status=%v errors=%v samples=%+v", login.Status, login.Errors, s.Samples)
			}
			fail := s.Steps[1]
			if fail.Errors[metrics.ErrHTTP5xx] != fail.Requests || fail.Requests == 0 {
				t.Fatalf("fail errors = %v", fail.Errors)
			}
			var smp *metrics.Sample
			for i := range s.Samples {
				if s.Samples[i].Kind == metrics.SampleKind(metrics.ErrHTTP5xx) {
					smp = &s.Samples[i]
				}
			}
			if smp == nil {
				t.Fatalf("no 5xx sample in %+v", s.Samples)
			}
			if smp.Status != 503 || smp.StepID != "fail" || len(smp.Body) != 1024 || !smp.BodyTruncated || smp.Error != "HTTP 503" {
				t.Fatalf("sample = %+v", smp)
			}
			hdr := map[string]string{}
			for _, h := range smp.RequestHeaders {
				hdr[h.Name] = h.Value
			}
			if hdr["Authorization"] != "***" || hdr["X-User"] != "zed" {
				t.Fatalf("request headers = %v (extracted user must be zed, auth masked)", hdr)
			}
			for _, h := range smp.ResponseHeaders {
				if strings.EqualFold(h.Name, "Set-Cookie") && h.Value != "***" {
					t.Fatalf("set-cookie not masked: %v", h)
				}
			}
		})
	}
}

func TestMaxRequests(t *testing.T) {
	srv := startTestServer(t)
	p := compile(t, `
steps: [{id: a, request: {url: BASE/ping}}]
load:
  max_requests: 777
  stages: [{duration: 0s, target: 16}, {duration: 30s, target: 16}]
`, srv.URL(), nil)
	for _, client := range clients {
		t.Run(string(client), func(t *testing.T) {
			r := run(t, p, Options{Client: client})
			if r.summary.Total.Requests != 777 {
				t.Fatalf("requests = %d, want exactly 777", r.summary.Total.Requests)
			}
			if r.elapsed > 10*time.Second {
				t.Fatalf("took %v", r.elapsed)
			}
		})
	}
}

func TestGracefulStopAndAbort(t *testing.T) {
	srv := startTestServer(t)
	src := `
steps:
  - {id: slow, request: {url: "BASE/delay?ms=DELAY"}, think: {type: fixed, min: 10s}}
load:
  graceful_stop: GRACE
  stages: [{duration: 0s, target: 4}, {duration: 60s, target: 4}]
`
	for _, client := range clients {
		t.Run(string(client), func(t *testing.T) {
			// Cancelling the context waits for in-flight requests and
			// interrupts think time.
			src1 := strings.NewReplacer("DELAY", "300", "GRACE", "5s").Replace(src)
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(500*time.Millisecond, cancel)
			r := runCtx(t, ctx, compile(t, src1, srv.URL(), nil), Options{Client: client})
			if r.elapsed > 2*time.Second || r.summary.Total.Requests != 4 || r.summary.Total.ErrorCount() != 0 {
				t.Fatalf("elapsed=%v requests=%d errors=%v", r.elapsed, r.summary.Total.Requests, r.summary.Total.Errors)
			}

			// Requests longer than graceful_stop are cut off and not recorded.
			src2 := strings.NewReplacer("DELAY", "20000", "GRACE", "300ms").Replace(src)
			ctx, cancel = context.WithCancel(context.Background())
			time.AfterFunc(300*time.Millisecond, cancel)
			r = runCtx(t, ctx, compile(t, src2, srv.URL(), nil), Options{Client: client})
			if r.elapsed > 3*time.Second || r.summary.Total.Requests != 0 {
				t.Fatalf("abort: elapsed=%v requests=%d", r.elapsed, r.summary.Total.Requests)
			}
		})
	}
}

func TestTLSBytesCountedBelowTLS(t *testing.T) {
	const body = "hello over tls"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer srv.Close()
	p := compile(t, `
http: {insecure_skip_verify: true}
steps: [{id: a, request: {url: BASE/x}, assert: [{type: body_contains, value: "over tls"}]}]
load: {max_requests: 20, stages: [{duration: 0s, target: 1}, {duration: 10s, target: 1}]}
`, srv.URL, nil)
	for _, client := range clients {
		t.Run(string(client), func(t *testing.T) {
			r := run(t, p, Options{Client: client})
			s := r.summary
			if s.Total.Requests != 20 || s.Total.ErrorCount() != 0 {
				t.Fatalf("requests=%d errors=%v samples=%+v", s.Total.Requests, s.Total.Errors, s.Samples)
			}
			// Plain HTTP response: status line + Date + Content-Length +
			// Content-Type + body is about 130 bytes; TLS records add a
			// header and an authentication tag to every record.
			plain := int64(len("HTTP/1.1 200 OK\r\nDate: Mon, 02 Jan 2006 15:04:05 GMT\r\nContent-Length: 14\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n") + len(body))
			if per := s.Total.BytesReceived / s.Total.Requests; per <= plain {
				t.Fatalf("received %d bytes per request, plaintext is %d: counting is above TLS", per, plain)
			}
			if s.Total.PhaseCount[metrics.PhaseTLS] != 1 {
				t.Fatalf("TLS handshakes = %d, want 1", s.Total.PhaseCount[metrics.PhaseTLS])
			}
			// The handshake (certificate etc.) is overhead, not request bytes.
			if s.OverheadReceived < 500 || s.OverheadSent < 100 {
				t.Fatalf("overhead sent=%d received=%d", s.OverheadSent, s.OverheadReceived)
			}
		})
	}
}

func TestConnectionOptions(t *testing.T) {
	var conns atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	cases := []struct {
		http      string
		wantConns int64
	}{
		{"{max_requests_per_conn: 5}", 6},
		{"{keep_alive: false}", 30},
		{"{conn_mode: shared}", 1},
	}
	for _, client := range clients {
		for _, c := range cases {
			t.Run(string(client)+c.http, func(t *testing.T) {
				conns.Store(0)
				p := compile(t, `
http: `+c.http+`
steps: [{id: a, request: {url: BASE/x}}]
load: {max_requests: 30, stages: [{duration: 0s, target: 1}, {duration: 10s, target: 1}]}
`, srv.URL, nil)
				r := run(t, p, Options{Client: client})
				if r.summary.Total.Requests != 30 || r.summary.Total.ErrorCount() != 0 {
					t.Fatalf("requests=%d errors=%v %+v", r.summary.Total.Requests, r.summary.Total.Errors, r.summary.Samples)
				}
				if got := conns.Load(); got != c.wantConns {
					t.Fatalf("server saw %d connections, want %d", got, c.wantConns)
				}
			})
		}
	}
}

func TestTimeoutsAndRefused(t *testing.T) {
	srv := startTestServer(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()
	src := strings.ReplaceAll(`
http: {timeout: {first_byte: 200ms, total: 1s, connect: 3s}}
steps:
  - {id: slow, request: {url: "BASE/delay?ms=2000"}}
  - {id: refused, request: {url: "http://CLOSED/x"}}
  - {id: nohost, request: {url: "http://no-such-host.invalid/x"}}
load: {max_requests: 3, graceful_stop: 10s, stages: [{duration: 0s, target: 1}, {duration: 30s, target: 1}]}
`, "CLOSED", closed)
	for _, client := range clients {
		t.Run(string(client), func(t *testing.T) {
			r := run(t, compile(t, src, srv.URL(), nil), Options{Client: client})
			s := r.summary
			if s.Steps[0].Errors[metrics.ErrTimeout] != 1 {
				t.Errorf("slow: %v %+v", s.Steps[0].Errors, s.Samples)
			}
			if lat := s.Steps[0].Latency.Max(); lat < 190_000 || lat > 600_000 {
				t.Errorf("first byte timeout fired after %dus", lat)
			}
			if s.Steps[1].Errors[metrics.ErrConnectRefused] != 1 {
				t.Errorf("refused: %v %+v", s.Steps[1].Errors, s.Samples)
			}
			if s.Steps[2].Errors[metrics.ErrDNS] != 1 {
				t.Errorf("dns: %v %+v", s.Steps[2].Errors, s.Samples)
			}
		})
	}
}

func TestDataRowsAndEcho(t *testing.T) {
	srv := startTestServer(t)
	p := compile(t, `
data: [{name: users, file: users.csv, mode: sequential}]
steps:
  - id: echo
    request:
      method: POST
      url: BASE/echo
      body: {type: json, content: '{"name":"{{users.name}}","n":{{$randomInt(1,9)}}}'}
    extract: [{name: who, from: jsonpath, expr: $.name}]
    assert: [{type: jsonpath, expr: $.name, exists: true}]
  - id: check
    request:
      method: POST
      url: BASE/echo
      body: {type: raw, content: "{{who}}"}
    assert: [{type: body_contains, value: "user-"}]
load: {max_requests: 40, stages: [{duration: 0s, target: 2}, {duration: 10s, target: 2}]}
`, srv.URL(), map[string]string{"users.csv": "name\nuser-1\nuser-2\nuser-3\n"})
	r := run(t, p, Options{})
	if r.summary.Total.ErrorCount() != 0 || r.summary.Total.Requests != 40 {
		t.Fatalf("errors=%v requests=%d samples=%+v", r.summary.Total.Errors, r.summary.Total.Requests, r.summary.Samples)
	}
}

func TestSetTarget(t *testing.T) {
	srv := startTestServer(t)
	p := compile(t, `
steps: [{id: a, request: {url: BASE/ping}, think: {type: fixed, min: 50ms}}]
load: {stages: [{duration: 0s, target: 2}, {duration: 1500ms, target: 2}]}
`, srv.URL(), nil)
	e, err := New(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		e.SetTarget(6)
	}()
	go e.Run(context.Background())
	var snaps []*metrics.Snapshot
	for s := range e.Snapshots() {
		snaps = append(snaps, s)
	}
	if snaps[0].Target != 2 && snaps[0].Target != 6 {
		t.Fatalf("first target = %v", snaps[0].Target)
	}
	if e.Summary().ActiveVUs != 6 {
		t.Fatalf("peak VUs = %d after SetTarget(6)", e.Summary().ActiveVUs)
	}
}
