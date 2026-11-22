package scenario

import (
	"fmt"
	"strings"
	"time"

	"tidegate/internal/metrics"
)

const (
	defaultThresholdGracePeriod  = 3 * time.Second
	minSamplesForAbortThreshold  = 5
)

// Threshold defines a pass/fail SLA target checked on run completion and, when
// AbortOnFail is true, continuously per second after GracePeriod elapses.
type Threshold struct {
	Metric      string   `yaml:"metric" json:"metric"`
	Op          string   `yaml:"op,omitempty" json:"op,omitempty"`
	Value       float64  `yaml:"value" json:"value"`
	Step        string   `yaml:"step,omitempty" json:"step,omitempty"`
	AbortOnFail bool     `yaml:"abort_on_fail,omitempty" json:"abort_on_fail,omitempty"`
	GracePeriod Duration `yaml:"grace_period,omitempty" json:"grace_period,omitempty"`
}

// ThresholdResult describes the evaluation outcome of one Threshold against a
// metrics SnapshotView.
type ThresholdResult struct {
	Metric      string  `json:"metric"`
	Op          string  `json:"op"`
	Value       float64 `json:"value"`
	Step        string  `json:"step,omitempty"`
	AbortOnFail bool    `json:"abort_on_fail,omitempty"`
	Actual      float64 `json:"actual"`
	Passed      bool    `json:"passed"`
	Message     string  `json:"message"`
}

func normalizeThresholdMetric(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "p50", "p50_ms":
		return "p50_ms", true
	case "p90", "p90_ms":
		return "p90_ms", true
	case "p95", "p95_ms":
		return "p95_ms", true
	case "p99", "p99_ms":
		return "p99_ms", true
	case "max", "max_ms":
		return "max_ms", true
	case "mean", "mean_ms", "avg", "avg_ms":
		return "mean_ms", true
	case "error_rate":
		return "error_rate", true
	case "error_rate_pct", "error_pct":
		return "error_rate_pct", true
	case "rps":
		return "rps", true
	default:
		return "", false
	}
}

func normalizeThresholdOp(rawOp, metric string) (string, bool) {
	op := strings.TrimSpace(rawOp)
	if op == "" {
		if metric == "rps" {
			return ">=", true
		}
		return "<=", true
	}
	switch op {
	case "<", "<=", ">", ">=":
		return op, true
	default:
		return "", false
	}
}

// EvaluateThresholds checks every configured threshold against view (overall
// StepView or the named step) and reports whether all thresholds passed.
func EvaluateThresholds(ts []Threshold, view *metrics.SnapshotView) ([]ThresholdResult, bool) {
	if len(ts) == 0 {
		return nil, true
	}
	results := make([]ThresholdResult, 0, len(ts))
	allPassed := true
	for _, raw := range ts {
		metric, ok := normalizeThresholdMetric(raw.Metric)
		if !ok {
			metric = strings.TrimSpace(raw.Metric)
		}
		op, opOK := normalizeThresholdOp(raw.Op, metric)
		if !opOK {
			op = raw.Op
		}
		res := ThresholdResult{
			Metric:      metric,
			Op:          op,
			Value:       raw.Value,
			Step:        strings.TrimSpace(raw.Step),
			AbortOnFail: raw.AbortOnFail,
		}
		sv, found := resolveThresholdTarget(view, res.Step)
		if !found {
			allPassed = false
			res.Passed = false
			if res.Step == "" {
				res.Message = "无可用指标数据，无法评估阈值"
			} else {
				res.Message = fmt.Sprintf("未找到步骤 %q 的指标数据", res.Step)
			}
			results = append(results, res)
			continue
		}

		actual := extractThresholdMetric(sv, metric)
		res.Actual = actual
		res.Passed = compareThreshold(actual, op, raw.Value)
		if !res.Passed {
			allPassed = false
		}
		res.Message = formatThresholdMessage(res)
		results = append(results, res)
	}
	return results, allPassed
}

