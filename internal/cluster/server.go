package cluster

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Defaults shared by server and client options.
const (
	// DefaultMaxMessageSize limits a single encoded message in either
	// direction: 64 MiB. A StartRun carries every data file of the run, so
	// that is the practical upper bound for the total data file size.
	DefaultMaxMessageSize = 64 << 20

	DefaultSendQueue        = 256
	DefaultPingInterval     = 5 * time.Second
	DefaultPingTimeout      = 20 * time.Second
	DefaultHelloTimeout     = 10 * time.Second
	DefaultKeepaliveTime    = 30 * time.Second
	DefaultKeepaliveTimeout = 10 * time.Second

	// minClientKeepalive is what the server tolerates from clients. gRPC
	// raises any client keepalive time below 10s to 10s.
	minClientKeepalive = 5 * time.Second
)

// ServerOptions configure a Server. Zero values select the defaults.
type ServerOptions struct {
	// Version is the controller version. Nodes reporting any other version
	// are rejected.
	Version string
	// Authenticate reports whether a node token is valid. Compare with
	// TokenEqual to stay constant time. When nil, Token is used.
	Authenticate func(token string) bool
	// Token is the accepted node token when Authenticate is nil. An empty
	// Token with a nil Authenticate rejects every node.
	Token string
	// Handler receives node events. Nil discards them.
	Handler ServerHandler

	// PingInterval is how often a Ping is sent to each node (default 5s).
	PingInterval time.Duration
	// PingTimeout drops a node when nothing at all (Pong, SysStats or any
	// other message) arrived from it for this long (default 20s). It is
	// checked every PingInterval.
	PingTimeout time.Duration
	// HelloTimeout limits the wait for the first message (default 10s).
	HelloTimeout time.Duration
	// SendQueue is the number of queued outbound messages per node (default
	// 256). See Node.StartRun for the policy when it is full.
	SendQueue int
	// MaxMessageSize limits encoded messages in both directions (default
	// DefaultMaxMessageSize).
	MaxMessageSize int
	// KeepaliveTime and KeepaliveTimeout configure gRPC transport keepalive
	// pings to nodes (defaults 30s and 10s).
	KeepaliveTime    time.Duration
	KeepaliveTimeout time.Duration

	// Credentials secures the listener, for example
	// credentials.NewTLS(&tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ...})
	// for mutual TLS. Nil serves plaintext.
	Credentials credentials.TransportCredentials
	// Enroll handles CSR certificate signing for an enrolling agent.
	Enroll func(ctx context.Context, req *EnrollRequest) (*EnrollResponse, error)
	// VerifyClientCert validates the verified mTLS client certificate and node ID.
	VerifyClientCert func(cert *x509.Certificate, nodeID string) error
	// GRPCOptions are appended to the options NewServer builds.
	GRPCOptions []grpc.ServerOption
	// OnReject, when set, is notified whenever a connecting node is rejected.
	OnReject func(RejectInfo)
	// Logf, when set, receives diagnostic messages.
	Logf func(format string, args ...any)
}

// RejectInfo describes a rejected node handshake for diagnostics and UI alerts.
type RejectInfo struct {
	RemoteAddr    string
	NodeName      string
	NodeVersion   string
	ServerVersion string
	Reason        RejectReason
	Message       string
	Time          time.Time
}

func (o *ServerOptions) setDefaults() {
	if o.PingInterval <= 0 {
		o.PingInterval = DefaultPingInterval
	}
	if o.PingTimeout <= 0 {
		o.PingTimeout = DefaultPingTimeout
	}
	if o.HelloTimeout <= 0 {
		o.HelloTimeout = DefaultHelloTimeout
	}
	if o.SendQueue <= 0 {
		o.SendQueue = DefaultSendQueue
	}
	if o.MaxMessageSize <= 0 {
		o.MaxMessageSize = DefaultMaxMessageSize
	}
	if o.KeepaliveTime <= 0 {
		o.KeepaliveTime = DefaultKeepaliveTime
	}
	if o.KeepaliveTimeout <= 0 {
		o.KeepaliveTimeout = DefaultKeepaliveTimeout
	}
}

