package engine

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type wsResult struct {
	url             string
	reqHeaders      []metrics.Header
	reqBodySummary  string
	status          int
	respHeaders     []metrics.Header
	body            []byte
	bodyTruncated   bool
	sent, recv      int64
	overSent        int64
	overRecv        int64
	newConn         bool
	dial            dialTiming
	start           int64
	writeStart      int64
	firstByte       int64
	end             int64
	err             error
}

func (v *vu) wsOriginFor(scheme, auth []byte) *scenario.Origin {
	v.keyBuf = append(append(append(v.keyBuf[:0], scheme...), "://"...), auth...)
	if o, ok := v.origins[string(v.keyBuf)]; ok {
		return o
	}
	o, err := scenario.NewWSOrigin(string(scheme), string(auth))
	if err != nil {
		return nil
	}
	if v.origins == nil || len(v.origins) > 64 {
		v.origins = map[string]*scenario.Origin{}
	}
	v.origins[string(v.keyBuf)] = o
	return o
}

func (v *vu) resolveWSTarget(s *scenario.WSStep) (*scenario.Origin, string, string, bool) {
	switch {
	case s.URL.Static != nil:
		return s.URL.Static.Origin, s.URL.Static.RequestURI, s.URL.Static.URL.String(), true
	case s.URL.Origin != nil:
		v.uriBuf = s.URL.Path.Append(v.uriBuf[:0], &v.env)
		uri := string(v.uriBuf)
		if uri == "" {
			uri = "/"
		}
		return s.URL.Origin, uri, s.URL.Origin.Scheme + "://" + s.URL.Origin.Host + uri, true
	case s.URL.Full != nil:
		v.urlBuf = s.URL.Full.Append(v.urlBuf[:0], &v.env)
		scheme, auth, uri, ok := scenario.SplitURL(v.urlBuf)
		if !ok {
			return nil, "", string(v.urlBuf), false
		}
		o := v.wsOriginFor(scheme, auth)
		if o == nil {
			return nil, "", string(v.urlBuf), false
		}
		reqURI := "/"
		if len(uri) > 0 {
			reqURI = string(uri)
		}
		return o, reqURI, o.Scheme + "://" + o.Host + reqURI, true
	default:
		return nil, "", "", false
	}
}

