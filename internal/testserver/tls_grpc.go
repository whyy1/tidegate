package testserver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// H2Server is an HTTPS (HTTP/2 + HTTP/1.1) test server built on net/http.
type H2Server struct {
	srv  *http.Server
	ln   net.Listener
	reqs atomic.Int64
}

// ListenH2 starts an HTTP/2-capable TLS test server on addr.
func ListenH2(addr string) (*H2Server, error) {
	cert, err := generateSelfSignedCert()
	if err != nil {
		return nil, err
	}
	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	tlsLn := tls.NewListener(tcpLn, &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2", "http/1.1"},
	})
	h := &H2Server{ln: tlsLn}
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		h.reqs.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "pong\n")
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		h.reqs.Add(1)
		body, _ := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
		if ct := r.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/delay", func(w http.ResponseWriter, r *http.Request) {
		h.reqs.Add(1)
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		if ms > 0 {
			timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		h.reqs.Add(1)
		code, err := strconv.Atoi(r.URL.Query().Get("code"))
		if err != nil || code < 200 || code > 599 {
			http.Error(w, "code must be 200-599", http.StatusBadRequest)
			return
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, "status "+strconv.Itoa(code)+"\n")
	})
	h.srv = &http.Server{Handler: mux}
	return h, nil
}

