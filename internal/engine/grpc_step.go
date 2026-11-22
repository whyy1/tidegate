package engine

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"

	"tidegate/internal/grpcproto"
	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
)

type rawGrpcCodec struct{}

func (rawGrpcCodec) Name() string { return "proto" }

func (rawGrpcCodec) Marshal(v any) ([]byte, error) {
	b, ok := v.([]byte)
	if !ok {
		return nil, fmt.Errorf("grpc codec: expected []byte, got %T", v)
	}
	return b, nil
}

func (rawGrpcCodec) Unmarshal(data []byte, v any) error {
	dst, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("grpc codec: expected *[]byte, got %T", v)
	}
	*dst = append((*dst)[:0], data...)
	return nil
}

type grpcConnEntry struct {
	cc       *grpc.ClientConn
	mu       sync.Mutex
	conns    []*countConn
	lastDial dialTiming
	dialSeen bool
}

func (e *grpcConnEntry) addConn(c *countConn, dt dialTiming) {
	e.mu.Lock()
	e.conns = append(e.conns, c)
	if !e.dialSeen {
		e.lastDial = dt
		e.dialSeen = true
	}
	e.mu.Unlock()
}

func (e *grpcConnEntry) takeBytes() (sent, recv int64, firstDial *dialTiming) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dialSeen {
		cp := e.lastDial
		firstDial = &cp
		e.dialSeen = false
	}
	for _, cc := range e.conns {
		s, r := cc.take()
		sent += s
		recv += r
	}
	return sent, recv, firstDial
}

type grpcPool struct {
	cfg          *scenario.HTTPConfig
	dial         *dialer
	tlsCache     tls.ClientSessionCache
	mu           sync.Mutex
	entries      map[string]*grpcConnEntry
	reflectMu    sync.Mutex
	reflectFiles map[string]*grpcproto.MethodCodec
}

func newGRPCPool(cfg *scenario.HTTPConfig, d *dialer) *grpcPool {
	return &grpcPool{
		cfg:          cfg,
		dial:         d,
		tlsCache:     tls.NewLRUClientSessionCache(256),
		entries:      make(map[string]*grpcConnEntry),
		reflectFiles: make(map[string]*grpcproto.MethodCodec),
	}
}

func (p *grpcPool) codecViaReflection(ctx context.Context, o *scenario.Origin, fullMethod string) (*grpcproto.MethodCodec, error) {
	key := o.Addr + "|tls=" + strconv.FormatBool(o.TLS) + "|" + fullMethod
	p.reflectMu.Lock()
	if c, ok := p.reflectFiles[key]; ok {
		p.reflectMu.Unlock()
		return c, nil
	}
	p.reflectMu.Unlock()

	var dialCheck func(host string, ip net.IP) error
	if p.dial != nil {
		dialCheck = p.dial.targetCheck
	}
	_, files, err := grpcproto.DiscoverViaReflection(ctx, grpcproto.ReflectOptions{
		Target:             o.Addr,
		TLS:                o.TLS,
		InsecureSkipVerify: p.cfg.InsecureSkipVerify,
		Timeout:            10 * time.Second,
		DialCheck:          dialCheck,
	})
	if err != nil {
		return nil, err
	}
	codec, err := grpcproto.NewMethodCodec(files, fullMethod)
	if err != nil {
		return nil, err
	}
	p.reflectMu.Lock()
	p.reflectFiles[key] = codec
	p.reflectMu.Unlock()
	return codec, nil
}

func (p *grpcPool) get(o *scenario.Origin) (*grpcConnEntry, error) {
	key := o.Addr + "|tls=" + strconv.FormatBool(o.TLS)
	p.mu.Lock()
	if e, ok := p.entries[key]; ok {
		p.mu.Unlock()
		return e, nil
	}
	p.mu.Unlock()

	entry := &grpcConnEntry{}
	creds := insecure.NewCredentials()
	if o.TLS {
		creds = credentials.NewTLS(&tls.Config{
			ServerName:         o.HostName,
			InsecureSkipVerify: p.cfg.InsecureSkipVerify,
			NextProtos:         []string{"h2"},
			ClientSessionCache: p.tlsCache,
		})
	}
	conn, err := grpc.NewClient(
		"passthrough:///"+o.Addr,
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(rawGrpcCodec{})),
		grpc.WithContextDialer(func(dctx context.Context, _ string) (net.Conn, error) {
			var dt dialTiming
			cc, derr := p.dial.dialTCP(dctx, o, &dt)
			if derr != nil {
				return nil, derr
			}
			entry.addConn(cc, dt)
			return cc, nil
		}),
	)
	if err != nil {
		return nil, err
	}
	entry.cc = conn

	p.mu.Lock()
	if existing, ok := p.entries[key]; ok {
		p.mu.Unlock()
		_ = conn.Close()
		return existing, nil
	}
	p.entries[key] = entry
	p.mu.Unlock()
	return entry, nil
}

