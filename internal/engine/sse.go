package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
)

type sseResult struct {
	method        string
	url           string
	reqHeaders    []metrics.Header
	reqBody       string
	status        int
	respHeaders   []metrics.Header
	contentType   string
	body          []byte
	bodyTruncated bool
	sent, recv    int64
	overSent      int64
	overRecv      int64
	newConn       bool
	dial          dialTiming
	start         int64
	writeStart    int64
	firstByte     int64
	end           int64
	err           error
}

func (v *vu) ensureSSEVU() *stdVU {
	if sv, ok := v.client.(*stdVU); ok {
		return sv
	}
	if v.sseVU == nil && v.eng.sseClient != nil {
		v.sseVU = v.eng.sseClient.newVU(v).(*stdVU)
	}
	return v.sseVU
}

func (v *vu) resolveSSEURL(s *scenario.SSEStepPlan) (*scenario.Origin, *url.URL, string, bool) {
	switch {
	case s.URL.Static != nil:
		return s.URL.Static.Origin, s.URL.Static.URL, s.URL.Static.URL.String(), true
	case s.URL.Origin != nil:
		v.uriBuf = s.URL.Path.Append(v.uriBuf[:0], &v.env)
		reqURI := string(v.uriBuf)
		if reqURI == "" {
			reqURI = "/"
		}
		u := &url.URL{Scheme: s.URL.Origin.Scheme, Host: s.URL.Origin.Host, Opaque: reqURI}
		return s.URL.Origin, u, s.URL.Origin.Scheme + "://" + s.URL.Origin.Host + reqURI, true
	case s.URL.Full != nil:
		v.urlBuf = s.URL.Full.Append(v.urlBuf[:0], &v.env)
		scheme, auth, uri, ok := scenario.SplitURL(v.urlBuf)
		if !ok {
			return nil, nil, string(v.urlBuf), false
		}
		o := v.originFor(scheme, auth)
		if o == nil {
			return nil, nil, string(v.urlBuf), false
		}
		reqURI := "/"
		if len(uri) > 0 {
			reqURI = string(uri)
		}
		u := &url.URL{Scheme: o.Scheme, Host: o.Host, Opaque: reqURI}
		return o, u, o.Scheme + "://" + o.Host + reqURI, true
	default:
		return nil, nil, "", false
	}
}

