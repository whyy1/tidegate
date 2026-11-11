package metrics

import (
	"sort"
	"strconv"
	"time"
)

// PhaseView is the aggregated time of one request phase.
type PhaseView struct {
	Count int64   `json:"count"`
	AvgMs float64 `json:"avg_ms"`
}

// StepView is the JSON form of StepStats with computed percentiles.
type StepView struct {
	ID            string               `json:"id,omitempty"`
	Name          string               `json:"name,omitempty"`
	Requests      int64                `json:"requests"`
	RPS           float64              `json:"rps"`
	Errors        int64                `json:"errors"`
	ErrorRate     float64              `json:"error_rate"`
	Latency       LatencyStats         `json:"latency"`
	BytesSent     int64                `json:"bytes_sent"`
	BytesReceived int64                `json:"bytes_received"`
	SentBps       float64              `json:"sent_bps"`
	ReceivedBps   float64              `json:"received_bps"`
	Status        map[string]int64     `json:"status,omitempty"`
	ErrorKinds    map[string]int64     `json:"error_kinds,omitempty"`
	Phases        map[string]PhaseView `json:"phases,omitempty"`
}

// SampleView is the JSON form of a Sample.
type SampleView struct {
	Kind            string    `json:"kind"`
	Step            string    `json:"step"`
	Time            time.Time `json:"time"`
	Method          string    `json:"method"`
	URL             string    `json:"url"`
	Status          int       `json:"status,omitempty"`
	Error           string    `json:"error,omitempty"`
	DurationMs      float64   `json:"duration_ms"`
	RequestHeaders  []Header  `json:"request_headers,omitempty"`
	ResponseHeaders []Header  `json:"response_headers,omitempty"`
	Body            string    `json:"body,omitempty"`
	BodyTruncated   bool      `json:"body_truncated,omitempty"`
}

// SnapshotView is the JSON form of a Snapshot: computed values only, no raw
// histograms.
type SnapshotView struct {
	Seq               int64         `json:"seq"`
	Time              time.Time     `json:"time"`
	DurationMs        float64       `json:"duration_ms"`
	ActiveVUs         int64         `json:"active_vus"`
	ActiveConns       int64         `json:"active_conns"`
	Target            float64       `json:"target"`
	Iterations        int64         `json:"iterations"`
	DroppedIterations int64         `json:"dropped_iterations"`
	SchedDelay        *LatencyStats `json:"sched_delay,omitempty"`
	OverheadSent      int64         `json:"overhead_bytes_sent"`
	OverheadReceived  int64         `json:"overhead_bytes_received"`
	StepView
	Steps   []StepView   `json:"steps"`
	Samples []SampleView `json:"samples,omitempty"`
}

func perSecond(n int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / d.Seconds()
}

func (s *StepStats) view(d time.Duration) StepView {
	v := StepView{
		ID:            s.ID,
		Name:          s.Name,
		Requests:      s.Requests,
		RPS:           round2(perSecond(s.Requests, d)),
		Errors:        s.ErrorCount(),
		Latency:       s.Latency.Stats(),
		BytesSent:     s.BytesSent,
		BytesReceived: s.BytesReceived,
		SentBps:       round2(perSecond(s.BytesSent*8, d)),
		ReceivedBps:   round2(perSecond(s.BytesReceived*8, d)),
	}
	if s.Requests > 0 {
		v.ErrorRate = float64(v.Errors) / float64(s.Requests)
	}
	if len(s.Status) > 0 {
		v.Status = make(map[string]int64, len(s.Status))
		for code, n := range s.Status {
			v.Status[strconv.Itoa(code)] = n
		}
	}
	for k, n := range s.Errors {
		if n != 0 {
			if v.ErrorKinds == nil {
				v.ErrorKinds = map[string]int64{}
			}
			v.ErrorKinds[ErrorKind(k).String()] = n
		}
	}
	for p, n := range s.PhaseCount {
		if n != 0 {
			if v.Phases == nil {
				v.Phases = map[string]PhaseView{}
			}
			v.Phases[Phase(p).String()] = PhaseView{Count: n, AvgMs: round3(float64(s.PhaseSum[p]) / float64(n) / 1e6)}
		}
	}
	return v
}

// View computes the JSON form of the snapshot.
func (s *Snapshot) View() *SnapshotView {
	v := &SnapshotView{
		Seq:               s.Seq,
		Time:              s.Time,
		DurationMs:        round3(float64(s.Duration) / 1e6),
		ActiveVUs:         s.ActiveVUs,
		ActiveConns:       s.ActiveConns,
		Target:            round2(s.Target),
		Iterations:        s.Iterations,
		DroppedIterations: s.DroppedIterations,
		OverheadSent:      s.OverheadSent,
		OverheadReceived:  s.OverheadReceived,
		StepView:          s.Total.view(s.Duration),
		Steps:             make([]StepView, len(s.Steps)),
	}
	v.StepView.ID, v.StepView.Name = "", ""
	if s.SchedDelay != nil {
		st := s.SchedDelay.Stats()
		v.SchedDelay = &st
	}
	for i := range s.Steps {
		v.Steps[i] = s.Steps[i].view(s.Duration)
	}
	for _, sm := range s.Samples {
		v.Samples = append(v.Samples, SampleView{
			Kind:            sm.Kind.String(),
			Step:            sm.StepID,
			Time:            sm.Time,
			Method:          sm.Method,
			URL:             sm.URL,
			Status:          sm.Status,
			Error:           sm.Error,
			DurationMs:      round3(float64(sm.Duration) / 1e6),
			RequestHeaders:  sm.RequestHeaders,
			ResponseHeaders: sm.ResponseHeaders,
			Body:            sm.Body,
			BodyTruncated:   sm.BodyTruncated,
		})
	}
	return v
}

// SortedStatus returns status codes in ascending order.
func (s *StepStats) SortedStatus() []int {
	codes := make([]int, 0, len(s.Status))
	for c := range s.Status {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	return codes
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }
func round3(f float64) float64 { return float64(int64(f*1000+0.5)) / 1000 }
