package engine

import (
	"context"
	"encoding/base64"
	"strconv"
	"time"
	"unicode/utf8"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
)

// DefaultDebugMaxBodyBytes limits retained request/response bodies during
// interactive debugging and scenario dry-runs (4 MiB).
const DefaultDebugMaxBodyBytes = 4 << 20

// DebugOptions configures RunDebug.
type DebugOptions struct {
	Client       ClientKind
	MaxBodyBytes int64
	SkipThink    bool
	StopOnError  bool
}

// DebugActualRequest is the concrete HTTP request rendered for a debug step.
type DebugActualRequest struct {
	Method        string           `json:"method"`
	URL           string           `json:"url"`
	Headers       []metrics.Header `json:"headers"`
	Body          string           `json:"body,omitempty"`
	BodyTruncated bool             `json:"body_truncated,omitempty"`
}

// DebugTiming breaks down one hop's latency into five phases.
type DebugTiming struct {
	DNSMs       float64 `json:"dns_ms"`
	ConnectMs   float64 `json:"connect_ms"`
	TLSMs       float64 `json:"tls_ms"`
	FirstByteMs float64 `json:"first_byte_ms"`
	DownloadMs  float64 `json:"download_ms"`
	TotalMs     float64 `json:"total_ms"`
	Reused      bool    `json:"reused"`
}

// DebugHop records one redirect response prior to the final response.
type DebugHop struct {
	Method     string      `json:"method"`
	URL        string      `json:"url"`
	Status     int         `json:"status"`
	Location   string      `json:"location,omitempty"`
	DurationMs float64     `json:"duration_ms"`
	Timing     DebugTiming `json:"timing"`
}

// DebugExtractResult is the outcome of one variable extractor.
type DebugExtractResult struct {
	Name        string `json:"name"`
	From        string `json:"from"`
	Expr        string `json:"expr"`
	Value       string `json:"value,omitempty"`
	Matched     bool   `json:"matched"`
	Description string `json:"description,omitempty"`
	Error       string `json:"error,omitempty"`
}

// DebugAssertResult is the outcome of one response assertion.
type DebugAssertResult struct {
	Type        string `json:"type"`
	Desc        string `json:"desc"`
	Description string `json:"description,omitempty"`
	Passed      bool   `json:"passed"`
	Message     string `json:"message,omitempty"`
}

// DebugStepResult holds the complete request, response, timing, extractor,
// and assertion details of one executed scenario step.
type DebugStepResult struct {
	StepID          string               `json:"step_id"`
	StepName        string               `json:"step_name"`
	Passed          bool                 `json:"passed"`
	ErrorKind       string               `json:"error_kind,omitempty"`
	Error           string               `json:"error,omitempty"`
	ActualRequest   DebugActualRequest   `json:"actual_request"`
	Redirects       []DebugHop           `json:"redirects,omitempty"`
	Status          int                  `json:"status"`
	ResponseHeaders []metrics.Header     `json:"response_headers,omitempty"`
	Body            string               `json:"body,omitempty"`
	BodyBase64      string               `json:"body_base64,omitempty"`
	IsBinary        bool                 `json:"is_binary,omitempty"`
	BodyTruncated   bool                 `json:"body_truncated,omitempty"`
	ContentType     string               `json:"content_type,omitempty"`
	BytesSent       int64                `json:"bytes_sent"`
	BytesReceived   int64                `json:"bytes_received"`
	Timing          DebugTiming          `json:"timing"`
	Extracts        []DebugExtractResult `json:"extracts,omitempty"`
	Asserts         []DebugAssertResult  `json:"asserts,omitempty"`
	ExtractedVars   map[string]string    `json:"extracted_vars,omitempty"`
}

// DebugRunResult is the full output of a single-API debug request or a
// multi-step scenario dry-run.
type DebugRunResult struct {
	Passed     bool                         `json:"passed"`
	DurationMs float64                      `json:"duration_ms"`
	Steps      []DebugStepResult            `json:"steps"`
	DataRows   map[string]map[string]string `json:"data_rows,omitempty"`
	FinalVars  map[string]string            `json:"final_vars,omitempty"`
}

