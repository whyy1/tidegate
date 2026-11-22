package cluster

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// Client defaults.
const (
	DefaultMinBackoff         = 500 * time.Millisecond
	DefaultMaxBackoff         = 30 * time.Second
	DefaultConnectTimeout     = 10 * time.Second
	DefaultMaxBufferedReports = 1024
	DefaultMaxBufferedBytes   = 64 << 20

	// dispatchQueue is the number of received controller messages waiting
	// for the handler before the receive loop pauses.
	dispatchQueue = 64
)

// ClientOptions configure a Client. Zero values select the defaults.
type ClientOptions struct {
	// Addr is the controller cluster address, host:port.
	Addr  string
	Token string
	// Name is the node name; defaults to Hello().Name, then the host name.
	Name    string
	Version string
	// Hello, when set, describes the machine. It is called before every
	// connection attempt; Token, NodeID, Version and a non-empty Name are
	// filled in by the client. Empty OS, Arch and CPUCores default to the
	// runtime values.
	Hello func() Hello

	// MinBackoff and MaxBackoff bound the reconnect delay (defaults 500ms
	// and 30s). The delay doubles after every failed attempt, is capped at
	// MaxBackoff, and a random value in [delay/2, delay] is used. It resets
	// after a successful Welcome.
	MinBackoff time.Duration
	MaxBackoff time.Duration
	// ConnectTimeout limits one attempt from dialing to Welcome (default 10s).
	ConnectTimeout time.Duration
	// PingTimeout, when positive, drops the connection when nothing arrived
	// from the controller for this long. The controller pings every few
	// seconds, but a large StartRun on a slow link can take longer than
	// that to arrive, so this is off by default and dead connections are
	// detected by gRPC keepalive instead.
	PingTimeout time.Duration
	// KeepaliveTime and KeepaliveTimeout configure gRPC transport keepalive
	// (defaults 30s and 10s; gRPC raises times below 10s to 10s).
	KeepaliveTime    time.Duration
	KeepaliveTimeout time.Duration
	// MaxMessageSize limits encoded messages in both directions (default
	// DefaultMaxMessageSize).
	MaxMessageSize int
	// MaxBufferedReports and MaxBufferedBytes bound the queue of unsent
	// RunSnapshot and RunFinished messages (defaults 1024 messages and
	// 64 MiB). See SendRunSnapshot for the overflow policy.
	MaxBufferedReports int
	MaxBufferedBytes   int

	// Credentials secures the connection, for example credentials.NewTLS
	// with a client certificate for mutual TLS. Nil connects in plaintext.
	Credentials credentials.TransportCredentials
	// DialOptions are appended to the options the client builds.
	DialOptions []grpc.DialOption
	// Logf, when set, receives diagnostic messages.
	Logf func(format string, args ...any)
}

func (o *ClientOptions) setDefaults() {
	if o.MinBackoff <= 0 {
		o.MinBackoff = DefaultMinBackoff
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = DefaultMaxBackoff
	}
	if o.MaxBackoff < o.MinBackoff {
		o.MaxBackoff = o.MinBackoff
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = DefaultConnectTimeout
	}
	if o.KeepaliveTime <= 0 {
		o.KeepaliveTime = DefaultKeepaliveTime
	}
	if o.KeepaliveTimeout <= 0 {
		o.KeepaliveTimeout = DefaultKeepaliveTimeout
	}
	if o.MaxMessageSize <= 0 {
		o.MaxMessageSize = DefaultMaxMessageSize
	}
	if o.MaxBufferedReports <= 0 {
		o.MaxBufferedReports = DefaultMaxBufferedReports
	}
	if o.MaxBufferedBytes <= 0 {
		o.MaxBufferedBytes = DefaultMaxBufferedBytes
	}
}

