package engine

import (
	"bytes"
	"compress/gzip"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"sync"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
)

// stepRT is the runtime form of an HTTP step: the parts of the request
// that never change are converted once here.
type stepRT struct {
	s          *scenario.HTTPStep
	uri        []byte     // static request URI
	hdrBytes   [][]byte   // static header values
	hdrStd     [][]string // static header values for net/http
	body       []byte     // static body
	bodyStatic bool
	userCookie int // index of a user Cookie header, or -1
}

func newStepRT(s *scenario.HTTPStep) *stepRT {
	rt := &stepRT{s: s, userCookie: -1}
	if s.URL.Static != nil {
		rt.uri = []byte(s.URL.Static.RequestURI)
	}
	rt.hdrBytes = make([][]byte, len(s.Headers))
	rt.hdrStd = make([][]string, len(s.Headers))
	for i, h := range s.Headers {
		if h.Value.Static() {
			rt.hdrBytes[i] = []byte(h.Value.Literal())
			rt.hdrStd[i] = []string{h.Value.Literal()}
		}
		if h.Canonical == "Cookie" {
			rt.userCookie = i
		}
	}
	if s.Body != nil && s.Body.Static() {
		rt.body, rt.bodyStatic = []byte(s.Body.Literal()), true
	}
	return rt
}

// vu is one virtual user. Everything it touches on the hot path is owned
// by it: buffers, random source, variable values and its metrics shard.
type vu struct {
	id       int
	eng      *Engine
	rec      *metrics.Recorder
	client   vuClient
	env      scenario.Env
	rng      *rand.Rand
	quit     chan struct{}
	quitOnce sync.Once
	timer    *time.Timer

	req  hopRequest
	resp hopResponse
	mrec metrics.RequestRecord

	uriBuf    []byte
	urlBuf    []byte
	hdrBuf    []byte
	hdrOff    []int
	bodyBuf   []byte
	cookieBuf []byte
	keyBuf    []byte
	redirURI  []byte
	extract   []byte
	origins   map[string]*scenario.Origin
	jar       *cookiejar.Jar
	gz        *gzip.Reader
	gzSrc     bytes.Reader
	gzBuf     []byte
	headerFn  func(string) ([]byte, bool)
}

func newVU(e *Engine, id int) *vu {
	v := &vu{
		id:   id,
		eng:  e,
		rec:  e.collector.Recorder(id),
		quit: make(chan struct{}),
		rng:  rand.New(rand.NewPCG(e.seed, uint64(id)*0x9E3779B97F4A7C15+1)),
	}
	v.env.Rand = v.rng
	v.env.Vars = make([][]byte, len(e.plan.VarInit))
	for i, init := range e.plan.VarInit {
		v.env.Vars[i] = []byte(init)
	}
	v.env.Rows = make([][]string, len(e.data))
	for i, d := range e.data {
		if d.ds.Used && d.ds.Mode == scenario.DataUnique {
			v.env.Rows[i] = d.rows[id%len(d.rows)]
		}
	}
	v.headerFn = v.resp.header
	v.client = e.client.newVU(v)
	return v
}

// stop asks the virtual user to finish at its next step boundary.
func (v *vu) stop() { v.quitOnce.Do(func() { close(v.quit) }) }

func (v *vu) stopped() bool {
	if v.eng.stopping.Load() {
		return true
	}
	select {
	case <-v.quit:
		return true
	default:
		return false
	}
}

func (v *vu) runClosed() {
	defer v.eng.vuExited(v)
	for !v.stopped() {
		v.iteration(0)
	}
}

func (v *vu) runOpen(planned int64) {
	defer v.eng.vuExited(v)
	for {
		v.iteration(planned)
		if v.stopped() {
			return
		}
		select {
		case planned = <-v.eng.jobs:
		case <-v.quit:
			return
		case <-v.eng.stopCh:
			return
		}
	}
}