// RunDebug executes one complete iteration of plan and captures full request,
// response, waterfall timing, redirect, extraction, and assertion details for
// every executed step without short-circuiting extractors or assertions.
func RunDebug(ctx context.Context, plan *scenario.Plan, opts DebugOptions) (*DebugRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = DefaultDebugMaxBodyBytes
	}
	engOpts := Options{
		Client:          opts.Client,
		Shards:          1,
		SampleBodyBytes: int(min(maxBody, 64<<10)),
	}
	e, err := New(plan, engOpts)
	if err != nil {
		return nil, err
	}
	// Clean up all background resources created by New() and the debug VU.
	var v *vu
	stopWatch := context.AfterFunc(ctx, e.Abort)
	defer func() {
		stopWatch()
		if v != nil {
			v.client.closeAll()
			if v.timer != nil {
				v.timer.Stop()
			}
			if v.gz != nil {
				_ = v.gz.Close()
			}
			e.vusMu.Lock()
			delete(e.vus, v)
			e.vusMu.Unlock()
		}
		e.client.close()
		e.hardCancel()
		e.queue.close()
	}()

	v = newVU(e, 0)
	e.vusMu.Lock()
	e.vus[v] = struct{}{}
	if e.aborted.Load() {
		v.client.abort()
	}
	e.vusMu.Unlock()

	runStart := nanotime()
	v.pickRows()

	out := &DebugRunResult{
		Passed: true,
		Steps:  make([]DebugStepResult, 0, len(e.steps)),
	}
	if len(e.data) > 0 {
		dataRows := make(map[string]map[string]string)
		for i, d := range e.data {
			if !d.ds.Used || len(v.env.Rows[i]) == 0 {
				continue
			}
			rowMap := make(map[string]string, len(d.ds.Columns))
			for ci, col := range d.ds.Columns {
				if ci < len(v.env.Rows[i]) {
					rowMap[col] = v.env.Rows[i][ci]
				}
			}
			dataRows[d.ds.Name] = rowMap
		}
		if len(dataRows) > 0 {
			out.DataRows = dataRows
		}
	}

	for idx, rt := range e.steps {
		if err := ctx.Err(); err != nil {
			out.Passed = false
			return nil, err
		}
		stepRes := v.runStepDebug(rt, maxBody)
		out.Steps = append(out.Steps, stepRes)
		if !stepRes.Passed {
			out.Passed = false
			if opts.StopOnError {
				break
			}
		}
		if !opts.SkipThink && idx < len(e.steps)-1 && rt.s.Think.Kind != scenario.ThinkNone {
			if d := rt.s.Think.Duration(v.rng); d > 0 {
				if !sleepContext(ctx, d) {
					out.Passed = false
					return nil, ctx.Err()
				}
			}
		}
	}

	out.DurationMs = nanosToMs(nanotime() - runStart)
	if len(plan.Vars) > 0 {
		vars := make(map[string]string, len(plan.Vars))
		for i, name := range plan.Vars {
			if i < len(v.env.Vars) {
				vars[name] = string(v.env.Vars[i])
			}
		}
		out.FinalVars = vars
	}
	return out, nil
}

