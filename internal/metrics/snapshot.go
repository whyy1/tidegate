package metrics

import (
	"fmt"
	"strings"
	"time"
)

// StepInfo identifies one step of a plan.
type StepInfo struct {
	ID   string
	Name string
}

// StepStats are the aggregated measurements of one step (or of all steps
// for Snapshot.Total) over an interval.
type StepStats struct {
	ID            string
	Name          string
	Requests      int64
	Status        map[int]int64 // response status code -> count
	Errors        [NumErrorKinds]int64
	Latency       *Histogram
	BytesSent     int64
	BytesReceived int64
	PhaseSum      [NumPhases]time.Duration
	PhaseCount    [NumPhases]int64
}

func newStepStats(id, name string) StepStats {
	return StepStats{ID: id, Name: name, Status: map[int]int64{}, Latency: NewHistogram()}
}

// ErrorCount returns the number of failed requests.
func (s *StepStats) ErrorCount() int64 {
	var n int64
	for _, c := range s.Errors {
		n += c
	}
	return n
}

func (s *StepStats) merge(o *StepStats) {
	s.Requests += o.Requests
	for code, n := range o.Status {
		s.Status[code] += n
	}
	for i, n := range o.Errors {
		s.Errors[i] += n
	}
	s.Latency.Merge(o.Latency)
	s.BytesSent += o.BytesSent
	s.BytesReceived += o.BytesReceived
	for i := range s.PhaseSum {
		s.PhaseSum[i] += o.PhaseSum[i]
		s.PhaseCount[i] += o.PhaseCount[i]
	}
}

// Header is one HTTP header in a sample.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Sample is a captured failed or slow request.
type Sample struct {
	Kind            SampleKind
	Step            int
	StepID          string
	Time            time.Time
	Method          string
	URL             string
	Status          int
	Error           string
	Duration        time.Duration
	RequestHeaders  []Header
	ResponseHeaders []Header
	Body            string
	BodyTruncated   bool
}

// Snapshot is the aggregated state of a run over one interval (normally one
// second) or, after merging, over several intervals or nodes.
type Snapshot struct {
	Seq               int64
	Time              time.Time     // start of the interval
	Duration          time.Duration // length of the interval
	ActiveVUs         int64
	ActiveConns       int64
	Target            float64 // target VUs or iterations per second
	Iterations        int64   // completed scenario iterations
	DroppedIterations int64   // open model: iterations that found no free VU
	SchedDelay        *Histogram
	OverheadSent      int64 // bytes not attributable to a request (TLS handshakes, closes)
	OverheadReceived  int64
	Total             StepStats
	Steps             []StepStats
	Samples           []Sample
}

// NewSnapshot returns an empty snapshot laid out for the given steps.
// schedDelay controls whether the scheduling delay histogram is allocated.
func NewSnapshot(steps []StepInfo, schedDelay bool) *Snapshot {
	s := &Snapshot{Total: newStepStats("", "total"), Steps: make([]StepStats, len(steps))}
	for i, st := range steps {
		s.Steps[i] = newStepStats(st.ID, st.Name)
	}
	if schedDelay {
		s.SchedDelay = NewHistogram()
	}
	return s
}

// ComputeTotal rebuilds Total from the per-step stats.
func (s *Snapshot) ComputeTotal() {
	s.Total = newStepStats("", "total")
	for i := range s.Steps {
		s.Total.merge(&s.Steps[i])
	}
}

// MergeMode says how gauges combine when merging snapshots.
type MergeMode uint8

const (
	// MergeNodes combines snapshots of the same interval from different
	// nodes: gauges and targets add up.
	MergeNodes MergeMode = iota
	// MergeIntervals combines consecutive intervals of the same run:
	// gauges and targets keep the peak value.
	MergeIntervals
)

// Merge adds o into s. Both must describe the same plan (same steps in the
// same order). Percentiles must be computed from the merged histograms,
// never averaged.
func (s *Snapshot) Merge(o *Snapshot, mode MergeMode) error {
	if len(s.Steps) != len(o.Steps) {
		return fmt.Errorf("metrics: cannot merge snapshots with %d and %d steps", len(s.Steps), len(o.Steps))
	}
	for i := range s.Steps {
		if s.Steps[i].ID != o.Steps[i].ID {
			return fmt.Errorf("metrics: step %d differs: %q vs %q", i, s.Steps[i].ID, o.Steps[i].ID)
		}
	}
	if s.Time.IsZero() {
		s.Time, s.Duration, s.Seq = o.Time, o.Duration, o.Seq
	} else if !o.Time.IsZero() {
		start, end := s.Time, s.Time.Add(s.Duration)
		if o.Time.Before(start) {
			start = o.Time
		}
		if oe := o.Time.Add(o.Duration); oe.After(end) {
			end = oe
		}
		s.Time, s.Duration = start, end.Sub(start)
		s.Seq = min(s.Seq, o.Seq)
	}
	switch mode {
	case MergeNodes:
		s.ActiveVUs += o.ActiveVUs
		s.ActiveConns += o.ActiveConns
		s.Target += o.Target
	default:
		s.ActiveVUs = max(s.ActiveVUs, o.ActiveVUs)
		s.ActiveConns = max(s.ActiveConns, o.ActiveConns)
		s.Target = max(s.Target, o.Target)
	}
	s.Iterations += o.Iterations
	s.DroppedIterations += o.DroppedIterations
	if o.SchedDelay != nil {
		if s.SchedDelay == nil {
			s.SchedDelay = NewHistogram()
		}
		s.SchedDelay.Merge(o.SchedDelay)
	}
	s.OverheadSent += o.OverheadSent
	s.OverheadReceived += o.OverheadReceived
	s.Total.merge(&o.Total)
	for i := range s.Steps {
		s.Steps[i].merge(&o.Steps[i])
	}
	s.Samples = append(s.Samples, o.Samples...)
	return nil
}

// TrimSamples keeps at most perKind samples of every category, preferring
// the earliest ones.
func (s *Snapshot) TrimSamples(perKind int) {
	var seen [NumSampleKinds]int
	out := s.Samples[:0]
	for _, smp := range s.Samples {
		if int(smp.Kind) < NumSampleKinds && seen[smp.Kind] < perKind {
			seen[smp.Kind]++
			out = append(out, smp)
		}
	}
	clear(s.Samples[len(out):])
	s.Samples = out
}

// sensitiveHeaders are masked in samples.
var sensitiveHeaders = []string{
	"authorization", "proxy-authorization", "cookie", "set-cookie",
	"x-api-key", "api-key", "x-auth-token", "x-access-token", "x-csrf-token",
}

// RedactHeader masks the value of authentication related headers.
func RedactHeader(name, value string) string {
	for _, h := range sensitiveHeaders {
		if strings.EqualFold(name, h) {
			if value == "" {
				return ""
			}
			return "***"
		}
	}
	return value
}
