package engine

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"tidegate/internal/scenario"
)

// minimal pure-Go RFC 6455 test server accepting WS upgrade, reading masked client
// frames, sending a Ping, and replying with a JSON payload.
func startTestWSServer(t *testing.T, useTLS bool) string {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Sec-WebSocket-Key")
		if key == "" || r.Header.Get("Authorization") != "Bearer ws-tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()

		accept := wsExpectedAccept(key)
		_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\nSec-WebSocket-Protocol: json\r\n\r\n", accept)
		_ = rw.Flush()

		for {
			fin, op, payload, err := wsReadClientFrameTest(rw.Reader)
			if err != nil {
				return
			}
			_ = fin
			switch op {
			case 0x8:
				_ = wsWriteServerFrameTest(conn, true, 0x8, nil)
				return
			case 0x1, 0x2:
				// Send a Ping frame before responding with data to verify client Ping/Pong handling.
				_ = wsWriteServerFrameTest(conn, true, 0x9, []byte("hb"))
				var pongOp byte
				for {
					_, pOp, _, perr := wsReadClientFrameTest(rw.Reader)
					if perr != nil {
						return
					}
					pongOp = pOp
					if pongOp == 0xA {
						break
					}
				}
				reply := fmt.Sprintf(`{"reply":"pong","echo":%q}`, string(payload))
				if err := wsWriteServerFrameTest(conn, true, 0x1, []byte(reply)); err != nil {
					return
				}
			}
		}
	})

	var srv *httptest.Server
	if useTLS {
		srv = httptest.NewTLSServer(handler)
		t.Cleanup(srv.Close)
		return "wss://" + srv.Listener.Addr().String() + "/chat"
	}
	srv = httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return "ws://" + srv.Listener.Addr().String() + "/chat"
}

func wsReadClientFrameTest(r io.Reader) (fin bool, opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return false, 0, nil, err
	}
	fin = hdr[0]&0x80 != 0
	opcode = hdr[0] & 0x0f
	if hdr[1]&0x80 == 0 {
		return false, 0, nil, fmt.Errorf("client frame must be masked")
	}
	plen := int(hdr[1] & 0x7f)
	if plen == 126 {
		var ext [2]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return false, 0, nil, err
		}
		plen = int(binary.BigEndian.Uint16(ext[:]))
	}
	var mask [4]byte
	if _, err = io.ReadFull(r, mask[:]); err != nil {
		return false, 0, nil, err
	}
	payload = make([]byte, plen)
	if plen > 0 {
		if _, err = io.ReadFull(r, payload); err != nil {
			return false, 0, nil, err
		}
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return fin, opcode, payload, nil
}

func wsWriteServerFrameTest(w io.Writer, fin bool, opcode byte, payload []byte) error {
	var hdr [4]byte
	b0 := opcode & 0x0f
	if fin {
		b0 |= 0x80
	}
	hdr[0] = b0
	n := len(payload)
	hlen := 2
	if n <= 125 {
		hdr[1] = byte(n)
	} else {
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		hlen = 4
	}
	_, err := w.Write(append(hdr[:hlen], payload...))
	return err
}