// sleep waits for d; it returns false when the user was asked to stop.
func (v *vu) sleep(d time.Duration) bool {
	if d <= 0 {
		return !v.stopped()
	}
	if v.timer == nil {
		v.timer = time.NewTimer(d)
	} else {
		v.timer.Reset(d)
	}
	select {
	case <-v.timer.C:
		return true
	case <-v.quit:
	case <-v.eng.stopCh:
	}
	v.timer.Stop()
	return false
}

func (v *vu) pickRows() {
	for i, d := range v.eng.data {
		if !d.ds.Used {
			continue
		}
		switch d.ds.Mode {
		case scenario.DataSequential:
			v.env.Rows[i] = d.rows[(d.next.Add(1)-1)%uint64(len(d.rows))]
		case scenario.DataRandom:
			v.env.Rows[i] = d.rows[v.rng.IntN(len(d.rows))]
		}
	}
}

// iteration runs the scenario once. planned is the scheduled start in the
// open model (0 in the closed model).
func (v *vu) iteration(planned int64) {
	if v.stopped() {
		return
	}
	if planned != 0 {
		v.rec.RecordSchedDelay(time.Duration(nanotime() - planned))
	}
	v.pickRows()
	for _, st := range v.eng.steps {
		if v.stopped() {
			return
		}
		if !v.runHTTP(st, planned) {
			return
		}
		planned = 0
		if th := st.s.Think; th.Kind != scenario.ThinkNone {
			if !v.sleep(th.Duration(v.rng)) {
				return
			}
		}
	}
	v.rec.RecordIteration()
}

// takeRequestSlot enforces load.max_requests. It returns false when the
// limit was reached and the request must not be sent.
func (v *vu) takeRequestSlot() bool {
	e := v.eng
	if e.maxReq <= 0 {
		return true
	}
	n := e.reqCount.Add(1)
	if n >= e.maxReq {
		e.Stop()
	}
	return n <= e.maxReq
}

// buildRequest renders the step's request into the VU's buffers.
func (v *vu) buildRequest(rt *stepRT) bool {
	s := rt.s
	q := &v.req
	env := &v.env
	cfg := &v.eng.plan.HTTP
	q.method = s.Method
	q.head = s.Method == "HEAD"
	q.url = nil
	q.close = false
	q.cookie = nil
	q.keepBody = s.NeedBody
	q.bodyLimit = cfg.MaxBodyRead
	q.errPrefix = v.eng.opts.SampleBodyBytes
	q.contentType = s.ContentType
	q.userAgent = !s.HasUserAgent
	q.gzip = !s.HasAcceptEncoding

	switch {
	case s.URL.Static != nil:
		q.origin, q.uri, q.url = s.URL.Static.Origin, rt.uri, s.URL.Static.URL
	case s.URL.Origin != nil:
		v.uriBuf = s.URL.Path.Append(v.uriBuf[:0], env)
		q.origin, q.uri = s.URL.Origin, v.uriBuf
	default:
		v.urlBuf = s.URL.Full.Append(v.urlBuf[:0], env)
		scheme, auth, uri, ok := scenario.SplitURL(v.urlBuf)
		if !ok {
			return false
		}
		o := v.originFor(scheme, auth)
		if o == nil {
			return false
		}
		if len(uri) == 0 {
			uri = slash
		}
		q.origin, q.uri = o, uri
	}

	// Render dynamic header values first (the buffer may grow), then
	// slice them.
	v.hdrBuf = v.hdrBuf[:0]
	v.hdrOff = v.hdrOff[:0]
	for i, h := range s.Headers {
		if rt.hdrBytes[i] == nil {
			v.hdrBuf = h.Value.Append(v.hdrBuf, env)
		}
		v.hdrOff = append(v.hdrOff, len(v.hdrBuf))
	}
	q.headers = q.headers[:0]
	start := 0
	for i, h := range s.Headers {
		rh := reqHeader{name: h.Name, canon: h.Canonical}
		if b := rt.hdrBytes[i]; b != nil {
			rh.value, rh.static = b, rt.hdrStd[i]
		} else {
			rh.value = v.hdrBuf[start:v.hdrOff[i]]
			start = v.hdrOff[i]
		}
		q.headers = append(q.headers, rh)
	}

	switch {
	case s.Body == nil:
		q.body = nil
	case rt.bodyStatic:
		q.body = rt.body
	default:
		v.bodyBuf = s.Body.Append(v.bodyBuf[:0], env)
		q.body = v.bodyBuf
	}
	return true
}