// ServerHandler receives node events.
//
// Calls for one node never overlap and follow arrival order: OnNodeJoined
// first, OnNodeLeft exactly once and last. Calls for different nodes run
// concurrently. A slow method delays only its own node (gRPC flow control
// then slows that node down). Methods must not call Server.Stop.
type ServerHandler interface {
	OnNodeJoined(n *Node)
	OnNodeLeft(n *Node, reason error)
	OnSysStats(n *Node, s *SysStats)
	OnRunSnapshot(n *Node, s *RunSnapshot)
	OnRunFinished(n *Node, f *RunFinished)
}

type nopServerHandler struct{}

func (nopServerHandler) OnNodeJoined(*Node)                {}
func (nopServerHandler) OnNodeLeft(*Node, error)           {}
func (nopServerHandler) OnSysStats(*Node, *SysStats)       {}
func (nopServerHandler) OnRunSnapshot(*Node, *RunSnapshot) {}
func (nopServerHandler) OnRunFinished(*Node, *RunFinished) {}

// NodeInfo describes a connected node.
type NodeInfo struct {
	ID          string
	Name        string
	Version     string
	OS          string
	Arch        string
	CPUCores    int
	MemoryBytes uint64
	Interfaces  []NetInterface
	RemoteAddr  string
	ConnectedAt time.Time
	// LastSeen is when the last message arrived from the node.
	LastSeen time.Time
	// RTT is the last Ping/Pong round trip time; 0 before the first Pong.
	RTT time.Duration
	// ClockOffset is the node clock minus the controller clock, estimated
	// from the last Pong.
	ClockOffset time.Duration
	// Resumed is true when the node reconnected with its previous ID.
	Resumed bool
	// Labels describes optional node labels (e.g. zone=ap-east).
	Labels map[string]string
	// CertSerial is the lowercase hex serial of the verified client mTLS certificate.
	CertSerial string
}

// Server accepts node connections.
type Server struct {
	opts    ServerOptions
	handler ServerHandler
	gs      *grpc.Server
	epoch   time.Time // monotonic reference for liveness checks

	mu     sync.Mutex
	nodes  map[string]*Node // sessions holding an ID, joined or not
	closed bool
	quit   chan struct{}
	wg     sync.WaitGroup // handlers and session goroutines
}

// NewServer creates a server. Call Serve to accept connections.
func NewServer(opts ServerOptions) *Server {
	opts.setDefaults()
	s := &Server{
		opts:    opts,
		handler: opts.Handler,
		epoch:   time.Now(),
		nodes:   map[string]*Node{},
		quit:    make(chan struct{}),
	}
	if s.handler == nil {
		s.handler = nopServerHandler{}
	}
	gopts := []grpc.ServerOption{
		grpc.ForceServerCodec(codec{}),
		grpc.MaxRecvMsgSize(opts.MaxMessageSize),
		grpc.MaxSendMsgSize(opts.MaxMessageSize),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    opts.KeepaliveTime,
			Timeout: opts.KeepaliveTimeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             minClientKeepalive,
			PermitWithoutStream: true,
		}),
	}
	if opts.Credentials != nil {
		gopts = append(gopts, grpc.Creds(opts.Credentials))
	}
	gopts = append(gopts, opts.GRPCOptions...)
	s.gs = grpc.NewServer(gopts...)
	s.gs.RegisterService(&serviceDesc, s)
	return s
}

// Serve accepts connections on lis until Stop. It returns nil after Stop.
func (s *Server) Serve(lis net.Listener) error {
	return s.gs.Serve(lis)
}

// Stop closes the listener and every node connection. It returns after
// OnNodeLeft has been delivered for every node that had joined.
func (s *Server) Stop() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.quit)
	}
	nodes := make([]*Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		nodes = append(nodes, n)
	}
	s.mu.Unlock()
	for _, n := range nodes {
		n.close(ErrServerClosed)
	}
	s.gs.Stop()
	s.wg.Wait()
}