func (v *vu) runStepDebug(rt *stepRT, maxBody int64) DebugStepResult {
	s := rt.s
	cfg := &v.eng.plan.HTTP
	res := DebugStepResult{
		StepID:   s.ID,
		StepName: s.Name,
	}

	if !v.buildRequest(rt) {
		res.ErrorKind = metrics.ErrOther.String()
		res.Error = "URL 无效：" + string(v.urlBuf)
		res.ActualRequest = DebugActualRequest{
			Method: s.Method,
			URL:    string(v.urlBuf),
		}
		return res
	}

	var stepStart int64
	for hop := 0; ; hop++ {
		if cfg.Cookies {
			v.addCookies(rt)
		}
		v.req.keepBody = true
		v.req.bodyLimit = maxBody

		res.ActualRequest = snapshotActualRequest(s, &v.req, maxBody)
		hopMethod := v.req.method
		hopURL := res.ActualRequest.URL

		r := &v.resp
		r.reset()
		err := v.client.do(&v.req, r)
		if hop == 0 {
			stepStart = r.start
		}
		res.BytesSent += r.sent + r.overSent
		res.BytesReceived += r.recv + r.overRecv

		timing := hopDebugTiming(r, err)
		if err != nil {
			res.Timing = timing
			res.ErrorKind = classify(err).String()
			res.Error = err.Error()
			return res
		}

		if cfg.Cookies {
			v.storeCookies()
		}

		if cfg.FollowRedirects && isRedirect(r.status) && hop < cfg.MaxRedirects {
			locBytes, _ := r.header("Location")
			loc := string(locBytes)
			if v.followRedirect() {
				res.Redirects = append(res.Redirects, DebugHop{
					Method:     hopMethod,
					URL:        hopURL,
					Status:     r.status,
					Location:   loc,
					DurationMs: timing.TotalMs,
					Timing:     timing,
				})
				continue
			}
		}

		res.Status = r.status
		res.Timing = timing
		r.eachHeader(func(name string, value []byte) {
			res.ResponseHeaders = append(res.ResponseHeaders, metrics.Header{
				Name:  name,
				Value: string(value),
			})
		})
		if ct, ok := r.header("Content-Type"); ok {
			res.ContentType = string(ct)
		}

		decoded := v.decodedBody()
		if int64(len(decoded)) > maxBody {
			decoded = decoded[:maxBody]
			res.BodyTruncated = true
		} else {
			res.BodyTruncated = r.truncated
		}
		if len(decoded) > 0 {
			if text, ok := utf8TextOrTruncated(decoded, res.BodyTruncated); ok {
				res.Body = text
			} else {
				res.IsBinary = true
				res.BodyBase64 = base64.StdEncoding.EncodeToString(decoded)
			}
		}

		stepDur := time.Duration(r.end - stepStart)
		view := scenario.ResponseView{
			Status:   r.status,
			Duration: stepDur,
			Body:     decoded,
			Header:   v.headerFn,
		}

		passed := true
		firstErrKind := metrics.ErrNone
		var firstErrMsg string

		if !s.HasStatusAssert {
			switch {
			case r.status >= 500:
				passed = false
				firstErrKind = metrics.ErrHTTP5xx
				firstErrMsg = "HTTP " + strconv.Itoa(r.status)
			case r.status >= 400:
				passed = false
				firstErrKind = metrics.ErrHTTP4xx
				firstErrMsg = "HTTP " + strconv.Itoa(r.status)
			}
		}

		if len(s.Extract) > 0 {
			res.Extracts = make([]DebugExtractResult, 0, len(s.Extract))
			for i := range s.Extract {
				ex := &s.Extract[i]
				val, ok := ex.Extract(v.extract[:0], &view)
				v.extract = val
				er := DebugExtractResult{
					Name:        ex.Name,
					From:        extractSourceName(ex.Source),
					Expr:        ex.Expr,
					Description: ex.Description,
					Matched:     ok,
				}
				if ok {
					sval := string(val)
					er.Value = sval
					v.env.Vars[ex.Slot] = append(v.env.Vars[ex.Slot][:0], val...)
					if res.ExtractedVars == nil {
						res.ExtractedVars = make(map[string]string)
					}
					res.ExtractedVars[ex.Name] = sval
				} else {
					passed = false
					er.Error = "提取变量 " + ex.Name + " 失败（" + ex.Expr + " 没有匹配）"
					if firstErrKind == metrics.ErrNone {
						firstErrKind = metrics.ErrAssertion
						firstErrMsg = er.Error
					}
				}
				res.Extracts = append(res.Extracts, er)
			}
		}

		if len(s.Assert) > 0 {
			res.Asserts = make([]DebugAssertResult, 0, len(s.Assert))
			for i := range s.Assert {
				as := &s.Assert[i]
				ok, msg := as.Check(&view)
				ar := DebugAssertResult{
					Type:        as.Type,
					Desc:        as.Desc,
					Description: as.Description,
					Passed:      ok,
					Message:     msg,
				}
				if !ok {
					passed = false
					if firstErrKind == metrics.ErrNone {
						firstErrKind = metrics.ErrAssertion
						firstErrMsg = msg
					}
				}
				res.Asserts = append(res.Asserts, ar)
			}
		}

		res.Passed = passed
		if !passed {
			res.ErrorKind = firstErrKind.String()
			res.Error = firstErrMsg
		}
		return res
	}
}