var slash = []byte("/")

// originFor returns a cached origin for a dynamic URL.
func (v *vu) originFor(scheme, auth []byte) *scenario.Origin {
	v.keyBuf = append(append(append(v.keyBuf[:0], scheme...), "://"...), auth...)
	if o, ok := v.origins[string(v.keyBuf)]; ok {
		return o
	}
	o, err := scenario.NewOrigin(string(scheme), string(auth))
	if err != nil {
		return nil
	}
	if v.origins == nil || len(v.origins) > 64 {
		v.origins = map[string]*scenario.Origin{}
	}
	v.origins[string(v.keyBuf)] = o
	return o
}

// addCookies sets the Cookie header from the jar.
func (v *vu) addCookies(rt *stepRT) {
	if v.jar == nil {
		return
	}
	cookies := v.jar.Cookies(v.req.parsedURL())
	if len(cookies) == 0 {
		v.req.cookie = nil
		return
	}
	b := v.cookieBuf[:0]
	if i := rt.userCookie; i >= 0 && i < len(v.req.headers) && v.req.headers[i].canon == "Cookie" {
		b = append(b, v.req.headers[i].value...)
		b = append(b, "; "...)
	}
	for i, c := range cookies {
		if i > 0 {
			b = append(b, "; "...)
		}
		b = append(b, c.Name...)
		b = append(b, '=')
		b = append(b, c.Value...)
	}
	v.cookieBuf = b
	if i := rt.userCookie; i >= 0 && i < len(v.req.headers) && v.req.headers[i].canon == "Cookie" {
		v.req.headers[i].value, v.req.headers[i].static = b, nil
		return
	}
	v.req.cookie = b
}

// storeCookies saves Set-Cookie headers of the response in the jar.
func (v *vu) storeCookies() {
	r := &v.resp
	var cookies []*http.Cookie
	if r.stdHeader != nil {
		for _, line := range r.stdHeader["Set-Cookie"] {
			if c, err := http.ParseSetCookie(line); err == nil {
				cookies = append(cookies, c)
			}
		}
	} else {
		for i := range r.raw {
			h := &r.raw[i]
			if n := r.rawName(h); len(n) == 10 && equalFoldBytes(n, "set-cookie") {
				if c, err := http.ParseSetCookie(string(r.rawValue(h))); err == nil {
					cookies = append(cookies, c)
				}
			}
		}
	}
	if len(cookies) == 0 {
		return
	}
	if v.jar == nil {
		v.jar, _ = cookiejar.New(nil)
	}
	v.jar.SetCookies(v.req.parsedURL(), cookies)
}

func isRedirect(code int) bool {
	switch code {
	case 301, 302, 303, 307, 308:
		return true
	}
	return false
}

// followRedirect prepares v.req for the next hop. It returns false when
// the response is final.
func (v *vu) followRedirect() bool {
	loc, ok := v.resp.header("Location")
	if !ok || len(loc) == 0 {
		return false
	}
	base := v.req.parsedURL()
	ref, err := url.Parse(string(loc))
	if err != nil {
		return false
	}
	next := base.ResolveReference(ref)
	o := v.originFor([]byte(next.Scheme), []byte(next.Host))
	if o == nil {
		return false
	}
	q := &v.req
	code := v.resp.status
	if (code == 303 && q.method != "HEAD") || ((code == 301 || code == 302) && q.method == "POST") {
		q.method, q.head = "GET", false
		q.body, q.contentType = nil, ""
	}
	if o.Host != q.origin.Host {
		// Do not leak credentials to another host.
		kept := q.headers[:0]
		for _, h := range q.headers {
			if h.canon != "Authorization" && h.canon != "Cookie" && h.canon != "Proxy-Authorization" {
				kept = append(kept, h)
			}
		}
		q.headers = kept
	}
	next.Fragment = ""
	v.redirURI = append(v.redirURI[:0], next.RequestURI()...)
	q.origin, q.uri, q.url = o, v.redirURI, next
	return true
}