func TestProtocolWebSocketPlainAndTLS(t *testing.T) {
	for _, useTLS := range []bool{false, true} {
		name := "ws"
		if useTLS {
			name = "wss"
		}
		t.Run(name, func(t *testing.T) {
			wsURL := startTestWSServer(t, useTLS)
			var seenEcho atomic.Value
			httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenEcho.Store(r.URL.Query().Get("reply"))
				w.WriteHeader(http.StatusOK)
			}))
			defer httpSrv.Close()

			def := &scenario.Definition{
				Version: 1,
				Name:    "ws-flow-" + name,
				HTTP: scenario.HTTPOptions{
					InsecureSkipVerify: true,
				},
				Steps: []scenario.Step{
					{
						ID: "ws_step",
						WebSocket: &scenario.WebSocketStep{
							URL: wsURL,
							Headers: map[string]string{
								"Authorization": "Bearer ws-tok",
							},
							Subprotocols: []string{"json"},
							Messages: []scenario.WSMessage{
								{Type: "text", Data: "hello-" + name},
							},
							RecvCount:   1,
							IdleTimeout: scenario.Duration(3 * time.Second),
						},
						Extract: []scenario.Extract{
							{Name: "ws_reply", From: "jsonpath", Expr: "$.reply"},
						},
						Assert: []scenario.Assert{
							{Type: "jsonpath", Expr: "$.echo", Contains: ptr("hello-" + name)},
						},
					},
					{
						ID: "after_http",
						Request: &scenario.Request{
							Method: "GET",
							URL:    httpSrv.URL + "/after?reply={{ws_reply}}",
						},
					},
				},
				Load: scenario.Load{
					Mode:        "vus",
					Stages:      []scenario.Stage{{Duration: scenario.Duration(2 * time.Second), Target: 1}},
					MaxRequests: 6, // 3 iterations * 2 steps
				},
			}
			plan, err := scenario.Compile(def, scenario.CompileOptions{})
			if err != nil {
				t.Fatalf("Compile(%s): %v", name, err)
			}

			dbg, err := RunDebug(context.Background(), plan, DebugOptions{SkipThink: true})
			if err != nil {
				t.Fatalf("RunDebug(%s): %v", name, err)
			}
			if !dbg.Passed || len(dbg.Steps) != 2 {
				t.Fatalf("RunDebug(%s) failed: %+v", name, dbg)
			}
			if dbg.Steps[0].Status != 101 || dbg.Steps[0].BytesSent == 0 || dbg.Steps[0].BytesReceived == 0 {
				t.Fatalf("unexpected WS debug stats: %+v", dbg.Steps[0])
			}
			if dbg.Steps[0].ExtractedVars["ws_reply"] != "pong" {
				t.Fatalf("extracted ws_reply = %q", dbg.Steps[0].ExtractedVars["ws_reply"])
			}
			if got, _ := seenEcho.Load().(string); got != "pong" {
				t.Fatalf("downstream HTTP saw reply=%q, want pong", got)
			}

			eng, err := New(plan, Options{Shards: 1})
			if err != nil {
				t.Fatal(err)
			}
			if err := eng.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			sum := eng.Summary()
			if sum.Total.Requests != 6 || sum.Total.ErrorCount() != 0 {
				t.Fatalf("WS load summary mismatch: reqs=%d errs=%d", sum.Total.Requests, sum.Total.ErrorCount())
			}
			if sum.Steps[0].BytesSent == 0 || sum.Steps[0].BytesReceived == 0 {
				t.Fatalf("expected non-zero WS wire bytes")
			}
		})
	}
}

type echoGRPCServer interface {
	UnaryEcho(context.Context, *[]byte) (*[]byte, error)
}

func startTestGRPCServer(t *testing.T, useTLS bool) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	var opts []grpc.ServerOption
	opts = append(opts, grpc.ForceServerCodec(rawGrpcCodec{}))
	if useTLS {
		// Reuse httptest TLS cert cleanly without external tools.
		ts := httptest.NewTLSServer(http.NewServeMux())
		cert := ts.TLS.Certificates[0]
		ts.Close()
		opts = append(opts, grpc.Creds(credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{cert},
			NextProtos:   []string{"h2"},
		})))
	}

	gsrv := grpc.NewServer(opts...)
	t.Cleanup(gsrv.Stop)

	handler := func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		var req []byte
		if err := dec(&req); err != nil {
			return nil, err
		}
		md, _ := metadata.FromIncomingContext(ctx)
		if md.Get("x-fail") != nil && md.Get("x-fail")[0] == "1" {
			return nil, status.Error(codes.PermissionDenied, "denied")
		}
		if len(md.Get("x-trace-id")) == 0 || md.Get("x-trace-id")[0] != "trace-1" {
			return nil, status.Error(codes.Unauthenticated, "missing trace")
		}
		if len(req) >= 2 && req[0] == 0x0a {
			// proto_string echo round-trip
			return req, nil
		}
		resp := fmt.Sprintf(`{"ok":true,"raw":%q}`, string(req))
		return []byte(resp), nil
	}

	gsrv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "echo.EchoService",
		HandlerType: (*echoGRPCServer)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "Echo", Handler: handler},
		},
	}, struct{ echoGRPCServer }{})

	go func() { _ = gsrv.Serve(lis) }()

	scheme := "grpc://"
	if useTLS {
		scheme = "grpcs://"
	}
	return scheme + lis.Addr().String()
}