// ClientHandler receives controller messages.
//
// Calls never overlap and follow the connection's order: OnWelcome first,
// then the controller's messages, then OnDisconnected once that connection
// has ended and none of its messages remain to be delivered. OnWelcome and
// OnDisconnected are only called for connections the controller accepted.
// Methods should return quickly; while they run, further controller messages
// wait (Pings are still answered).
type ClientHandler interface {
	OnWelcome(w WelcomeInfo)
	OnStartRun(r *StartRun)
	OnStopRun(r *StopRun)
	OnSetTarget(t *SetTarget)
	OnDisconnected(err error)
}

// WelcomeInfo describes an accepted connection.
type WelcomeInfo struct {
	NodeID        string
	ServerVersion string
	ServerTime    time.Time
	// ClockOffset is the controller clock minus the local clock, estimated
	// from the handshake.
	ClockOffset time.Duration
	// RTT is the handshake round trip time.
	RTT time.Duration
	// Resumed is true when the controller kept the previous node ID.
	Resumed bool
}

// ClientStats are counters of a Client.
type ClientStats struct {
	Connected bool
	NodeID    string
	// BufferedReports and BufferedBytes describe RunSnapshot and RunFinished
	// messages waiting to be sent.
	BufferedReports int
	BufferedBytes   int
	// DroppedSnapshots counts RunSnapshot messages dropped because the
	// buffer was full.
	DroppedSnapshots uint64
}

// Client is the node side of the protocol.
type Client struct {
	opts    ClientOptions
	out     outbox
	running atomic.Bool

	mu     sync.Mutex
	nodeID string
	offset time.Duration
}

// NewClient creates a client. Call Run to connect.
func NewClient(opts ClientOptions) *Client {
	opts.setDefaults()
	c := &Client{opts: opts}
	c.out.init(opts.MaxBufferedReports, opts.MaxBufferedBytes)
	return c
}

// Run connects to the controller, says hello and serves the connection,
// reconnecting with capped exponential backoff and jitter until ctx ends.
// It returns ctx.Err() when ctx ends and a *RejectedError, without retrying,
// when the controller refuses the node. Only one Run may be active per
// client.
func (c *Client) Run(ctx context.Context, h ClientHandler) error {
	if !c.running.CompareAndSwap(false, true) {
		return ErrAlreadyRunning
	}
	defer c.running.Store(false)
	attempt := 0
	for {
		welcomed, err := c.session(ctx, h)
		if rej := (*RejectedError)(nil); errors.As(err, &rej) {
			c.logf("控制器拒绝接入：%s", rej.Message)
			return rej
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if welcomed {
			attempt = 0
		}
		delay := c.backoff(attempt)
		attempt++
		c.logf("与控制器 %s 的连接失败或断开：%v，%s 后重连", c.opts.Addr, err, delay.Round(time.Millisecond))
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}

func (c *Client) backoff(attempt int) time.Duration {
	d := c.opts.MinBackoff
	for i := 0; i < attempt && d < c.opts.MaxBackoff; i++ {
		d *= 2
	}
	d = min(d, c.opts.MaxBackoff)
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(d-half)+1))
}

func (c *Client) logf(format string, args ...any) {
	if c.opts.Logf != nil {
		c.opts.Logf(format, args...)
	}
}

func (c *Client) dialOptions() []grpc.DialOption {
	creds := c.opts.Credentials
	if creds == nil {
		creds = insecure.NewCredentials()
	}
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(
			grpc.ForceCodec(codec{}),
			grpc.MaxCallRecvMsgSize(c.opts.MaxMessageSize),
			grpc.MaxCallSendMsgSize(c.opts.MaxMessageSize),
		),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    c.opts.KeepaliveTime,
			Timeout: c.opts.KeepaliveTimeout,
		}),
		grpc.WithDisableRetry(),
	}
	return append(opts, c.opts.DialOptions...)
}