func (v *vu) execSSE(s *scenario.SSEStepPlan, maxKeep int64) sseResult {
	var out sseResult
	out.method = s.Method
	out.start = nanotime()

	sv := v.ensureSSEVU()
	if sv == nil {
		out.end = nanotime()
		out.err = &hopError{phase: phaseDial, err: errors.New("SSE 传输层未初始化")}
		return out
	}

	origin, reqURL, fullURL, ok := v.resolveSSEURL(s)
	out.url = fullURL
	if !ok || origin == nil {
		out.end = nanotime()
		out.err = &hopError{phase: phaseDial, err: fmt.Errorf("SSE URL 无效：%s", fullURL)}
		return out
	}

	hdrList := v.renderHeaderTemplates(s.Headers)
	if err := v.applyDynamicAuthHeader(&hdrList, s.BasicUser, s.BasicPass, s.OAuth2); err != nil {
		out.end = nanotime()
		out.err = &hopError{phase: phaseDial, err: err}
		return out
	}

	var bodyBytes []byte
	if s.Body != nil {
		bodyBytes = s.Body.Append(nil, &v.env)
		out.reqBody = string(bodyBytes)
	}

	hdr := make(http.Header)
	hasAccept := false
	reqHost := origin.Host
	for _, h := range hdrList {
		val := string(h.value)
		if h.canon == "Host" {
			reqHost = val
		} else {
			hdr.Add(h.canon, val)
		}
		if h.canon == "Accept" {
			hasAccept = true
		}
		out.reqHeaders = append(out.reqHeaders, metrics.Header{Name: h.name, Value: val})
	}
	if !s.HasHost {
		out.reqHeaders = append(out.reqHeaders, metrics.Header{Name: "Host", Value: reqHost})
	}
	if !s.HasUserAgent {
		hdr.Set("User-Agent", UserAgent)
		out.reqHeaders = append(out.reqHeaders, metrics.Header{Name: "User-Agent", Value: UserAgent})
	}
	if !hasAccept {
		hdr.Set("Accept", "text/event-stream")
		out.reqHeaders = append(out.reqHeaders, metrics.Header{Name: "Accept", Value: "text/event-stream"})
	}
	if s.ContentType != "" && len(bodyBytes) > 0 && hdr.Get("Content-Type") == "" {
		hdr.Set("Content-Type", s.ContentType)
		out.reqHeaders = append(out.reqHeaders, metrics.Header{Name: "Content-Type", Value: s.ContentType})
	}

	var reqBodyReader io.ReadCloser
	if len(bodyBytes) > 0 {
		reqBodyReader = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	req := &http.Request{
		Method:        s.Method,
		URL:           reqURL,
		Host:          reqHost,
		Header:        hdr,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Body:          reqBodyReader,
		ContentLength: int64(len(bodyBytes)),
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = v.eng.plan.HTTP.TotalTimeout
	}
	ctx, cancel := context.WithTimeout(v.eng.hardCtx, timeout)
	defer cancel()

	var (
		conn      *countConn
		reused    bool
		gotConnAt int64
		firstByte int64
		preSent   int64
		preRecv   int64
	)
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			gotConnAt = nanotime()
			reused = info.Reused
			conn = connOf(info.Conn)
			if conn != nil {
				preSent, preRecv = conn.take()
			}
		},
		GotFirstResponseByte: func() {
			firstByte = nanotime()
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(ctx, trace))

	res, err := sv.tr.RoundTrip(req)
	out.writeStart = gotConnAt
	out.firstByte = firstByte
	out.overSent, out.overRecv = preSent, preRecv
	if conn != nil && !reused {
		out.newConn = true
		out.dial = dialTiming{dns: conn.dns, connect: conn.connect, tls: conn.tlsDur}
	}
	if err != nil {
		out.end = nanotime()
		if conn != nil {
			out.sent, out.recv = conn.take()
		}
		var he *hopError
		if errors.As(err, &he) {
			out.err = he
		} else if conn == nil {
			out.err = &hopError{phase: phaseDial, err: err}
		} else {
			out.err = &hopError{phase: phaseRead, err: err, noResponse: firstByte == 0}
		}
		return out
	}
	defer res.Body.Close()

	if out.firstByte == 0 {
		out.firstByte = nanotime()
	}
	out.status = res.StatusCode
	out.contentType = res.Header.Get("Content-Type")
	for k, vals := range res.Header {
		for _, val := range vals {
			out.respHeaders = append(out.respHeaders, metrics.Header{Name: k, Value: val})
		}
	}

	if res.StatusCode >= 400 {
		rawErr, _ := io.ReadAll(io.LimitReader(res.Body, maxKeep+1))
		if int64(len(rawErr)) > maxKeep {
			rawErr = rawErr[:maxKeep]
			out.bodyTruncated = true
		}
		out.body = rawErr
		out.end = nanotime()
		if conn != nil {
			out.sent, out.recv = conn.take()
		}
		_ = res.Body.Close()
		return out
	}

	payload, trunc, streamErr := readSSEEvents(res.Body, s.MaxEvents, s.StopContains, maxKeep)
	out.end = nanotime()
	if conn != nil {
		out.sent, out.recv = conn.take()
	}
	_ = res.Body.Close()
	out.body = payload
	out.bodyTruncated = trunc
	if streamErr != nil {
		out.err = &hopError{phase: phaseRead, err: streamErr}
	}
	return out
}