func TestProtocolGRPCPlaintextAndTLS(t *testing.T) {
	for _, useTLS := range []bool{false, true} {
		name := "grpc"
		if useTLS {
			name = "grpcs"
		}
		t.Run(name, func(t *testing.T) {
			target := startTestGRPCServer(t, useTLS)
			def := &scenario.Definition{
				Version: 1,
				Name:    "grpc-flow-" + name,
				HTTP: scenario.HTTPOptions{
					InsecureSkipVerify: true,
				},
				Steps: []scenario.Step{
					{
						ID: "grpc_json",
						GRPC: &scenario.GRPCStep{
							Target:           target,
							Service:          "echo.EchoService",
							Method:           "Echo",
							Metadata:         map[string]string{"x-trace-id": "trace-1"},
							Payload:          `{"msg":"ping"}`,
							PayloadEncoding:  "json",
							ResponseEncoding: "raw",
						},
						Extract: []scenario.Extract{
							{Name: "is_ok", From: "jsonpath", Expr: "$.ok"},
						},
						Assert: []scenario.Assert{
							{Type: "jsonpath", Expr: "$.ok", Equals: sPtr("true")},
						},
					},
					{
						ID: "grpc_proto_str",
						GRPC: &scenario.GRPCStep{
							Target:           target,
							Service:          "echo.EchoService",
							Method:           "Echo",
							Metadata:         map[string]string{"x-trace-id": "trace-1"},
							Payload:          "hello-proto",
							PayloadEncoding:  "proto_string",
							ResponseEncoding: "proto_string",
						},
						Assert: []scenario.Assert{
							{Type: "body_contains", Value: "hello-proto"},
						},
					},
					{
						ID: "grpc_expected_err",
						GRPC: &scenario.GRPCStep{
							Target:         target,
							Service:        "echo.EchoService",
							Method:         "Echo",
							Metadata:       map[string]string{"x-trace-id": "trace-1", "x-fail": "1"},
							ExpectedStatus: intPtr(int(codes.PermissionDenied)),
						},
					},
				},
				Load: scenario.Load{
					Mode:        "vus",
					Stages:      []scenario.Stage{{Duration: scenario.Duration(2 * time.Second), Target: 1}},
					MaxRequests: 6, // 2 iterations * 3 steps
				},
			}
			plan, err := scenario.Compile(def, scenario.CompileOptions{})
			if err != nil {
				t.Fatalf("Compile(%s): %v", name, err)
			}

			dbg, err := RunDebug(context.Background(), plan, DebugOptions{SkipThink: true})
			if err != nil {
				t.Fatalf("RunDebug(%s): %v", name, err)
			}
			if !dbg.Passed || len(dbg.Steps) != 3 {
				t.Fatalf("RunDebug(%s) failed: %+v", name, dbg)
			}
			if dbg.Steps[0].ExtractedVars["is_ok"] != "true" || dbg.Steps[1].Body != "hello-proto" || dbg.Steps[2].Status != int(codes.PermissionDenied) {
				t.Fatalf("unexpected gRPC debug results: %+v", dbg.Steps)
			}

			eng, err := New(plan, Options{Shards: 1})
			if err != nil {
				t.Fatal(err)
			}
			if err := eng.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			sum := eng.Summary()
			if sum.Total.Requests != 6 || sum.Total.ErrorCount() != 0 {
				t.Fatalf("gRPC load summary mismatch: reqs=%d errs=%d", sum.Total.Requests, sum.Total.ErrorCount())
			}
			if sum.Steps[0].BytesSent == 0 || sum.Steps[0].BytesReceived == 0 {
				t.Fatalf("expected non-zero gRPC wire bytes")
			}
		})
	}
}

