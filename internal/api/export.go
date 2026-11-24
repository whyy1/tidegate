package api

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tidegate/internal/exporter"
	"tidegate/internal/metrics"
	"tidegate/internal/store"
)

var exportCSVHeader = []string{
	"seq", "time", "elapsed_s", "duration_s", "active_vus", "active_conns", "target",
	"iterations", "dropped_iterations", "requests", "rps", "errors", "error_rate",
	"p50_ms", "p90_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms",
	"bytes_sent", "bytes_received", "sent_bps", "received_bps",
}

func (s *Server) handleExportProject(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	pr, err := s.store.GetProject(pid)
	if err != nil {
		writeError(w, err)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "tidegate"
	}

	stem := sanitizeHeaderValue(pr.ID)
	h := w.Header()
	switch format {
	case "tidegate":
		h.Set("Content-Type", "application/zip")
		h.Set("Content-Disposition", `attachment; filename="`+stem+`.tidegate"`)
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if err := exporter.ExportArchive(s.store, pid, w); err != nil {
			s.log.Warn("导出项目归档中断", "project_id", pid, "err", err)
		}
	case "openapi-json":
		data, err := exporter.ExportOpenAPI(s.store, pid, false)
		if err != nil {
			writeError(w, err)
			return
		}
		h.Set("Content-Type", "application/json; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="`+stem+`-openapi.json"`)
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case "openapi-yaml", "openapi":
		data, err := exporter.ExportOpenAPI(s.store, pid, true)
		if err != nil {
			writeError(w, err)
			return
		}
		h.Set("Content-Type", "application/yaml; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="`+stem+`-openapi.yaml"`)
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case "postman":
		data, err := exporter.ExportPostman(s.store, pid)
		if err != nil {
			writeError(w, err)
			return
		}
		h.Set("Content-Type", "application/json; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="`+stem+`-postman.json"`)
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case "k6":
		sid := strings.TrimSpace(r.URL.Query().Get("scenario_id"))
		if sid == "" {
			scens, lerr := s.store.ListScenarios(pid)
			if lerr != nil {
				writeError(w, lerr)
				return
			}
			if len(scens) == 0 {
				writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "项目中没有可导出为 k6 脚本的场景"))
				return
			}
			sid = scens[0].ID
		}
		script, err := exporter.ExportScenarioK6(s.store, pid, sid, store.ResolveOptions{
			EnvironmentID: r.URL.Query().Get("env_id"),
		})
		if err != nil {
			writeError(w, err)
			return
		}
		h.Set("Content-Type", "application/javascript; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="`+stem+`-`+sanitizeHeaderValue(sid)+`-k6.js"`)
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(script)
	default:
		writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "导出格式只支持 tidegate、openapi-json、openapi-yaml、postman 或 k6"))
	}
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
	case "html":
		s.exportRunHTML(w, r, run, snaps)
	case "har":
		s.exportRunHAR(w, r, run)
	case "jtl":
		s.exportRunJTL(w, r, run, snaps)
	default:
		writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "导出格式只支持 json、csv、html、har 或 jtl"))
	}
}

func (s *Server) handleExportScenario(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	sid := r.PathValue("id")
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "yaml"
	}
	opts := store.ResolveOptions{
		EnvironmentID: r.URL.Query().Get("env_id"),
	}
	stem := sanitizeHeaderValue(sid)
	h := w.Header()

	switch format {
	case "yaml", "tidegate-yaml":
		data, err := exporter.ExportScenarioNative(s.store, pid, sid, opts, true)
		if err != nil {
			writeError(w, err)
			return
		}
		h.Set("Content-Type", "application/yaml; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="tidegate-scenario-`+stem+`.yaml"`)
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case "json":
		data, err := exporter.ExportScenarioNative(s.store, pid, sid, opts, false)
		if err != nil {
			writeError(w, err)
			return
		}
		h.Set("Content-Type", "application/json; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="tidegate-scenario-`+stem+`.json"`)
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case "k6":
		script, err := exporter.ExportScenarioK6(s.store, pid, sid, opts)
		if err != nil {
			writeError(w, err)
			return
		}
		h.Set("Content-Type", "application/javascript; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="tidegate-scenario-`+stem+`-k6.js"`)
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(script)
	default:
		writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "场景导出格式只支持 yaml、json 或 k6"))
	}
}

func (s *Server) exportRunJTL(w http.ResponseWriter, r *http.Request, run *store.Run, snaps []store.SnapshotRecord) {
	samples, err := s.results.Samples(r.Context(), run.ID, store.SampleQuery{Limit: 1000})
	if err != nil {
		writeError(w, err)
		return
	}
	var buf bytes.Buffer
	if err := exporter.ExportRunJTL(run, snaps, samples, &buf); err != nil {
		writeError(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="tidegate-run-`+sanitizeHeaderValue(run.ID)+`.jtl"`)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) exportRunHAR(w http.ResponseWriter, r *http.Request, run *store.Run) {
	samples, err := s.results.Samples(r.Context(), run.ID, store.SampleQuery{Limit: 1000})
	if err != nil {
		writeError(w, err)
		return
	}
	data, err := exporter.ExportSamplesHAR(run, samples)
	if err != nil {
		writeError(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="tidegate-run-`+sanitizeHeaderValue(run.ID)+`-samples.har"`)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) exportRunHTML(w http.ResponseWriter, r *http.Request, run *store.Run, snaps []store.SnapshotRecord) {
	alerts, err := s.results.Alerts(r.Context(), run.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	samples, err := s.results.Samples(r.Context(), run.ID, store.SampleQuery{Limit: 200})
	if err != nil {
		writeError(w, err)
		return
	}
	var buf bytes.Buffer
	if err := exporter.ExportRunHTML(run, snaps, alerts, samples, &buf); err != nil {
		writeError(w, err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="tidegate-run-`+sanitizeHeaderValue(run.ID)+`.html"`)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
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