func (p *grpcPool) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	entries := make([]*grpcConnEntry, 0, len(p.entries))
	for _, e := range p.entries {
		entries = append(entries, e)
	}
	clear(p.entries)
	p.mu.Unlock()
	for _, e := range entries {
		if e.cc != nil {
			_ = e.cc.Close()
		}
	}
}

type grpcCallResult struct {
	targetStr     string
	reqHeaders    []metrics.Header
	reqBody       string
	status        int
	grpcMsg       string
	respHeaders   []metrics.Header
	respBody      []byte
	bodyTruncated bool
	sent, recv    int64
	newConn       bool
	dial          dialTiming
	start         int64
	writeStart    int64
	end           int64
	err           error
}

func encodeGRPCPayload(encoding string, raw []byte) ([]byte, error) {
	switch encoding {
	case "base64":
		return base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	case "hex":
		return hex.DecodeString(strings.TrimSpace(string(raw)))
	case "proto_string":
		if len(raw) == 0 {
			return nil, nil
		}
		buf := make([]byte, 0, 1+binarySize(uint64(len(raw)))+len(raw))
		buf = append(buf, 0x0a)
		buf = protowire.AppendVarint(buf, uint64(len(raw)))
		buf = append(buf, raw...)
		return buf, nil
	default: // utf8 / json
		return raw, nil
	}
}

func binarySize(n uint64) int {
	return protowire.SizeVarint(n)
}

func decodeGRPCResponse(encoding string, raw []byte) []byte {
	switch encoding {
	case "proto_string":
		if len(raw) >= 2 && raw[0] == 0x0a {
			n, m := protowire.ConsumeVarint(raw[1:])
			if m > 0 && 1+m+int(n) <= len(raw) {
				return raw[1+m : 1+m+int(n)]
			}
		}
		return raw
	case "base64":
		if len(raw) == 0 {
			return nil
		}
		return []byte(base64.StdEncoding.EncodeToString(raw))
	default:
		return raw
	}
}

