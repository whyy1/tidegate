package engine

import (
	"encoding/base64"
	"strconv"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
)

func (v *vu) renderHeaderTemplates(hdrs []scenario.HeaderTemplate) []reqHeader {
	if len(hdrs) == 0 {
		return nil
	}
	out := make([]reqHeader, len(hdrs))
	for i, h := range hdrs {
		var val []byte
		var static []string
		if h.Value.Static() {
			lit := h.Value.Literal()
			val = []byte(lit)
			static = []string{lit}
		} else {
			val = h.Value.Append(nil, &v.env)
		}
		out[i] = reqHeader{
			name:   h.Name,
			canon:  h.Canonical,
			value:  val,
			static: static,
		}
	}
	return out
}

func (v *vu) applyDynamicAuthHeader(headers *[]reqHeader, bu, bp *scenario.Template, oa *scenario.OAuth2Plan) error {
	if bu != nil {
		raw := bu.Append(nil, &v.env)
		raw = append(raw, ':')
		if bp != nil {
			raw = bp.Append(raw, &v.env)
		}
		enc := "Basic " + base64.StdEncoding.EncodeToString(raw)
		*headers = append(*headers, reqHeader{name: "Authorization", canon: "Authorization", value: []byte(enc)})
		return nil
	}
	if oa != nil {
		tok, err := v.eng.oauth2.token(v.eng.hardCtx, oa, &v.env)
		if err != nil {
			return err
		}
		enc := "Bearer " + tok
		*headers = append(*headers, reqHeader{name: "Authorization", canon: "Authorization", value: []byte(enc)})
	}
	return nil
}

func (v *vu) evalNonHTTPUponResponse(
	initKind metrics.ErrorKind,
	initMsg string,
	extract []scenario.Extractor,
	assert []scenario.Assertion,
	view *scenario.ResponseView,
) (metrics.ErrorKind, string) {
	kind, msg := initKind, initMsg
	for i := range extract {
		ex := &extract[i]
		out, ok := ex.Extract(v.extract[:0], view)
		v.extract = out
		if ok {
			v.env.Vars[ex.Slot] = append(v.env.Vars[ex.Slot][:0], out...)
		} else if kind == metrics.ErrNone {
			kind, msg = metrics.ErrAssertion, "提取变量 "+ex.Name+" 失败（"+ex.Expr+" 没有匹配）"
		}
	}
	for i := range assert {
		if ok, m := assert[i].Check(view); !ok && kind == metrics.ErrNone {
			kind, msg = metrics.ErrAssertion, m
		}
	}
	return kind, msg
}

func (v *vu) evalDebugResponse(
	initKind metrics.ErrorKind,
	initMsg string,
	extract []scenario.Extractor,
	assert []scenario.Assertion,
	view *scenario.ResponseView,
	res *DebugStepResult,
) {
	passed := initKind == metrics.ErrNone
	firstKind, firstMsg := initKind, initMsg

	if len(extract) > 0 {
		res.Extracts = make([]DebugExtractResult, 0, len(extract))
		for i := range extract {
			ex := &extract[i]
			val, ok := ex.Extract(v.extract[:0], view)
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
				if firstKind == metrics.ErrNone {
					firstKind = metrics.ErrAssertion
					firstMsg = er.Error
				}
			}
			res.Extracts = append(res.Extracts, er)
		}
	}

	if len(assert) > 0 {
		res.Asserts = make([]DebugAssertResult, 0, len(assert))
		for i := range assert {
			as := &assert[i]
			ok, msg := as.Check(view)
			ar := DebugAssertResult{
				Type:        as.Type,
				Desc:        as.Desc,
				Description: as.Description,
				Passed:      ok,
				Message:     msg,
			}
			if !ok {
				passed = false
				if firstKind == metrics.ErrNone {
					firstKind = metrics.ErrAssertion
					firstMsg = msg
				}
			}
			res.Asserts = append(res.Asserts, ar)
		}
	}

	res.Passed = passed
	if !passed {
		res.ErrorKind = firstKind.String()
		res.Error = firstMsg
	}
}

