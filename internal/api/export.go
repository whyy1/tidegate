package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/store"
)

var exportCSVHeader = []string{
	"seq", "time", "elapsed_s", "duration_s", "active_vus", "active_conns", "target",
	"iterations", "dropped_iterations", "requests", "rps", "errors", "error_rate",
	"p50_ms", "p90_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms",
	"bytes_sent", "bytes_received", "sent_bps", "received_bps",
}

func (s *Server) handleRunExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	run, err := s.results.GetRun(ctx, id)
	if err != nil {
		writeError(w, err)
		return
	}
	snaps, err := s.results.Snapshots(ctx, id, 0, 0)
	if err != nil {
		writeError(w, err)
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}

	switch format {
	case "csv":
		s.exportRunCSV(w, run, snaps)
	case "json":
		s.exportRunJSON(w, r, run, snaps)
	default:
		writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "导出格式只支持 json 或 csv"))
	}
}

func (s *Server) exportRunJSON(w http.ResponseWriter, r *http.Request, run *store.Run, snaps []store.SnapshotRecord) {
	alerts, err := s.results.Alerts(r.Context(), run.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	samples, err := s.results.Samples(r.Context(), run.ID, store.SampleQuery{Limit: 1000})
	if err != nil {
		writeError(w, err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="tidegate-run-`+sanitizeHeaderValue(run.ID)+`.json"`)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(struct {
		Run       *store.Run             `json:"run"`
		Snapshots []store.SnapshotRecord `json:"snapshots"`
		Alerts    []store.Alert          `json:"alerts"`
		Samples   []store.SampleRecord   `json:"samples"`
	}{
		Run:       run,
		Snapshots: snaps,
		Alerts:    alerts,
		Samples:   samples,
	})
}

func (s *Server) exportRunCSV(w http.ResponseWriter, run *store.Run, snaps []store.SnapshotRecord) {
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="tidegate-run-`+sanitizeHeaderValue(run.ID)+`.csv"`)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	cw := csv.NewWriter(w)
	_ = cw.Write(exportCSVHeader)

	f := func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
	i64 := func(x int64) string { return strconv.FormatInt(x, 10) }
	round3 := func(x float64) float64 { return float64(int64(x*1000+0.5)) / 1000 }

	var elapsedMs float64
	for _, rec := range snaps {
		var sv metrics.SnapshotView
		if err := json.Unmarshal(rec.Data, &sv); err != nil {
			continue
		}
		elapsedMs += sv.DurationMs
		_ = cw.Write([]string{
			i64(sv.Seq),
			sv.Time.Format(time.RFC3339Nano),
			f(round3(elapsedMs / 1000)),
			f(round3(sv.DurationMs / 1000)),
			i64(sv.ActiveVUs),
			i64(sv.ActiveConns),
			f(sv.Target),
			i64(sv.Iterations),
			i64(sv.DroppedIterations),
			i64(sv.Requests),
			f(sv.RPS),
			i64(sv.Errors),
			f(sv.ErrorRate),
			f(sv.Latency.P50),
			f(sv.Latency.P90),
			f(sv.Latency.P95),
			f(sv.Latency.P99),
			f(sv.Latency.Max),
			f(sv.Latency.Mean),
			i64(sv.BytesSent),
			i64(sv.BytesReceived),
			f(sv.SentBps),
			f(sv.ReceivedBps),
		})
	}
	cw.Flush()
}

func sanitizeHeaderValue(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "export"
	}
	return string(out)
}