func (c *Client) hello() *Hello {
	var h Hello
	if c.opts.Hello != nil {
		h = c.opts.Hello()
	}
	if h.OS == "" {
		h.OS = runtime.GOOS
	}
	if h.Arch == "" {
		h.Arch = runtime.GOARCH
	}
	if h.CPUCores == 0 {
		h.CPUCores = runtime.NumCPU()
	}
	if c.opts.Name != "" {
		h.Name = c.opts.Name
	}
	if h.Name == "" {
		h.Name, _ = os.Hostname()
	}
	h.Token = c.opts.Token
	h.Version = c.opts.Version
	c.mu.Lock()
	h.NodeID = c.nodeID
	c.mu.Unlock()
	return &h
}

// session runs one connection attempt. welcomed reports whether the
// controller accepted the node.
func (c *Client) session(ctx context.Context, h ClientHandler) (welcomed bool, err error) {
	conn, err := grpc.NewClient("passthrough:///"+c.opts.Addr, c.dialOptions()...)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	hsTimer := time.AfterFunc(c.opts.ConnectTimeout, func() { cancel(ErrHandshakeTimeout) })
	stream, w, rtt, offset, err := c.handshake(sctx, conn)
	if !hsTimer.Stop() && err == nil {
		err = ErrHandshakeTimeout
	}
	if err != nil {
		var rej *RejectedError
		if !errors.As(err, &rej) {
			if cause := context.Cause(sctx); cause != nil {
				err = cause
			}
		}
		return false, err
	}

	c.mu.Lock()
	resumed := c.nodeID != "" && c.nodeID == w.NodeID
	c.nodeID = w.NodeID
	c.offset = offset
	c.mu.Unlock()
	c.out.setConnected(true)
	c.logf("已接入控制器 %s，节点 ID %s", c.opts.Addr, w.NodeID)
	h.OnWelcome(WelcomeInfo{
		NodeID:        w.NodeID,
		ServerVersion: w.ServerVersion,
		ServerTime:    w.ServerTime,
		ClockOffset:   offset,
		RTT:           rtt,
		Resumed:       resumed,
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.sendLoop(sctx, cancel, stream)
	}()

	var lastRecv atomic.Int64 // time since start of the last received message
	start := time.Now()
	if c.opts.PingTimeout > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(max(c.opts.PingTimeout/4, time.Millisecond))
			defer t.Stop()
			for {
				select {
				case <-sctx.Done():
					return
				case <-t.C:
					if time.Since(start)-time.Duration(lastRecv.Load()) > c.opts.PingTimeout {
						cancel(ErrControllerTimeout)
						return
					}
				}
			}
		}()
	}

	dispatch := make(chan ControllerBody, dispatchQueue)
	dispatched := make(chan struct{})
	go func() {
		defer close(dispatched)
		for b := range dispatch {
			switch v := b.(type) {
			case *StartRun:
				h.OnStartRun(v)
			case *StopRun:
				h.OnStopRun(v)
			case *SetTarget:
				h.OnSetTarget(v)
			}
		}
	}()

	for {
		m := new(ControllerMessage)
		if err := stream.RecvMsg(m); err != nil {
			cancel(controllerDisconnected(err))
			break
		}
		lastRecv.Store(int64(time.Since(start)))
		switch b := m.Body.(type) {
		case *Ping:
			if f, err := encodeNode(&Pong{Nonce: b.Nonce, PingTime: b.Time, NodeTime: time.Now()}, c.opts.MaxMessageSize); err == nil {
				c.out.setPong(f)
			}
		case *StartRun, *StopRun, *SetTarget:
			select {
			case dispatch <- b:
			case <-sctx.Done():
			}
		case *Reject:
			cancel(&RejectedError{Reason: b.Reason, Message: b.Message, ServerVersion: b.ServerVersion})
		}
		if sctx.Err() != nil {
			break
		}
	}
	c.out.setConnected(false)
	close(dispatch)
	<-dispatched
	wg.Wait()
	err = context.Cause(sctx)
	h.OnDisconnected(err)
	return true, err
}