func (v *vu) recordGeneric(
	step int,
	status int,
	kind metrics.ErrorKind,
	dur time.Duration,
	sent, recv, overSent, overRecv int64,
	newConn bool,
	dial dialTiming,
	writeStart, firstByte, end int64,
) {
	if dur < 0 {
		dur = 0
	}
	m := metrics.RequestRecord{
		Step:             step,
		Status:           status,
		Error:            kind,
		Latency:          dur,
		Sent:             sent,
		Received:         recv,
		OverheadSent:     overSent,
		OverheadReceived: overRecv,
	}
	if newConn {
		if dial.dns > 0 {
			m.SetPhase(metrics.PhaseDNS, dial.dns)
		}
		m.SetPhase(metrics.PhaseConnect, dial.connect)
		if dial.tls > 0 {
			m.SetPhase(metrics.PhaseTLS, dial.tls)
		}
	}
	if firstByte > 0 && writeStart > 0 {
		m.SetPhase(metrics.PhaseFirstByte, durOf(writeStart, firstByte))
		m.SetPhase(metrics.PhaseDownload, durOf(firstByte, end))
	}
	v.rec.RecordRequest(&m)
}

func (v *vu) maybeSampleGeneric(
	step int,
	stepID string,
	method string,
	targetURL string,
	reqHeaders []metrics.Header,
	reqBody string,
	status int,
	respHeaders []metrics.Header,
	respBody []byte,
	bodyTrunc bool,
	kind metrics.ErrorKind,
	msg string,
	start, end int64,
) {
	dur := time.Duration(max(0, end-start))
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
	smp := metrics.Sample{
		Kind:          sk,
		Step:          step,
		StepID:        stepID,
		Time:          time.Now().Add(-dur),
		Method:        method,
		URL:           targetURL,
		Status:        status,
		Error:         msg,
		Duration:      dur,
		BodyTruncated: bodyTrunc,
	}
	for _, h := range reqHeaders {
		smp.RequestHeaders = append(smp.RequestHeaders, metrics.Header{
			Name:  h.Name,
			Value: metrics.RedactHeader(h.Name, h.Value),
		})
	}
	for _, h := range respHeaders {
		smp.ResponseHeaders = append(smp.ResponseHeaders, metrics.Header{
			Name:  h.Name,
			Value: metrics.RedactHeader(h.Name, h.Value),
		})
	}
	if limit := v.eng.opts.SampleBodyBytes; limit > 0 {
		if len(reqBody) > limit {
			smp.RequestBody = reqBody[:limit]
			smp.RequestBodyTruncated = true
		} else {
			smp.RequestBody = reqBody
		}
		if len(respBody) > limit {
			smp.Body = string(respBody[:limit])
			smp.BodyTruncated = true
		} else {
			smp.Body = string(respBody)
		}
	} else {
		smp.RequestBody = reqBody
		smp.Body = string(respBody)
	}
	v.rec.AddSample(&smp)
}

func genericDebugTiming(start, writeStart, firstByte, end int64, newConn bool, dial dialTiming, err error) DebugTiming {
	t := DebugTiming{
		Reused:  !newConn && err == nil,
		TotalMs: nanosToMs(max(0, end-start)),
	}
	if newConn {
		t.DNSMs = nanosToMs(int64(dial.dns))
		t.ConnectMs = nanosToMs(int64(dial.connect))
		t.TLSMs = nanosToMs(int64(dial.tls))
	}
	if firstByte > 0 && writeStart > 0 {
		t.FirstByteMs = nanosToMs(int64(durOf(writeStart, firstByte)))
		t.DownloadMs = nanosToMs(int64(durOf(firstByte, end)))
	}
	return t
}

func setDebugBody(res *DebugStepResult, body []byte, truncated bool) {
	res.BodyTruncated = truncated
	if len(body) == 0 {
		return
	}
	if text, ok := utf8TextOrTruncated(body, truncated); ok {
		res.Body = text
	} else {
		res.IsBinary = true
		res.BodyBase64 = base64.StdEncoding.EncodeToString(body)
	}
}

func httpStatusInitOutcome(hasStatusAssert bool, status int) (metrics.ErrorKind, string) {
	if hasStatusAssert {
		return metrics.ErrNone, ""
	}
	switch {
	case status >= 500:
		return metrics.ErrHTTP5xx, "HTTP " + strconv.Itoa(status)
	case status >= 400:
		return metrics.ErrHTTP4xx, "HTTP " + strconv.Itoa(status)
	default:
		return metrics.ErrNone, ""
	}
}