func (v *vu) execWS(s *scenario.WSStep, maxKeep int64) wsResult {
	var out wsResult
	out.start = nanotime()

	origin, reqURI, fullURL, ok := v.resolveWSTarget(s)
	out.url = fullURL
	if !ok || origin == nil {
		out.end = nanotime()
		out.err = &hopError{phase: phaseDial, err: fmt.Errorf("WebSocket URL 无效：%s", fullURL)}
		return out
	}

	reqHdrs := v.renderHeaderTemplates(s.Headers)
	var nonce [16]byte
	for i := 0; i < 2; i++ {
		binary.LittleEndian.PutUint64(nonce[i*8:], v.rng.Uint64())
	}
	secKey := base64.StdEncoding.EncodeToString(nonce[:])

	var reqBuf bytes.Buffer
	fmt.Fprintf(&reqBuf, "GET %s HTTP/1.1\r\n", reqURI)
	if !s.HasHost {
		fmt.Fprintf(&reqBuf, "Host: %s\r\n", origin.Host)
		out.reqHeaders = append(out.reqHeaders, metrics.Header{Name: "Host", Value: origin.Host})
	}
	fmt.Fprintf(&reqBuf, "Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", secKey)
	out.reqHeaders = append(out.reqHeaders,
		metrics.Header{Name: "Upgrade", Value: "websocket"},
		metrics.Header{Name: "Connection", Value: "Upgrade"},
		metrics.Header{Name: "Sec-WebSocket-Key", Value: secKey},
		metrics.Header{Name: "Sec-WebSocket-Version", Value: "13"},
	)
	if len(s.Subprotocols) > 0 {
		sp := strings.Join(s.Subprotocols, ", ")
		fmt.Fprintf(&reqBuf, "Sec-WebSocket-Protocol: %s\r\n", sp)
		out.reqHeaders = append(out.reqHeaders, metrics.Header{Name: "Sec-WebSocket-Protocol", Value: sp})
	}
	if !s.HasUserAgent {
		fmt.Fprintf(&reqBuf, "User-Agent: %s\r\n", UserAgent)
		out.reqHeaders = append(out.reqHeaders, metrics.Header{Name: "User-Agent", Value: UserAgent})
	}
	for _, h := range reqHdrs {
		val := string(h.value)
		fmt.Fprintf(&reqBuf, "%s: %s\r\n", h.name, val)
		out.reqHeaders = append(out.reqHeaders, metrics.Header{Name: h.name, Value: val})
	}
	reqBuf.WriteString("\r\n")

	// Pre-render outgoing frames before dialing so syntax/base64 issues never consume a socket.
	type frameOut struct {
		opcode  byte
		payload []byte
	}
	frames := make([]frameOut, 0, len(s.Messages))
	var msgSummary strings.Builder
	for i, m := range s.Messages {
		raw := m.Data.Append(nil, &v.env)
		if i > 0 {
			msgSummary.WriteByte('\n')
		}
		if m.Binary {
			dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
			if err != nil {
				out.end = nanotime()
				out.err = &hopError{phase: phaseWrite, err: fmt.Errorf("WebSocket 二进制消息解码失败：%w", err)}
				return out
			}
			frames = append(frames, frameOut{opcode: 0x2, payload: dec})
			fmt.Fprintf(&msgSummary, "[binary %dB]", len(dec))
		} else {
			frames = append(frames, frameOut{opcode: 0x1, payload: raw})
			msgSummary.Write(raw)
		}
	}
	out.reqBodySummary = msgSummary.String()

	dialCtx := v.eng.hardCtx
	var dialCancel context.CancelFunc
	if s.IdleTimeout > 0 {
		dialCtx, dialCancel = context.WithTimeout(dialCtx, max(s.IdleTimeout, v.eng.plan.HTTP.ConnectTimeout+v.eng.plan.HTTP.TLSTimeout))
	}
	cc, nc, err := v.eng.dialer.dial(dialCtx, origin, &out.dial)
	if dialCancel != nil {
		dialCancel()
	}
	if err != nil {
		out.end = nanotime()
		out.err = err
		return out
	}
	out.newConn = true
	out.overSent, out.overRecv = cc.take()
	defer func() {
		if nc != nil {
			_ = nc.Close()
		}
	}()

	stopClose := context.AfterFunc(v.eng.hardCtx, func() {
		_ = nc.SetDeadline(time.Unix(1, 0))
	})
	defer stopClose()

	if s.IdleTimeout > 0 {
		_ = nc.SetDeadline(time.Now().Add(s.IdleTimeout))
	}

	out.writeStart = nanotime()
	if _, err := nc.Write(reqBuf.Bytes()); err != nil {
		out.end = nanotime()
		out.sent, out.recv = cc.take()
		out.err = &hopError{phase: phaseWrite, err: err}
		return out
	}

	br := bufio.NewReaderSize(nc, 4096)
	status, respHdrs, err := wsReadHTTPHandshake(br)
	out.firstByte = nanotime()
	out.status = status
	out.respHeaders = respHdrs
	if err != nil {
		out.end = nanotime()
		out.sent, out.recv = cc.take()
		out.err = &hopError{phase: phaseRead, err: err, noResponse: status == 0}
		return out
	}
	if status != 101 {
		out.end = nanotime()
		out.sent, out.recv = cc.take()
		return out
	}

	wantAccept := wsExpectedAccept(secKey)
	gotAccept := wsHeaderValue(respHdrs, "Sec-WebSocket-Accept")
	if gotAccept != wantAccept {
		out.end = nanotime()
		out.sent, out.recv = cc.take()
		out.err = &hopError{phase: phaseRead, err: fmt.Errorf("WebSocket 握手失败：Sec-WebSocket-Accept 不匹配")}
		return out
	}

	for _, fr := range frames {
		if err := wsWriteClientFrame(nc, v.rng.Uint32(), true, fr.opcode, fr.payload); err != nil {
			out.end = nanotime()
			out.sent, out.recv = cc.take()
			out.err = &hopError{phase: phaseWrite, err: err}
			return out
		}
	}

	wantRecv := s.RecvCount
	if wantRecv == 0 && (len(s.Extract) > 0 || wsNeedsBodyAssert(s.Assert)) {
		wantRecv = 1
	}

	var lastMsg []byte
	var fragBuf bytes.Buffer
	inFrag := false
	recvCount := 0
	maxFrame := max(maxKeep, 16<<20)

	for recvCount < wantRecv {
		if s.IdleTimeout > 0 {
			_ = nc.SetReadDeadline(time.Now().Add(s.IdleTimeout))
		}
		fin, opcode, payload, rerr := wsReadServerFrame(br, maxFrame)
		if rerr != nil {
			out.end = nanotime()
			out.sent, out.recv = cc.take()
			out.err = &hopError{phase: phaseRead, err: rerr}
			return out
		}
		switch opcode {
		case 0x8: // Close
			if s.RecvCount > 0 && recvCount < s.RecvCount {
				out.end = nanotime()
				out.sent, out.recv = cc.take()
				out.err = &hopError{phase: phaseRead, err: fmt.Errorf("WebSocket 连接已关闭：收到 %d 条消息，期望 %d 条", recvCount, s.RecvCount)}
				return out
			}
			recvCount = wantRecv
		case 0x9: // Ping -> reply Pong
			_ = wsWriteClientFrame(nc, v.rng.Uint32(), true, 0xA, payload)
		case 0xA: // Pong
			continue
		case 0x1, 0x2: // Text or Binary start
			fragBuf.Reset()
			inFrag = true
			if int64(len(payload)) <= maxKeep {
				fragBuf.Write(payload)
			} else {
				fragBuf.Write(payload[:maxKeep])
				out.bodyTruncated = true
			}
			if fin {
				lastMsg = append([]byte(nil), fragBuf.Bytes()...)
				inFrag = false
				recvCount++
			}
		case 0x0: // Continuation
			if !inFrag {
				continue
			}
			rem := maxKeep - int64(fragBuf.Len())
			if rem > 0 {
				if int64(len(payload)) > rem {
					fragBuf.Write(payload[:rem])
					out.bodyTruncated = true
				} else {
					fragBuf.Write(payload)
				}
			}
			if fin {
				lastMsg = append([]byte(nil), fragBuf.Bytes()...)
				inFrag = false
				recvCount++
			}
		}
	}

	// Best-effort clean close frame before closing socket.
	var closeCode [2]byte
	binary.BigEndian.PutUint16(closeCode[:], 1000)
	_ = wsWriteClientFrame(nc, v.rng.Uint32(), true, 0x8, closeCode[:])

	out.body = lastMsg
	out.end = nanotime()
	out.sent, out.recv = cc.take()
	_ = nc.Close()
	nc = nil
	return out
}

