package exporter

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"tidegate/internal/metrics"
)

// PrometheusAgent represents one online agent's latest telemetry for Prometheus exposition.
type PrometheusAgent struct {
	ID          string
	Name        string
	CPURatio    float64
	MemoryRatio float64
	UplinkBps   float64
	DownlinkBps float64
	RTTMs       float64
}

// PrometheusRun holds active run totals and the latest per-second snapshot view.
type PrometheusRun struct {
	Active            bool
	RunID             string
	ProjectID         string
	ScenarioName      string
	Snapshot          *metrics.SnapshotView
	RequestsTotal     int64
	ErrorsTotal       int64
	StepRequestsTotal map[string]int64
	StepErrorsTotal   map[string]int64
}

// PrometheusState is the complete controller telemetry rendered by WritePrometheus.
type PrometheusState struct {
	Agents []PrometheusAgent
	Run    PrometheusRun
}

// WritePrometheus writes cluster node and active run metrics in Prometheus text exposition format (0.0.4).
func WritePrometheus(w io.Writer, st PrometheusState) error {
	var b strings.Builder

	b.WriteString("# HELP tidegate_agents_online Number of online load generator agents.\n")
	b.WriteString("# TYPE tidegate_agents_online gauge\n")
	fmt.Fprintf(&b, "tidegate_agents_online %d\n", len(st.Agents))

	if len(st.Agents) > 0 {
		agents := append([]PrometheusAgent(nil), st.Agents...)
		sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })

		writeAgentMetric := func(name, help string, val func(PrometheusAgent) float64) {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
			for _, ag := range agents {
				fmt.Fprintf(&b, "%s{agent_id=\"%s\",agent_name=\"%s\"} %s\n",
					name, promEsc(ag.ID), promEsc(ag.Name), promFloat(val(ag)))
			}
		}
		writeAgentMetric("tidegate_agent_cpu_usage_ratio", "CPU utilization ratio (0-1) of the agent.", func(a PrometheusAgent) float64 { return a.CPURatio })
		writeAgentMetric("tidegate_agent_memory_usage_ratio", "Memory utilization ratio (0-1) of the agent.", func(a PrometheusAgent) float64 { return a.MemoryRatio })
		writeAgentMetric("tidegate_agent_uplink_bps", "Observed outbound bandwidth in bits per second.", func(a PrometheusAgent) float64 { return a.UplinkBps })
		writeAgentMetric("tidegate_agent_downlink_bps", "Observed inbound bandwidth in bits per second.", func(a PrometheusAgent) float64 { return a.DownlinkBps })
		writeAgentMetric("tidegate_agent_rtt_ms", "Heartbeat round-trip latency in milliseconds.", func(a PrometheusAgent) float64 { return a.RTTMs })
	}

	b.WriteString("# HELP tidegate_run_active Whether a load test run is currently active (1 or 0).\n")
	b.WriteString("# TYPE tidegate_run_active gauge\n")
	if !st.Run.Active {
		b.WriteString("tidegate_run_active 0\n")
		_, err := io.WriteString(w, b.String())
		return err
	}

	runLabels := fmt.Sprintf("run_id=\"%s\",scenario=\"%s\"", promEsc(st.Run.RunID), promEsc(st.Run.ScenarioName))
	fmt.Fprintf(&b, "tidegate_run_active{%s} 1\n", runLabels)

	fmt.Fprintf(&b, "# HELP tidegate_run_requests_total Cumulative requests completed in the active run.\n# TYPE tidegate_run_requests_total counter\ntidegate_run_requests_total{%s} %d\n", runLabels, st.Run.RequestsTotal)
	fmt.Fprintf(&b, "# HELP tidegate_run_errors_total Cumulative failed requests in the active run.\n# TYPE tidegate_run_errors_total counter\ntidegate_run_errors_total{%s} %d\n", runLabels, st.Run.ErrorsTotal)

	if sv := st.Run.Snapshot; sv != nil {
		writeRunGauge := func(name, help string, v float64) {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s{%s} %s\n", name, help, name, name, runLabels, promFloat(v))
		}
		writeRunGauge("tidegate_run_active_vus", "Current active virtual users.", float64(sv.ActiveVUs))
		writeRunGauge("tidegate_run_active_conns", "Current active TCP connections across all agents.", float64(sv.ActiveConns))
		writeRunGauge("tidegate_run_target", "Configured load target (VUs or target RPS).", sv.Target)
		writeRunGauge("tidegate_run_rps", "Aggregate requests per second in the latest 1s window.", sv.RPS)
		writeRunGauge("tidegate_run_error_rate", "Fraction of requests failing in the latest 1s window (0-1).", sv.ErrorRate)
		writeRunGauge("tidegate_run_sent_bps", "Wire uplink throughput in bits per second.", sv.SentBps)
		writeRunGauge("tidegate_run_received_bps", "Wire downlink throughput in bits per second.", sv.ReceivedBps)

		b.WriteString("# HELP tidegate_run_latency_ms Request latency quantiles in milliseconds.\n")
		b.WriteString("# TYPE tidegate_run_latency_ms gauge\n")
		fmt.Fprintf(&b, "tidegate_run_latency_ms{%s,quantile=\"0.5\"} %s\n", runLabels, promFloat(sv.Latency.P50))
		fmt.Fprintf(&b, "tidegate_run_latency_ms{%s,quantile=\"0.9\"} %s\n", runLabels, promFloat(sv.Latency.P90))
		fmt.Fprintf(&b, "tidegate_run_latency_ms{%s,quantile=\"0.95\"} %s\n", runLabels, promFloat(sv.Latency.P95))
		fmt.Fprintf(&b, "tidegate_run_latency_ms{%s,quantile=\"0.99\"} %s\n", runLabels, promFloat(sv.Latency.P99))
		fmt.Fprintf(&b, "tidegate_run_latency_ms{%s,quantile=\"1\"} %s\n", runLabels, promFloat(sv.Latency.Max))

		if len(sv.Steps) > 0 {
			b.WriteString("# HELP tidegate_step_rps Requests per second per step.\n# TYPE tidegate_step_rps gauge\n")
			for _, step := range sv.Steps {
				lbl := fmt.Sprintf("%s,step_id=\"%s\",step_name=\"%s\",is_group=\"%t\"",
					runLabels, promEsc(step.ID), promEsc(step.Name), step.IsGroup)
				fmt.Fprintf(&b, "tidegate_step_rps{%s} %s\n", lbl, promFloat(step.RPS))
			}
			b.WriteString("# HELP tidegate_step_error_rate Error rate (0-1) per step.\n# TYPE tidegate_step_error_rate gauge\n")
			for _, step := range sv.Steps {
				lbl := fmt.Sprintf("%s,step_id=\"%s\",step_name=\"%s\",is_group=\"%t\"",
					runLabels, promEsc(step.ID), promEsc(step.Name), step.IsGroup)
				fmt.Fprintf(&b, "tidegate_step_error_rate{%s} %s\n", lbl, promFloat(step.ErrorRate))
			}
			b.WriteString("# HELP tidegate_step_latency_p95_ms P95 latency per step in milliseconds.\n# TYPE tidegate_step_latency_p95_ms gauge\n")
			for _, step := range sv.Steps {
				lbl := fmt.Sprintf("%s,step_id=\"%s\",step_name=\"%s\",is_group=\"%t\"",
					runLabels, promEsc(step.ID), promEsc(step.Name), step.IsGroup)
				fmt.Fprintf(&b, "tidegate_step_latency_p95_ms{%s} %s\n", lbl, promFloat(step.Latency.P95))
			}
			b.WriteString("# HELP tidegate_step_requests_total Cumulative executions per step.\n# TYPE tidegate_step_requests_total counter\n")
			for _, step := range sv.Steps {
				total := step.Requests
				if c, ok := st.Run.StepRequestsTotal[step.ID]; ok {
					total = c
				}
				lbl := fmt.Sprintf("%s,step_id=\"%s\",step_name=\"%s\",is_group=\"%t\"",
					runLabels, promEsc(step.ID), promEsc(step.Name), step.IsGroup)
				fmt.Fprintf(&b, "tidegate_step_requests_total{%s} %d\n", lbl, total)
			}
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

func promEsc(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func promFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}
