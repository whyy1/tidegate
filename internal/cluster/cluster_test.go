package cluster

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type testServerHandler struct {
	mu       sync.Mutex
	joined   chan *Node
	left     chan leftEvent
	sys      chan *SysStats
	snaps    chan *RunSnapshot
	finished chan *RunFinished
}

type leftEvent struct {
	id     string
	reason error
}

func newTestServerHandler() *testServerHandler {
	return &testServerHandler{
		joined:   make(chan *Node, 8),
		left:     make(chan leftEvent, 8),
		sys:      make(chan *SysStats, 16),
		snaps:    make(chan *RunSnapshot, 64),
		finished: make(chan *RunFinished, 8),
	}
}

func (h *testServerHandler) OnNodeJoined(n *Node)                { h.joined <- n }
func (h *testServerHandler) OnNodeLeft(n *Node, err error)       { h.left <- leftEvent{n.ID(), err} }
func (h *testServerHandler) OnSysStats(_ *Node, s *SysStats)     { h.sys <- s }
func (h *testServerHandler) OnRunSnapshot(_ *Node, s *RunSnapshot) { h.snaps <- s }
func (h *testServerHandler) OnRunFinished(_ *Node, f *RunFinished) { h.finished <- f }

type testClientHandler struct {
	welcomes chan WelcomeInfo
	starts   chan *StartRun
	stops    chan *StopRun
	targets  chan *SetTarget
	discs    chan error
}

func newTestClientHandler() *testClientHandler {
	return &testClientHandler{
		welcomes: make(chan WelcomeInfo, 8),
		starts:   make(chan *StartRun, 8),
		stops:    make(chan *StopRun, 8),
		targets:  make(chan *SetTarget, 8),
		discs:    make(chan error, 8),
	}
}

func (h *testClientHandler) OnWelcome(w WelcomeInfo) { h.welcomes <- w }
func (h *testClientHandler) OnStartRun(r *StartRun)  { h.starts <- r }
func (h *testClientHandler) OnStopRun(r *StopRun)    { h.stops <- r }
func (h *testClientHandler) OnSetTarget(t *SetTarget) { h.targets <- t }
func (h *testClientHandler) OnDisconnected(err error) { h.discs <- err }

func startTestClusterServer(t *testing.T, opts ServerOptions) (*Server, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(opts)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() {
		s.Stop()
		_ = lis.Close()
	})
	return s, lis.Addr().String()
}