func wsNeedsBodyAssert(asserts []scenario.Assertion) bool {
	for i := range asserts {
		switch asserts[i].Kind {
		case scenario.AssertBodyContains, scenario.AssertJSONPath, scenario.AssertJSONSchema:
			return true
		}
	}
	return false
}

func wsExpectedAccept(secKey string) string {
	h := sha1.Sum([]byte(secKey + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

func wsHeaderValue(hdrs []metrics.Header, name string) string {
	for _, h := range hdrs {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func wsReadHTTPHandshake(br *bufio.Reader) (int, []metrics.Header, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return 0, nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/1.") {
		return 0, nil, fmt.Errorf("WebSocket 握手响应行无效：%q", line)
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil || code < 100 || code > 999 {
		return 0, nil, fmt.Errorf("WebSocket 握手状态码无效：%q", parts[1])
	}
	var hdrs []metrics.Header
	for {
		hline, err := br.ReadString('\n')
		if err != nil {
			return code, hdrs, err
		}
		hline = strings.TrimRight(hline, "\r\n")
		if hline == "" {
			return code, hdrs, nil
		}
		name, val, ok := strings.Cut(hline, ":")
		if !ok {
			continue
		}
		hdrs = append(hdrs, metrics.Header{
			Name:  textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name)),
			Value: strings.TrimSpace(val),
		})
	}
}

func wsWriteClientFrame(w io.Writer, maskKey uint32, fin bool, opcode byte, payload []byte) error {
	var hdr [14]byte
	b0 := opcode & 0x0f
	if fin {
		b0 |= 0x80
	}
	hdr[0] = b0
	n := len(payload)
	hlen := 2
	switch {
	case n <= 125:
		hdr[1] = 0x80 | byte(n)
	case n <= 65535:
		hdr[1] = 0x80 | 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		hlen = 4
	default:
		hdr[1] = 0x80 | 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
		hlen = 10
	}
	binary.LittleEndian.PutUint32(hdr[hlen:hlen+4], maskKey)
	mask := hdr[hlen : hlen+4]
	hlen += 4

	buf := make([]byte, hlen+n)
	copy(buf[:hlen], hdr[:hlen])
	for i := 0; i < n; i++ {
		buf[hlen+i] = payload[i] ^ mask[i&3]
	}
	_, err := w.Write(buf)
	return err
}

func wsReadServerFrame(r io.Reader, maxLen int64) (fin bool, opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return false, 0, nil, err
	}
	fin = hdr[0]&0x80 != 0
	opcode = hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	var plen int64
	switch lenCode := hdr[1] & 0x7f; lenCode {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return false, 0, nil, err
		}
		plen = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return false, 0, nil, err
		}
		u := binary.BigEndian.Uint64(ext[:])
		if u > uint64(maxLen) {
			return false, 0, nil, errors.New("WebSocket 服务端帧超过最大尺寸限制")
		}
		plen = int64(u)
	default:
		plen = int64(lenCode)
	}
	if plen > maxLen {
		return false, 0, nil, errors.New("WebSocket 服务端帧超过最大尺寸限制")
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(r, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload = make([]byte, plen)
	if plen > 0 {
		if _, err = io.ReadFull(r, payload); err != nil {
			return false, 0, nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i&3]
			}
		}
	}
	return fin, opcode, payload, nil
}