// handshake opens the stream, sends Hello and waits for Welcome or Reject.
func (c *Client) handshake(ctx context.Context, conn *grpc.ClientConn) (grpc.ClientStream, *Welcome, time.Duration, time.Duration, error) {
	stream, err := conn.NewStream(ctx, &connectStreamDesc, connectMethod)
	if err != nil {
		return nil, nil, 0, 0, controllerDisconnected(err)
	}
	f, err := encodeNode(c.hello(), c.opts.MaxMessageSize)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	t0 := time.Now()
	// On io.EOF the stream was ended by the controller; RecvMsg below
	// returns whatever it sent first, such as a Reject.
	if err := stream.SendMsg(f); err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, 0, 0, controllerDisconnected(err)
	}
	m := new(ControllerMessage)
	if err := stream.RecvMsg(m); err != nil {
		return nil, nil, 0, 0, controllerDisconnected(err)
	}
	rtt := time.Since(t0)
	switch b := m.Body.(type) {
	case *Reject:
		return nil, nil, 0, 0, &RejectedError{Reason: b.Reason, Message: b.Message, ServerVersion: b.ServerVersion}
	case *Welcome:
		var offset time.Duration
		if !b.ServerTime.IsZero() {
			offset = b.ServerTime.Sub(t0.Add(rtt / 2))
		}
		return stream, b, rtt, offset, nil
	}
	return nil, nil, 0, 0, errors.New("协议错误：控制器没有回复 Welcome")
}

// sendLoop writes outbound messages until the connection ends. A report
// whose write failed goes back to the head of the buffer.
func (c *Client) sendLoop(ctx context.Context, cancel context.CancelCauseFunc, stream grpc.ClientStream) {
	for {
		if ctx.Err() != nil {
			return
		}
		f, rep, ok := c.out.next()
		if !ok {
			select {
			case <-c.out.notify:
				continue
			case <-ctx.Done():
				return
			}
		}
		if err := stream.SendMsg(f); err != nil {
			if rep.f != nil {
				c.out.requeue(rep)
			}
			cancel(controllerDisconnected(err))
			return
		}
	}
}

// SendSysStats sends one machine statistics sample. Only the newest unsent
// sample is kept; while disconnected the sample is dropped and
// ErrNotConnected is returned.
func (c *Client) SendSysStats(t time.Time, json []byte) error {
	f, err := encodeNode(&SysStats{Time: t, JSON: json}, c.opts.MaxMessageSize)
	if err != nil {
		return err
	}
	if !c.out.setSys(f) {
		return ErrNotConnected
	}
	return nil
}

// SendRunSnapshot queues a snapshot of a run. snapshot is copied.
//
// Snapshots and RunFinished messages share one FIFO buffer that survives
// reconnects and is flushed in order after the next Welcome. The buffer
// holds at most MaxBufferedReports messages and MaxBufferedBytes encoded
// bytes; when either bound is exceeded the oldest RunSnapshot is dropped
// (counted in Stats().DroppedSnapshots). RunFinished messages are never
// dropped and may exceed the bounds. A message already handed to a
// connection that then breaks may be lost; the message whose write failed
// is sent again first, so the controller may see a gap or a duplicate seq.
func (c *Client) SendRunSnapshot(runID string, seq int64, snapshot []byte) error {
	f, err := encodeNode(&RunSnapshot{RunID: runID, Seq: seq, Snapshot: snapshot}, c.opts.MaxMessageSize)
	if err != nil {
		return err
	}
	c.out.pushReport(report{f: f})
	return nil
}

// SendRunFinished queues the end of a run. summary is copied. See
// SendRunSnapshot for buffering.
func (c *Client) SendRunFinished(runID string, summary []byte, errText string) error {
	f, err := encodeNode(&RunFinished{RunID: runID, Summary: summary, Error: errText}, c.opts.MaxMessageSize)
	if err != nil {
		return err
	}
	c.out.pushReport(report{f: f, finished: true})
	return nil
}

