package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tidegate/internal/agent"
	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
	"tidegate/internal/sysmon"
	"tidegate/internal/testserver"
)

func newTestController(t *testing.T, optsFunc func(*Options)) (*Controller, *store.Store, *store.ResultDB) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenDB(filepath.Join(dir, "tidegate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rdb := store.NewResultDB(db)

	opts := Options{
		Store:               st,
		Results:             rdb,
		Version:             "1.0-test",
		ClusterListen:       "127.0.0.1:0",
		ClusterToken:        "external-cluster-token",
		InProcessLocalAgent: true,
		StartDelay:          30 * time.Millisecond,
	}
	if optsFunc != nil {
		optsFunc(&opts)
	}

	c, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, st, rdb
}

func waitForAgents(t *testing.T, c *Controller, want int) []AgentView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list, err := c.Agents(context.Background())
		if err == nil && len(list) >= want {
			return list
		}
		time.Sleep(20 * time.Millisecond)
	}
	list, _ := c.Agents(context.Background())
	t.Fatalf("timed out waiting for %d agents, got %d", want, len(list))
	return nil
}

func TestControllerDefaultProjectDebugAndDryRun(t *testing.T) {
	tsrv, err := testserver.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tsrv.Start()
	defer tsrv.Close()

	c, st, rdb := newTestController(t, nil)
	ctx := context.Background()

	// Empty project triggers default project creation once.
	p1, err := c.EnsureDefaultProject()
	if err != nil {
		t.Fatal(err)
	}
	if p1.Name != "默认项目" {
		t.Fatalf("default project name = %q", p1.Name)
	}
	p2, err := c.EnsureDefaultProject()
	if err != nil || p2.ID != p1.ID {
		t.Fatalf("expected same default project, got p1=%s p2=%s err=%v", p1.ID, p2.ID, err)
	}

	env, err := st.CreateEnvironment(p1.ID, &store.Environment{
		Name:    "local",
		BaseURL: tsrv.URL(),
		Variables: store.KeyValueList{
			{Name: "u", Value: "alice"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Debug an unsaved draft API (scratchpad flow) and check history record.
	dbgResp, err := c.DebugAPI(ctx, p1.ID, DebugAPIRequest{
		EnvironmentID: env.ID,
		API: &store.API{
			Name: "draft-ping",
			Request: store.APIRequest{
				Method: "GET",
				URL:    "/ping?user={{u}}",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dbgResp.Result.Passed || dbgResp.Result.Status != 200 || dbgResp.RecordID == "" {
		t.Fatalf("unexpected debug response: %+v", dbgResp)
	}
	hist, err := rdb.ListDebugHistory(ctx, p1.ID, 10, true)
	if err != nil || len(hist) != 1 || hist[0].ID != dbgResp.RecordID {
		t.Fatalf("unexpected debug history: %+v err=%v", hist, err)
	}

	// Save two APIs and run a multi-step scenario dry-run.
	loginAPI, err := st.CreateAPI(p1.ID, &store.API{
		Name: "login",
		Request: store.APIRequest{
			Method: "POST",
			URL:    "/login",
			Body: &store.APIBody{
				Type:    "json",
				Content: `{"user":"{{u}}","pwd":"123"}`,
			},
		},
		Extract: []scenario.Extract{
			{Name: "token", From: "jsonpath", Expr: "$.data.token"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	itemsAPI, err := st.CreateAPI(p1.ID, &store.API{
		Name: "items",
		Request: store.APIRequest{
			Method: "GET",
			URL:    "/items",
		},
		Auth: &store.Auth{Type: store.AuthBearer, Token: "{{token}}"},
	})
	if err != nil {
		t.Fatal(err)
	}

	dryRes, err := c.DebugScenario(ctx, p1.ID, DebugScenarioRequest{
		EnvironmentID: env.ID,
		Scenario: &store.Scenario{
			Name: "login-then-items",
			Steps: []store.ScenarioStep{
				{ID: "login", APIID: loginAPI.ID, Think: &scenario.Think{Type: "fixed", Min: scenario.Duration(2 * time.Second), Max: scenario.Duration(2 * time.Second)}},
				{ID: "items", APIID: itemsAPI.ID},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dryRes.Passed || len(dryRes.Steps) != 2 || dryRes.FinalVars["token"] == "" {
		t.Fatalf("unexpected dry-run result: %+v", dryRes)
	}
}

func TestControllerClusterRunAggregationAndSettings(t *testing.T) {
	tsrv, err := testserver.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tsrv.Start()
	defer tsrv.Close()

	// Also verify MarkInterrupted cleans stale active runs at startup.
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenDB(filepath.Join(dir, "tidegate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rdb := store.NewResultDB(db)
	if err := rdb.CreateRun(context.Background(), &store.Run{
		ID:           "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		ProjectID:    "proj1",
		ScenarioName: "crashed",
		Scenario:     json.RawMessage(`{"version":1,"name":"crashed"}`),
		Status:       store.RunRunning,
	}); err != nil {
		t.Fatal(err)
	}

	c, err := New(context.Background(), Options{
		Store:               st,
		Results:             rdb,
		Version:             "1.0-test",
		ClusterListen:       "127.0.0.1:0",
		ClusterToken:        "cluster-secret",
		InProcessLocalAgent: true,
		StartDelay:          50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	interrupted, err := rdb.GetRun(context.Background(), "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err != nil || interrupted.Status != store.RunFailed {
		t.Fatalf("expected stale run marked failed at startup, got %+v err=%v", interrupted, err)
	}

	// Start a second remote agent using ClusterToken.
	ag2, err := agent.New(agent.Options{
		JoinAddr:    c.ClusterAddr(),
		Token:       "cluster-secret",
		Name:        "worker-2",
		Version:     "1.0-test",
		SysInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	agCtx, agCancel := context.WithCancel(context.Background())
	defer agCancel()
	go func() { _ = ag2.Run(agCtx) }()

	agents := waitForAgents(t, c, 2)
	if _, err := c.UpdateAgentSettings(context.Background(), agents[0].ID, 125_000_000); err != nil {
		t.Fatal(err)
	}

	sub := c.Subscribe()
	defer sub.Close()
	sub.SubscribeRun("*")

	proj, err := c.EnsureDefaultProject()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFile(proj.ID, "ids.csv", strings.NewReader("id\n1\n2\n3\n4\n"), ""); err != nil {
		t.Fatal(err)
	}
	pingAPI, err := st.CreateAPI(proj.ID, &store.API{
		Name: "ping",
		Request: store.APIRequest{
			Method: "GET",
			URL:    tsrv.URL() + "/ping?id={{data.id}}",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	scen, err := st.CreateScenario(proj.ID, &store.Scenario{
		Name: "cluster-run",
		Data: []scenario.DataSource{{Name: "data", File: "ids.csv", Mode: "sequential"}},
		Steps: []store.ScenarioStep{
			{ID: "p", APIID: pingAPI.ID},
		},
		Load: scenario.Load{
			Mode: "vus",
			Stages: []scenario.Stage{
				{Duration: scenario.Duration(1400 * time.Millisecond), Target: 4},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	run, err := c.StartRun(context.Background(), proj.ID, StartRunRequest{ScenarioID: scen.ID})
	if err != nil {
		t.Fatal(err)
	}

	// Second concurrent run must be rejected with ErrRunActive.
	if _, err := c.StartRun(context.Background(), proj.ID, StartRunRequest{ScenarioID: scen.ID}); !errors.Is(err, ErrRunActive) {
		t.Fatalf("expected ErrRunActive, got %v", err)
	}

	// Adjust load dynamically mid-run.
	time.Sleep(300 * time.Millisecond)
	if err := c.SetRunTarget(context.Background(), run.ID, 6); err != nil {
		t.Fatalf("SetRunTarget: %v", err)
	}

	var gotFinished bool
	snapCount := 0
	deadline := time.After(10 * time.Second)
	for !gotFinished {
		select {
		case ev := <-sub.C():
			if ev.RunID == run.ID && ev.Type == EventSnapshot {
				snapCount++
			}
			if ev.RunID == run.ID && ev.Type == EventRunStatus && ev.Status == store.RunFinished {
				gotFinished = true
			}
			if ev.RunID == run.ID && ev.Type == EventRunStatus && ev.Status == store.RunFailed {
				t.Fatalf("run failed: %s", ev.Error)
			}
		case <-deadline:
			t.Fatal("timed out waiting for cluster run finish")
		}
	}
	if snapCount == 0 {
		t.Fatal("expected at least 1 merged snapshot event")
	}

	savedRun, err := rdb.GetRun(context.Background(), run.ID)
	if err != nil || savedRun.Status != store.RunFinished || len(savedRun.Summary) == 0 {
		t.Fatalf("unexpected persisted run: %+v err=%v", savedRun, err)
	}
	snaps, err := rdb.Snapshots(context.Background(), run.ID, 0, 0)
	if err != nil || len(snaps) == 0 {
		t.Fatalf("expected persisted snapshots, got len=%d err=%v", len(snaps), err)
	}
	merged, err := rdb.MergedSnapshot(context.Background(), run.ID)
	if err != nil || merged.Total.Requests == 0 || merged.Total.ErrorCount() != 0 {
		t.Fatalf("unexpected merged histogram snapshot: %+v err=%v", merged, err)
	}
}

func TestAlertEvaluatorOpenAndClose(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.OpenDB(filepath.Join(dir, "tidegate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rdb := store.NewResultDB(db)
	if err := rdb.CreateRun(ctx, &store.Run{
		ID:           "run-alert-test",
		ProjectID:    "p1",
		ScenarioName: "alerts",
		Scenario:     json.RawMessage(`{"version":1,"name":"alerts"}`),
		Status:       store.RunRunning,
	}); err != nil {
		t.Fatal(err)
	}

	hub := newEventHub()
	defer hub.closeAll()
	sub := hub.subscribe()
	sub.SubscribeRun("run-alert-test")

	ev := newAlertEvaluator("run-alert-test", rdb, hub, targetEgress{hasNonLoopback: true, egressName: "eth0"})

	t0 := time.Now().UTC()
	// Trigger CPU + memory + ports + bandwidth alerts.
	ev.evaluateSys(ctx, "local", &sysmon.Stats{
		Time: t0,
		CPU:  sysmon.CPUStats{Machine: 0.92},
		Memory: sysmon.MemoryStats{
			UsedBytes:  900,
			TotalBytes: 1000,
		},
		Ports: sysmon.PortStats{
			EphemeralCount: 1000,
			TCPConns:       950,
		},
		NICs: []sysmon.NICStats{
			{Name: "eth0", Up: true, LinkSpeedBitsPerSec: 1_000_000_000, TxBytesPerSec: 110_000_000},
		},
	}, 0)

	// Snapshot alerts: ErrLocal + DroppedIterations + high SchedDelay.
	snap := metrics.NewSnapshot(nil, true)
	snap.Time = t0
	snap.Total.Errors[metrics.ErrLocal] = 3
	snap.DroppedIterations = 5
	snap.SchedDelay.Record(200 * time.Millisecond)
	ev.evaluateSnapshot(ctx, snap)

	alerts, err := rdb.Alerts(ctx, "run-alert-test")
	if err != nil || len(alerts) != 7 {
		t.Fatalf("expected 7 opened alerts, got len=%d (%+v) err=%v", len(alerts), alerts, err)
	}
	for _, a := range alerts {
		if !a.EndedAt.IsZero() {
			t.Fatalf("expected open alert, got %+v", a)
		}
	}

	// Send 2 healthy samples -> all alerts should close.
	healthy := &sysmon.Stats{
		Time:   t0.Add(time.Second),
		CPU:    sysmon.CPUStats{Machine: 0.2},
		Memory: sysmon.MemoryStats{UsedBytes: 200, TotalBytes: 1000},
		Ports:  sysmon.PortStats{EphemeralCount: 1000, TCPConns: 50},
		NICs:   []sysmon.NICStats{{Name: "eth0", Up: true, LinkSpeedBitsPerSec: 1_000_000_000, TxBytesPerSec: 1_000}},
	}
	healthySnap := metrics.NewSnapshot(nil, true)
	healthySnap.Time = t0.Add(time.Second)

	ev.evaluateSys(ctx, "local", healthy, 0)
	ev.evaluateSnapshot(ctx, healthySnap)
	stillOpen, _ := rdb.Alerts(ctx, "run-alert-test")
	if !stillOpen[0].EndedAt.IsZero() {
		t.Fatal("alert closed after only 1 healthy interval; want hysteresis of 2")
	}

	healthy.Time = t0.Add(2 * time.Second)
	healthySnap.Time = t0.Add(2 * time.Second)
	ev.evaluateSys(ctx, "local", healthy, 0)
	ev.evaluateSnapshot(ctx, healthySnap)

	closedAlerts, _ := rdb.Alerts(ctx, "run-alert-test")
	for _, a := range closedAlerts {
		if a.EndedAt.IsZero() {
			t.Fatalf("expected all alerts closed after 2 healthy intervals, still open: %+v", a)
		}
	}
}

func TestStopRunAbort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte("slow"))
	}))
	defer srv.Close()

	c, _, _ := newTestController(t, nil)
	waitForAgents(t, c, 1)

	proj, err := c.EnsureDefaultProject()
	if err != nil {
		t.Fatal(err)
	}
	run, err := c.StartRun(context.Background(), proj.ID, StartRunRequest{
		API: &store.API{
			Name:    "slow",
			Request: store.APIRequest{Method: "GET", URL: srv.URL + "/slow"},
		},
		Load: &scenario.Load{
			Mode:   "vus",
			Stages: []scenario.Stage{{Duration: scenario.Duration(20 * time.Second), Target: 1}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	sub := c.Subscribe()
	defer sub.Close()
	sub.SubscribeRun(run.ID)

	time.Sleep(150 * time.Millisecond)
	stopped, err := c.StopRun(context.Background(), run.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != store.RunStopping && stopped.Status.Active() {
		t.Logf("stop status: %s", stopped.Status)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-sub.C():
			if ev.Type == EventRunStatus && !ev.Status.Active() {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for aborted run to end")
		}
	}
}

func TestControllerMixAndEstimate(t *testing.T) {
	var buyHits, sellHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/buy":
			buyHits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":"buy"}`))
		case "/sell":
			sellHits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":"sell"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, st, rdb := newTestController(t, nil)
	waitForAgents(t, c, 1)

	proj, err := c.EnsureDefaultProject()
	if err != nil {
		t.Fatal(err)
	}
	// Two files using the SAME dataset alias "users" across two mixed scenarios.
	if _, err := st.SaveFile(proj.ID, "buyers.csv", strings.NewReader("uid\nb1\nb2\n"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFile(proj.ID, "sellers.json", strings.NewReader(`[{"uid":"s1"},{"uid":"s2"}]`), ""); err != nil {
		t.Fatal(err)
	}

	buyAPI, err := st.CreateAPI(proj.ID, &store.API{
		Name: "buy",
		Request: store.APIRequest{
			Method: "GET",
			URL:    srv.URL + "/buy?u={{users.uid}}",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sellAPI, err := st.CreateAPI(proj.ID, &store.API{
		Name: "sell",
		Request: store.APIRequest{
			Method: "GET",
			URL:    srv.URL + "/sell?u={{users.uid}}",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	scenBuy, err := st.CreateScenario(proj.ID, &store.Scenario{
		Name: "buy-flow",
		Data: []scenario.DataSource{{Name: "users", File: "buyers.csv", Mode: "sequential"}},
		// Colliding step ID "step1" across both scenarios tests mix ID rewriting.
		Steps: []store.ScenarioStep{{ID: "step1", APIID: buyAPI.ID}},
		Load: scenario.Load{
			Mode:   "vus",
			Stages: []scenario.Stage{{Duration: scenario.Duration(400 * time.Millisecond), Target: 2}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	scenSell, err := st.CreateScenario(proj.ID, &store.Scenario{
		Name:  "sell-flow",
		Data:  []scenario.DataSource{{Name: "users", File: "sellers.json", Mode: "sequential"}},
		Steps: []store.ScenarioStep{{ID: "step1", APIID: sellAPI.ID}},
		Load: scenario.Load{
			Mode:   "vus",
			Stages: []scenario.Stage{{Duration: scenario.Duration(400 * time.Millisecond), Target: 2}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	mixReq := StartRunRequest{
		Mix: []ScenarioMixItem{
			{ScenarioID: scenBuy.ID, Weight: 70},
			{ScenarioID: scenSell.ID, Weight: 30},
		},
		Load: &scenario.Load{
			Mode:   "vus",
			Stages: []scenario.Stage{{Duration: scenario.Duration(500 * time.Millisecond), Target: 4}},
		},
	}

	est, err := c.EstimateRun(context.Background(), proj.ID, mixReq)
	if err != nil {
		t.Fatalf("EstimateRun(mix): %v", err)
	}
	if est.StepCount < 2 || est.EstimatedPeakRPS <= 0 || est.OnlineAgents < 1 {
		t.Fatalf("unexpected mix EstimateRun: %+v", est)
	}

	sub := c.Subscribe()
	defer sub.Close()
	sub.SubscribeRun("*")

	run, err := c.StartRun(context.Background(), proj.ID, mixReq)
	if err != nil {
		t.Fatalf("StartRun(mix): %v", err)
	}

	deadline := time.After(8 * time.Second)
	for {
		select {
		case ev := <-sub.C():
			if ev.RunID == run.ID && ev.Type == EventRunStatus && !ev.Status.Active() {
				if ev.Status != store.RunFinished {
					t.Fatalf("mix run ended with status %s (%s)", ev.Status, ev.Error)
				}
				if buyHits.Load() == 0 || sellHits.Load() == 0 {
					t.Fatalf("expected both weighted scenarios hit, buy=%d sell=%d", buyHits.Load(), sellHits.Load())
				}
				saved, err := rdb.GetRun(context.Background(), run.ID)
				if err != nil || saved.Status != store.RunFinished {
					t.Fatalf("GetRun(%s) = %+v, err=%v", run.ID, saved, err)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for mix run completion")
		}
	}
}

func TestSecurityPolicyAllowlistAndRoleScaleLimits(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/redir":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, st, _ := newTestController(t, nil)
	waitForAgents(t, c, 1)

	proj, err := c.EnsureDefaultProject()
	if err != nil {
		t.Fatal(err)
	}

	// Reject wildcard "*" rule.
	if _, err := c.SetSecurityPolicy(ctx, SecurityPolicy{
		AllowlistEnabled: true,
		AllowedTargets:   []string{"*"},
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for '*', got %v", err)
	}

	pol, err := c.SetSecurityPolicy(ctx, SecurityPolicy{
		AllowlistEnabled: true,
		AllowedTargets:   []string{"127.0.0.1/32", "allowed.tidegate.test"},
		MemberMaxVUs:     2,
		MemberMaxRPS:     10,
	})
	if err != nil {
		t.Fatalf("SetSecurityPolicy: %v", err)
	}
	loaded, err := c.GetSecurityPolicy(ctx)
	if err != nil || !loaded.AllowlistEnabled || loaded.MemberMaxVUs != 2 || len(loaded.AllowedTargets) != 2 || pol.MemberMaxRPS != 10 {
		t.Fatalf("GetSecurityPolicy mismatch: %+v err=%v", loaded, err)
	}

	// Allowed target works.
	okRes, err := c.DebugAPI(ctx, proj.ID, DebugAPIRequest{
		API: &store.API{
			Name:    "allowed",
			Request: store.APIRequest{Method: "GET", URL: srv.URL + "/ok"},
		},
	})
	if err != nil || !okRes.Result.Passed || okRes.Result.Status != 200 {
		t.Fatalf("expected allowed DebugAPI to pass, got %+v err=%v", okRes, err)
	}

	// Disallowed host rejected pre-flight.
	if _, err := c.DebugAPI(ctx, proj.ID, DebugAPIRequest{
		API: &store.API{
			Name:    "blocked",
			Request: store.APIRequest{Method: "GET", URL: "http://evil.invalid/test"},
		},
	}); !errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("expected ErrTargetNotAllowed for evil.invalid, got %v", err)
	}

	// DNS override pointing an allowed domain at metadata IP is blocked pre-flight.
	if _, err := c.DebugAPI(ctx, proj.ID, DebugAPIRequest{
		API: &store.API{
			Name:    "dns-bypass",
			Request: store.APIRequest{Method: "GET", URL: "https://allowed.tidegate.test/v1"},
		},
		HTTP: &scenario.HTTPOptions{
			DNSOverrides: map[string]string{"allowed.tidegate.test": "169.254.169.254"},
		},
	}); !errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("expected ErrTargetNotAllowed for metadata DNSOverride, got %v", err)
	}

	// Redirect to disallowed metadata IP is blocked at dial time before opening socket.
	redirRes, err := c.DebugAPI(ctx, proj.ID, DebugAPIRequest{
		API: &store.API{
			Name:    "ssrf-redir",
			Request: store.APIRequest{Method: "GET", URL: srv.URL + "/redir"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected outer error on redirect debug: %v", err)
	}
	if redirRes.Result.Passed || !strings.Contains(redirRes.Result.Error, ErrTargetNotAllowed.Error()) {
		t.Fatalf("expected redirect dial blocked with allowlist message, got %+v", redirRes.Result)
	}

	// Role scale enforcement on StartRun and SetRunTargetWithRole.
	scen, err := st.CreateScenario(proj.ID, &store.Scenario{
		Name: "scale-check",
		Load: scenario.Load{
			Mode:   "vus",
			Stages: []scenario.Stage{{Duration: scenario.Duration(800 * time.Millisecond), Target: 5}},
		},
		Steps: []store.ScenarioStep{
			{ID: "s1", APIID: func() string {
				a, err := st.CreateAPI(proj.ID, &store.API{Name: "ok", Request: store.APIRequest{Method: "GET", URL: srv.URL + "/ok"}})
				if err != nil {
					t.Fatal(err)
				}
				return a.ID
			}()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.StartRun(ctx, proj.ID, StartRunRequest{
		ScenarioID: scen.ID,
		ActorRole:  "member",
	}); !errors.Is(err, ErrScaleLimitExceeded) {
		t.Fatalf("expected ErrScaleLimitExceeded for member VUs=5 > 2, got %v", err)
	}
	if _, err := c.StartRun(ctx, proj.ID, StartRunRequest{
		ScenarioID: scen.ID,
		ActorRole:  "viewer",
		Load: &scenario.Load{
			Mode:   "vus",
			Stages: []scenario.Stage{{Duration: scenario.Duration(400 * time.Millisecond), Target: 1}},
		},
	}); !errors.Is(err, ErrScaleLimitExceeded) {
		t.Fatalf("expected ErrScaleLimitExceeded for viewer, got %v", err)
	}

	sub := c.Subscribe()
	defer sub.Close()
	sub.SubscribeRun("*")

	run, err := c.StartRun(ctx, proj.ID, StartRunRequest{
		ScenarioID: scen.ID,
		ActorRole:  "member",
		Load: &scenario.Load{
			Mode:   "vus",
			Stages: []scenario.Stage{{Duration: scenario.Duration(600 * time.Millisecond), Target: 2}},
		},
	})
	if err != nil {
		t.Fatalf("StartRun within member limit failed: %v", err)
	}
	if err := c.SetRunTargetWithRole(ctx, run.ID, 5, "member"); !errors.Is(err, ErrScaleLimitExceeded) {
		t.Fatalf("expected ErrScaleLimitExceeded when member scales to 5, got %v", err)
	}
	if err := c.SetRunTargetWithRole(ctx, run.ID, 3, "admin"); err != nil {
		t.Fatalf("admin SetRunTargetWithRole failed: %v", err)
	}

	deadline := time.After(6 * time.Second)
	for {
		select {
		case ev := <-sub.C():
			if ev.RunID == run.ID && ev.Type == EventRunStatus && !ev.Status.Active() {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for scale-check run completion")
		}
	}
}

func TestStartRunScrubsPersistedSecrets(t *testing.T) {
	var seenHeader atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeader.Store(r.Header.Get("X-Api-Key"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, st, _ := newTestController(t, func(o *Options) {
		box, err := store.LoadOrCreateMasterKey(filepath.Join(t.TempDir(), "master.key"))
		if err != nil {
			t.Fatal(err)
		}
		o.Store.SetSecretVault(store.NewSecretVault(o.Results, box))
	})
	ctx := context.Background()
	waitForAgents(t, c, 1)

	proj, err := st.CreateProject(&store.Project{
		Name: "secret-run",
		Variables: store.KeyValueList{
			{Name: "api_secret", Value: "super-secret-value", Secret: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	apiObj, err := st.CreateAPI(proj.ID, &store.API{
		Name: "secret-call",
		Request: store.APIRequest{
			Method: "GET",
			URL:    srv.URL + "/check",
			Headers: store.KeyValueList{
				{Name: "X-Api-Key", Value: "{{api_secret}}"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	sub := c.Subscribe()
	defer sub.Close()
	sub.SubscribeRun("*")

	run, err := c.StartRun(ctx, proj.ID, StartRunRequest{
		APIID: apiObj.ID,
		Load: &scenario.Load{
			Mode:        "vus",
			Stages:      []scenario.Stage{{Duration: scenario.Duration(2 * time.Second), Target: 1}},
			MaxRequests: 1,
		},
	})
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}

	deadline := time.After(6 * time.Second)
	for done := false; !done; {
		select {
		case ev := <-sub.C():
			if ev.RunID == run.ID && ev.Type == EventRunStatus && !ev.Status.Active() {
				done = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for secret-run completion")
		}
	}

	if got, _ := seenHeader.Load().(string); got != "super-secret-value" {
		t.Fatalf("target server X-Api-Key = %q, want %q", got, "super-secret-value")
	}

	stored, err := c.results.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored.Scenario), "super-secret-value") {
		t.Fatalf("persisted run scenario leaked plaintext secret: %s", stored.Scenario)
	}
	if !strings.Contains(string(stored.Scenario), store.SecretMask) {
		t.Fatalf("expected persisted run scenario to contain %s mask, got: %s", store.SecretMask, stored.Scenario)
	}
}

func TestStartRunEnforcesDialTimeRedirectAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.2:1/ssrf", http.StatusFound)
	}))
	defer srv.Close()

	c, st, _ := newTestController(t, nil)
	ctx := context.Background()
	waitForAgents(t, c, 1)

	if _, err := c.SetSecurityPolicy(ctx, SecurityPolicy{
		AllowlistEnabled: true,
		AllowedTargets:   []string{"127.0.0.1"},
	}); err != nil {
		t.Fatal(err)
	}

	proj, err := st.CreateProject(&store.Project{Name: "redir-allowlist"})
	if err != nil {
		t.Fatal(err)
	}
	apiObj, err := st.CreateAPI(proj.ID, &store.API{
		Name: "redir",
		Request: store.APIRequest{
			Method: "GET",
			URL:    srv.URL + "/start",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	sub := c.Subscribe()
	defer sub.Close()
	sub.SubscribeRun("*")

	run, err := c.StartRun(ctx, proj.ID, StartRunRequest{
		APIID: apiObj.ID,
		Load: &scenario.Load{
			Mode:        "vus",
			Stages:      []scenario.Stage{{Duration: scenario.Duration(2 * time.Second), Target: 1}},
			MaxRequests: 2,
		},
	})
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}

	deadline := time.After(6 * time.Second)
	done := false
	for !done {
		select {
		case ev := <-sub.C():
			if ev.RunID == run.ID && ev.Type == EventRunStatus && !ev.Status.Active() {
				done = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for redirect-allowlist run completion")
		}
	}

	stored, err := c.results.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sum metrics.SnapshotView
	if err := json.Unmarshal(stored.Summary, &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Errors == 0 {
		t.Fatalf("expected redirect to 127.0.0.2 to be blocked with error, summary = %+v", sum)
	}
	samples, err := c.results.Samples(ctx, run.ID, store.SampleQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	foundBlockMsg := false
	for _, s := range samples {
		if strings.Contains(string(s.Data), ErrTargetNotAllowed.Error()) {
			foundBlockMsg = true
			break
		}
	}
	if !foundBlockMsg {
		t.Fatalf("expected sample error containing %q, got %+v", ErrTargetNotAllowed.Error(), samples)
	}
}