func wsStatusInitOutcome(hasStatusAssert bool, status int) (metrics.ErrorKind, string) {
	if hasStatusAssert || status == 101 {
		return metrics.ErrNone, ""
	}
	switch {
	case status >= 500:
		return metrics.ErrHTTP5xx, "WebSocket 握手失败：HTTP " + strconv.Itoa(status)
	case status >= 400:
		return metrics.ErrHTTP4xx, "WebSocket 握手失败：HTTP " + strconv.Itoa(status)
	default:
		return metrics.ErrOther, "WebSocket 握手期望 101，实际 HTTP " + strconv.Itoa(status)
	}
}

func (v *vu) runWS(s *scenario.WSStep, planned int64) (bool, metrics.ErrorKind) {
	if !v.takeRequestSlot() {
		return false, metrics.ErrNone
	}
	if failed, kind := v.execProtoPreScriptLoad(s.Index, s.ID, "WS", s.PreScript, planned); failed {
		return true, kind
	}
	maxKeep := v.eng.plan.HTTP.MaxBodyRead
	if maxKeep <= 0 {
		maxKeep = DefaultDebugMaxBodyBytes
	}
	wr := v.execWS(s, maxKeep)
	if v.eng.aborted.Load() {
		v.finishRequestSlot()
		return false, metrics.ErrNone
	}
	start := wr.start
	if planned != 0 {
		start = planned
	}
	var kind metrics.ErrorKind
	var msg string
	if wr.err != nil {
		kind, msg = classify(wr.err), wr.err.Error()
	} else {
		initKind, initMsg := wsStatusInitOutcome(s.HasStatusAssert, wr.status)
		view := scenario.ResponseView{
			Status:   wr.status,
			Duration: time.Duration(wr.end - start),
			Body:     wr.body,
			Header: func(name string) ([]byte, bool) {
				val := wsHeaderValue(wr.respHeaders, name)
				if val == "" {
					return nil, false
				}
				return []byte(val), true
			},
		}
		kind, msg = v.evalNonHTTPUponResponse(initKind, initMsg, s.Extract, s.Assert, s.PostScript, &view)
	}
	v.recordGeneric(s.Index, wr.status, kind, time.Duration(wr.end-start), wr.sent, wr.recv, wr.overSent, wr.overRecv, wr.newConn, wr.dial, wr.writeStart, wr.firstByte, wr.end)
	v.maybeSampleGeneric(s.Index, s.ID, "WS", wr.url, wr.reqHeaders, wr.reqBodySummary, wr.status, wr.respHeaders, wr.body, wr.bodyTruncated, kind, msg, start, wr.end)
	v.finishRequestSlot()
	if wr.err != nil && v.eng.plan.Load.Mode == scenario.LoadVUs && v.eng.plan.HTTP.ConnectBackoff > 0 && isConnectPhaseErr(wr.err) && !v.stopped() {
		v.sleep(v.eng.plan.HTTP.ConnectBackoff)
	}
	return true, kind
}

func (v *vu) runWSDebug(s *scenario.WSStep, maxBody int64) DebugStepResult {
	res := DebugStepResult{
		StepID:   s.ID,
		StepName: s.Name,
	}
	if !v.execStepPreScriptDebug(s.PreScript, &res) {
		res.ActualRequest = DebugActualRequest{Method: "WS"}
		return res
	}
	wr := v.execWS(s, maxBody)
	res.ActualRequest = DebugActualRequest{
		Method:  "WS",
		URL:     wr.url,
		Headers: wr.reqHeaders,
		Body:    wr.reqBodySummary,
	}
	res.Status = wr.status
	res.ResponseHeaders = wr.respHeaders
	res.BytesSent = wr.sent + wr.overSent
	res.BytesReceived = wr.recv + wr.overRecv
	res.Timing = genericDebugTiming(wr.start, wr.writeStart, wr.firstByte, wr.end, wr.newConn, wr.dial, wr.err)
	if wr.err != nil {
		res.ErrorKind = classify(wr.err).String()
		res.Error = wr.err.Error()
		return res
	}
	setDebugBody(&res, wr.body, wr.bodyTruncated)
	initKind, initMsg := wsStatusInitOutcome(s.HasStatusAssert, wr.status)
	view := scenario.ResponseView{
		Status:   wr.status,
		Duration: time.Duration(wr.end - wr.start),
		Body:     wr.body,
		Header: func(name string) ([]byte, bool) {
			val := wsHeaderValue(wr.respHeaders, name)
			if val == "" {
				return nil, false
			}
			return []byte(val), true
		},
	}
	v.evalDebugResponse(initKind, initMsg, s.Extract, s.Assert, s.PostScript, &view, &res)
	return res
}

// Unused import safety check for net (if needed by callers).
var _ net.Conn
