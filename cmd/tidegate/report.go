package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
)

// displayWidth counts East Asian wide characters as two columns.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r >= 0x1100 && (r <= 0x115f || r >= 0x2e80) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func padLeft(s string, width int) string {
	if n := width - displayWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

func padRight(s string, width int) string {
	if n := width - displayWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// formatInt adds thousands separators.
func formatInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func formatMs(ms float64) string {
	switch {
	case ms <= 0:
		return "-"
	case ms < 10:
		return strconv.FormatFloat(ms, 'f', 2, 64) + "ms"
	case ms < 1000:
		return strconv.FormatFloat(ms, 'f', 1, 64) + "ms"
	default:
		return strconv.FormatFloat(ms/1000, 'f', 2, 64) + "s"
	}
}

func formatBits(bps float64) string {
	switch {
	case bps >= 1e9:
		return strconv.FormatFloat(bps/1e9, 'f', 2, 64) + " Gbps"
	case bps >= 1e6:
		return strconv.FormatFloat(bps/1e6, 'f', 1, 64) + " Mbps"
	case bps >= 1e3:
		return strconv.FormatFloat(bps/1e3, 'f', 1, 64) + " Kbps"
	}
	return strconv.FormatFloat(bps, 'f', 0, 64) + " bps"
}

func formatBytes(n int64) string {
	f := float64(n)
	switch {
	case f >= 1<<30:
		return strconv.FormatFloat(f/(1<<30), 'f', 2, 64) + " GB"
	case f >= 1<<20:
		return strconv.FormatFloat(f/(1<<20), 'f', 1, 64) + " MB"
	case f >= 1<<10:
		return strconv.FormatFloat(f/(1<<10), 'f', 1, 64) + " KB"
	}
	return strconv.FormatInt(n, 10) + " B"
}

func formatPct(errors, total int64) string {
	if total == 0 {
		return "0.00%"
	}
	return strconv.FormatFloat(float64(errors)*100/float64(total), 'f', 2, 64) + "%"
}

func rate(n int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / d.Seconds()
}

func printHeader(w io.Writer, p *scenario.Plan, path, client string) {
	mode := "vus（闭环，目标为并发虚拟用户数）"
	if p.Load.Mode == scenario.LoadRate {
		mode = fmt.Sprintf("rate（开环，目标为每秒迭代数，最多 %d 个虚拟用户）", p.Load.MaxVUs)
	}
	fmt.Fprintf(w, "场景：%s（%s）\n", p.Name, path)
	fmt.Fprintf(w, "模式：%s\n", mode)
	var stages []string
	for _, s := range p.Load.Stages {
		stages = append(stages, fmt.Sprintf("%v→%s", s.Duration, strconv.FormatFloat(s.Target, 'f', -1, 64)))
	}
	fmt.Fprintf(w, "阶段：%s，共 %v；连接：%s；客户端：%s\n", strings.Join(stages, "，"), p.Load.TotalDuration, p.HTTP.ConnMode, client)
	if p.Load.MaxRequests > 0 {
		fmt.Fprintf(w, "请求数上限：%s\n", formatInt(p.Load.MaxRequests))
	}
	fmt.Fprintln(w)
}

var secondCols = []struct {
	title string
	width int
}{{"时间", 7}, {"并发", 7}, {"请求/秒", 10}, {"P50", 9}, {"P95", 9}, {"P99", 9}, {"错误率", 8}, {"上行", 12}, {"下行", 12}}

func printColumns(w io.Writer, mode scenario.LoadMode) {
	var b strings.Builder
	for _, c := range secondCols {
		b.WriteString(padLeft(c.title, c.width))
	}
	if mode == scenario.LoadRate {
		b.WriteString(padLeft("丢弃", 8))
	}
	fmt.Fprintln(w, b.String())
}

func printSecond(w io.Writer, s *metrics.Snapshot, elapsed time.Duration) {
	t := &s.Total
	lat := t.Latency.Stats()
	at := strconv.FormatFloat(elapsed.Seconds(), 'f', 0, 64) + "s"
	if s.Duration < 900*time.Millisecond || s.Duration > 1100*time.Millisecond {
		at = strconv.FormatFloat(elapsed.Seconds(), 'f', 1, 64) + "s"
	}
	cols := []string{
		at,
		formatInt(s.ActiveVUs),
		formatInt(int64(rate(t.Requests, s.Duration) + 0.5)),
		formatMs(lat.P50),
		formatMs(lat.P95),
		formatMs(lat.P99),
		formatPct(t.ErrorCount(), t.Requests),
		formatBits(rate(t.BytesSent*8, s.Duration)),
		formatBits(rate(t.BytesReceived*8, s.Duration)),
	}
	var b strings.Builder
	for i, c := range cols {
		b.WriteString(padLeft(c, secondCols[i].width))
	}
	if s.SchedDelay != nil {
		b.WriteString(padLeft(formatInt(s.DroppedIterations), 8))
	}
	fmt.Fprintln(w, b.String())
}

func latencyLine(st metrics.LatencyStats) string {
	return fmt.Sprintf("P50 %s  P90 %s  P95 %s  P99 %s  最大 %s  平均 %s",
		formatMs(st.P50), formatMs(st.P90), formatMs(st.P95), formatMs(st.P99), formatMs(st.Max), formatMs(st.Mean))
}

func errorKinds(s *metrics.StepStats) string {
	var parts []string
	for k, n := range s.Errors {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s %s", metrics.ErrorKind(k), formatInt(n)))
		}
	}
	return strings.Join(parts, "，")
}