// Nodes returns the connected nodes ordered by connection time.
func (s *Server) Nodes() []NodeInfo {
	s.mu.Lock()
	out := make([]NodeInfo, 0, len(s.nodes))
	for _, n := range s.nodes {
		if n.joined.Load() {
			out = append(out, n.Info())
		}
	}
	s.mu.Unlock()
	slices.SortFunc(out, func(a, b NodeInfo) int {
		if c := a.ConnectedAt.Compare(b.ConnectedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// Node returns the connected node with the given ID, or nil.
func (s *Server) Node(id string) *Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.nodes[id]; n != nil && n.joined.Load() {
		return n
	}
	return nil
}

// DisconnectNode sends a Reject frame with reason to an active node and closes its stream.
func (s *Server) DisconnectNode(id string, reason string) bool {
	s.mu.Lock()
	n := s.nodes[id]
	s.mu.Unlock()
	if n == nil || !n.joined.Load() {
		return false
	}
	n.rejectAndClose(RejectBadToken, reason)
	return true
}

func (s *Server) enroll(ctx context.Context, req *EnrollRequest) (*EnrollResponse, error) {
	if !s.enter() {
		return nil, status.Error(codes.Unavailable, ErrServerClosed.Error())
	}
	defer s.wg.Done()
	if s.opts.Enroll == nil {
		return &EnrollResponse{RejectMessage: "控制器未启用节点证书签发服务"}, nil
	}
	return s.opts.Enroll(ctx, req)
}

func (s *Server) logf(format string, args ...any) {
	if s.opts.Logf != nil {
		s.opts.Logf(format, args...)
	}
}

func (s *Server) now() int64 { return int64(time.Since(s.epoch)) }

// enter registers a handler with the wait group unless the server stopped.
func (s *Server) enter() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.wg.Add(1)
	return true
}

func (s *Server) authenticate(token string) bool {
	if s.opts.Authenticate != nil {
		return s.opts.Authenticate(token)
	}
	return s.opts.Token != "" && TokenEqual(token, s.opts.Token)
}

// register claims n.id for n. An existing session with the same ID is closed
// and register waits until its OnNodeLeft has been delivered, so events of
// one ID never interleave.
func (s *Server) register(n *Node) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrServerClosed
	}
	old := s.nodes[n.id]
	s.nodes[n.id] = n
	s.mu.Unlock()
	if old == nil {
		return nil
	}
	old.close(ErrNodeReplaced)
	select {
	case <-old.left:
		return nil
	case <-n.quit:
		return n.cause
	case <-s.quit:
		return ErrServerClosed
	}
}

func (s *Server) unregister(n *Node) {
	s.mu.Lock()
	if s.nodes[n.id] == n {
		delete(s.nodes, n.id)
	}
	s.mu.Unlock()
}

// newNodeID returns hint when it is a usable ID, otherwise a random one.
func (s *Server) newNodeID(hint string) (string, bool) {
	if validNodeID(hint) {
		return hint, true
	}
	var b [8]byte
	for {
		_, _ = rand.Read(b[:])
		id := "node-" + hex.EncodeToString(b[:])
		s.mu.Lock()
		_, taken := s.nodes[id]
		s.mu.Unlock()
		if !taken {
			return id, false
		}
	}
}

func validNodeID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

type recvResult struct {
	msg *NodeMessage
	err error
}