// runHTTP executes one step, including redirects, extraction, assertions
// and recording. It returns false when the iteration must end.
func (v *vu) runHTTP(rt *stepRT, planned int64) bool {
	e := v.eng
	s := rt.s
	cfg := &e.plan.HTTP
	if !v.buildRequest(rt) {
		v.recordFailure(rt, planned, metrics.ErrOther, "URL 无效："+string(v.urlBuf))
		return true
	}
	stepStart := planned
	for hop := 0; ; hop++ {
		if !v.takeRequestSlot() {
			return false
		}
		if cfg.Cookies {
			v.addCookies(rt)
		}
		r := &v.resp
		r.reset()
		err := v.client.do(&v.req, r)
		if e.aborted.Load() {
			return false // interrupted by a hard stop: not recorded
		}
		start := r.start
		if hop == 0 && planned != 0 {
			start = planned
		}
		if stepStart == 0 {
			stepStart = r.start
		}
		if err != nil {
			kind := classify(err)
			v.record(s.Index, start, kind)
			v.maybeSample(rt, kind, err.Error(), start)
			return true
		}
		if cfg.Cookies {
			v.storeCookies()
		}
		if cfg.FollowRedirects && isRedirect(r.status) && hop < cfg.MaxRedirects {
			if v.followRedirect() {
				v.record(s.Index, start, metrics.ErrNone)
				if v.stopped() {
					return false // stopping: do not start the next hop
				}
				continue
			}
		}
		kind, msg := v.evaluate(rt, time.Duration(r.end-stepStart))
		v.record(s.Index, start, kind)
		v.maybeSample(rt, kind, msg, start)
		return true
	}
}

// evaluate applies the status rule, extractors and assertions to the
// final response.
func (v *vu) evaluate(rt *stepRT, stepDur time.Duration) (metrics.ErrorKind, string) {
	s := rt.s
	r := &v.resp
	kind, msg := metrics.ErrNone, ""
	if !s.HasStatusAssert {
		switch {
		case r.status >= 500:
			kind, msg = metrics.ErrHTTP5xx, "HTTP "+strconv.Itoa(r.status)
		case r.status >= 400:
			kind, msg = metrics.ErrHTTP4xx, "HTTP "+strconv.Itoa(r.status)
		}
	}
	if len(s.Extract) == 0 && len(s.Assert) == 0 {
		return kind, msg
	}
	view := scenario.ResponseView{Status: r.status, Duration: stepDur, Body: v.decodedBody(), Header: v.headerFn}
	for i := range s.Extract {
		ex := &s.Extract[i]
		out, ok := ex.Extract(v.extract[:0], &view)
		v.extract = out
		if ok {
			v.env.Vars[ex.Slot] = append(v.env.Vars[ex.Slot][:0], out...)
		} else if kind == metrics.ErrNone {
			kind, msg = metrics.ErrAssertion, "提取变量 "+ex.Name+" 失败（"+ex.Expr+" 没有匹配）"
		}
	}
	for i := range s.Assert {
		if ok, m := s.Assert[i].Check(&view); !ok && kind == metrics.ErrNone {
			kind, msg = metrics.ErrAssertion, m
		}
	}
	return kind, msg
}

const maxDecodedBody = 64 << 20

