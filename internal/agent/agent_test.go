package agent

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"

	"tidegate/internal/cluster"
	"tidegate/internal/metrics"
	"tidegate/internal/sysmon"
	"tidegate/internal/testserver"
)

type recordingHandler struct {
	mu       sync.Mutex
	joined   chan *cluster.Node
	sysCh    chan *cluster.SysStats
	snapCh   chan *cluster.RunSnapshot
	finishCh chan *cluster.RunFinished
	leftCh   chan struct{}
}

func newRecordingHandler() *recordingHandler {
	return &recordingHandler{
		joined:   make(chan *cluster.Node, 4),
		sysCh:    make(chan *cluster.SysStats, 16),
		snapCh:   make(chan *cluster.RunSnapshot, 64),
		finishCh: make(chan *cluster.RunFinished, 8),
		leftCh:   make(chan struct{}, 4),
	}
}

func (h *recordingHandler) OnNodeJoined(n *cluster.Node) {
	select {
	case h.joined <- n:
	default:
	}
}

func (h *recordingHandler) OnNodeLeft(*cluster.Node, error) {
	select {
	case h.leftCh <- struct{}{}:
	default:
	}
}

func (h *recordingHandler) OnSysStats(_ *cluster.Node, s *cluster.SysStats) {
	cp := *s
	cp.JSON = append([]byte(nil), s.JSON...)
	select {
	case h.sysCh <- &cp:
	default:
	}
}

func (h *recordingHandler) OnRunSnapshot(_ *cluster.Node, s *cluster.RunSnapshot) {
	cp := *s
	cp.Snapshot = append([]byte(nil), s.Snapshot...)
	h.snapCh <- &cp
}

func (h *recordingHandler) OnRunFinished(_ *cluster.Node, f *cluster.RunFinished) {
	cp := *f
	cp.Summary = append([]byte(nil), f.Summary...)
	h.finishCh <- &cp
}