// connect handles one node stream. The handler goroutine performs the
// handshake and the ping schedule; a separate receive goroutine delivers
// events and, after the stream ends, OnNodeLeft; a send goroutine drains
// the node's queue. Server-side RecvMsg can only be interrupted by
// returning from the handler, which is why receiving is not done here.
func (s *Server) connect(stream grpc.ServerStream) error {
	if !s.enter() {
		return status.Error(codes.Unavailable, ErrServerClosed.Error())
	}
	defer s.wg.Done()
	ctx := stream.Context()
	remote := ""
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		remote = p.Addr.String()
	}

	helloCh := make(chan recvResult, 1)
	startCh := make(chan *Node, 1)
	var n *Node
	started := false
	defer func() {
		if started {
			return
		}
		startCh <- nil
		if n != nil {
			n.close(ErrNodeDisconnected)
			s.unregister(n)
			close(n.left)
		}
	}()
	s.wg.Add(1)
	go s.receive(stream, helloCh, startCh)

	timer := time.NewTimer(s.opts.HelloTimeout)
	defer timer.Stop()
	var first recvResult
	select {
	case first = <-helloCh:
	case <-timer.C:
		s.logf("节点 %s 未在 %s 内发送问候消息，已断开", remote, s.opts.HelloTimeout)
		return status.Error(codes.DeadlineExceeded, "等待节点问候消息超时")
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	case <-s.quit:
		return status.Error(codes.Unavailable, ErrServerClosed.Error())
	}
	if first.err != nil {
		return first.err
	}
	hello, ok := first.msg.Body.(*Hello)
	if !ok {
		return s.reject(stream, remote, "", "", RejectProtocol, "协议错误：节点的第一条消息必须是 Hello", "")
	}

	var certSerial string
	if s.opts.VerifyClientCert != nil {
		var leafCert *x509.Certificate
		if p, hasPeer := peer.FromContext(ctx); hasPeer {
			if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok && len(tlsInfo.State.VerifiedChains) > 0 && len(tlsInfo.State.VerifiedChains[0]) > 0 {
				leafCert = tlsInfo.State.VerifiedChains[0][0]
			}
		}
		if leafCert == nil {
			return s.reject(stream, remote, hello.Name, hello.Version, RejectBadToken, "集群已启用双向 TLS，节点必须先凭令牌签发有效客户端证书", "")
		}
		if err := s.opts.VerifyClientCert(leafCert, hello.NodeID); err != nil {
			return s.reject(stream, remote, hello.Name, hello.Version, RejectBadToken, err.Error(), "")
		}
		certSerial = CertSerialHex(leafCert)
	} else if !s.authenticate(hello.Token) {
		return s.reject(stream, remote, hello.Name, hello.Version, RejectBadToken, "节点令牌无效，控制器拒绝接入", "")
	}
	if hello.Version != s.opts.Version {
		msg := fmt.Sprintf("节点版本 %s 与控制器版本 %s 不一致，请使用与控制器相同版本的 tidegate", orUnknown(hello.Version), orUnknown(s.opts.Version))
		return s.reject(stream, remote, hello.Name, hello.Version, RejectVersionMismatch, msg, s.opts.Version)
	}

	n = s.newNode(hello, remote, certSerial)
	if err := s.register(n); err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	welcome, err := encodeController(&Welcome{
		NodeID:        n.id,
		ServerTime:    time.Now(),
		ServerVersion: s.opts.Version,
		PingInterval:  s.opts.PingInterval,
	}, s.opts.MaxMessageSize)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	if err := stream.SendMsg(welcome); err != nil {
		return err
	}
	s.wg.Add(1)
	go n.sendLoop(stream)
	n.joined.Store(true)
	s.logf("节点 %s（%s，%s）已接入", n.id, n.hello.Name, remote)
	s.handler.OnNodeJoined(n)
	started = true
	startCh <- n

	n.watch(ctx)
	<-n.sendDone
	s.unregister(n)
	return status.Error(codes.Unavailable, n.cause.Error())
}

func orUnknown(v string) string {
	if v == "" {
		return "（未知）"
	}
	return v
}

func (s *Server) reject(stream grpc.ServerStream, remote, nodeName, nodeVersion string, reason RejectReason, msg, serverVersion string) error {
	s.logf("拒绝节点 %s 接入：%s", remote, msg)
	if s.opts.OnReject != nil {
		s.opts.OnReject(RejectInfo{
			RemoteAddr:    remote,
			NodeName:      nodeName,
			NodeVersion:   nodeVersion,
			ServerVersion: serverVersion,
			Reason:        reason,
			Message:       msg,
			Time:          time.Now(),
		})
	}
	f, err := encodeController(&Reject{Reason: reason, Message: msg, ServerVersion: serverVersion}, s.opts.MaxMessageSize)
	if err == nil {
		_ = stream.SendMsg(f)
	}
	code := codes.InvalidArgument
	switch reason {
	case RejectBadToken:
		code = codes.Unauthenticated
	case RejectVersionMismatch:
		code = codes.FailedPrecondition
	}
	return status.Error(code, msg)
}

// receive reads the stream. The first message goes to the handler through
// helloCh; once the handler hands over an established session it delivers
// events until the stream ends and then emits OnNodeLeft.
func (s *Server) receive(stream grpc.ServerStream, helloCh chan<- recvResult, startCh <-chan *Node) {
	defer s.wg.Done()
	first := new(NodeMessage)
	err := stream.RecvMsg(first)
	helloCh <- recvResult{first, err}
	if err != nil {
		return
	}
	n := <-startCh
	if n == nil {
		return
	}
	for {
		m := new(NodeMessage)
		if err := stream.RecvMsg(m); err != nil {
			n.close(nodeDisconnected(err))
			break
		}
		n.lastSeen.Store(s.now())
		switch b := m.Body.(type) {
		case *SysStats:
			s.handler.OnSysStats(n, b)
		case *RunSnapshot:
			s.handler.OnRunSnapshot(n, b)
		case *RunFinished:
			s.handler.OnRunFinished(n, b)
		case *Pong:
			n.onPong(b)
		}
	}
	s.unregister(n)
	s.logf("节点 %s 已离开：%v", n.id, n.cause)
	s.handler.OnNodeLeft(n, n.cause)
	close(n.left)
}