func printSummary(w io.Writer, p *scenario.Plan, s *metrics.Snapshot) {
	t := &s.Total
	d := s.Duration
	line := func(label, value string) { fmt.Fprintf(w, "  %s%s\n", padRight(label, 10), value) }
	fmt.Fprintln(w)
	fmt.Fprintln(w, "========== 汇总 ==========")
	line("场景", p.Name)
	line("时长", strconv.FormatFloat(d.Seconds(), 'f', 2, 64)+"s")
	line("并发峰值", fmt.Sprintf("%s 个虚拟用户，%s 个连接", formatInt(s.ActiveVUs), formatInt(s.ActiveConns)))
	it := formatInt(s.Iterations)
	if p.Load.Mode == scenario.LoadRate {
		it += fmt.Sprintf("（丢弃 %s）", formatInt(s.DroppedIterations))
	}
	line("完成迭代", it)
	line("请求", fmt.Sprintf("%s（平均 %s/秒）", formatInt(t.Requests), formatInt(int64(rate(t.Requests, d)+0.5))))
	errLine := fmt.Sprintf("%s（%s）", formatInt(t.ErrorCount()), formatPct(t.ErrorCount(), t.Requests))
	if k := errorKinds(t); k != "" {
		errLine += "：" + k
	}
	line("错误", errLine)
	var codes []string
	for _, c := range t.SortedStatus() {
		codes = append(codes, fmt.Sprintf("%d×%s", c, formatInt(t.Status[c])))
	}
	if len(codes) > 0 {
		line("状态码", strings.Join(codes, "  "))
	}
	line("延迟", latencyLine(t.Latency.Stats()))
	line("流量", fmt.Sprintf("上行 %s（%s）  下行 %s（%s）",
		formatBytes(t.BytesSent), formatBits(rate(t.BytesSent*8, d)),
		formatBytes(t.BytesReceived), formatBits(rate(t.BytesReceived*8, d))))
	if s.OverheadSent > 0 || s.OverheadReceived > 0 {
		line("公共开销", fmt.Sprintf("上行 %s  下行 %s（TLS 握手、重试等，不计入步骤）", formatBytes(s.OverheadSent), formatBytes(s.OverheadReceived)))
	}
	if s.SchedDelay != nil && s.SchedDelay.Count() > 0 {
		line("调度延迟", latencyLine(s.SchedDelay.Stats()))
	}
	var phases []string
	for ph := metrics.Phase(0); ph < metrics.NumPhases; ph++ {
		if n := t.PhaseCount[ph]; n > 0 {
			avg := float64(t.PhaseSum[ph]) / float64(n) / 1e6
			phases = append(phases, fmt.Sprintf("%s %s×%s", phaseName(ph), formatMs(avg), formatInt(n)))
		}
	}
	if len(phases) > 0 {
		line("耗时分解", strings.Join(phases, "  "))
	}

	{
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  按步骤：")
		cols := []struct {
			t string
			w int
		}{{"步骤", 22}, {"请求数", 12}, {"每秒", 10}, {"错误率", 9}, {"P50", 9}, {"P95", 9}, {"P99", 9}, {"最大", 9}}
		var b strings.Builder
		b.WriteString("  ")
		for i, c := range cols {
			if i == 0 {
				b.WriteString(padRight(c.t, c.w))
			} else {
				b.WriteString(padLeft(c.t, c.w))
			}
		}
		fmt.Fprintln(w, b.String())
		for i := range s.Steps {
			st := &s.Steps[i]
			lat := st.Latency.Stats()
			name := st.ID
			if st.Name != "" && st.Name != st.ID {
				name += " " + st.Name
			}
			if displayWidth(name) > 21 {
				name = truncateDisplay(name, 21)
			}
			vals := []string{name, formatInt(st.Requests), formatInt(int64(rate(st.Requests, d) + 0.5)),
				formatPct(st.ErrorCount(), st.Requests), formatMs(lat.P50), formatMs(lat.P95), formatMs(lat.P99), formatMs(lat.Max)}
			b.Reset()
			b.WriteString("  ")
			for j, v := range vals {
				if j == 0 {
					b.WriteString(padRight(v, cols[j].w))
				} else {
					b.WriteString(padLeft(v, cols[j].w))
				}
			}
			if k := errorKinds(st); k != "" {
				b.WriteString("   " + k)
			}
			fmt.Fprintln(w, b.String())
		}
	}

	if len(s.Samples) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  样例（每类最多显示 2 条，完整内容见 --out 文件）：")
		var shown [metrics.NumSampleKinds]int
		for _, smp := range s.Samples {
			if shown[smp.Kind] >= 2 {
				continue
			}
			shown[smp.Kind]++
			status := "-"
			if smp.Status > 0 {
				status = strconv.Itoa(smp.Status)
			}
			msg := smp.Error
			if msg == "" {
				msg = "慢请求"
			}
			fmt.Fprintf(w, "    [%s] 步骤 %s  %s %s  状态 %s  耗时 %s  %s\n", smp.Kind, smp.StepID, smp.Method,
				truncateDisplay(smp.URL, 60), status, formatMs(float64(smp.Duration)/1e6), truncateDisplay(msg, 120))
		}
	}
	fmt.Fprintln(w)
}

