package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
)

var resultMigrations = []Migration{{
	Version: 1,
	Name:    "result tables",
	SQL: `
CREATE TABLE runs (
	id             TEXT    PRIMARY KEY,
	project_id     TEXT    NOT NULL,
	scenario_id    TEXT    NOT NULL DEFAULT '',
	api_id         TEXT    NOT NULL DEFAULT '',
	environment_id TEXT    NOT NULL DEFAULT '',
	scenario_name  TEXT    NOT NULL,
	scenario_json  TEXT    NOT NULL,
	status         TEXT    NOT NULL,
	created_at     INTEGER NOT NULL,
	started_at     INTEGER,
	finished_at    INTEGER,
	error          TEXT    NOT NULL DEFAULT '',
	summary_json   TEXT
);
CREATE INDEX runs_project_created ON runs (project_id, created_at DESC, id DESC);
CREATE INDEX runs_status ON runs (status);

CREATE TABLE run_snapshots (
	run_id TEXT    NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
	seq    INTEGER NOT NULL,
	time   INTEGER NOT NULL,
	data   TEXT    NOT NULL,
	PRIMARY KEY (run_id, seq)
) WITHOUT ROWID;

CREATE TABLE run_histograms (
	run_id TEXT PRIMARY KEY REFERENCES runs (id) ON DELETE CASCADE,
	data   BLOB NOT NULL
);

CREATE TABLE run_samples (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id TEXT    NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
	seq    INTEGER NOT NULL,
	kind   TEXT    NOT NULL,
	step   TEXT    NOT NULL,
	time   INTEGER NOT NULL,
	data   TEXT    NOT NULL
);
CREATE INDEX run_samples_run ON run_samples (run_id, id);

CREATE TABLE run_alerts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id     TEXT    NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
	agent      TEXT    NOT NULL DEFAULT '',
	kind       TEXT    NOT NULL,
	level      TEXT    NOT NULL DEFAULT 'warning',
	message    TEXT    NOT NULL DEFAULT '',
	value      REAL    NOT NULL DEFAULT 0,
	threshold  REAL    NOT NULL DEFAULT 0,
	started_at INTEGER NOT NULL,
	ended_at   INTEGER
);
CREATE INDEX run_alerts_run ON run_alerts (run_id, started_at);

CREATE TABLE settings (
	key        TEXT    PRIMARY KEY,
	value      TEXT    NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE agent_settings (
	agent_name    TEXT    PRIMARY KEY,
	bandwidth_bps INTEGER NOT NULL DEFAULT 0,
	updated_at    INTEGER NOT NULL
);

CREATE TABLE debug_history (
	id          TEXT    PRIMARY KEY,
	project_id  TEXT    NOT NULL,
	api_id      TEXT    NOT NULL DEFAULT '',
	method      TEXT    NOT NULL,
	url         TEXT    NOT NULL,
	status      INTEGER NOT NULL,
	duration_ms REAL    NOT NULL,
	created_at  INTEGER NOT NULL,
	data        TEXT    NOT NULL
);
CREATE INDEX debug_history_project ON debug_history (project_id, created_at DESC, id DESC);

CREATE TABLE project_secrets (
	project_id TEXT    NOT NULL,
	scope      TEXT    NOT NULL,
	name       TEXT    NOT NULL,
	ciphertext BLOB    NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (project_id, scope, name)
) WITHOUT ROWID;
`,
}, {
	Version: 2,
	Name:    "audit logs",
	SQL: `
CREATE TABLE audit_logs (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	created_at  INTEGER NOT NULL,
	user_id     INTEGER NOT NULL DEFAULT 0,
	username    TEXT    NOT NULL DEFAULT '',
	role        TEXT    NOT NULL DEFAULT '',
	action      TEXT    NOT NULL,
	resource    TEXT    NOT NULL DEFAULT '',
	resource_id TEXT    NOT NULL DEFAULT '',
	remote_addr TEXT    NOT NULL DEFAULT '',
	detail_json TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_logs_created ON audit_logs (created_at DESC, id DESC);
CREATE INDEX audit_logs_action ON audit_logs (action, created_at DESC);
`,
}, {
	Version: 3,
	Name:    "cluster m3",
	SQL: `
ALTER TABLE agent_settings ADD COLUMN weight REAL NOT NULL DEFAULT 0;

CREATE TABLE agent_join_tokens (
	id          TEXT    PRIMARY KEY,
	name        TEXT    NOT NULL DEFAULT '',
	token_hash  TEXT    NOT NULL UNIQUE,
	single_use  INTEGER NOT NULL DEFAULT 1,
	used_count  INTEGER NOT NULL DEFAULT 0,
	created_by  TEXT    NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	expires_at  INTEGER NOT NULL,
	used_at     INTEGER
);

CREATE TABLE agent_certs (
	serial_hex  TEXT    PRIMARY KEY,
	node_id     TEXT    NOT NULL DEFAULT '',
	node_name   TEXT    NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	expires_at  INTEGER NOT NULL,
	revoked_at  INTEGER
);
CREATE INDEX agent_certs_node ON agent_certs (node_id, node_name);

CREATE TABLE revoked_nodes (
	node_id     TEXT    PRIMARY KEY,
	reason      TEXT    NOT NULL DEFAULT '',
	revoked_at  INTEGER NOT NULL
);
`,
}}