// NodeID returns the ID from the last Welcome, or "".
func (c *Client) NodeID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nodeID
}

// Connected reports whether a connection is established.
func (c *Client) Connected() bool {
	c.out.mu.Lock()
	defer c.out.mu.Unlock()
	return c.out.connected
}

// ClockOffset returns the controller clock minus the local clock, as
// estimated by the last handshake.
func (c *Client) ClockOffset() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.offset
}

// ControllerToLocal converts a controller timestamp, such as
// StartRun.StartTime, to the local clock.
func (c *Client) ControllerToLocal(t time.Time) time.Time {
	return t.Add(-c.ClockOffset())
}

// Stats returns the client's counters.
func (c *Client) Stats() ClientStats {
	o := &c.out
	o.mu.Lock()
	st := ClientStats{
		Connected:        o.connected,
		BufferedReports:  len(o.reports),
		BufferedBytes:    o.bytes,
		DroppedSnapshots: o.dropped,
	}
	o.mu.Unlock()
	st.NodeID = c.NodeID()
	return st
}

type report struct {
	f        frame
	finished bool
}

// outbox holds the client's unsent messages. Priority: Pong, then SysStats,
// then reports in FIFO order.
type outbox struct {
	notify   chan struct{}
	maxCount int
	maxBytes int

	mu        sync.Mutex
	connected bool
	pong      frame
	sys       frame
	reports   []report
	bytes     int
	dropped   uint64
}

func (o *outbox) init(maxCount, maxBytes int) {
	o.notify = make(chan struct{}, 1)
	o.maxCount = maxCount
	o.maxBytes = maxBytes
}

func (o *outbox) signal() {
	select {
	case o.notify <- struct{}{}:
	default:
	}
}

func (o *outbox) setConnected(v bool) {
	o.mu.Lock()
	o.connected = v
	if !v {
		o.pong, o.sys = nil, nil
	}
	o.mu.Unlock()
}

func (o *outbox) setPong(f frame) {
	o.mu.Lock()
	o.pong = f
	o.mu.Unlock()
	o.signal()
}

func (o *outbox) setSys(f frame) bool {
	o.mu.Lock()
	ok := o.connected
	if ok {
		o.sys = f
	}
	o.mu.Unlock()
	if ok {
		o.signal()
	}
	return ok
}

func (o *outbox) pushReport(r report) {
	o.mu.Lock()
	o.reports = append(o.reports, r)
	o.bytes += len(r.f)
	for len(o.reports) > o.maxCount || o.bytes > o.maxBytes {
		i := slices.IndexFunc(o.reports, func(r report) bool { return !r.finished })
		if i < 0 {
			break
		}
		o.bytes -= len(o.reports[i].f)
		o.reports = slices.Delete(o.reports, i, i+1)
		o.dropped++
	}
	o.mu.Unlock()
	o.signal()
}

// requeue puts a report whose write failed back at the head.
func (o *outbox) requeue(r report) {
	o.mu.Lock()
	o.reports = slices.Insert(o.reports, 0, r)
	o.bytes += len(r.f)
	o.mu.Unlock()
}

// next removes and returns the next message to send. rep is set when the
// message is a report.
func (o *outbox) next() (f frame, rep report, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch {
	case o.pong != nil:
		f, o.pong = o.pong, nil
		return f, report{}, true
	case o.sys != nil:
		f, o.sys = o.sys, nil
		return f, report{}, true
	case len(o.reports) > 0:
		rep = o.reports[0]
		o.reports[0] = report{}
		o.reports = o.reports[1:]
		if len(o.reports) == 0 {
			o.reports = nil
		}
		o.bytes -= len(rep.f)
		return rep.f, rep, true
	}
	return nil, report{}, false
}