func TestClusterHandshakeCommandsAndPing(t *testing.T) {
	sh := newTestServerHandler()
	srv, addr := startTestClusterServer(t, ServerOptions{
		Version:      "0.1.0",
		Token:        "good-token",
		Handler:      sh,
		PingInterval: 30 * time.Millisecond,
		PingTimeout:  2 * time.Second,
	})

	ch := newTestClientHandler()
	cl := NewClient(ClientOptions{
		Addr:    addr,
		Token:   "good-token",
		Name:    "agent-1",
		Version: "0.1.0",
		Hello: func() Hello {
			return Hello{
				OS:          "windows",
				Arch:        "amd64",
				CPUCores:    8,
				MemoryBytes: 16 << 30,
				Interfaces:  []NetInterface{{Name: "eth0", LinkSpeedBps: 1_000_000_000}},
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cl.Run(ctx, ch) }()

	var node *Node
	select {
	case node = <-sh.joined:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OnNodeJoined")
	}
	var w WelcomeInfo
	select {
	case w = <-ch.welcomes:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OnWelcome")
	}
	if w.NodeID != node.ID() || w.Resumed || w.ServerVersion != "0.1.0" {
		t.Fatalf("unexpected WelcomeInfo: %+v", w)
	}
	if !cl.Connected() || cl.NodeID() != node.ID() {
		t.Fatalf("client connected=%v id=%s", cl.Connected(), cl.NodeID())
	}

	nodes := srv.Nodes()
	if len(nodes) != 1 || nodes[0].Name != "agent-1" || nodes[0].CPUCores != 8 {
		t.Fatalf("Nodes() = %+v", nodes)
	}

	// Send StartRun, SetTarget, StopRun from controller to node.
	if err := node.StartRun(&StartRun{
		RunID:      "r1",
		Scenario:   []byte(`{"name":"demo"}`),
		DataFiles:  []DataFile{{Name: "u.csv", Content: []byte("a\n1\n")}},
		LoadShare:  0.5,
		SliceIndex: 0,
		SliceCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := node.SetTarget("r1", 200); err != nil {
		t.Fatal(err)
	}
	if err := node.StopRun("r1", StopGraceful); err != nil {
		t.Fatal(err)
	}

	select {
	case sr := <-ch.starts:
		if sr.RunID != "r1" || sr.LoadShare != 0.5 || len(sr.DataFiles) != 1 {
			t.Fatalf("StartRun = %+v", sr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for StartRun")
	}
	select {
	case st := <-ch.targets:
		if st.RunID != "r1" || st.Value != 200 {
			t.Fatalf("SetTarget = %+v", st)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SetTarget")
	}
	select {
	case sp := <-ch.stops:
		if sp.RunID != "r1" || sp.Mode != StopGraceful {
			t.Fatalf("StopRun = %+v", sp)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for StopRun")
	}

	// Send SysStats, RunSnapshot, RunFinished from node to controller.
	if err := cl.SendSysStats(time.Now(), []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := cl.SendRunSnapshot("r1", 0, []byte("snap0")); err != nil {
		t.Fatal(err)
	}
	if err := cl.SendRunFinished("r1", []byte("sum"), ""); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-sh.sys:
		if string(s.JSON) != `{"ok":true}` {
			t.Fatalf("SysStats = %s", s.JSON)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SysStats")
	}
	select {
	case rs := <-sh.snaps:
		if rs.RunID != "r1" || rs.Seq != 0 || string(rs.Snapshot) != "snap0" {
			t.Fatalf("RunSnapshot = %+v", rs)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for RunSnapshot")
	}
	select {
	case rf := <-sh.finished:
		if rf.RunID != "r1" || string(rf.Summary) != "sum" || rf.Error != "" {
			t.Fatalf("RunFinished = %+v", rf)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for RunFinished")
	}

	// Wait briefly for at least one Ping/Pong cycle so RTT updates.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if info := node.Info(); info.RTT > 0 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if info := node.Info(); info.RTT <= 0 {
		t.Fatalf("expected Ping/Pong RTT > 0, got %v", info.RTT)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Client.Run returned %v, want context.Canceled", err)
	}
	select {
	case <-sh.left:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for OnNodeLeft")
	}
}

func TestClusterRejectionStopsClientImmediately(t *testing.T) {
	_, addr := startTestClusterServer(t, ServerOptions{
		Version: "0.2.0",
		Token:   "right-token",
	})

	cases := []struct {
		name    string
		token   string
		version string
		wantErr error
	}{
		{"bad_token", "wrong", "0.2.0", ErrBadToken},
		{"version_mismatch", "right-token", "0.1.0", ErrVersionMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := NewClient(ClientOptions{
				Addr:       addr,
				Token:      tc.token,
				Version:    tc.version,
				MinBackoff: 10 * time.Millisecond,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := cl.Run(ctx, newTestClientHandler())
			if !errors.Is(err, ErrRejected) || !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestClusterReconnectKeepsIDAndFlushesBufferedReports(t *testing.T) {
	sh := newTestServerHandler()
	srv, addr := startTestClusterServer(t, ServerOptions{
		Version:      "0.1.0",
		Token:        "tok",
		Handler:      sh,
		PingInterval: 200 * time.Millisecond,
		PingTimeout:  5 * time.Second,
	})

	ch := newTestClientHandler()
	cl := NewClient(ClientOptions{
		Addr:               addr,
		Token:              "tok",
		Version:            "0.1.0",
		MinBackoff:         20 * time.Millisecond,
		MaxBackoff:         50 * time.Millisecond,
		MaxBufferedReports: 2,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = cl.Run(ctx, ch) }()

	node1 := <-sh.joined
	w1 := <-ch.welcomes

	// Force disconnect from server side.
	node1.Close()
	le := <-sh.left
	if !errors.Is(le.reason, ErrNodeClosed) {
		t.Fatalf("left reason = %v, want ErrNodeClosed", le.reason)
	}
	<-ch.discs

	// Wait until disconnected, then queue 3 snapshots + 1 RunFinished while offline:
	// MaxBufferedReports=2 should drop 2 oldest snapshots but keep the newest and RunFinished!
	for i := 0; i < 3; i++ {
		_ = cl.SendRunSnapshot("r-buf", int64(i), []byte(fmt.Sprintf("s%d", i)))
	}
	_ = cl.SendRunFinished("r-buf", []byte("final"), "")

	node2 := <-sh.joined
	w2 := <-ch.welcomes
	if !w2.Resumed || w2.NodeID != w1.NodeID || node2.ID() != node1.ID() {
		t.Fatalf("expected resumed NodeID %s, got w2=%+v node2=%s", w1.NodeID, w2, node2.ID())
	}

	var gotSnaps []int64
	timeout := time.After(3 * time.Second)
	for {
		select {
		case rs := <-sh.snaps:
			gotSnaps = append(gotSnaps, rs.Seq)
		case rf := <-sh.finished:
			if rf.RunID != "r-buf" || string(rf.Summary) != "final" {
				t.Fatalf("RunFinished = %+v", rf)
			}
			if st := cl.Stats(); st.DroppedSnapshots < 1 {
				t.Fatalf("expected DroppedSnapshots >= 1, got %+v", st)
			}
			if len(gotSnaps) == 0 || gotSnaps[len(gotSnaps)-1] != 2 {
				t.Fatalf("expected newest snapshot seq 2 preserved, got %v", gotSnaps)
			}
			_ = srv
			return
		case <-timeout:
			t.Fatalf("timed out waiting for buffered RunFinished (snaps so far: %v)", gotSnaps)
		}
	}
}

func TestClusterSendQueueFullAndNonBlocking(t *testing.T) {
	sh := newTestServerHandler()
	_, addr := startTestClusterServer(t, ServerOptions{
		Version:      "0.1.0",
		Token:        "tok",
		Handler:      sh,
		SendQueue:    2,
		PingInterval: 200 * time.Millisecond,
		PingTimeout:  5 * time.Second,
	})

	ch := newTestClientHandler()
	block := make(chan struct{})
	var blockedOnce sync.Once
	blockingHandler := &wrapHandler{
		ClientHandler: ch,
		onStart: func(r *StartRun) {
			blockedOnce.Do(func() { <-block })
		},
	}
	cl := NewClient(ClientOptions{
		Addr:        addr,
		Token:       "tok",
		Version:     "0.1.0",
		DialOptions: []grpc.DialOption{grpc.WithInitialWindowSize(65535), grpc.WithInitialConnWindowSize(65535)},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = cl.Run(ctx, blockingHandler) }()

	node := <-sh.joined
	<-ch.welcomes

	// First StartRun blocks the client dispatch goroutine, backing up HTTP/2
	// flow control once dispatchQueue (64) and window fill up, triggering ErrSendQueueFull.
	payload := []byte(strings.Repeat("x", 16*1024))
	sawFull := false
	for i := 0; i < 200; i++ {
		err := node.StartRun(&StartRun{RunID: fmt.Sprintf("r-%d", i), Scenario: payload})
		if errors.Is(err, ErrSendQueueFull) {
			sawFull = true
			break
		}
		if err != nil {
			t.Fatalf("unexpected send error: %v", err)
		}
	}
	close(block)
	if !sawFull {
		t.Log("stream drained fast enough before SendQueue overflow")
	}
	cancel()
	<-sh.left
}

func TestClusterPingTimeoutDropsSilentNode(t *testing.T) {
	sh := newTestServerHandler()
	_, addr := startTestClusterServer(t, ServerOptions{
		Version:      "0.1.0",
		Token:        "tok",
		Handler:      sh,
		PingInterval: 15 * time.Millisecond,
		PingTimeout:  60 * time.Millisecond,
	})

	conn, err := grpc.NewClient("passthrough:///"+addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(codec{})),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := conn.NewStream(ctx, &connectStreamDesc, connectMethod)
	if err != nil {
		t.Fatal(err)
	}
	f, err := encodeNode(&Hello{Token: "tok", Version: "0.1.0", Name: "silent"}, DefaultMaxMessageSize)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.SendMsg(f); err != nil {
		t.Fatal(err)
	}
	var welcome ControllerMessage
	if err := stream.RecvMsg(&welcome); err != nil {
		t.Fatal(err)
	}
	<-sh.joined

	select {
	case le := <-sh.left:
		if !errors.Is(le.reason, ErrNodeTimeout) {
			t.Fatalf("left reason = %v, want ErrNodeTimeout", le.reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ErrNodeTimeout")
	}
}

type wrapHandler struct {
	ClientHandler
	onStart func(*StartRun)
}

func (w *wrapHandler) OnStartRun(r *StartRun) {
	if w.onStart != nil {
		w.onStart(r)
	}
	w.ClientHandler.OnStartRun(r)
}
