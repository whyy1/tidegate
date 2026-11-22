package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"tidegate/internal/auth"
	"tidegate/internal/metrics"
)

func openTestResultDB(t *testing.T) *ResultDB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), DBFileName)
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Verify auth.Migrate coexists cleanly on the exact same SQLite database.
	if err := auth.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return NewResultDB(db)
}

func TestResultDBRunsSnapshotsHistogramAlertsAndCascade(t *testing.T) {
	ctx := context.Background()
	rdb := openTestResultDB(t)

	authSvc, err := auth.New(rdb.DB(), auth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := authSvc.SetupPending(ctx)
	if err != nil || !pending {
		t.Fatalf("auth coexistence SetupPending=%v err=%v", pending, err)
	}

	run := &Run{
		ProjectID:    "p1",
		ScenarioID:   "s1",
		ScenarioName: "登录压测",
		Scenario:     json.RawMessage(`{"version":1,"name":"登录压测"}`),
		Status:       RunPreparing,
	}
	if err := rdb.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := rdb.UpdateRun(ctx, run.ID, RunUpdate{Status: RunRunning, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// Build two snapshots with HDR latency and sample including RequestBody.
	s0 := metrics.NewSnapshot([]metrics.StepInfo{{ID: "step1", Name: "登录"}}, false)
	s0.Seq = 0
	s0.Time = time.Unix(1700000000, 0)
	s0.Duration = time.Second
	s0.Steps[0].Requests = 10
	for i := 1; i <= 10; i++ {
		s0.Steps[0].Latency.Record(time.Duration(i) * time.Millisecond)
	}
	s0.Samples = []metrics.Sample{{
		Kind:        metrics.SampleKind(metrics.ErrHTTP5xx),
		Step:        0,
		StepID:      "step1",
		Time:        s0.Time,
		Method:      "POST",
		URL:         "http://127.0.0.1/login",
		Status:      500,
		RequestBody: `{"user":"alice"}`,
		Body:        "err",
	}}
	s0.ComputeTotal()
	if err := rdb.AppendSnapshot(ctx, run.ID, s0.View()); err != nil {
		t.Fatal(err)
	}

	snaps, err := rdb.Snapshots(ctx, run.ID, 0, 10)
	if err != nil || len(snaps) != 1 || snaps[0].Seq != 0 {
		t.Fatalf("Snapshots = %+v err=%v", snaps, err)
	}
	samples, err := rdb.Samples(ctx, run.ID, SampleQuery{})
	if err != nil || len(samples) != 1 || !json.Valid(samples[0].Data) {
		t.Fatalf("Samples = %+v err=%v", samples, err)
	}

	// Add and close an alert.
	al := &Alert{RunID: run.ID, Agent: "local", Kind: "cpu", Level: AlertWarning, Message: "CPU 高", Value: 0.9, Threshold: 0.85}
	if err := rdb.AddAlert(ctx, al); err != nil {
		t.Fatal(err)
	}
	if err := rdb.EndAlert(ctx, al.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	mergedBin, err := s0.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	sumJSON, _ := json.Marshal(s0.View())
	if err := rdb.FinishRun(ctx, run.ID, RunFinish{
		Status:  RunFinished,
		Summary: sumJSON,
		Merged:  mergedBin,
	}); err != nil {
		t.Fatal(err)
	}
	gotRun, err := rdb.GetRun(ctx, run.ID)
	if err != nil || gotRun.Status != RunFinished || len(gotRun.Summary) == 0 {
		t.Fatalf("GetRun = %+v err=%v", gotRun, err)
	}
	restored, err := rdb.MergedSnapshot(ctx, run.ID)
	if err != nil || restored.Total.Latency.Stats().P99 != s0.Total.Latency.Stats().P99 {
		t.Fatalf("MergedSnapshot = %+v err=%v", restored, err)
	}
	alerts, err := rdb.Alerts(ctx, run.ID)
	if err != nil || len(alerts) != 1 || alerts[0].EndedAt.IsZero() {
		t.Fatalf("Alerts = %+v err=%v", alerts, err)
	}

	// MarkInterrupted fails active runs.
	active := &Run{ProjectID: "p1", ScenarioName: "interrupted", Scenario: json.RawMessage(`{}`), Status: RunRunning}
	if err := rdb.CreateRun(ctx, active); err != nil {
		t.Fatal(err)
	}
	if n, err := rdb.MarkInterrupted(ctx, ""); err != nil || n != 1 {
		t.Fatalf("MarkInterrupted n=%d err=%v", n, err)
	}

	// DeleteRun cascades to snapshots, samples, alerts, histograms.
	if err := rdb.DeleteRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rdb.Histogram(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Histogram after delete: got %v, want ErrNotFound", err)
	}
}

func TestResultDBDebugHistorySecretsAndSettings(t *testing.T) {
	ctx := context.Background()
	rdb := openTestResultDB(t)

	// Save 205 records -> automatically pruned to DefaultMaxDebugHistory (200).
	base := time.Unix(1700000000, 0)
	for i := 0; i < DefaultMaxDebugHistory+5; i++ {
		err := rdb.SaveDebugRecord(ctx, &DebugRecord{
			ProjectID:  "p1",
			Method:     "GET",
			URL:        "http://127.0.0.1/ping",
			Status:     200,
			DurationMs: float64(i),
			CreatedAt:  base.Add(time.Duration(i) * time.Second),
			Data:       json.RawMessage(`{"ok":true}`),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	list, err := rdb.ListDebugHistory(ctx, "p1", DefaultMaxDebugHistory, false)
	if err != nil || len(list) != DefaultMaxDebugHistory {
		t.Fatalf("len=%d err=%v", len(list), err)
	}
	// Newest record should be first (DurationMs == 204).
	if list[0].DurationMs != float64(DefaultMaxDebugHistory+4) || len(list[0].Data) != 0 {
		t.Fatalf("newest = %+v", list[0])
	}
	full, err := rdb.GetDebugRecord(ctx, list[0].ID)
	if err != nil || string(full.Data) != `{"ok":true}` {
		t.Fatalf("GetDebugRecord = %+v err=%v", full, err)
	}
	if err := rdb.ClearDebugHistory(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if after, _ := rdb.ListDebugHistory(ctx, "p1", 10, false); len(after) != 0 {
		t.Fatalf("expected empty history after clear, got %d", len(after))
	}

	// Project secrets and agent settings.
	if err := rdb.SetProjectSecret(ctx, "p1", "env1", "api_key", []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	sec, err := rdb.ProjectSecrets(ctx, "p1", "env1")
	if err != nil || string(sec["api_key"]) != "\x01\x02\x03" {
		t.Fatalf("ProjectSecrets = %+v err=%v", sec, err)
	}
	if err := rdb.DeleteProjectSecret(ctx, "p1", "env1", "api_key"); err != nil {
		t.Fatal(err)
	}

	if err := rdb.SetAgentSettings(ctx, AgentSettings{Name: "local", BandwidthBps: 100_000_000}); err != nil {
		t.Fatal(err)
	}
	as, err := rdb.AgentSettings(ctx, "local")
	if err != nil || as.BandwidthBps != 100_000_000 {
		t.Fatalf("AgentSettings = %+v err=%v", as, err)
	}
	if err := rdb.SetSetting(ctx, "k1", "v1"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := rdb.Setting(ctx, "k1"); err != nil || !ok || v != "v1" {
		t.Fatalf("Setting = (%q, %v, %v)", v, ok, err)
	}
}