func (v *vu) execGRPC(s *scenario.GRPCStepPlan, maxKeep int64) grpcCallResult {
	var out grpcCallResult
	out.start = nanotime()

	origin := s.Origin
	if origin == nil && s.Target != nil {
		rendered := string(s.Target.Append(nil, &v.env))
		out.targetStr = rendered
		var tlsOv *bool
		if s.HasTLSOverride {
			b := s.TLS
			tlsOv = &b
		}
		parsed, err := scenario.ParseGRPCTarget(rendered, tlsOv)
		if err != nil {
			out.end = nanotime()
			out.err = &hopError{phase: phaseDial, err: err}
			return out
		}
		origin = parsed
	} else if origin != nil {
		out.targetStr = origin.Scheme + "://" + origin.Addr + s.FullMethod
	}

	codec := s.Codec
	if codec == nil && (s.UseReflection || s.PayloadEncoding == "proto_json" || s.ResponseEncoding == "proto_json") {
		c, rerr := v.eng.grpc.codecViaReflection(v.eng.hardCtx, origin, s.FullMethod)
		if rerr != nil {
			out.end = nanotime()
			if errors.Is(rerr, scenario.ErrTargetNotAllowed) {
				out.err = &hopError{phase: phaseDial, err: rerr}
			} else {
				out.err = &hopError{phase: phaseWrite, err: fmt.Errorf("gRPC Reflection 获取契约失败：%w", rerr)}
			}
			return out
		}
		codec = c
	}

	rawPayload := s.Payload.Append(nil, &v.env)
	var wireReq []byte
	var err error
	if s.PayloadEncoding == "proto_json" && codec != nil {
		wireReq, err = codec.EncodeJSON(rawPayload)
	} else {
		wireReq, err = encodeGRPCPayload(s.PayloadEncoding, rawPayload)
	}
	if err != nil {
		out.end = nanotime()
		out.err = &hopError{phase: phaseWrite, err: fmt.Errorf("gRPC 请求编码失败：%w", err)}
		return out
	}
	out.reqBody = string(rawPayload)

	hdrs := v.renderHeaderTemplates(s.Metadata)
	var mdPairs []string
	for _, h := range hdrs {
		val := string(h.value)
		mdPairs = append(mdPairs, h.canon, val)
		out.reqHeaders = append(out.reqHeaders, metrics.Header{Name: h.canon, Value: val})
	}

	entry, err := v.eng.grpc.get(origin)
	if err != nil {
		out.end = nanotime()
		out.err = &hopError{phase: phaseDial, err: err}
		return out
	}

	callCtx := v.eng.hardCtx
	var cancel context.CancelFunc
	if s.Timeout > 0 {
		callCtx, cancel = context.WithTimeout(callCtx, s.Timeout)
	} else {
		callCtx, cancel = context.WithCancel(callCtx)
	}
	defer cancel()
	if len(mdPairs) > 0 {
		callCtx = metadata.NewOutgoingContext(callCtx, metadata.Pairs(mdPairs...))
	}

	out.writeStart = nanotime()
	var (
		respHdr     metadata.MD
		respTrailer metadata.MD
		wireResp    []byte
	)
	invokeErr := entry.cc.Invoke(callCtx, s.FullMethod, wireReq, &wireResp, grpc.Header(&respHdr), grpc.Trailer(&respTrailer))
	out.end = nanotime()

	sent, recv, firstDial := entry.takeBytes()
	out.sent, out.recv = sent, recv
	if firstDial != nil {
		out.newConn = true
		out.dial = *firstDial
	}

	for k, vals := range respHdr {
		for _, val := range vals {
			out.respHeaders = append(out.respHeaders, metrics.Header{Name: k, Value: val})
		}
	}
	for k, vals := range respTrailer {
		for _, val := range vals {
			out.respHeaders = append(out.respHeaders, metrics.Header{Name: k, Value: val})
		}
	}

	if invokeErr != nil {
		var he *hopError
		if errors.As(invokeErr, &he) {
			out.err = he
			return out
		}
		if errors.Is(invokeErr, scenario.ErrTargetNotAllowed) {
			out.err = &hopError{phase: phaseDial, err: invokeErr}
			return out
		}
		st, _ := status.FromError(invokeErr)
		out.status = int(st.Code())
		out.grpcMsg = st.Message()
		out.respHeaders = append(out.respHeaders,
			metrics.Header{Name: "grpc-status", Value: strconv.Itoa(out.status)},
			metrics.Header{Name: "grpc-message", Value: out.grpcMsg},
		)
		if st.Code() == codes.DeadlineExceeded || errors.Is(invokeErr, context.DeadlineExceeded) {
			out.err = &hopError{phase: phaseRead, err: context.DeadlineExceeded}
			return out
		}
		if st.Code() == codes.Unavailable && len(respHdr) == 0 && len(respTrailer) == 0 {
			out.err = &hopError{phase: phaseDial, err: errors.New(st.Message())}
			return out
		}
		// Server responded with a structured gRPC status code.
	} else {
		out.status = int(codes.OK)
		out.respHeaders = append(out.respHeaders, metrics.Header{Name: "grpc-status", Value: "0"})
	}

	var decoded []byte
	if s.ResponseEncoding == "proto_json" && codec != nil && invokeErr == nil {
		var derr error
		decoded, derr = codec.DecodeToJSON(wireResp)
		if derr != nil {
			decoded = decodeGRPCResponse("base64", wireResp)
		}
	} else {
		decoded = decodeGRPCResponse(s.ResponseEncoding, wireResp)
	}
	if int64(len(decoded)) > maxKeep {
		decoded = decoded[:maxKeep]
		out.bodyTruncated = true
	}
	out.respBody = decoded
	return out
}

func grpcStatusInitOutcome(s *scenario.GRPCStepPlan, got int, grpcMsg string) (metrics.ErrorKind, string) {
	if s.HasStatusAssert {
		return metrics.ErrNone, ""
	}
	want := 0
	if s.ExpectedStatus != nil {
		want = *s.ExpectedStatus
	}
	if got == want {
		return metrics.ErrNone, ""
	}
	if grpcMsg != "" {
		return metrics.ErrAssertion, fmt.Sprintf("gRPC 状态码期望 %d，实际 %d（%s）", want, got, grpcMsg)
	}
	return metrics.ErrAssertion, fmt.Sprintf("gRPC 状态码期望 %d，实际 %d", want, got)
}