func TestProtocolSSEEarlyStopAndExtract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			http.Error(w, "bad accept", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		bw := bufio.NewWriter(w)
		_, _ = fmt.Fprint(bw, ": keepalive\n\n")
		_, _ = fmt.Fprint(bw, "id: 1\nevent: token\ndata: {\"seq\":1,\"tok\":\"hi\"}\n\n")
		_, _ = fmt.Fprint(bw, "id: 2\nevent: done\ndata: {\"seq\":2,\"tok\":\"[DONE]\"}\n\n")
		_ = bw.Flush()
		if fl != nil {
			fl.Flush()
		}
		// Keep stream open briefly if client didn't disconnect cleanly on [DONE].
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()

	def := &scenario.Definition{
		Version: 1,
		Name:    "sse-flow",
		Steps: []scenario.Step{
			{
				ID: "sse1",
				SSE: &scenario.SSEStep{
					URL:          srv.URL + "/stream",
					MaxEvents:    5,
					StopContains: "[DONE]",
					Timeout:      scenario.Duration(2 * time.Second),
				},
				Extract: []scenario.Extract{
					{Name: "last_seq", From: "regex", Expr: `"seq":(\d+)`, Group: intPtr(1)},
				},
				Assert: []scenario.Assert{
					{Type: "body_contains", Value: "[DONE]"},
				},
			},
		},
		Load: scenario.Load{
			Mode:        "vus",
			Stages:      []scenario.Stage{{Duration: scenario.Duration(2 * time.Second), Target: 1}},
			MaxRequests: 3,
		},
	}
	plan, err := scenario.Compile(def, scenario.CompileOptions{})
	if err != nil {
		t.Fatal(err)
	}

	dbg, err := RunDebug(context.Background(), plan, DebugOptions{SkipThink: true})
	if err != nil {
		t.Fatal(err)
	}
	if !dbg.Passed || dbg.Steps[0].Timing.TotalMs > 1000 {
		t.Fatalf("expected fast SSE early stop & pass, got: %+v", dbg.Steps[0])
	}
	if !strings.Contains(dbg.Steps[0].Body, "[DONE]") {
		t.Fatalf("unexpected SSE body: %q", dbg.Steps[0].Body)
	}

	eng, err := New(plan, Options{Shards: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	sum := eng.Summary()
	if sum.Total.Requests != 3 || sum.Total.ErrorCount() != 0 {
		t.Fatalf("SSE load summary mismatch: reqs=%d errs=%d", sum.Total.Requests, sum.Total.ErrorCount())
	}
	if sum.Steps[0].BytesSent == 0 || sum.Steps[0].BytesReceived == 0 {
		t.Fatalf("expected non-zero SSE wire bytes")
	}
}

func TestProtocolScriptStepChainAndNonDoubleCount(t *testing.T) {
	var hits atomic.Int64
	var badSig atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mac := hmac.New(sha256.New, []byte("s3cr3t"))
		mac.Write([]byte("payload-1"))
		want := hex.EncodeToString(mac.Sum(nil))
		if r.Header.Get("X-Signature") != want {
			badSig.Add(1)
			http.Error(w, "bad sig", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	def := &scenario.Definition{
		Version: 1,
		Name:    "script-http-chain",
		Variables: map[string]scenario.Scalar{
			"api_secret": "s3cr3t",
		},
		Steps: []scenario.Step{
			{
				ID:     "sign",
				Script: &scenario.ScriptStep{Code: `vars.sig = hmac_sha256(vars.api_secret, "payload-1"); assert(len(vars.sig) == 64, "sig len")`},
			},
			{
				ID: "call",
				Request: &scenario.Request{
					Method: "GET",
					URL:    srv.URL + "/verify",
					Headers: map[string]string{
						"X-Signature": "{{sig}}",
					},
				},
			},
		},
		Load: scenario.Load{
			Mode:        "vus",
			Stages:      []scenario.Stage{{Duration: scenario.Duration(2 * time.Second), Target: 1}},
			MaxRequests: 4, // script is IsGroup=true, so 4 HTTP hits
		},
	}
	plan, err := scenario.Compile(def, scenario.CompileOptions{})
	if err != nil {
		t.Fatal(err)
	}

	dbg, err := RunDebug(context.Background(), plan, DebugOptions{SkipThink: true})
	if err != nil {
		t.Fatal(err)
	}
	if !dbg.Passed || len(dbg.Steps) != 2 || len(dbg.Steps[0].ExtractedVars["sig"]) != 64 || dbg.Steps[1].Status != 200 {
		t.Fatalf("unexpected script+http debug result: %+v", dbg)
	}

	hits.Store(0)
	badSig.Store(0)

	eng, err := New(plan, Options{Shards: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	sum := eng.Summary()
	if hits.Load() != 4 || badSig.Load() != 0 {
		t.Fatalf("expected 4 valid signed HTTP hits, got hits=%d badSig=%d", hits.Load(), badSig.Load())
	}
	if sum.Total.Requests != 4 || sum.Total.ErrorCount() != 0 {
		t.Fatalf("Total.Requests=%d errs=%d, want 4 network requests without script double-counting", sum.Total.Requests, sum.Total.ErrorCount())
	}
	if !sum.Steps[0].IsGroup || sum.Steps[0].Requests != 4 {
		t.Fatalf("expected script step[0] IsGroup=true with 4 executions, got %+v", sum.Steps[0])
	}
}

func ptr(s string) *string { return &s }
func sPtr(s string) *scenario.Scalar {
	v := scenario.Scalar(s)
	return &v
}
func intPtr(i int) *int { return &i }
