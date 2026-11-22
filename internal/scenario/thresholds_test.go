package scenario

import (
	"strings"
	"testing"
	"time"

	"tidegate/internal/metrics"
)

func TestCompileAndEvaluateThresholds(t *testing.T) {
	yamlSrc := `
version: 1
name: Threshold Smoke
steps:
  - id: login
    name: 用户登录
    request:
      method: GET
      url: http://127.0.0.1:8080/login
load:
  mode: vus
  stages:
    - duration: 1s
      target: 1
  thresholds:
    - metric: p95
      value: 200
    - metric: error_pct
      op: "<"
      value: 1
      step: login
    - metric: rps
      value: 50
      abort_on_fail: true
`
	def, err := Parse([]byte(yamlSrc), FormatYAML)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(def, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(plan.Load.Thresholds) != 3 {
		t.Fatalf("got %d thresholds", len(plan.Load.Thresholds))
	}
	if plan.Load.Thresholds[0].Metric != "p95_ms" || plan.Load.Thresholds[0].Op != "<=" {
		t.Fatalf("normalized[0] = %+v", plan.Load.Thresholds[0])
	}
	if plan.Load.Thresholds[1].Metric != "error_rate_pct" || plan.Load.Thresholds[1].Op != "<" {
		t.Fatalf("normalized[1] = %+v", plan.Load.Thresholds[1])
	}
	if plan.Load.Thresholds[2].Metric != "rps" || plan.Load.Thresholds[2].Op != ">=" {
		t.Fatalf("normalized[2] = %+v", plan.Load.Thresholds[2])
	}

	view := &metrics.SnapshotView{
		StepView: metrics.StepView{
			Requests:  100,
			RPS:       80,
			ErrorRate: 0.005,
			Latency:   metrics.LatencyStats{P50: 10, P90: 40, P95: 120, P99: 180, Max: 250, Mean: 30},
		},
		Steps: []metrics.StepView{
			{
				ID:        "login",
				Name:      "用户登录",
				Requests:  100,
				RPS:       80,
				ErrorRate: 0.005,
			},
		},
	}

	res, ok := EvaluateThresholds(plan.Load.Thresholds, view)
	if !ok || len(res) != 3 {
		t.Fatalf("expected all pass, got ok=%v res=%+v", ok, res)
	}

	// Failing latency & step error rate.
	view.Latency.P95 = 250
	view.Steps[0].ErrorRate = 0.02
	res, ok = EvaluateThresholds(plan.Load.Thresholds, view)
	if ok || res[0].Passed || res[1].Passed || !res[2].Passed {
		t.Fatalf("unexpected mixed threshold results: ok=%v res=%+v", ok, res)
	}
}

func TestCompileThresholdValidationErrors(t *testing.T) {
	makeYAML := func(thBlock string) []byte {
		return []byte(`
version: 1
steps:
  - id: s1
    request:
      method: GET
      url: http://127.0.0.1:8080/ping
load:
  mode: vus
  stages: [{duration: 1s, target: 1}]
  thresholds:
` + thBlock)
	}

	badCases := []struct {
		name  string
		block string
		want  string
	}{
		{"unknown_metric", "    - metric: bogus\n      value: 1", "metric"},
		{"bad_op", "    - metric: p95\n      op: ==\n      value: 10", "op"},
		{"neg_value", "    - metric: p95\n      value: -5", "value"},
		{"neg_grace", "    - metric: p95\n      value: 10\n      grace_period: -1s", "grace_period"},
		{"unknown_step", "    - metric: p95\n      value: 10\n      step: missing", "step"},
	}

	for _, tc := range badCases {
		def, err := Parse(makeYAML(tc.block), FormatYAML)
		if err != nil {
			t.Fatalf("%s parse: %v", tc.name, err)
		}
		_, err = Compile(def, CompileOptions{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected error mentioning %q, got %v", tc.name, tc.want, err)
		}
	}
}

func TestCheckAbortThresholdsGraceAndMinSamples(t *testing.T) {
	ts := []Threshold{
		{Metric: "rps", Op: ">=", Value: 500, AbortOnFail: true},
		{Metric: "error_rate_pct", Op: "<=", Value: 5, AbortOnFail: true, GracePeriod: Duration(500 * time.Millisecond)},
	}
	view := &metrics.SnapshotView{
		StepView: metrics.StepView{
			Requests:  10,
			RPS:       10,
			ErrorRate: 0.25,
		},
	}

	// Below explicit grace period (500ms) -> neither triggers.
	if msg, hit := CheckAbortThresholds(ts, view, 200*time.Millisecond); hit {
		t.Fatalf("triggered before grace period: %s", msg)
	}

	// Below minimum sample count (5) -> does not trigger even after grace period.
	view.Requests = 4
	if msg, hit := CheckAbortThresholds(ts, view, time.Second); hit {
		t.Fatalf("triggered with fewer than 5 samples: %s", msg)
	}

	// >= 5 samples & >= 500ms -> explicit grace period rule triggers (not default 3s rule yet).
	view.Requests = 10
	msg, hit := CheckAbortThresholds(ts, view, 600*time.Millisecond)
	if !hit || !strings.Contains(msg, "错误率") {
		t.Fatalf("expected error_rate abort after 500ms grace, got hit=%v msg=%q", hit, msg)
	}

	// Past default 3s grace period -> first rule triggers when error rate recovers.
	view.ErrorRate = 0
	msg, hit = CheckAbortThresholds(ts, view, 3100*time.Millisecond)
	if !hit || !strings.Contains(msg, "RPS") {
		t.Fatalf("expected RPS abort after 3s default grace, got hit=%v msg=%q", hit, msg)
	}
}
