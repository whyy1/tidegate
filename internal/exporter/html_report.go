package exporter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strconv"
	"strings"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/store"
)

type htmlReportPayload struct {
	Run       *store.Run             `json:"run"`
	Summary   *metrics.SnapshotView  `json:"summary,omitempty"`
	Snapshots []json.RawMessage      `json:"snapshots"`
	Alerts    []store.Alert          `json:"alerts"`
	Samples   []store.SampleRecord   `json:"samples"`
}

// ExportRunHTML writes a self-contained single-file Chinese HTML load test
// report with summary cards, bottleneck alert timeline, interactive Canvas
// charts, per-step metrics, and escaped failure/slow samples.
func ExportRunHTML(
	run *store.Run,
	snaps []store.SnapshotRecord,
	alerts []store.Alert,
	samples []store.SampleRecord,
	w io.Writer,
) error {
	var summary *metrics.SnapshotView
	if len(run.Summary) > 0 {
		var sv metrics.SnapshotView
		if err := json.Unmarshal(run.Summary, &sv); err == nil {
			summary = &sv
		}
	}

	snapMsgs := make([]json.RawMessage, 0, len(snaps))
	for _, s := range snaps {
		if len(s.Data) > 0 {
			snapMsgs = append(snapMsgs, s.Data)
		}
	}

	payloadJSON, err := json.Marshal(htmlReportPayload{
		Run:       run,
		Summary:   summary,
		Snapshots: snapMsgs,
		Alerts:    alerts,
		Samples:   redactSampleRecords(samples),
	})
	if err != nil {
		return err
	}
	safePayload := escapeScriptJSON(payloadJSON)

	var out bytes.Buffer
	out.WriteString(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Tidegate 压测报告 - `)
	out.WriteString(html.EscapeString(run.ScenarioName))
	out.WriteString(`</title>
<style>
  :root{--bg:#f8fafc;--card:#ffffff;--ink:#0f172a;--muted:#64748b;--border:#e2e8f0;--accent:#2563eb;--danger:#dc2626;--warn:#d97706;--ok:#16a34a}
  *{box-sizing:border-box}
  body{margin:0;padding:24px;font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;background:var(--bg);color:var(--ink);line-height:1.5}
  .wrap{max-width:1200px;margin:0 auto}
  .hdr{display:flex;flex-wrap:wrap;justify-content:space-between;align-items:flex-end;gap:12px;margin-bottom:20px}
  h1{margin:0;font-size:22px}
  .meta{color:var(--muted);font-size:13px}
  .badge{display:inline-block;padding:2px 10px;border-radius:999px;font-size:12px;font-weight:600;background:#e0f2fe;color:#0369a1}
  .badge.finished{background:#dcfce7;color:#15803d}
  .badge.failed{background:#fee2e2;color:#b91c1c}
  .alert-banner{background:#fef3c7;border:1px solid #f59e0b;border-left:5px solid #d97706;border-radius:8px;padding:12px 16px;margin-bottom:20px}
  .alert-banner h3{margin:0 0 6px;font-size:15px;color:#92400e}
  .alert-banner ul{margin:0;padding-left:20px;font-size:13px;color:#78350f}
  .grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(175px,1fr));gap:12px;margin-bottom:20px}
  .kpi{background:var(--card);border:1px solid var(--border);border-radius:8px;padding:14px}
  .kpi .lbl{font-size:12px;color:var(--muted);margin-bottom:4px}
  .kpi .val{font-size:20px;font-weight:700}
  .card{background:var(--card);border:1px solid var(--border);border-radius:8px;padding:16px;margin-bottom:18px}
  .card h2{margin:0 0 12px;font-size:16px}
  .charts{display:grid;grid-template-columns:repeat(auto-fit,minmax(500px,1fr));gap:16px;margin-bottom:18px}
  canvas{width:100%;height:220px;display:block}
  table{width:100%;border-collapse:collapse;font-size:13px}
  th,td{padding:8px 10px;border-bottom:1px solid var(--border);text-align:left}
  th{color:var(--muted);font-weight:600;background:#f8fafc}
  details{border:1px solid var(--border);border-radius:6px;padding:8px 12px;margin-bottom:8px;background:#fff}
  summary{cursor:pointer;font-size:13px;font-weight:500}
  pre{margin:8px 0 0;padding:10px;background:#0f172a;color:#e2e8f0;border-radius:6px;font-size:12px;overflow:auto;max-height:260px}
</style>
</head>
<body>
<div class="wrap">
  <div class="hdr">
    <div>
      <h1>`)
	out.WriteString(html.EscapeString(run.ScenarioName))
	out.WriteString(`</h1>
      <div class="meta">运行 ID：`)
	out.WriteString(html.EscapeString(run.ID))
	if !run.StartedAt.IsZero() {
		out.WriteString(` · 开始时间：`)
		out.WriteString(html.EscapeString(run.StartedAt.Local().Format("2006-01-02 15:04:05")))
	}
	if !run.FinishedAt.IsZero() && !run.StartedAt.IsZero() {
		out.WriteString(` · 持续：`)
		out.WriteString(html.EscapeString(run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond).String()))
	}
	out.WriteString(`</div>
    </div>
    <span class="badge `)
	out.WriteString(html.EscapeString(string(run.Status)))
	out.WriteString(`">`)
	out.WriteString(html.EscapeString(statusLabel(run.Status)))
	out.WriteString(`</span>
  </div>
`)

	if len(alerts) > 0 {
		out.WriteString(`  <div class="alert-banner">
    <h3>⚠ 发压机在本次压测期间曾出现资源瓶颈，结果可能低估了被测服务的能力</h3>
    <ul>
`)
		for _, a := range alerts {
			out.WriteString(`      <li>`)
			if a.Agent != "" {
				out.WriteString(`[` + html.EscapeString(a.Agent) + `] `)
			}
			out.WriteString(html.EscapeString(a.Message))
			out.WriteString(`（` + html.EscapeString(a.StartedAt.Local().Format("15:04:05")))
			if !a.EndedAt.IsZero() {
				out.WriteString(` ~ ` + html.EscapeString(a.EndedAt.Local().Format("15:04:05")))
			}
			out.WriteString("）</li>\n")
		}
		out.WriteString(`    </ul>
  </div>
`)
	}

	if summary != nil {
		out.WriteString(`  <div class="grid">
    <div class="kpi"><div class="lbl">总请求数</div><div class="val">` + formatInt64(summary.Requests) + `</div></div>
    <div class="kpi"><div class="lbl">平均 RPS</div><div class="val">` + fmt.Sprintf("%.1f/s", summary.RPS) + `</div></div>
    <div class="kpi"><div class="lbl">错误率</div><div class="val">` + fmt.Sprintf("%.2f%% (%d)", summary.ErrorRate*100, summary.Errors) + `</div></div>
    <div class="kpi"><div class="lbl">P50 / P95 / P99</div><div class="val">` + fmt.Sprintf("%.1f / %.1f / %.1f ms", summary.Latency.P50, summary.Latency.P95, summary.Latency.P99) + `</div></div>
    <div class="kpi"><div class="lbl">峰值并发 / 连接</div><div class="val">` + fmt.Sprintf("%d VU / %d", summary.ActiveVUs, summary.ActiveConns) + `</div></div>
    <div class="kpi"><div class="lbl">总流量（上/下行）</div><div class="val">` + fmt.Sprintf("%s / %s", formatBytesIEC(summary.BytesSent), formatBytesIEC(summary.BytesReceived)) + `</div></div>
  </div>
`)
	}

	out.WriteString(`  <div class="charts">
    <div class="card"><h2>吞吐量与错误 (req/s)</h2><canvas id="c-rps"></canvas></div>
    <div class="card"><h2>延迟百分位 (ms)</h2><canvas id="c-lat"></canvas></div>
    <div class="card"><h2>上下行带宽 (Mbps)</h2><canvas id="c-bw"></canvas></div>
    <div class="card"><h2>活跃并发与连接数</h2><canvas id="c-vus"></canvas></div>
  </div>
`)

	if summary != nil && len(summary.Steps) > 0 {
		out.WriteString(`  <div class="card">
    <h2>按步骤统计</h2>
    <table>
      <thead><tr><th>步骤</th><th>请求数</th><th>RPS</th><th>错误率</th><th>P50 (ms)</th><th>P95 (ms)</th><th>P99 (ms)</th><th>最大 (ms)</th></tr></thead>
      <tbody>
`)
		for _, st := range summary.Steps {
			label := st.Name
			if label == "" {
				label = st.ID
			}
			out.WriteString(`        <tr><td>` + html.EscapeString(label) + `</td><td>` + formatInt64(st.Requests) + `</td><td>` +
				fmt.Sprintf("%.1f", st.RPS) + `</td><td>` + fmt.Sprintf("%.2f%%", st.ErrorRate*100) + `</td><td>` +
				fmt.Sprintf("%.2f", st.Latency.P50) + `</td><td>` + fmt.Sprintf("%.2f", st.Latency.P95) + `</td><td>` +
				fmt.Sprintf("%.2f", st.Latency.P99) + `</td><td>` + fmt.Sprintf("%.2f", st.Latency.Max) + "</td></tr>\n")
		}
		out.WriteString(`      </tbody>
    </table>
  </div>
`)
	}

	if len(samples) > 0 {
		out.WriteString(`  <div class="card">
    <h2>错误与慢请求采样（最多展示 `)
		out.WriteString(strconv.Itoa(len(samples)))
		out.WriteString(` 条，已脱敏鉴权头）</h2>
    <div id="samples-root"></div>
  </div>
`)
	}

	out.WriteString(`</div>
<script type="application/json" id="tidegate-report-data">`)
	out.Write(safePayload)
	out.WriteString(`</script>
<script>
(function(){
  const raw = document.getElementById('tidegate-report-data').textContent;
  const data = JSON.parse(raw);
  const snaps = data.snapshots || [];
  const alerts = data.alerts || [];

  function drawChart(canvasId, series) {
    const canvas = document.getElementById(canvasId);
    if (!canvas || snaps.length === 0) return;
    const dpr = window.devicePixelRatio || 1;
    const w = canvas.clientWidth || 520;
    const h = canvas.clientHeight || 220;
    canvas.width = w * dpr;
    canvas.height = h * dpr;
    const ctx = canvas.getContext('2d');
    ctx.scale(dpr, dpr);

    const padL = 52, padR = 14, padT = 22, padB = 26;
    const pw = w - padL - padR, ph = h - padT - padB;
    let maxY = 0;
    for (const s of series) {
      for (const v of s.vals) { if (v > maxY) maxY = v; }
    }
    if (maxY <= 0) maxY = 1;
    maxY *= 1.1;

    ctx.fillStyle = '#ffffff';
    ctx.fillRect(0, 0, w, h);

    // Highlight bottleneck alert windows when timestamps fall within snapshot span.
    const t0 = new Date(snaps[0].time).getTime();
    const t1 = snaps.length > 1 ? new Date(snaps[snaps.length - 1].time).getTime() : t0 + 1000;
    if (alerts.length > 0 && t1 > t0) {
      ctx.fillStyle = 'rgba(245, 158, 11, 0.14)';
      for (const a of alerts) {
        const as = Math.max(t0, new Date(a.started_at).getTime());
        const ae = a.ended_at ? Math.min(t1, new Date(a.ended_at).getTime()) : t1;
        if (ae >= as) {
          const x1 = padL + ((as - t0) / (t1 - t0)) * pw;
          const x2 = padL + ((ae - t0) / (t1 - t0)) * pw;
          ctx.fillRect(x1, padT, Math.max(2, x2 - x1), ph);
        }
      }
    }

    ctx.strokeStyle = '#e2e8f0';
    ctx.lineWidth = 1;
    ctx.font = '11px system-ui,sans-serif';
    ctx.fillStyle = '#64748b';
    for (let i = 0; i <= 4; i++) {
      const y = padT + (ph * i) / 4;
      ctx.beginPath(); ctx.moveTo(padL, y); ctx.lineTo(padL + pw, y); ctx.stroke();
      const label = (maxY * (1 - i / 4)).toFixed(maxY < 10 ? 2 : 0);
      ctx.fillText(label, 4, y + 4);
    }

    series.forEach((s, idx) => {
      ctx.strokeStyle = s.color;
      ctx.lineWidth = 2;
      ctx.beginPath();
      s.vals.forEach((v, i) => {
        const x = snaps.length === 1 ? padL + pw / 2 : padL + (i / (snaps.length - 1)) * pw;
        const y = padT + ph - (v / maxY) * ph;
        if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
      });
      ctx.stroke();

      ctx.fillStyle = s.color;
      ctx.fillRect(padL + idx * 110, 6, 10, 10);
      ctx.fillStyle = '#334155';
      ctx.fillText(s.label, padL + idx * 110 + 14, 15);
    });
  }

  drawChart('c-rps', [
    { label: 'RPS', color: '#2563eb', vals: snaps.map(s => s.rps || 0) },
    { label: '错误/s', color: '#dc2626', vals: snaps.map(s => s.errors || 0) }
  ]);
  drawChart('c-lat', [
    { label: 'P50', color: '#16a34a', vals: snaps.map(s => s.latency ? s.latency.p50_ms : 0) },
    { label: 'P95', color: '#d97706', vals: snaps.map(s => s.latency ? s.latency.p95_ms : 0) },
    { label: 'P99', color: '#dc2626', vals: snaps.map(s => s.latency ? s.latency.p99_ms : 0) }
  ]);
  drawChart('c-bw', [
    { label: '上行 Mbps', color: '#0ea5e9', vals: snaps.map(s => (s.sent_bps || 0) / 1e6) },
    { label: '下行 Mbps', color: '#8b5cf6', vals: snaps.map(s => (s.received_bps || 0) / 1e6) }
  ]);
  drawChart('c-vus', [
    { label: '活跃 VU', color: '#0f766e', vals: snaps.map(s => s.active_vus || 0) },
    { label: '连接数', color: '#64748b', vals: snaps.map(s => s.active_conns || 0) }
  ]);

  const sRoot = document.getElementById('samples-root');
  if (sRoot && Array.isArray(data.samples)) {
    for (const rec of data.samples) {
      const sm = rec.data || {};
      const det = document.createElement('details');
      const sum = document.createElement('summary');
      sum.textContent = '[' + (sm.kind || rec.kind) + '] ' + (sm.method || '') + ' ' + (sm.url || '') + ' · 状态 ' + (sm.status || '-') + ' · ' + (sm.duration_ms || 0) + 'ms ' + (sm.error || '');
      const pre = document.createElement('pre');
      pre.textContent = JSON.stringify(sm, null, 2);
      det.appendChild(sum);
      det.appendChild(pre);
      sRoot.appendChild(det);
    }
  }
})();
</script>
</body>
</html>`)

	_, err = io.Copy(w, &out)
	return err
}

func redactSampleRecords(in []store.SampleRecord) []store.SampleRecord {
	if len(in) == 0 {
		return in
	}
	out := make([]store.SampleRecord, len(in))
	for i, rec := range in {
		if len(rec.Data) == 0 {
			out[i] = rec
			continue
		}
		var sv metrics.SampleView
		if err := json.Unmarshal(rec.Data, &sv); err != nil {
			out[i] = rec
			continue
		}
		redactHeaderList(sv.RequestHeaders)
		redactHeaderList(sv.ResponseHeaders)
		if b, err := json.Marshal(&sv); err == nil {
			rec.Data = b
		}
		out[i] = rec
	}
	return out
}

func redactHeaderList(hdrs []metrics.Header) {
	for i := range hdrs {
		if isSensitiveHeaderName(hdrs[i].Name) && hdrs[i].Value != "" {
			hdrs[i].Value = "[REDACTED]"
		}
	}
}

func escapeScriptJSON(b []byte) []byte {
	r := strings.NewReplacer(
		"<", `\u003c`,
		">", `\u003e`,
		"&", `\u0026`,
		"\u2028", `\u2028`,
		"\u2029", `\u2029`,
	)
	return []byte(r.Replace(string(b)))
}

func statusLabel(s store.RunStatus) string {
	switch s {
	case store.RunPreparing:
		return "准备中"
	case store.RunRunning:
		return "运行中"
	case store.RunStopping:
		return "停止中"
	case store.RunFinished:
		return "已完成"
	case store.RunFailed:
		return "失败"
	}
	return string(s)
}

func formatInt64(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func formatBytesIEC(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