// Node is one connected node session. Its methods are safe for concurrent
// use and never block on the network.
type Node struct {
	srv         *Server
	id          string
	hello       Hello // Token cleared
	certSerial  string
	remote      string
	connectedAt time.Time
	resumed     bool

	queue   chan frame
	pingq   chan frame
	rejectf chan frame

	closeOnce sync.Once
	cause     error         // set before quit is closed
	quit      chan struct{} // closed when the session ends
	left      chan struct{} // closed after OnNodeLeft returned
	sendDone  chan struct{} // closed after sendLoop flushes and exits
	joined    atomic.Bool

	lastSeen  atomic.Int64 // Server.now() of the last received message
	pingNonce atomic.Uint64

	mu        sync.Mutex
	rtt       time.Duration
	offset    time.Duration
	sentNonce uint64
	sentAt    time.Time
}

func (s *Server) newNode(h *Hello, remote, certSerial string) *Node {
	id, resumed := s.newNodeID(h.NodeID)
	hello := *h
	hello.Token = ""
	hello.Interfaces = slices.Clone(h.Interfaces)
	hello.Labels = maps.Clone(h.Labels)
	n := &Node{
		srv:         s,
		id:          id,
		hello:       hello,
		certSerial:  certSerial,
		remote:      remote,
		connectedAt: time.Now(),
		resumed:     resumed,
		queue:       make(chan frame, s.opts.SendQueue),
		pingq:       make(chan frame, 1),
		rejectf:     make(chan frame, 1),
		quit:        make(chan struct{}),
		left:        make(chan struct{}),
		sendDone:    make(chan struct{}),
	}
	n.lastSeen.Store(s.now())
	return n
}

// ID returns the node ID assigned in Welcome.
func (n *Node) ID() string { return n.id }

// Info returns a snapshot of the node's description and link statistics.
func (n *Node) Info() NodeInfo {
	n.mu.Lock()
	rtt, offset := n.rtt, n.offset
	n.mu.Unlock()
	h := &n.hello
	return NodeInfo{
		ID:          n.id,
		Name:        h.Name,
		Version:     h.Version,
		OS:          h.OS,
		Arch:        h.Arch,
		CPUCores:    h.CPUCores,
		MemoryBytes: h.MemoryBytes,
		Interfaces:  slices.Clone(h.Interfaces),
		RemoteAddr:  n.remote,
		ConnectedAt: n.connectedAt,
		LastSeen:    n.srv.epoch.Add(time.Duration(n.lastSeen.Load())),
		RTT:         rtt,
		ClockOffset: offset,
		Resumed:     n.resumed,
		Labels:      maps.Clone(h.Labels),
		CertSerial:  n.certSerial,
	}
}

// StartRun queues a StartRun for the node.
//
// Like every send method it never blocks. It returns ErrMessageTooLarge
// (wrapped) when the encoded message exceeds the maximum message size,
// ErrNodeGone when the session has ended, and ErrSendQueueFull when the
// node's queue (ServerOptions.SendQueue messages) is full; in all three
// cases nothing is sent and the session is left as it is. A nil error means
// the message was queued, not that the node received it: if the session
// ends first, queued messages are discarded and OnNodeLeft follows.
func (n *Node) StartRun(r *StartRun) error {
	if r == nil || r.RunID == "" {
		return errors.New("压测 ID 不能为空")
	}
	return n.send(r)
}

// StopRun queues a StopRun for the node. See StartRun for errors.
func (n *Node) StopRun(runID string, mode StopMode) error {
	return n.send(&StopRun{RunID: runID, Mode: mode})
}

// SetTarget queues a SetTarget for the node. See StartRun for errors.
func (n *Node) SetTarget(runID string, value float64) error {
	return n.send(&SetTarget{RunID: runID, Value: value})
}