func phaseName(p metrics.Phase) string {
	switch p {
	case metrics.PhaseDNS:
		return "DNS"
	case metrics.PhaseConnect:
		return "建连"
	case metrics.PhaseTLS:
		return "TLS"
	case metrics.PhaseFirstByte:
		return "首字节"
	case metrics.PhaseDownload:
		return "下载"
	}
	return p.String()
}

func truncateDisplay(s string, width int) string {
	if displayWidth(s) <= width {
		return s
	}
	w := 0
	for i, r := range s {
		rw := displayWidth(string(r))
		if w+rw > width-1 {
			return s[:i] + "…"
		}
		w += rw
	}
	return s
}

// csvWriter writes one row of aggregated metrics per second.
type csvWriter struct {
	bw *bufio.Writer
	w  *csv.Writer
}

var csvHeader = []string{
	"seq", "time", "elapsed_s", "duration_s", "active_vus", "active_conns", "target",
	"iterations", "dropped_iterations", "requests", "rps", "errors", "error_rate",
	"p50_ms", "p90_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms",
	"bytes_sent", "bytes_received", "sent_bps", "received_bps",
}

func newCSVWriter(f io.Writer) *csvWriter {
	bw := bufio.NewWriter(f)
	c := &csvWriter{bw: bw, w: csv.NewWriter(bw)}
	c.w.Write(csvHeader)
	return c
}

func (c *csvWriter) write(s *metrics.Snapshot, v *metrics.SnapshotView, elapsed time.Duration) {
	f := func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
	i := func(x int64) string { return strconv.FormatInt(x, 10) }
	c.w.Write([]string{
		i(s.Seq), s.Time.Format(time.RFC3339Nano), f(round3(elapsed.Seconds())), f(round3(s.Duration.Seconds())),
		i(s.ActiveVUs), i(s.ActiveConns), f(v.Target), i(s.Iterations), i(s.DroppedIterations),
		i(v.Requests), f(v.RPS), i(v.Errors), f(v.ErrorRate),
		f(v.Latency.P50), f(v.Latency.P90), f(v.Latency.P95), f(v.Latency.P99), f(v.Latency.Max), f(v.Latency.Mean),
		i(v.BytesSent), i(v.BytesReceived), f(v.SentBps), f(v.ReceivedBps),
	})
}

func (c *csvWriter) flush() error {
	c.w.Flush()
	if err := c.w.Error(); err != nil {
		return err
	}
	return c.bw.Flush()
}

func round3(x float64) float64 { return float64(int64(x*1000+0.5)) / 1000 }