// RunStatus is the life cycle state of a run.
type RunStatus string

// Run states.
const (
	RunPreparing RunStatus = "preparing"
	RunRunning   RunStatus = "running"
	RunStopping  RunStatus = "stopping"
	RunFinished  RunStatus = "finished"
	RunFailed    RunStatus = "failed"
)

// Valid reports whether s is a known state.
func (s RunStatus) Valid() bool {
	switch s {
	case RunPreparing, RunRunning, RunStopping, RunFinished, RunFailed:
		return true
	}
	return false
}

// Active reports whether the run has not ended.
func (s RunStatus) Active() bool {
	return s == RunPreparing || s == RunRunning || s == RunStopping
}

// Run is a load test record.
type Run struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	ScenarioID    string `json:"scenario_id,omitempty"`
	APIID         string `json:"api_id,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
	ScenarioName  string `json:"scenario_name"`
	// Scenario is the resolved scenario (scenario.Definition as JSON).
	// ListRuns leaves it empty.
	Scenario   json.RawMessage `json:"scenario,omitempty"`
	Status     RunStatus       `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  time.Time       `json:"started_at,omitzero"`
	FinishedAt time.Time       `json:"finished_at,omitzero"`
	Error      string          `json:"error,omitempty"`
	// Summary is the whole-run summary (metrics.SnapshotView as JSON),
	// empty until the run ends.
	Summary json.RawMessage `json:"summary,omitempty"`
}

// Definition decodes the stored scenario.
func (r *Run) Definition() (*scenario.Definition, error) {
	return scenario.Parse(r.Scenario, scenario.FormatJSON)
}

// RunUpdate changes fields of a run; zero fields are left unchanged.
type RunUpdate struct {
	Status     RunStatus
	StartedAt  time.Time
	FinishedAt time.Time
	// Error replaces the error message when not nil.
	Error   *string
	Summary json.RawMessage
}

// RunFinish records the end of a run.
type RunFinish struct {
	Status     RunStatus // RunFinished or RunFailed
	FinishedAt time.Time // defaults to now
	Error      string
	// Summary is the whole-run summary as JSON (metrics.SnapshotView).
	Summary json.RawMessage
	// Merged is the whole-run merged snapshot in binary form
	// (metrics.Snapshot.MarshalBinary); its histograms give exact whole-run
	// percentiles. Optional.
	Merged []byte
}

// RunQuery pages through runs, newest first.
type RunQuery struct {
	// Limit defaults to 50 and is capped at 500.
	Limit  int
	Offset int
}

// SnapshotRecord is a stored per-second snapshot. It marshals to JSON as
// the snapshot itself (metrics.SnapshotView without samples).
type SnapshotRecord struct {
	Seq  int64
	Time time.Time
	Data json.RawMessage
}

// MarshalJSON implements json.Marshaler.
func (s SnapshotRecord) MarshalJSON() ([]byte, error) {
	if len(s.Data) == 0 {
		return []byte("null"), nil
	}
	return s.Data, nil
}

// SampleRecord is a stored sample.
type SampleRecord struct {
	ID   int64     `json:"id"`
	Seq  int64     `json:"seq"`
	Kind string    `json:"kind"`
	Step string    `json:"step"`
	Time time.Time `json:"time"`
	// Data is the sample as JSON (metrics.SampleView).
	Data json.RawMessage `json:"data"`
}