func snapshotActualRequest(s *scenario.HTTPStep, q *hopRequest, maxBody int64) DebugActualRequest {
	fullURL := ""
	if q.origin != nil {
		fullURL = q.fullURL()
	}
	headers := make([]metrics.Header, 0, len(q.headers)+6)
	hasHost := s.HasHost
	for _, h := range q.headers {
		if h.canon == "Host" {
			hasHost = true
		}
		headers = append(headers, metrics.Header{
			Name:  h.name,
			Value: string(h.value),
		})
	}
	if !hasHost && q.origin != nil {
		headers = append(headers, metrics.Header{Name: "Host", Value: q.origin.Host})
	}
	if q.userAgent {
		headers = append(headers, metrics.Header{Name: "User-Agent", Value: UserAgent})
	}
	if q.gzip {
		headers = append(headers, metrics.Header{Name: "Accept-Encoding", Value: "gzip"})
	}
	if q.contentType != "" && len(q.body) > 0 {
		headers = append(headers, metrics.Header{Name: "Content-Type", Value: q.contentType})
	}
	if len(q.cookie) > 0 {
		headers = append(headers, metrics.Header{Name: "Cookie", Value: string(q.cookie)})
	}
	if len(q.body) > 0 || q.method == "POST" || q.method == "PUT" || q.method == "PATCH" {
		headers = append(headers, metrics.Header{
			Name:  "Content-Length",
			Value: strconv.FormatInt(int64(len(q.body)), 10),
		})
	}

	reqBody := q.body
	var truncated bool
	if int64(len(reqBody)) > maxBody {
		reqBody = reqBody[:maxBody]
		truncated = true
	}
	bodyStr := ""
	if len(reqBody) > 0 {
		if text, ok := utf8TextOrTruncated(reqBody, truncated); ok {
			bodyStr = text
		} else {
			bodyStr = "[二进制请求体，共 " + strconv.Itoa(len(q.body)) + " 字节]"
		}
	}
	return DebugActualRequest{
		Method:        q.method,
		URL:           fullURL,
		Headers:       headers,
		Body:          bodyStr,
		BodyTruncated: truncated,
	}
}

func hopDebugTiming(r *hopResponse, err error) DebugTiming {
	t := DebugTiming{
		Reused:  !r.newConn && err == nil,
		TotalMs: nanosToMs(max(0, r.end-r.start)),
	}
	if r.newConn {
		t.DNSMs = nanosToMs(int64(r.dial.dns))
		t.ConnectMs = nanosToMs(int64(r.dial.connect))
		t.TLSMs = nanosToMs(int64(r.dial.tls))
	}
	if r.firstByte > 0 && r.writeStart > 0 {
		t.FirstByteMs = nanosToMs(int64(durOf(r.writeStart, r.firstByte)))
		t.DownloadMs = nanosToMs(int64(durOf(r.firstByte, r.end)))
	}
	return t
}

func nanosToMs(ns int64) float64 {
	if ns <= 0 {
		return 0
	}
	return float64(ns) / 1e6
}

func extractSourceName(src scenario.ExtractSource) string {
	switch src {
	case scenario.ExtractJSONPath:
		return "jsonpath"
	case scenario.ExtractRegex:
		return "regex"
	case scenario.ExtractHeader:
		return "header"
	default:
		return "unknown"
	}
}

// utf8TextOrTruncated returns (string(b), true) when b is valid UTF-8, or when
// truncated is true and trimming at most 3 incomplete trailing rune bytes makes
// it valid UTF-8.
func utf8TextOrTruncated(b []byte, truncated bool) (string, bool) {
	if utf8.Valid(b) {
		return string(b), true
	}
	if !truncated {
		return "", false
	}
	for trim := 1; trim <= 3 && trim < len(b); trim++ {
		sub := b[:len(b)-trim]
		if utf8.Valid(sub) {
			return string(sub), true
		}
	}
	return "", false
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