func (v *vu) runGRPC(s *scenario.GRPCStepPlan, planned int64) (bool, metrics.ErrorKind) {
	if !v.takeRequestSlot() {
		return false, metrics.ErrNone
	}
	if failed, kind := v.execProtoPreScriptLoad(s.Index, s.ID, "GRPC", s.PreScript, planned); failed {
		return true, kind
	}
	maxKeep := v.eng.plan.HTTP.MaxBodyRead
	if maxKeep <= 0 {
		maxKeep = DefaultDebugMaxBodyBytes
	}
	gr := v.execGRPC(s, maxKeep)
	if v.eng.aborted.Load() {
		v.finishRequestSlot()
		return false, metrics.ErrNone
	}
	start := gr.start
	if planned != 0 {
		start = planned
	}
	var kind metrics.ErrorKind
	var msg string
	if gr.err != nil {
		kind, msg = classify(gr.err), gr.err.Error()
	} else {
		initKind, initMsg := grpcStatusInitOutcome(s, gr.status, gr.grpcMsg)
		view := scenario.ResponseView{
			Status:   gr.status,
			Duration: time.Duration(gr.end - start),
			Body:     gr.respBody,
			Header: func(name string) ([]byte, bool) {
				val := wsHeaderValue(gr.respHeaders, name)
				if val == "" {
					return nil, false
				}
				return []byte(val), true
			},
		}
		kind, msg = v.evalNonHTTPUponResponse(initKind, initMsg, s.Extract, s.Assert, s.PostScript, &view)
	}
	v.recordGeneric(s.Index, gr.status, kind, time.Duration(gr.end-start), gr.sent, gr.recv, 0, 0, gr.newConn, gr.dial, gr.writeStart, gr.end, gr.end)
	v.maybeSampleGeneric(s.Index, s.ID, "GRPC", gr.targetStr, gr.reqHeaders, gr.reqBody, gr.status, gr.respHeaders, gr.respBody, gr.bodyTruncated, kind, msg, start, gr.end)
	v.finishRequestSlot()
	if gr.err != nil && v.eng.plan.Load.Mode == scenario.LoadVUs && v.eng.plan.HTTP.ConnectBackoff > 0 && isConnectPhaseErr(gr.err) && !v.stopped() {
		v.sleep(v.eng.plan.HTTP.ConnectBackoff)
	}
	return true, kind
}

func (v *vu) runGRPCDebug(s *scenario.GRPCStepPlan, maxBody int64) DebugStepResult {
	res := DebugStepResult{
		StepID:   s.ID,
		StepName: s.Name,
	}
	if !v.execStepPreScriptDebug(s.PreScript, &res) {
		res.ActualRequest = DebugActualRequest{Method: "GRPC"}
		return res
	}
	gr := v.execGRPC(s, maxBody)
	res.ActualRequest = DebugActualRequest{
		Method:  "GRPC",
		URL:     gr.targetStr,
		Headers: gr.reqHeaders,
		Body:    gr.reqBody,
	}
	res.Status = gr.status
	res.ResponseHeaders = gr.respHeaders
	res.BytesSent = gr.sent
	res.BytesReceived = gr.recv
	res.Timing = genericDebugTiming(gr.start, gr.writeStart, gr.end, gr.end, gr.newConn, gr.dial, gr.err)
	if gr.err != nil {
		res.ErrorKind = classify(gr.err).String()
		res.Error = gr.err.Error()
		return res
	}
	setDebugBody(&res, gr.respBody, gr.bodyTruncated)
	initKind, initMsg := grpcStatusInitOutcome(s, gr.status, gr.grpcMsg)
	view := scenario.ResponseView{
		Status:   gr.status,
		Duration: time.Duration(gr.end - gr.start),
		Body:     gr.respBody,
		Header: func(name string) ([]byte, bool) {
			val := wsHeaderValue(gr.respHeaders, name)
			if val == "" {
				return nil, false
			}
			return []byte(val), true
		},
	}
	v.evalDebugResponse(initKind, initMsg, s.Extract, s.Assert, s.PostScript, &view, &res)
	return res
}