func TestAgentLifecycleAndRuns(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("expected error for empty options")
	}

	tsrv, err := testserver.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tsrv.Start()
	defer tsrv.Close()

	h := newRecordingHandler()
	csrv := cluster.NewServer(cluster.ServerOptions{
		Version: "1.0-test",
		Token:   "secret-node-token",
		Handler: h,
	})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	go func() { _ = csrv.Serve(lis) }()
	defer csrv.Stop()

	workDir := t.TempDir()
	ag, err := New(Options{
		JoinAddr:          lis.Addr().String(),
		Token:             "secret-node-token",
		Name:              "local-test",
		Version:           "1.0-test",
		WorkDir:           workDir,
		SysInterval:       50 * time.Millisecond,
		InsecurePlaintext: true,
		ClusterOptions: cluster.ClientOptions{
			MinBackoff: 50 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	agDone := make(chan error, 1)
	go func() { agDone <- ag.Run(ctx) }()

	var node *cluster.Node
	select {
	case node = <-h.joined:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for agent to join")
	}

	if info := node.Info(); info.Name != "local-test" || info.CPUCores == 0 {
		t.Fatalf("unexpected node info: %+v", info)
	}

	// Verify SysStats arrives and decodes to sysmon.Stats.
	select {
	case ss := <-h.sysCh:
		var st sysmon.Stats
		if err := json.Unmarshal(ss.JSON, &st); err != nil {
			t.Fatalf("invalid sysmon JSON: %v", err)
		}
		if st.Memory.TotalBytes == 0 {
			t.Fatalf("expected non-zero memory total in %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SysStats")
	}

	// 1. Run with DataFile + SetTarget + normal completion.
	scenJSON, _ := json.Marshal(map[string]any{
		"version": 1,
		"name":    "agent-csv-test",
		"data": []map[string]any{
			{"name": "users", "file": "users.csv", "mode": "sequential"},
		},
		"steps": []map[string]any{
			{
				"id": "ping",
				"request": map[string]any{
					"method": "GET",
					"url":    tsrv.URL() + "/ping?u={{users.name}}",
				},
			},
		},
		"load": map[string]any{
			"mode": "vus",
			"stages": []map[string]any{
				{"duration": "1200ms", "target": 2},
			},
		},
	})

	if err := node.StartRun(&cluster.StartRun{
		RunID:      "run-1",
		Scenario:   scenJSON,
		DataFiles:  []cluster.DataFile{{Name: "users.csv", Content: []byte("name\nalice\nbob\n")}},
		LoadShare:  1,
		SliceIndex: 0,
		SliceCount: 1,
		StartTime:  time.Now().Add(50 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(250 * time.Millisecond)
	_ = node.SetTarget("run-1", 4)

	var snaps []*metrics.Snapshot
	var fin *cluster.RunFinished
loop1:
	for {
		select {
		case rs := <-h.snapCh:
			if rs.RunID != "run-1" {
				continue
			}
			var s metrics.Snapshot
			if err := s.UnmarshalBinary(rs.Snapshot); err != nil {
				t.Fatal(err)
			}
			snaps = append(snaps, &s)
		case fin = <-h.finishCh:
			if fin.RunID == "run-1" {
				break loop1
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for run-1 to finish")
		}
	}
	if fin.Error != "" {
		t.Fatalf("run-1 finished with error: %s", fin.Error)
	}
	var sum metrics.Snapshot
	if err := sum.UnmarshalBinary(fin.Summary); err != nil {
		t.Fatal(err)
	}
	if len(snaps) == 0 || sum.Total.Requests == 0 || sum.Total.ErrorCount() != 0 {
		t.Fatalf("unexpected run-1 results: snaps=%d reqs=%d errs=%d", len(snaps), sum.Total.Requests, sum.Total.ErrorCount())
	}

	// Verify temp dir removed.
	entries, err := os.ReadDir(workDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("expected clean workDir %s, got entries=%v err=%v", workDir, entries, err)
	}

	// 2. Path traversal in DataFiles must fail cleanly.
	if err := node.StartRun(&cluster.StartRun{
		RunID:      "run-bad-file",
		Scenario:   scenJSON,
		DataFiles:  []cluster.DataFile{{Name: "../evil.csv", Content: []byte("x\n1\n")}},
		LoadShare:  1,
		SliceIndex: 0,
		SliceCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case fin = <-h.finishCh:
		if fin.RunID != "run-bad-file" || fin.Error == "" {
			t.Fatalf("expected error for invalid data filename, got %+v", fin)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run-bad-file failure")
	}

	// 3. Abort a long run immediately via StopAbort.
	longScen, _ := json.Marshal(map[string]any{
		"version": 1,
		"name":    "abort-test",
		"steps": []map[string]any{
			{
				"id":      "slow",
				"request": map[string]any{"method": "GET", "url": tsrv.URL() + "/delay?ms=5000"},
			},
		},
		"load": map[string]any{
			"mode":          "vus",
			"graceful_stop": "5s",
			"stages":        []map[string]any{{"duration": "30s", "target": 2}},
		},
	})
	if err := node.StartRun(&cluster.StartRun{
		RunID:      "run-abort",
		Scenario:   longScen,
		LoadShare:  1,
		SliceIndex: 0,
		SliceCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	abortStart := time.Now()
	if err := node.StopRun("run-abort", cluster.StopAbort); err != nil {
		t.Fatal(err)
	}
	select {
	case fin = <-h.finishCh:
		if fin.RunID != "run-abort" {
			t.Fatalf("unexpected finish: %+v", fin)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for StopAbort")
	}
	if time.Since(abortStart) >= 2*time.Second {
		t.Fatalf("StopAbort took too long: %v", time.Since(abortStart))
	}

	// Shutdown agent cleanly and confirm no leftover files.
	cancel()
	<-agDone
	if _, err := os.Stat(filepath.Join(workDir, "..")); err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(workDir)
	if len(entries) != 0 {
		t.Fatalf("workDir not clean on exit: %v", entries)
	}
}

func TestAgentMTLSEnrollmentLabelsAndPersistedCertRestart(t *testing.T) {
	ca, err := cluster.NewEphemeralClusterCA()
	if err != nil {
		t.Fatal(err)
	}
	srvTLS, err := ca.IssueServerTLSConfig([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}

	h := newRecordingHandler()
	tokenUsed := false
	csrv := cluster.NewServer(cluster.ServerOptions{
		Version:     "1.0-mtls",
		Credentials: credentials.NewTLS(srvTLS),
		Handler:     h,
		Enroll: func(_ context.Context, req *cluster.EnrollRequest) (*cluster.EnrollResponse, error) {
			if tokenUsed || req.Token != "one-time-join" {
				return &cluster.EnrollResponse{RejectMessage: "加入令牌已失效"}, nil
			}
			tokenUsed = true
			certPEM, _, _, err := ca.SignCSR(req.CSRDER, req.Name, time.Hour)
			if err != nil {
				return nil, err
			}
			return &cluster.EnrollResponse{
				ClientCertPEM: certPEM,
				CACertPEM:     ca.CertPEM(),
			}, nil
		},
		VerifyClientCert: func(cert *x509.Certificate, _ string) error {
			if cert == nil {
				return errors.New("missing cert")
			}
			return nil
		},
	})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	go func() { _ = csrv.Serve(lis) }()
	defer csrv.Stop()

	certDir := t.TempDir()
	ag1, err := New(Options{
		JoinAddr:      lis.Addr().String(),
		Token:         "one-time-join",
		Name:          "edge-ap",
		Version:       "1.0-mtls",
		CAFingerprint: ca.Fingerprint(),
		CertDir:       certDir,
		Labels:        map[string]string{"zone": "ap-east", "tier": "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	go func() { done1 <- ag1.Run(ctx1) }()

	node1 := <-h.joined
	if info := node1.Info(); info.Labels["zone"] != "ap-east" || info.CertSerial == "" {
		t.Fatalf("unexpected agent mTLS info: %+v", info)
	}
	cancel1()
	<-done1
	<-h.leftCh

	// Restart agent with NO token using persisted certificates in certDir.
	ag2, err := New(Options{
		JoinAddr:      lis.Addr().String(),
		Name:          "edge-ap",
		Version:       "1.0-mtls",
		CAFingerprint: ca.Fingerprint(),
		CertDir:       certDir,
		Labels:        map[string]string{"zone": "ap-east", "tier": "high"},
	})
	if err != nil {
		t.Fatalf("New without token failed: %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan error, 1)
	go func() { done2 <- ag2.Run(ctx2) }()

	node2 := <-h.joined
	if node2.Info().CertSerial != node1.Info().CertSerial {
		t.Fatalf("expected reused persisted cert serial %s, got %s", node1.Info().CertSerial, node2.Info().CertSerial)
	}
	cancel2()
	<-done2
}