// SampleQuery filters samples.
type SampleQuery struct {
	Kind string
	Step string
	// AfterID returns samples with a larger ID, for paging.
	AfterID int64
	// Limit defaults to 100 and is capped at 1000.
	Limit int
}

// Alert levels.
const (
	AlertWarning  = "warning"
	AlertCritical = "critical"
)

// Alert is a load generator alert during a run (bandwidth, CPU, memory,
// scheduling delay, local errors). EndedAt is zero while it is active.
type Alert struct {
	ID        int64     `json:"id"`
	RunID     string    `json:"run_id"`
	Agent     string    `json:"agent,omitempty"`
	Kind      string    `json:"kind"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Value     float64   `json:"value"`
	Threshold float64   `json:"threshold"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
}

// AgentSettings are per agent settings, keyed by agent name.
type AgentSettings struct {
	Name string `json:"name"`
	// BandwidthBps is the manually set bandwidth limit in bits per second;
	// 0 means unset (the negotiated link speed is used).
	BandwidthBps int64 `json:"bandwidth_bps"`
	// Weight configures proportional load distribution; 0 means auto (by CPU cores).
	Weight    float64   `json:"weight"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// DefaultMaxSamplesPerKind bounds the samples stored per run and category.
const DefaultMaxSamplesPerKind = 200

// ResultDB stores runs, snapshots, samples, alerts and settings in the
// database opened by OpenDB. It is safe for concurrent use.
type ResultDB struct {
	db *sql.DB
	// MaxSamplesPerKind bounds the samples kept per run and sample kind;
	// further samples are dropped.
	MaxSamplesPerKind int
}

// NewResultDB wraps a database opened by OpenDB.
func NewResultDB(db *sql.DB) *ResultDB {
	return &ResultDB{db: db, MaxSamplesPerKind: DefaultMaxSamplesPerKind}
}

// DB returns the underlying database.
func (r *ResultDB) DB() *sql.DB { return r.db }

func ms(t time.Time) int64 { return t.UnixMilli() }

func nullMS(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func fromNullMS(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return fromMS(v.Int64)
}

func nullJSON(b json.RawMessage) sql.NullString {
	if len(b) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: string(b), Valid: true}
}

func validJSON(field string, b json.RawMessage) error {
	if len(b) > 0 && !json.Valid(b) {
		return invalid(field, "不是合法的 JSON")
	}
	return nil
}

func runErr(err error, runID string) error {
	if isForeignKeyError(err) {
		return notFound(KindRun, runID)
	}
	return err
}

// CreateRun inserts a run. An empty ID is generated, an empty status
// becomes RunPreparing and a zero CreatedAt becomes now.
func (r *ResultDB) CreateRun(ctx context.Context, run *Run) error {
	if run.ID == "" {
		run.ID = newID()
	}
	if run.ProjectID == "" {
		return invalid("project_id", "不能为空")
	}
	if len(run.Scenario) == 0 {
		return invalid("scenario", "不能为空")
	}
	if err := validJSON("scenario", run.Scenario); err != nil {
		return err
	}
	if err := validJSON("summary", run.Summary); err != nil {
		return err
	}
	if run.Status == "" {
		run.Status = RunPreparing
	}
	if !run.Status.Valid() {
		return invalid("status", "未知的状态 %q", run.Status)
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now()
	}
	run.CreatedAt = fromMS(ms(run.CreatedAt))
	_, err := r.db.ExecContext(ctx, `INSERT INTO runs
		(id, project_id, scenario_id, api_id, environment_id, scenario_name, scenario_json, status, created_at, started_at, finished_at, error, summary_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ProjectID, run.ScenarioID, run.APIID, run.EnvironmentID, run.ScenarioName, string(run.Scenario),
		string(run.Status), ms(run.CreatedAt), nullMS(run.StartedAt), nullMS(run.FinishedAt), run.Error, nullJSON(run.Summary))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return &ConflictError{Kind: KindRun, ID: run.ID, Msg: fmt.Sprintf("压测记录 %q 已存在", run.ID)}
		}
		return fmt.Errorf("保存压测记录失败：%w", err)
	}
	return nil
}