// SetLoadShare queues a dynamic LoadShare adjustment for the node.
func (n *Node) SetLoadShare(runID string, share float64) error {
	return n.send(&SetTarget{RunID: runID, LoadShare: share})
}

// Close disconnects the node. OnNodeLeft follows with ErrNodeClosed.
func (n *Node) Close() { n.close(ErrNodeClosed) }

func (n *Node) rejectAndClose(reason RejectReason, message string) {
	if f, err := encodeController(&Reject{Reason: reason, Message: message}, n.srv.opts.MaxMessageSize); err == nil {
		select {
		case n.rejectf <- f:
		default:
		}
	}
	n.close(errors.New(message))
}

func (n *Node) send(b ControllerBody) error {
	select {
	case <-n.quit:
		return ErrNodeGone
	default:
	}
	f, err := encodeController(b, n.srv.opts.MaxMessageSize)
	if err != nil {
		return err
	}
	select {
	case n.queue <- f:
		return nil
	default:
	}
	select {
	case <-n.quit:
		return ErrNodeGone
	default:
		return ErrSendQueueFull
	}
}

func (n *Node) close(cause error) {
	n.closeOnce.Do(func() {
		n.cause = cause
		close(n.quit)
	})
}

// sendLoop writes queued messages. Rejects and pings skip ahead of queued
// messages. A slow node blocks only this goroutine.
func (n *Node) sendLoop(stream grpc.ServerStream) {
	defer n.srv.wg.Done()
	defer close(n.sendDone)
	for {
		select {
		case f := <-n.rejectf:
			_ = stream.SendMsg(f)
			return
		default:
		}
		var f frame
		select {
		case f = <-n.rejectf:
			_ = stream.SendMsg(f)
			return
		case <-n.quit:
			select {
			case rf := <-n.rejectf:
				_ = stream.SendMsg(rf)
			default:
			}
			return
		case f = <-n.pingq:
		default:
			select {
			case f = <-n.rejectf:
				_ = stream.SendMsg(f)
				return
			case <-n.quit:
				select {
				case rf := <-n.rejectf:
					_ = stream.SendMsg(rf)
				default:
				}
				return
			case f = <-n.pingq:
			case f = <-n.queue:
			}
		}
		if err := stream.SendMsg(f); err != nil {
			n.close(nodeDisconnected(err))
			return
		}
	}
}

// watch sends pings and enforces the liveness deadline until the session
// ends.
func (n *Node) watch(ctx context.Context) {
	opts := &n.srv.opts
	t := time.NewTicker(opts.PingInterval)
	defer t.Stop()
	n.ping()
	for {
		select {
		case <-n.quit:
			return
		case <-ctx.Done():
			n.close(nodeDisconnected(ctx.Err()))
			return
		case <-t.C:
			if idle := time.Duration(n.srv.now() - n.lastSeen.Load()); idle > opts.PingTimeout {
				n.close(ErrNodeTimeout)
				return
			}
			n.ping()
		}
	}
}

// ping queues a Ping unless one is already waiting to be sent.
func (n *Node) ping() {
	nonce := n.pingNonce.Add(1)
	sent := time.Now()
	f, err := encodeController(&Ping{Nonce: nonce, Time: sent}, n.srv.opts.MaxMessageSize)
	if err != nil {
		return
	}
	n.mu.Lock()
	n.sentNonce = nonce
	n.sentAt = sent
	n.mu.Unlock()
	select {
	case n.pingq <- f:
	default:
	}
}

func (n *Node) onPong(p *Pong) {
	if p.PingTime.IsZero() {
		return
	}
	now := time.Now()
	n.mu.Lock()
	var rtt time.Duration
	if p.Nonce != 0 && p.Nonce == n.sentNonce && !n.sentAt.IsZero() {
		rtt = now.Sub(n.sentAt)
	} else {
		rtt = now.Sub(p.PingTime)
	}
	if rtt < 0 {
		n.mu.Unlock()
		return
	}
	if rtt == 0 {
		rtt = time.Nanosecond
	}
	n.rtt = rtt
	if !p.NodeTime.IsZero() {
		n.offset = p.NodeTime.Sub(p.PingTime.Add(rtt / 2))
	}
	n.mu.Unlock()
}
