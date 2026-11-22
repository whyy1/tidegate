package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tidegate/internal/auth"
	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
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

func TestAuditLogsAndSecretVault(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rdb := NewResultDB(db)

	// 1. Audit log persistence & filtering.
	if err := rdb.RecordAudit(ctx, AuditEntry{
		UserID:     1,
		Username:   "admin",
		Role:       "admin",
		Action:     "run.start",
		Resource:   "run",
		ResourceID: "run-1",
		RemoteAddr: "127.0.0.1",
		Detail:     json.RawMessage(`{"target":"https://api.example.com"}`),
	}); err != nil {
		t.Fatalf("RecordAudit 1: %v", err)
	}
	if err := rdb.RecordAudit(ctx, AuditEntry{
		UserID:   2,
		Username: "bob",
		Role:     "member",
		Action:   "user.login",
		Resource: "auth",
	}); err != nil {
		t.Fatalf("RecordAudit 2: %v", err)
	}
	allAudit, err := rdb.ListAuditLogs(ctx, AuditQuery{Limit: 10})
	if err != nil || len(allAudit) != 2 || allAudit[0].Action != "user.login" {
		t.Fatalf("ListAuditLogs = %+v, err=%v", allAudit, err)
	}
	filteredAudit, err := rdb.ListAuditLogs(ctx, AuditQuery{Action: "run.start", Username: "ADMIN"})
	if err != nil || len(filteredAudit) != 1 || filteredAudit[0].ResourceID != "run-1" {
		t.Fatalf("filtered ListAuditLogs = %+v, err=%v", filteredAudit, err)
	}

	// 2. Master key & SecretBox AEAD + AAD tamper detection.
	keyPath := filepath.Join(dir, "master.key")
	box, err := LoadOrCreateMasterKey(keyPath)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey(create): %v", err)
	}
	boxReloaded, err := LoadOrCreateMasterKey(keyPath)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey(reload): %v", err)
	}
	ct, err := box.Seal("proj1", "env1", "api_key", "super-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := boxReloaded.Open("proj1", "env1", "api_key", ct)
	if err != nil || plain != "super-secret-value" {
		t.Fatalf("Open = (%q, %v)", plain, err)
	}
	if _, err := boxReloaded.Open("proj1", "env1", "other_key", ct); err == nil {
		t.Fatal("expected AAD mismatch error when swapping variable name")
	}

	// 3. Store + SecretVault leaves zero secret plaintext in YAML while resolving at runtime.
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.SetSecretVault(NewSecretVault(rdb, boxReloaded))

	pr, err := st.CreateProject(&Project{
		Name: "vault-proj",
		Variables: KeyValueList{
			{Name: "proj_pub", Value: "visible"},
			{Name: "proj_sec", Value: "proj-secret-raw", Secret: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Variables[1].Value != SecretMask {
		t.Fatalf("expected SecretMask on CreateProject view, got %q", pr.Variables[1].Value)
	}
	projYAML, err := os.ReadFile(filepath.Join(st.Root(), pr.ID, projectFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(projYAML), "proj-secret-raw") {
		t.Fatalf("project.yaml leaked plaintext secret: %s", projYAML)
	}

	env, err := st.CreateEnvironment(pr.ID, &Environment{
		Name:    "prod",
		BaseURL: "https://example.com",
		Variables: KeyValueList{
			{Name: "env_sec", Value: "env-secret-raw", Secret: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	envYAML, err := os.ReadFile(filepath.Join(st.Root(), pr.ID, environmentsDir, env.ID+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envYAML), "env-secret-raw") {
		t.Fatalf("env yaml leaked plaintext secret: %s", envYAML)
	}

	// Updating project with SecretMask preserves existing ciphertext without re-typing secret.
	prUpdated, err := st.UpdateProject(&Project{
		ID:      pr.ID,
		Name:    "vault-proj-2",
		Version: pr.Version,
		Variables: KeyValueList{
			{Name: "proj_pub", Value: "visible-2"},
			{Name: "proj_sec", Value: SecretMask, Secret: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prUpdated.Variables[1].Value != SecretMask {
		t.Fatalf("expected SecretMask preserved after UpdateProject, got %q", prUpdated.Variables[1].Value)
	}

	apiObj, err := st.CreateAPI(pr.ID, &API{
		Name: "call",
		Request: APIRequest{
			Method: "GET",
			URL:    "/check",
			Headers: KeyValueList{
				{Name: "X-Proj-Sec", Value: "{{proj_sec}}"},
				{Name: "X-Env-Sec", Value: "{{env_sec}}"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// PreviewEffectiveAPI masks secrets by default and decrypts only with IncludeSecrets.
	pvMasked, err := st.PreviewEffectiveAPI(pr.ID, apiObj, ResolveOptions{EnvironmentID: env.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, rv := range pvMasked.Variables {
		if rv.Secret && rv.Value != SecretMask {
			t.Fatalf("PreviewEffectiveAPI leaked secret variable %+v", rv)
		}
	}
	pvPlain, err := st.PreviewEffectiveAPI(pr.ID, apiObj, ResolveOptions{EnvironmentID: env.ID, IncludeSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	var gotPlainProj, gotPlainEnv bool
	for _, rv := range pvPlain.Variables {
		if rv.Name == "proj_sec" && rv.Value == "proj-secret-raw" {
			gotPlainProj = true
		}
		if rv.Name == "env_sec" && rv.Value == "env-secret-raw" {
			gotPlainEnv = true
		}
	}
	if !gotPlainProj || !gotPlainEnv {
		t.Fatalf("IncludeSecrets did not decrypt variables: %+v", pvPlain.Variables)
	}

	// ResolveAPI decrypts real secret values into compiled execution plan.
	res, err := st.ResolveAPI(pr.ID, apiObj.ID, APIRunOptions{ResolveOptions: ResolveOptions{EnvironmentID: env.ID}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := res.Compile()
	if err != nil {
		t.Fatal(err)
	}
	hs := plan.Steps[0].(*scenario.HTTPStep)
	var foundProjHdr, foundEnvHdr bool
	for _, h := range hs.Headers {
		if h.Name == "X-Proj-Sec" && h.Value.Literal() == "proj-secret-raw" {
			foundProjHdr = true
		}
		if h.Name == "X-Env-Sec" && h.Value.Literal() == "env-secret-raw" {
			foundEnvHdr = true
		}
	}
	if !foundProjHdr || !foundEnvHdr {
		t.Fatalf("compiled request headers missing decrypted secrets: %+v", hs.Headers)
	}

	// SkipSecretDecrypt skips SQLite decryption (used by k6 script export).
	resNoSec, err := st.ResolveAPI(pr.ID, apiObj.ID, APIRunOptions{ResolveOptions: ResolveOptions{EnvironmentID: env.ID, SkipSecretDecrypt: true}})
	if err != nil {
		t.Fatal(err)
	}
	if string(resNoSec.Definition.Variables["proj_sec"]) == "proj-secret-raw" || string(resNoSec.Definition.Variables["env_sec"]) == "env-secret-raw" {
		t.Fatalf("SkipSecretDecrypt still leaked plaintext: %+v", resNoSec.Definition.Variables)
	}
}

func TestClusterJoinTokensWeightsAndRevocationStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rdb := NewResultDB(db)

	if err := rdb.SetAgentSettings(ctx, AgentSettings{Name: "edge-a", BandwidthBps: 500_000_000, Weight: 3.5}); err != nil {
		t.Fatal(err)
	}
	as, err := rdb.AgentSettings(ctx, "edge-a")
	if err != nil || as.Weight != 3.5 || as.BandwidthBps != 500_000_000 {
		t.Fatalf("AgentSettings weight = %+v, err=%v", as, err)
	}

	now := time.Now()
	tok := AgentJoinToken{
		Name:      "edge-token",
		SingleUse: true,
		CreatedBy: "admin",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := rdb.CreateJoinToken(ctx, &tok, "hash-1"); err != nil {
		t.Fatal(err)
	}
	list, err := rdb.ListJoinTokens(ctx)
	if err != nil || len(list) != 1 || !list[0].SingleUse {
		t.Fatalf("ListJoinTokens = %+v, err=%v", list, err)
	}
	if _, err := rdb.ConsumeJoinToken(ctx, "hash-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("first ConsumeJoinToken failed: %v", err)
	}
	if _, err := rdb.ConsumeJoinToken(ctx, "hash-1", now.Add(2*time.Minute)); err == nil {
		t.Fatal("expected error reusing single-use join token")
	}

	if err := rdb.SaveAgentCert(ctx, "abcd1234", "edge-a", now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := rdb.BindAgentCertNode(ctx, "abcd1234", "node-1", "edge-a"); err != nil {
		t.Fatal(err)
	}
	if rev, err := rdb.IsAgentRevoked(ctx, "abcd1234", "node-1"); err != nil || rev {
		t.Fatalf("expected non-revoked, got rev=%v err=%v", rev, err)
	}
	if err := rdb.RevokeAgent(ctx, "node-1", "edge-a", "abcd1234", "revoked"); err != nil {
		t.Fatal(err)
	}
	if rev, err := rdb.IsAgentRevoked(ctx, "abcd1234", ""); err != nil || !rev {
		t.Fatalf("expected revoked cert serial, got rev=%v err=%v", rev, err)
	}
	if rev, err := rdb.IsAgentRevoked(ctx, "", "node-1"); err != nil || !rev {
		t.Fatalf("expected revoked node id, got rev=%v err=%v", rev, err)
	}
	if err := rdb.DeleteJoinToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
}