// CheckAbortThresholds checks per-second intermediate metrics during a run and
// returns a failure message when any AbortOnFail threshold fails after its
// grace period and minimum sample count have been met.
func CheckAbortThresholds(ts []Threshold, view *metrics.SnapshotView, elapsed time.Duration) (string, bool) {
	if view == nil || len(ts) == 0 {
		return "", false
	}
	for _, raw := range ts {
		if !raw.AbortOnFail {
			continue
		}
		grace := raw.GracePeriod.D()
		if grace <= 0 {
			grace = defaultThresholdGracePeriod
		}
		if elapsed < grace {
			continue
		}
		sv, found := resolveThresholdTarget(view, strings.TrimSpace(raw.Step))
		if !found || sv.Requests < minSamplesForAbortThreshold {
			continue
		}
		single, passed := EvaluateThresholds([]Threshold{raw}, view)
		if !passed && len(single) == 1 {
			return single[0].Message, true
		}
	}
	return "", false
}

func resolveThresholdTarget(view *metrics.SnapshotView, step string) (*metrics.StepView, bool) {
	if view == nil {
		return nil, false
	}
	if step == "" {
		sv := view.StepView
		return &sv, true
	}
	for i := range view.Steps {
		if view.Steps[i].ID == step || view.Steps[i].Name == step {
			sv := view.Steps[i]
			return &sv, true
		}
	}
	return nil, false
}

func extractThresholdMetric(sv *metrics.StepView, metric string) float64 {
	switch metric {
	case "p50_ms":
		return sv.Latency.P50
	case "p90_ms":
		return sv.Latency.P90
	case "p95_ms":
		return sv.Latency.P95
	case "p99_ms":
		return sv.Latency.P99
	case "max_ms":
		return sv.Latency.Max
	case "mean_ms":
		return sv.Latency.Mean
	case "error_rate":
		return sv.ErrorRate
	case "error_rate_pct":
		return sv.ErrorRate * 100
	case "rps":
		return sv.RPS
	default:
		return 0
	}
}

func compareThreshold(actual float64, op string, target float64) bool {
	switch op {
	case "<":
		return actual < target
	case "<=":
		return actual <= target
	case ">":
		return actual > target
	case ">=":
		return actual >= target
	default:
		return false
	}
}

func formatThresholdMessage(r ThresholdResult) string {
	scope := "全局"
	if r.Step != "" {
		scope = fmt.Sprintf("步骤 %q", r.Step)
	}
	label := thresholdMetricLabel(r.Metric)
	actualStr := formatThresholdValue(r.Metric, r.Actual)
	targetStr := formatThresholdValue(r.Metric, r.Value)
	if r.Passed {
		return fmt.Sprintf("%s %s 实际 %s（满足 %s %s）", scope, label, actualStr, r.Op, targetStr)
	}
	return fmt.Sprintf("%s %s 实际 %s（未满足要求 %s %s）", scope, label, actualStr, r.Op, targetStr)
}

func thresholdMetricLabel(metric string) string {
	switch metric {
	case "p50_ms":
		return "P50 延迟"
	case "p90_ms":
		return "P90 延迟"
	case "p95_ms":
		return "P95 延迟"
	case "p99_ms":
		return "P99 延迟"
	case "max_ms":
		return "最大延迟"
	case "mean_ms":
		return "平均延迟"
	case "error_rate", "error_rate_pct":
		return "错误率"
	case "rps":
		return "RPS"
	default:
		return metric
	}
}

func formatThresholdValue(metric string, val float64) string {
	switch metric {
	case "p50_ms", "p90_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms":
		return fmt.Sprintf("%.2f ms", val)
	case "error_rate":
		return fmt.Sprintf("%.2f%% (%.4f)", val*100, val)
	case "error_rate_pct":
		return fmt.Sprintf("%.2f%%", val)
	case "rps":
		return fmt.Sprintf("%.2f req/s", val)
	default:
		return fmt.Sprintf("%.4f", val)
	}
}