func readSSEEvents(r io.Reader, maxEvents int, stopContains []byte, maxKeep int64) ([]byte, bool, error) {
	br := bufio.NewReaderSize(r, 4096)
	var (
		dataLines []string
		buf       bytes.Buffer
		events    int
		truncated bool
	)
	flushEvent := func() bool {
		if len(dataLines) == 0 {
			return false
		}
		evData := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		events++

		if maxKeep > 0 && int64(buf.Len()) < maxKeep {
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			rem := maxKeep - int64(buf.Len())
			if int64(len(evData)) > rem {
				buf.WriteString(evData[:rem])
				truncated = true
			} else {
				buf.WriteString(evData)
			}
		} else if maxKeep > 0 {
			truncated = true
		}

		if maxEvents > 0 && events >= maxEvents {
			return true
		}
		if len(stopContains) > 0 && strings.Contains(evData, string(stopContains)) {
			return true
		}
		return false
	}

	for {
		line, err := br.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		if line != "" {
			if trimmed == "" {
				if flushEvent() {
					return buf.Bytes(), truncated, nil
				}
			} else if !strings.HasPrefix(trimmed, ":") {
				field, val, hasColon := strings.Cut(trimmed, ":")
				if hasColon && strings.HasPrefix(val, " ") {
					val = val[1:]
				}
				if field == "data" {
					dataLines = append(dataLines, val)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				flushEvent()
				return buf.Bytes(), truncated, nil
			}
			flushEvent()
			return buf.Bytes(), truncated, err
		}
	}
}

func (v *vu) runSSE(s *scenario.SSEStepPlan, planned int64) (bool, metrics.ErrorKind) {
	if !v.takeRequestSlot() {
		return false, metrics.ErrNone
	}
	maxKeep := v.eng.plan.HTTP.MaxBodyRead
	if maxKeep <= 0 {
		maxKeep = DefaultDebugMaxBodyBytes
	}
	sr := v.execSSE(s, maxKeep)
	if v.eng.aborted.Load() {
		v.finishRequestSlot()
		return false, metrics.ErrNone
	}
	start := sr.start
	if planned != 0 {
		start = planned
	}
	var kind metrics.ErrorKind
	var msg string
	if sr.err != nil {
		kind, msg = classify(sr.err), sr.err.Error()
	} else {
		initKind, initMsg := httpStatusInitOutcome(s.HasStatusAssert, sr.status)
		view := scenario.ResponseView{
			Status:   sr.status,
			Duration: time.Duration(sr.end - start),
			Body:     sr.body,
			Header: func(name string) ([]byte, bool) {
				val := wsHeaderValue(sr.respHeaders, name)
				if val == "" {
					return nil, false
				}
				return []byte(val), true
			},
		}
		kind, msg = v.evalNonHTTPUponResponse(initKind, initMsg, s.Extract, s.Assert, &view)
	}
	v.recordGeneric(s.Index, sr.status, kind, time.Duration(sr.end-start), sr.sent, sr.recv, sr.overSent, sr.overRecv, sr.newConn, sr.dial, sr.writeStart, sr.firstByte, sr.end)
	v.maybeSampleGeneric(s.Index, s.ID, sr.method, sr.url, sr.reqHeaders, sr.reqBody, sr.status, sr.respHeaders, sr.body, sr.bodyTruncated, kind, msg, start, sr.end)
	v.finishRequestSlot()
	if sr.err != nil && v.eng.plan.Load.Mode == scenario.LoadVUs && v.eng.plan.HTTP.ConnectBackoff > 0 && isConnectPhaseErr(sr.err) && !v.stopped() {
		v.sleep(v.eng.plan.HTTP.ConnectBackoff)
	}
	return true, kind
}

func (v *vu) runSSEDebug(s *scenario.SSEStepPlan, maxBody int64) DebugStepResult {
	res := DebugStepResult{
		StepID:   s.ID,
		StepName: s.Name,
	}
	sr := v.execSSE(s, maxBody)
	res.ActualRequest = DebugActualRequest{
		Method:  sr.method,
		URL:     sr.url,
		Headers: sr.reqHeaders,
		Body:    sr.reqBody,
	}
	res.Status = sr.status
	res.ContentType = sr.contentType
	res.ResponseHeaders = sr.respHeaders
	res.BytesSent = sr.sent + sr.overSent
	res.BytesReceived = sr.recv + sr.overRecv
	res.Timing = genericDebugTiming(sr.start, sr.writeStart, sr.firstByte, sr.end, sr.newConn, sr.dial, sr.err)
	if sr.err != nil {
		res.ErrorKind = classify(sr.err).String()
		res.Error = sr.err.Error()
		return res
	}
	setDebugBody(&res, sr.body, sr.bodyTruncated)
	initKind, initMsg := httpStatusInitOutcome(s.HasStatusAssert, sr.status)
	view := scenario.ResponseView{
		Status:   sr.status,
		Duration: time.Duration(sr.end - sr.start),
		Body:     sr.body,
		Header: func(name string) ([]byte, bool) {
			val := wsHeaderValue(sr.respHeaders, name)
			if val == "" {
				return nil, false
			}
			return []byte(val), true
		},
	}
	v.evalDebugResponse(initKind, initMsg, s.Extract, s.Assert, &view, &res)
	return res
}