func (h *H2Server) Addr() string    { return h.ln.Addr().String() }
func (h *H2Server) Requests() int64 { return h.reqs.Load() }
func (h *H2Server) URL() string {
	addr := h.ln.Addr().(*net.TCPAddr)
	host := addr.IP.String()
	if addr.IP.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(addr.Port))
}
func (h *H2Server) Start() { go h.Serve() }
func (h *H2Server) Serve() error {
	err := h.srv.Serve(h.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (h *H2Server) Close() error { return h.srv.Close() }

// GRPCTestServer serves tidegate.test.EchoService with gRPC Server Reflection enabled.
type GRPCTestServer struct {
	srv      *grpc.Server
	ln       net.Listener
	calls    atomic.Int64
	tlsMode  bool
	inDesc   protoreflect.MessageDescriptor
	outDesc  protoreflect.MessageDescriptor
	codecReg sync.Once
}

var (
	echoProtoOnce sync.Once
	echoInDesc    protoreflect.MessageDescriptor
	echoOutDesc   protoreflect.MessageDescriptor
	echoProtoErr  error
)

func registerEchoProto() (protoreflect.MessageDescriptor, protoreflect.MessageDescriptor, error) {
	echoProtoOnce.Do(func() {
		str := descriptorpb.FieldDescriptorProto_TYPE_STRING
		i64 := descriptorpb.FieldDescriptorProto_TYPE_INT64
		opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
		pkg := "tidegate.test"
		fdp := &descriptorpb.FileDescriptorProto{
			Name:    proto.String("tidegate_test_echo.proto"),
			Package: proto.String(pkg),
			Syntax:  proto.String("proto3"),
			MessageType: []*descriptorpb.DescriptorProto{
				{
					Name: proto.String("EchoRequest"),
					Field: []*descriptorpb.FieldDescriptorProto{
						{Name: proto.String("message"), JsonName: proto.String("message"), Number: proto.Int32(1), Label: &opt, Type: &str},
						{Name: proto.String("count"), JsonName: proto.String("count"), Number: proto.Int32(2), Label: &opt, Type: &i64},
					},
				},
				{
					Name: proto.String("EchoReply"),
					Field: []*descriptorpb.FieldDescriptorProto{
						{Name: proto.String("message"), JsonName: proto.String("message"), Number: proto.Int32(1), Label: &opt, Type: &str},
						{Name: proto.String("count"), JsonName: proto.String("count"), Number: proto.Int32(2), Label: &opt, Type: &i64},
						{Name: proto.String("seq"), JsonName: proto.String("seq"), Number: proto.Int32(3), Label: &opt, Type: &i64},
					},
				},
			},
			Service: []*descriptorpb.ServiceDescriptorProto{
				{
					Name: proto.String("EchoService"),
					Method: []*descriptorpb.MethodDescriptorProto{
						{
							Name:       proto.String("UnaryEcho"),
							InputType:  proto.String(".tidegate.test.EchoRequest"),
							OutputType: proto.String(".tidegate.test.EchoReply"),
						},
					},
				},
			},
		}
		fd, err := protodesc.NewFile(fdp, nil)
		if err != nil {
			echoProtoErr = err
			return
		}
		if _, err := protoregistry.GlobalFiles.FindFileByPath(fd.Path()); err != nil {
			if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
				echoProtoErr = err
				return
			}
		}
		echoInDesc = fd.Messages().ByName("EchoRequest")
		echoOutDesc = fd.Messages().ByName("EchoReply")
	})
	return echoInDesc, echoOutDesc, echoProtoErr
}

type rawTestCodec struct{}

func (rawTestCodec) Name() string { return "proto" }
func (rawTestCodec) Marshal(v any) ([]byte, error) {
	if b, ok := v.([]byte); ok {
		return b, nil
	}
	if m, ok := v.(proto.Message); ok {
		return proto.Marshal(m)
	}
	return nil, errors.New("unsupported marshal value")
}
func (rawTestCodec) Unmarshal(data []byte, v any) error {
	if dst, ok := v.(*[]byte); ok {
		*dst = append((*dst)[:0], data...)
		return nil
	}
	if m, ok := v.(proto.Message); ok {
		return proto.Unmarshal(data, m)
	}
	return errors.New("unsupported unmarshal value")
}

// ListenGRPC starts a gRPC test server on addr with Server Reflection registered.
func ListenGRPC(addr string, withTLS bool) (*GRPCTestServer, error) {
	inDesc, outDesc, err := registerEchoProto()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if withTLS {
		cert, err := generateSelfSignedCert()
		if err != nil {
			ln.Close()
			return nil, err
		}
		ln = tls.NewListener(ln, &tls.Config{
			Certificates: []tls.Certificate{cert},
			NextProtos:   []string{"h2"},
		})
	}
	gs := &GRPCTestServer{ln: ln, tlsMode: withTLS, inDesc: inDesc, outDesc: outDesc}
	srv := grpc.NewServer(grpc.ForceServerCodec(rawTestCodec{}))
	svcDesc := &grpc.ServiceDesc{
		ServiceName: "tidegate.test.EchoService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "UnaryEcho",
				Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
					var raw []byte
					if err := dec(&raw); err != nil {
						return nil, err
					}
					seq := gs.calls.Add(1)
					inMsg := dynamicpb.NewMessage(gs.inDesc)
					outMsg := dynamicpb.NewMessage(gs.outDesc)
					if err := proto.Unmarshal(raw, inMsg); err == nil {
						msgVal := inMsg.Get(gs.inDesc.Fields().ByName("message")).String()
						cntVal := inMsg.Get(gs.inDesc.Fields().ByName("count")).Int()
						outMsg.Set(gs.outDesc.Fields().ByName("message"), protoreflect.ValueOfString(msgVal))
						outMsg.Set(gs.outDesc.Fields().ByName("count"), protoreflect.ValueOfInt64(cntVal))
						outMsg.Set(gs.outDesc.Fields().ByName("seq"), protoreflect.ValueOfInt64(seq))
						return proto.Marshal(outMsg)
					}
					// Fallback if caller sent JSON payload directly with non-proto encoding
					var m map[string]any
					if json.Unmarshal(raw, &m) == nil {
						m["seq"] = seq
						return json.Marshal(m)
					}
					return raw, nil
				},
			},
		},
	}
	var impl any = gs
	srv.RegisterService(svcDesc, impl)
	reflection.Register(srv)
	gs.srv = srv
	return gs, nil
}

func (g *GRPCTestServer) Addr() string  { return g.ln.Addr().String() }
func (g *GRPCTestServer) Calls() int64  { return g.calls.Load() }
func (g *GRPCTestServer) Start()        { go g.Serve() }
func (g *GRPCTestServer) Serve() error  { return g.srv.Serve(g.ln) }
func (g *GRPCTestServer) Close()        { g.srv.Stop(); _ = g.ln.Close() }