// UpdateRun changes fields of a run.
func (r *ResultDB) UpdateRun(ctx context.Context, id string, u RunUpdate) error {
	var sets []string
	var args []any
	if u.Status != "" {
		if !u.Status.Valid() {
			return invalid("status", "未知的状态 %q", u.Status)
		}
		sets, args = append(sets, "status = ?"), append(args, string(u.Status))
	}
	if !u.StartedAt.IsZero() {
		sets, args = append(sets, "started_at = ?"), append(args, ms(u.StartedAt))
	}
	if !u.FinishedAt.IsZero() {
		sets, args = append(sets, "finished_at = ?"), append(args, ms(u.FinishedAt))
	}
	if u.Error != nil {
		sets, args = append(sets, "error = ?"), append(args, *u.Error)
	}
	if len(u.Summary) > 0 {
		if err := validJSON("summary", u.Summary); err != nil {
			return err
		}
		sets, args = append(sets, "summary_json = ?"), append(args, string(u.Summary))
	}
	if len(sets) == 0 {
		_, err := r.GetRun(ctx, id)
		return err
	}
	args = append(args, id)
	res, err := r.db.ExecContext(ctx, "UPDATE runs SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return fmt.Errorf("更新压测记录失败：%w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound(KindRun, id)
	}
	return nil
}

