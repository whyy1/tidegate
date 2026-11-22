package api

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"

	"tidegate/internal/metrics"
	"tidegate/internal/store"
)

// RunDelta compares a run's summary metrics against the baseline run.
type RunDelta struct {
	RPSPct        *float64             `json:"rps_pct,omitempty"`
	P50Pct        *float64             `json:"p50_pct,omitempty"`
	P95Pct        *float64             `json:"p95_pct,omitempty"`
	P99Pct        *float64             `json:"p99_pct,omitempty"`
	MaxPct        *float64             `json:"max_pct,omitempty"`
	ErrorRateDiff float64              `json:"error_rate_diff"`
	StepDeltas    map[string]StepDelta `json:"step_deltas,omitempty"`
}

// StepDelta compares one scenario step against the corresponding step in the baseline run.
type StepDelta struct {
	ID            string   `json:"id,omitempty"`
	Name          string   `json:"name,omitempty"`
	RPSPct        *float64 `json:"rps_pct,omitempty"`
	P50Pct        *float64 `json:"p50_pct,omitempty"`
	P95Pct        *float64 `json:"p95_pct,omitempty"`
	P99Pct        *float64 `json:"p99_pct,omitempty"`
	MaxPct        *float64 `json:"max_pct,omitempty"`
	ErrorRateDiff float64  `json:"error_rate_diff"`
}

// RunComparisonItem is one run entry returned by GET /api/v1/runs/compare.
type RunComparisonItem struct {
	Run       *store.Run             `json:"run"`
	Summary   *metrics.SnapshotView  `json:"summary,omitempty"`
	Alerts    []store.Alert          `json:"alerts"`
	Snapshots []store.SnapshotRecord `json:"snapshots"`
	Delta     *RunDelta              `json:"delta,omitempty"`
}

func (s *Server) handleCompareRuns(w http.ResponseWriter, r *http.Request) {
	rawIDs := strings.Split(r.URL.Query().Get("ids"), ",")
	seen := make(map[string]bool, len(rawIDs))
	ids := make([]string, 0, len(rawIDs))
	for _, raw := range rawIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if seen[id] {
			writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "对比压测 ID 不能重复"))
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) < 2 || len(ids) > 6 {
		writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "请传入 2 到 6 个用于对比的压测记录 ID（?ids=id1,id2）"))
		return
	}

	maxPoints := 600
	if rawMax := strings.TrimSpace(r.URL.Query().Get("max_points")); rawMax != "" {
		if n, err := strconv.Atoi(rawMax); err == nil {
			if n < 60 {
				n = 60
			} else if n > 3600 {
				n = 3600
			}
			maxPoints = n
		}
	}

	ctx := r.Context()
	items := make([]RunComparisonItem, 0, len(ids))
	var baseSummary *metrics.SnapshotView

	for i, id := range ids {
		run, err := s.results.GetRun(ctx, id)
		if err != nil {
			writeError(w, err)
			return
		}
		alerts, err := s.results.Alerts(ctx, id)
		if err != nil {
			writeError(w, err)
			return
		}
		snaps, err := s.results.Snapshots(ctx, id, 0, 0)
		if err != nil {
			writeError(w, err)
			return
		}
		snaps = downsampleSnapshots(snaps, maxPoints)
		var summary *metrics.SnapshotView
		if len(run.Summary) > 0 {
			var sv metrics.SnapshotView
			if err := json.Unmarshal(run.Summary, &sv); err == nil {
				summary = &sv
			}
		}

		item := RunComparisonItem{
			Run:       run,
			Summary:   summary,
			Alerts:    alerts,
			Snapshots: snaps,
		}
		if i == 0 {
			baseSummary = summary
		} else if baseSummary != nil && summary != nil {
			item.Delta = computeRunDelta(baseSummary, summary)
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"baseline_id": ids[0],
		"runs":        items,
	})
}

func computeRunDelta(base, cur *metrics.SnapshotView) *RunDelta {
	d := &RunDelta{
		RPSPct:        pctDelta(cur.RPS, base.RPS),
		P50Pct:        pctDelta(cur.Latency.P50, base.Latency.P50),
		P95Pct:        pctDelta(cur.Latency.P95, base.Latency.P95),
		P99Pct:        pctDelta(cur.Latency.P99, base.Latency.P99),
		MaxPct:        pctDelta(cur.Latency.Max, base.Latency.Max),
		ErrorRateDiff: math.Round((cur.ErrorRate-base.ErrorRate)*1_000_000) / 1_000_000,
	}

	if len(base.Steps) > 0 && len(cur.Steps) > 0 {
		baseByID := make(map[string]*metrics.StepView, len(base.Steps))
		baseByName := make(map[string]*metrics.StepView, len(base.Steps))
		for i := range base.Steps {
			st := &base.Steps[i]
			if st.ID != "" {
				baseByID[st.ID] = st
			}
			if st.Name != "" {
				baseByName[st.Name] = st
			}
		}

		stepDeltas := make(map[string]StepDelta, len(cur.Steps))
		for i := range cur.Steps {
			cs := &cur.Steps[i]
			var bs *metrics.StepView
			if cs.ID != "" {
				bs = baseByID[cs.ID]
			}
			if bs == nil && cs.Name != "" {
				bs = baseByName[cs.Name]
			}
			if bs == nil {
				continue
			}
			key := cs.ID
			if key == "" {
				key = cs.Name
			}
			stepDeltas[key] = StepDelta{
				ID:            cs.ID,
				Name:          cs.Name,
				RPSPct:        pctDelta(cs.RPS, bs.RPS),
				P50Pct:        pctDelta(cs.Latency.P50, bs.Latency.P50),
				P95Pct:        pctDelta(cs.Latency.P95, bs.Latency.P95),
				P99Pct:        pctDelta(cs.Latency.P99, bs.Latency.P99),
				MaxPct:        pctDelta(cs.Latency.Max, bs.Latency.Max),
				ErrorRateDiff: math.Round((cs.ErrorRate-bs.ErrorRate)*1_000_000) / 1_000_000,
			}
		}
		if len(stepDeltas) > 0 {
			d.StepDeltas = stepDeltas
		}
	}
	return d
}

func pctDelta(cur, base float64) *float64 {
	if base == 0 || math.IsNaN(base) || math.IsInf(base, 0) || math.IsNaN(cur) || math.IsInf(cur, 0) {
		return nil
	}
	v := math.Round(((cur-base)/base)*10000) / 100
	return &v
}

func downsampleSnapshots(in []store.SnapshotRecord, maxPoints int) []store.SnapshotRecord {
	if maxPoints <= 1 || len(in) <= maxPoints {
		return in
	}
	out := make([]store.SnapshotRecord, maxPoints)
	last := len(in) - 1
	for i := 0; i < maxPoints; i++ {
		idx := i * last / (maxPoints - 1)
		out[i] = in[idx]
	}
	return out
}