// decodedBody returns the retained body, gunzipped if needed.
func (v *vu) decodedBody() []byte {
	r := &v.resp
	if !r.gzip || len(r.body) == 0 {
		return r.body
	}
	v.gzSrc.Reset(r.body)
	if v.gz == nil {
		gz, err := gzip.NewReader(&v.gzSrc)
		if err != nil {
			return r.body
		}
		v.gz = gz
	} else if err := v.gz.Reset(&v.gzSrc); err != nil {
		return r.body
	}
	v.gzBuf = v.gzBuf[:0]
	for len(v.gzBuf) < maxDecodedBody {
		old := len(v.gzBuf)
		v.gzBuf = growLen(v.gzBuf, 4096)
		n, err := v.gz.Read(v.gzBuf[old:])
		v.gzBuf = v.gzBuf[:old+n]
		if err == io.EOF {
			break
		}
		if err != nil {
			break // a truncated body still yields its prefix
		}
	}
	return v.gzBuf
}

// record books the current hop.
func (v *vu) record(step int, start int64, kind metrics.ErrorKind) {
	r := &v.resp
	m := &v.mrec
	*m = metrics.RequestRecord{
		Step:             step,
		Status:           r.status,
		Error:            kind,
		Latency:          time.Duration(r.end - start),
		Sent:             r.sent,
		Received:         r.recv,
		OverheadSent:     r.overSent,
		OverheadReceived: r.overRecv,
	}
	if r.newConn {
		if r.dial.dns > 0 {
			m.SetPhase(metrics.PhaseDNS, r.dial.dns)
		}
		m.SetPhase(metrics.PhaseConnect, r.dial.connect)
		if r.dial.tls > 0 {
			m.SetPhase(metrics.PhaseTLS, r.dial.tls)
		}
	}
	if r.firstByte > 0 && r.writeStart > 0 {
		m.SetPhase(metrics.PhaseFirstByte, durOf(r.writeStart, r.firstByte))
		m.SetPhase(metrics.PhaseDownload, durOf(r.firstByte, r.end))
	}
	v.rec.RecordRequest(m)
}

// recordFailure books a step that failed before any request was sent.
func (v *vu) recordFailure(rt *stepRT, planned int64, kind metrics.ErrorKind, msg string) {
	v.resp.reset()
	now := nanotime()
	v.resp.start, v.resp.end = now, now
	start := now
	if planned != 0 {
		start = planned
	}
	v.record(rt.s.Index, start, kind)
	v.maybeSample(rt, kind, msg, start)
}

// maybeSample captures a failed or slow request if this second's budget
// for its category allows.
func (v *vu) maybeSample(rt *stepRT, kind metrics.ErrorKind, msg string, start int64) {
	r := &v.resp
	dur := time.Duration(r.end - start)
	sk := metrics.SampleKind(kind)
	if kind == metrics.ErrNone {
		th := v.eng.opts.SlowThreshold
		if th < 0 || dur < th {
			return
		}
		sk = metrics.SampleSlow
	}
	if !v.rec.WantSample(sk) {
		return
	}
	q := &v.req
	smp := metrics.Sample{
		Kind:     sk,
		Step:     rt.s.Index,
		StepID:   rt.s.ID,
		Time:     time.Now().Add(-dur),
		Method:   q.method,
		Status:   r.status,
		Error:    msg,
		Duration: dur,
	}
	if q.origin != nil {
		smp.URL = q.fullURL()
	}
	for _, h := range q.headers {
		smp.RequestHeaders = append(smp.RequestHeaders, metrics.Header{Name: h.name, Value: metrics.RedactHeader(h.name, string(h.value))})
	}
	if len(q.cookie) > 0 {
		smp.RequestHeaders = append(smp.RequestHeaders, metrics.Header{Name: "Cookie", Value: "***"})
	}
	v.resp.eachHeader(func(name string, value []byte) {
		smp.ResponseHeaders = append(smp.ResponseHeaders, metrics.Header{Name: name, Value: metrics.RedactHeader(name, string(value))})
	})
	body := r.body
	if limit := v.eng.opts.SampleBodyBytes; len(body) > limit {
		body, smp.BodyTruncated = body[:limit], true
	}
	smp.Body = string(body)
	smp.BodyTruncated = smp.BodyTruncated || r.truncated
	v.rec.AddSample(&smp)
}