// FinishRun records the final state, summary and merged histograms of a run
// in one transaction and closes alerts that are still open.
func (r *ResultDB) FinishRun(ctx context.Context, id string, f RunFinish) error {
	if f.Status == "" {
		f.Status = RunFinished
	}
	if !f.Status.Valid() || f.Status.Active() {
		return invalid("status", "结束状态只能是 %s 或 %s", RunFinished, RunFailed)
	}
	if err := validJSON("summary", f.Summary); err != nil {
		return err
	}
	if f.FinishedAt.IsZero() {
		f.FinishedAt = time.Now()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, finished_at = ?, error = ?, summary_json = COALESCE(?, summary_json) WHERE id = ?`,
		string(f.Status), ms(f.FinishedAt), f.Error, nullJSON(f.Summary), id)
	if err != nil {
		return fmt.Errorf("更新压测记录失败：%w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound(KindRun, id)
	}
	if len(f.Merged) > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_histograms (run_id, data) VALUES (?, ?)
			ON CONFLICT (run_id) DO UPDATE SET data = excluded.data`, id, f.Merged); err != nil {
			return fmt.Errorf("保存直方图失败：%w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_alerts SET ended_at = ? WHERE run_id = ? AND ended_at IS NULL`, ms(f.FinishedAt), id); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkInterrupted fails every run that is still active, for use at startup
// after a crash or restart. It returns the number of runs changed.
func (r *ResultDB) MarkInterrupted(ctx context.Context, msg string) (int64, error) {
	if msg == "" {
		msg = "服务重启，压测被中断"
	}
	now := ms(time.Now())
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE run_alerts SET ended_at = ? WHERE ended_at IS NULL AND run_id IN
		(SELECT id FROM runs WHERE status IN (?, ?, ?))`, now, RunPreparing, RunRunning, RunStopping); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, error = ?, finished_at = COALESCE(finished_at, ?)
		WHERE status IN (?, ?, ?)`, RunFailed, msg, now, RunPreparing, RunRunning, RunStopping)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

const runColumns = `id, project_id, scenario_id, api_id, environment_id, scenario_name, status, created_at, started_at, finished_at, error, summary_json`

type rowScanner interface{ Scan(dest ...any) error }

func scanRun(row rowScanner, withScenario bool) (*Run, error) {
	var (
		run              Run
		status           string
		created          int64
		started, ended   sql.NullInt64
		summary, scenJSON sql.NullString
	)
	dest := []any{&run.ID, &run.ProjectID, &run.ScenarioID, &run.APIID, &run.EnvironmentID, &run.ScenarioName,
		&status, &created, &started, &ended, &run.Error, &summary}
	if withScenario {
		dest = append(dest, &scenJSON)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	run.Status = RunStatus(status)
	run.CreatedAt = fromMS(created)
	run.StartedAt, run.FinishedAt = fromNullMS(started), fromNullMS(ended)
	if summary.Valid {
		run.Summary = json.RawMessage(summary.String)
	}
	if scenJSON.Valid {
		run.Scenario = json.RawMessage(scenJSON.String)
	}
	return &run, nil
}

// GetRun returns a run including its scenario.
func (r *ResultDB) GetRun(ctx context.Context, id string) (*Run, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+runColumns+`, scenario_json FROM runs WHERE id = ?`, id)
	run, err := scanRun(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound(KindRun, id)
	}
	if err != nil {
		return nil, fmt.Errorf("读取压测记录失败：%w", err)
	}
	return run, nil
}

// ListRuns returns the runs of a project (all projects when projectID is
// empty), newest first, without their scenarios.
func (r *ResultDB) ListRuns(ctx context.Context, projectID string, q RunQuery) ([]*Run, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 500)
	where, args := "", []any{}
	if projectID != "" {
		where, args = "WHERE project_id = ?", append(args, projectID)
	}
	args = append(args, limit, max(q.Offset, 0))
	rows, err := r.db.QueryContext(ctx, `SELECT `+runColumns+` FROM runs `+where+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("读取压测记录失败：%w", err)
	}
	defer rows.Close()
	out := []*Run{}
	for rows.Next() {
		run, err := scanRun(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// DeleteRun removes a run with its snapshots, samples, alerts and
// histogram.
func (r *ResultDB) DeleteRun(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除压测记录失败：%w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound(KindRun, id)
	}
	return nil
}

// DeleteProjectRuns removes every run of a project and returns how many
// were removed.
func (r *ResultDB) DeleteProjectRuns(ctx context.Context, projectID string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM runs WHERE project_id = ?`, projectID)
	if err != nil {
		return 0, fmt.Errorf("删除压测记录失败：%w", err)
	}
	return res.RowsAffected()
}

func (r *ResultDB) runExists(ctx context.Context, id string) error {
	var one int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM runs WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound(KindRun, id)
	}
	return err
}

// AppendSnapshot stores the per-second snapshot of a run, replacing a
// snapshot with the same sequence number. Its samples are moved to the
// sample table (see AddSamples) and not kept in the snapshot JSON.
func (r *ResultDB) AppendSnapshot(ctx context.Context, runID string, v *metrics.SnapshotView) error {
	cp := *v
	samples := cp.Samples
	cp.Samples = nil
	data, err := json.Marshal(&cp)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_snapshots (run_id, seq, time, data) VALUES (?, ?, ?, ?)
		ON CONFLICT (run_id, seq) DO UPDATE SET time = excluded.time, data = excluded.data`,
		runID, v.Seq, ms(v.Time), string(data)); err != nil {
		return runErr(err, runID)
	}
	if len(samples) > 0 {
		if _, err := r.addSamples(ctx, tx, runID, v.Seq, samples); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Snapshots returns the snapshots of a run with seq >= from, in order. A
// limit <= 0 returns all.
func (r *ResultDB) Snapshots(ctx context.Context, runID string, from int64, limit int) ([]SnapshotRecord, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := r.db.QueryContext(ctx, `SELECT seq, time, data FROM run_snapshots WHERE run_id = ? AND seq >= ? ORDER BY seq LIMIT ?`, runID, from, limit)
	if err != nil {
		return nil, fmt.Errorf("读取快照失败：%w", err)
	}
	defer rows.Close()
	out := []SnapshotRecord{}
	for rows.Next() {
		var rec SnapshotRecord
		var t int64
		var data string
		if err := rows.Scan(&rec.Seq, &t, &data); err != nil {
			return nil, err
		}
		rec.Time, rec.Data = fromMS(t), json.RawMessage(data)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		if err := r.runExists(ctx, runID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// AddSamples stores samples of a run, keeping at most MaxSamplesPerKind per
// sample kind. It returns the number stored.
func (r *ResultDB) AddSamples(ctx context.Context, runID string, seq int64, samples []metrics.SampleView) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n, err := r.addSamples(ctx, tx, runID, seq, samples)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

func (r *ResultDB) addSamples(ctx context.Context, tx *sql.Tx, runID string, seq int64, samples []metrics.SampleView) (int, error) {
	if len(samples) == 0 {
		return 0, nil
	}
	limit := r.MaxSamplesPerKind
	if limit <= 0 {
		limit = DefaultMaxSamplesPerKind
	}
	counts := map[string]int{}
	rows, err := tx.QueryContext(ctx, `SELECT kind, COUNT(*) FROM run_samples WHERE run_id = ? GROUP BY kind`, runID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return 0, err
		}
		counts[k] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	stored := 0
	for i := range samples {
		sm := &samples[i]
		if counts[sm.Kind] >= limit {
			continue
		}
		data, err := json.Marshal(sm)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_samples (run_id, seq, kind, step, time, data) VALUES (?, ?, ?, ?, ?, ?)`,
			runID, seq, sm.Kind, sm.Step, ms(sm.Time), string(data)); err != nil {
			return 0, runErr(err, runID)
		}
		counts[sm.Kind]++
		stored++
	}
	return stored, nil
}

// Samples returns samples of a run in insertion order.
func (r *ResultDB) Samples(ctx context.Context, runID string, q SampleQuery) ([]SampleRecord, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 1000)
	query := `SELECT id, seq, kind, step, time, data FROM run_samples WHERE run_id = ? AND id > ?`
	args := []any{runID, q.AfterID}
	if q.Kind != "" {
		query, args = query+` AND kind = ?`, append(args, q.Kind)
	}
	if q.Step != "" {
		query, args = query+` AND step = ?`, append(args, q.Step)
	}
	query, args = query+` ORDER BY id LIMIT ?`, append(args, limit)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取采样失败：%w", err)
	}
	defer rows.Close()
	out := []SampleRecord{}
	for rows.Next() {
		var rec SampleRecord
		var t int64
		var data string
		if err := rows.Scan(&rec.ID, &rec.Seq, &rec.Kind, &rec.Step, &t, &data); err != nil {
			return nil, err
		}
		rec.Time, rec.Data = fromMS(t), json.RawMessage(data)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 && q.AfterID == 0 {
		if err := r.runExists(ctx, runID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SetHistogram stores the whole-run merged histogram blob of a run.
func (r *ResultDB) SetHistogram(ctx context.Context, runID string, data []byte) error {
	if len(data) == 0 {
		return invalid("histogram", "不能为空")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO run_histograms (run_id, data) VALUES (?, ?)
		ON CONFLICT (run_id) DO UPDATE SET data = excluded.data`, runID, data)
	if err != nil {
		return runErr(err, runID)
	}
	return nil
}

// Histogram returns the whole-run merged histogram blob of a run.
func (r *ResultDB) Histogram(ctx context.Context, runID string) ([]byte, error) {
	var data []byte
	err := r.db.QueryRowContext(ctx, `SELECT data FROM run_histograms WHERE run_id = ?`, runID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		if err := r.runExists(ctx, runID); err != nil {
			return nil, err
		}
		return nil, &NotFoundError{Kind: KindRun, ID: runID + " 的直方图"}
	}
	return data, err
}

// SetMergedSnapshot stores the whole-run merged snapshot in binary form.
func (r *ResultDB) SetMergedSnapshot(ctx context.Context, runID string, s *metrics.Snapshot) error {
	data, err := s.MarshalBinary()
	if err != nil {
		return err
	}
	return r.SetHistogram(ctx, runID, data)
}

// MergedSnapshot decodes the whole-run merged snapshot of a run.
func (r *ResultDB) MergedSnapshot(ctx context.Context, runID string) (*metrics.Snapshot, error) {
	data, err := r.Histogram(ctx, runID)
	if err != nil {
		return nil, err
	}
	var s metrics.Snapshot
	if err := s.UnmarshalBinary(data); err != nil {
		return nil, fmt.Errorf("直方图数据损坏：%w", err)
	}
	return &s, nil
}

// AddAlert stores an alert and sets its ID. A zero StartedAt becomes now
// and an empty level becomes AlertWarning.
func (r *ResultDB) AddAlert(ctx context.Context, a *Alert) error {
	if a.Kind == "" {
		return invalid("kind", "不能为空")
	}
	if a.Level == "" {
		a.Level = AlertWarning
	}
	if a.StartedAt.IsZero() {
		a.StartedAt = time.Now()
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO run_alerts (run_id, agent, kind, level, message, value, threshold, started_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.RunID, a.Agent, a.Kind, a.Level, a.Message, a.Value, a.Threshold, ms(a.StartedAt), nullMS(a.EndedAt))
	if err != nil {
		return runErr(err, a.RunID)
	}
	a.ID, err = res.LastInsertId()
	return err
}

// EndAlert sets the end time of an alert.
func (r *ResultDB) EndAlert(ctx context.Context, id int64, end time.Time) error {
	if end.IsZero() {
		end = time.Now()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE run_alerts SET ended_at = ? WHERE id = ?`, ms(end), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound(KindAlert, fmt.Sprint(id))
	}
	return nil
}

// Alerts returns the alerts of a run ordered by start time.
func (r *ResultDB) Alerts(ctx context.Context, runID string) ([]Alert, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, run_id, agent, kind, level, message, value, threshold, started_at, ended_at
		FROM run_alerts WHERE run_id = ? ORDER BY started_at, id`, runID)
	if err != nil {
		return nil, fmt.Errorf("读取告警失败：%w", err)
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var a Alert
		var start int64
		var end sql.NullInt64
		if err := rows.Scan(&a.ID, &a.RunID, &a.Agent, &a.Kind, &a.Level, &a.Message, &a.Value, &a.Threshold, &start, &end); err != nil {
			return nil, err
		}
		a.StartedAt, a.EndedAt = fromMS(start), fromNullMS(end)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Setting returns a setting; ok is false when it is not set.
func (r *ResultDB) Setting(ctx context.Context, key string) (value string, ok bool, err error) {
	err = r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// SetSetting stores a setting.
func (r *ResultDB) SetSetting(ctx context.Context, key, value string) error {
	if key == "" {
		return invalid("key", "不能为空")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, ms(time.Now()))
	return err
}

// DeleteSetting removes a setting; removing a missing setting is not an
// error.
func (r *ResultDB) DeleteSetting(ctx context.Context, key string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// Settings returns all settings.
func (r *ResultDB) Settings(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// AgentSettings returns the settings of an agent; unset agents get zero
// settings.
func (r *ResultDB) AgentSettings(ctx context.Context, name string) (AgentSettings, error) {
	s := AgentSettings{Name: name}
	var updated int64
	err := r.db.QueryRowContext(ctx, `SELECT bandwidth_bps, weight, updated_at FROM agent_settings WHERE agent_name = ?`, name).Scan(&s.BandwidthBps, &s.Weight, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.UpdatedAt = fromMS(updated)
	return s, nil
}

// ListAgentSettings returns the settings of every agent that has any,
// sorted by name.
func (r *ResultDB) ListAgentSettings(ctx context.Context) ([]AgentSettings, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT agent_name, bandwidth_bps, weight, updated_at FROM agent_settings ORDER BY agent_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentSettings{}
	for rows.Next() {
		var s AgentSettings
		var updated int64
		if err := rows.Scan(&s.Name, &s.BandwidthBps, &s.Weight, &updated); err != nil {
			return nil, err
		}
		s.UpdatedAt = fromMS(updated)
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetAgentSettings stores the settings of an agent.
func (r *ResultDB) SetAgentSettings(ctx context.Context, s AgentSettings) error {
	if strings.TrimSpace(s.Name) == "" {
		return invalid("name", "节点名不能为空")
	}
	if s.BandwidthBps < 0 {
		return invalid("bandwidth_bps", "不能是负数")
	}
	if math.IsNaN(s.Weight) || math.IsInf(s.Weight, 0) || s.Weight < 0 {
		return invalid("weight", "节点权重必须是非负有限数字")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO agent_settings (agent_name, bandwidth_bps, weight, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (agent_name) DO UPDATE SET bandwidth_bps = excluded.bandwidth_bps, weight = excluded.weight, updated_at = excluded.updated_at`,
		s.Name, s.BandwidthBps, s.Weight, ms(time.Now()))
	return err
}

// DeleteAgentSettings removes the settings of an agent.
func (r *ResultDB) DeleteAgentSettings(ctx context.Context, name string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM agent_settings WHERE agent_name = ?`, name)
	return err
}

// DefaultMaxDebugHistory bounds debug history records retained per project.
const DefaultMaxDebugHistory = 200

// DebugRecord is one stored single-shot debug request and response.
type DebugRecord struct {
	ID         string          `json:"id"`
	ProjectID  string          `json:"project_id"`
	APIID      string          `json:"api_id,omitempty"`
	Method     string          `json:"method"`
	URL        string          `json:"url"`
	Status     int             `json:"status"`
	DurationMs float64         `json:"duration_ms"`
	CreatedAt  time.Time       `json:"created_at"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// SaveDebugRecord stores a debug record and trims older records for the
// project beyond DefaultMaxDebugHistory.
func (r *ResultDB) SaveDebugRecord(ctx context.Context, rec *DebugRecord) error {
	if rec.ProjectID == "" {
		return invalid("project_id", "不能为空")
	}
	if rec.ID == "" {
		rec.ID = newID()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	rec.CreatedAt = fromMS(ms(rec.CreatedAt))
	if len(rec.Data) == 0 {
		rec.Data = json.RawMessage("{}")
	}
	if err := validJSON("data", rec.Data); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO debug_history (id, project_id, api_id, method, url, status, duration_ms, created_at, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.ProjectID, rec.APIID, rec.Method, rec.URL, rec.Status, rec.DurationMs, ms(rec.CreatedAt), string(rec.Data)); err != nil {
		return fmt.Errorf("保存调试记录失败：%w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM debug_history WHERE project_id = ? AND id NOT IN (
		SELECT id FROM debug_history WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT ?
	)`, rec.ProjectID, rec.ProjectID, DefaultMaxDebugHistory); err != nil {
		return fmt.Errorf("清理调试记录失败：%w", err)
	}
	return tx.Commit()
}

// ListDebugHistory returns debug records of a project, newest first,
// without the full Data payload unless includeData is true.
func (r *ResultDB) ListDebugHistory(ctx context.Context, projectID string, limit int, includeData bool) ([]DebugRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, DefaultMaxDebugHistory)
	cols := `id, project_id, api_id, method, url, status, duration_ms, created_at`
	if includeData {
		cols += `, data`
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+cols+` FROM debug_history WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("读取调试历史失败：%w", err)
	}
	defer rows.Close()
	out := []DebugRecord{}
	for rows.Next() {
		var rec DebugRecord
		var created int64
		var dataStr string
		dest := []any{&rec.ID, &rec.ProjectID, &rec.APIID, &rec.Method, &rec.URL, &rec.Status, &rec.DurationMs, &created}
		if includeData {
			dest = append(dest, &dataStr)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		rec.CreatedAt = fromMS(created)
		if includeData {
			rec.Data = json.RawMessage(dataStr)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// GetDebugRecord returns one stored debug record with full Data payload.
func (r *ResultDB) GetDebugRecord(ctx context.Context, id string) (*DebugRecord, error) {
	var rec DebugRecord
	var created int64
	var dataStr string
	err := r.db.QueryRowContext(ctx, `SELECT id, project_id, api_id, method, url, status, duration_ms, created_at, data
		FROM debug_history WHERE id = ?`, id).Scan(&rec.ID, &rec.ProjectID, &rec.APIID, &rec.Method, &rec.URL, &rec.Status, &rec.DurationMs, &created, &dataStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound(KindDebug, id)
	}
	if err != nil {
		return nil, fmt.Errorf("读取调试记录失败：%w", err)
	}
	rec.CreatedAt = fromMS(created)
	rec.Data = json.RawMessage(dataStr)
	return &rec, nil
}

// ClearDebugHistory removes all debug history records of a project.
func (r *ResultDB) ClearDebugHistory(ctx context.Context, projectID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM debug_history WHERE project_id = ?`, projectID)
	return err
}

// SetProjectSecret stores an encrypted secret value for a project scope
// ("project" or an environment ID).
func (r *ResultDB) SetProjectSecret(ctx context.Context, projectID, scope, name string, ciphertext []byte) error {
	if projectID == "" || scope == "" || name == "" {
		return invalid("secret", "项目 ID、作用域和名称不能为空")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO project_secrets (project_id, scope, name, ciphertext, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (project_id, scope, name) DO UPDATE SET ciphertext = excluded.ciphertext, updated_at = excluded.updated_at`,
		projectID, scope, name, ciphertext, ms(time.Now()))
	return err
}

// ProjectSecrets returns the secret blobs stored for a project scope.
func (r *ResultDB) ProjectSecrets(ctx context.Context, projectID, scope string) (map[string][]byte, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT name, ciphertext FROM project_secrets WHERE project_id = ? AND scope = ?`, projectID, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var name string
		var blob []byte
		if err := rows.Scan(&name, &blob); err != nil {
			return nil, err
		}
		out[name] = blob
	}
	return out, rows.Err()
}

// DeleteProjectSecret deletes a stored secret.
func (r *ResultDB) DeleteProjectSecret(ctx context.Context, projectID, scope, name string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM project_secrets WHERE project_id = ? AND scope = ? AND name = ?`, projectID, scope, name)
	return err
}

// DeleteScopeProjectSecrets deletes all secrets for a project scope.
func (r *ResultDB) DeleteScopeProjectSecrets(ctx context.Context, projectID, scope string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM project_secrets WHERE project_id = ? AND scope = ?`, projectID, scope)
	return err
}

// DeleteAllProjectSecrets deletes every secret belonging to projectID.
func (r *ResultDB) DeleteAllProjectSecrets(ctx context.Context, projectID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM project_secrets WHERE project_id = ?`, projectID)
	return err
}
