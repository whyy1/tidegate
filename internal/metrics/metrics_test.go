package metrics

import (
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"
)

func TestHistogramStats(t *testing.T) {
	h := NewHistogram()
	for i := 1; i <= 1000; i++ {
		h.Record(time.Duration(i) * time.Millisecond)
	}
	st := h.Stats()
	if st.Count != 1000 {
		t.Fatalf("count = %d", st.Count)
	}
	check := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want)/want > 0.002 {
			t.Errorf("%s = %v, want about %v", name, got, want)
		}
	}
	check("p50", st.P50, 500)
	check("p90", st.P90, 900)
	check("p99", st.P99, 990)
	check("max", st.Max, 1000)
	check("mean", st.Mean, 500.5)
}

func TestHistogramClamp(t *testing.T) {
	h := NewHistogram()
	h.Record(0)
	h.Record(-time.Second)
	h.Record(5 * time.Hour)
	if h.Count() != 3 {
		t.Fatalf("count = %d, want 3", h.Count())
	}
	if h.Max() < HistHighest-HistHighest/500 {
		t.Fatalf("max = %d, want clamped to about 1h", h.Max())
	}
}

func TestMergeComputesPercentilesFromMergedHistogram(t *testing.T) {
	steps := []StepInfo{{ID: "a"}}
	fast := NewSnapshot(steps, false)
	slow := NewSnapshot(steps, false)
	for i := 0; i < 990; i++ {
		fast.Steps[0].Latency.Record(time.Millisecond)
		fast.Steps[0].Requests++
	}
	for i := 0; i < 10; i++ {
		slow.Steps[0].Latency.Record(time.Second)
		slow.Steps[0].Requests++
	}
	fast.ComputeTotal()
	slow.ComputeTotal()
	if err := fast.Merge(slow, MergeNodes); err != nil {
		t.Fatal(err)
	}
	st := fast.Total.Latency.Stats()
	// The average of the two P99 values would be about 500ms; the true P99 of
	// the merged data is 1ms because only 1% of values are slow.
	if st.P99 > 1.01 {
		t.Fatalf("merged p99 = %vms, want about 1ms", st.P99)
	}
	if st.Max < 999 {
		t.Fatalf("merged max = %vms", st.Max)
	}
	if fast.Total.Requests != 1000 || fast.Steps[0].Requests != 1000 {
		t.Fatalf("requests total=%d step=%d", fast.Total.Requests, fast.Steps[0].Requests)
	}
}

func TestMergeModes(t *testing.T) {
	steps := []StepInfo{{ID: "a"}}
	t0 := time.Unix(1000, 0)
	a := NewSnapshot(steps, false)
	a.Time, a.Duration, a.ActiveVUs, a.Target, a.Seq = t0, time.Second, 10, 10, 0
	b := NewSnapshot(steps, false)
	b.Time, b.Duration, b.ActiveVUs, b.Target, b.Seq = t0.Add(time.Second), time.Second, 20, 20, 1

	seq := NewSnapshot(steps, false)
	_ = seq.Merge(a, MergeIntervals)
	_ = seq.Merge(b, MergeIntervals)
	if seq.ActiveVUs != 20 || seq.Target != 20 || seq.Duration != 2*time.Second || !seq.Time.Equal(t0) || seq.Seq != 0 {
		t.Fatalf("interval merge: %+v", seq)
	}

	nodes := NewSnapshot(steps, false)
	_ = nodes.Merge(a, MergeNodes)
	c := NewSnapshot(steps, false)
	c.Time, c.Duration, c.ActiveVUs, c.Target = t0, time.Second, 5, 5
	_ = nodes.Merge(c, MergeNodes)
	if nodes.ActiveVUs != 15 || nodes.Target != 15 || nodes.Duration != time.Second {
		t.Fatalf("node merge: %+v", nodes)
	}

	other := NewSnapshot([]StepInfo{{ID: "x"}}, false)
	if err := seq.Merge(other, MergeNodes); err == nil {
		t.Fatal("merging different plans should fail")
	}
}

func TestRecorderCollect(t *testing.T) {
	steps := []StepInfo{{ID: "login", Name: "登录"}, {ID: "query"}}
	c := NewCollector(4, steps, true, 2)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := c.Recorder(w)
			for i := 0; i < 1000; i++ {
				rec := RequestRecord{Step: i % 2, Status: 200, Error: ErrNone, Latency: time.Duration(i+1) * time.Microsecond, Sent: 10, Received: 20}
				if i%100 == 0 {
					rec.Status, rec.Error = 500, ErrHTTP5xx
				}
				rec.SetPhase(PhaseFirstByte, time.Microsecond)
				r.RecordRequest(&rec)
				if i%2 == 1 {
					r.RecordIteration()
					r.RecordSchedDelay(time.Millisecond)
				}
				if r.WantSample(SampleKind(ErrHTTP5xx)) {
					r.AddSample(&Sample{Kind: SampleKind(ErrHTTP5xx), Status: 500})
				}
			}
			r.RecordOverhead(5, 7)
		}(w)
	}
	wg.Wait()
	snap := c.NewSnapshot()
	c.Collect(snap, 3)
	if snap.Total.Requests != 8000 {
		t.Fatalf("total requests = %d", snap.Total.Requests)
	}
	if snap.Steps[0].Requests != 4000 || snap.Steps[1].Requests != 4000 {
		t.Fatalf("step requests = %d/%d", snap.Steps[0].Requests, snap.Steps[1].Requests)
	}
	if got := snap.Total.Errors[ErrHTTP5xx]; got != 80 {
		t.Fatalf("5xx = %d", got)
	}
	if snap.Total.Status[200] != 7920 || snap.Total.Status[500] != 80 {
		t.Fatalf("status = %v", snap.Total.Status)
	}
	if snap.Iterations != 4000 || snap.SchedDelay.Count() != 4000 {
		t.Fatalf("iterations = %d, sched = %d", snap.Iterations, snap.SchedDelay.Count())
	}
	if snap.Total.BytesSent != 80000 || snap.Total.BytesReceived != 160000 {
		t.Fatalf("bytes = %d/%d", snap.Total.BytesSent, snap.Total.BytesReceived)
	}
	if snap.OverheadSent != 40 || snap.OverheadReceived != 56 {
		t.Fatalf("overhead = %d/%d", snap.OverheadSent, snap.OverheadReceived)
	}
	if snap.Total.PhaseCount[PhaseFirstByte] != 8000 {
		t.Fatalf("phase count = %d", snap.Total.PhaseCount[PhaseFirstByte])
	}
	if len(snap.Samples) != 3 {
		t.Fatalf("samples = %d, want 3 after trimming", len(snap.Samples))
	}

	// The next interval starts empty and sample budgets are reset.
	next := c.NewSnapshot()
	c.Recorder(0).RecordRequest(&RequestRecord{Step: 1, Status: 204, Error: ErrNone, Latency: time.Millisecond})
	if !c.Recorder(0).WantSample(SampleSlow) {
		t.Fatal("sample budget not reset")
	}
	c.Collect(next, 3)
	if next.Total.Requests != 1 || next.Steps[1].Status[204] != 1 || len(next.Samples) != 0 || next.Iterations != 0 {
		t.Fatalf("second interval: requests=%d status=%v samples=%d", next.Total.Requests, next.Steps[1].Status, len(next.Samples))
	}
}

func sampleSnapshot() *Snapshot {
	steps := []StepInfo{{ID: "login", Name: "登录"}, {ID: "query", Name: "查询"}}
	s := NewSnapshot(steps, true)
	s.Seq = 7
	s.Time = time.Unix(1700000000, 123456789)
	s.Duration = time.Second
	s.ActiveVUs, s.ActiveConns, s.Target = 100, 98, 100.5
	s.Iterations, s.DroppedIterations = 1234, 5
	s.SchedDelay.Record(300 * time.Microsecond)
	s.OverheadSent, s.OverheadReceived = 1000, 2000
	for i := 0; i < 500; i++ {
		s.Steps[0].Latency.Record(time.Duration(i) * time.Millisecond)
	}
	s.Steps[0].Requests = 500
	s.Steps[0].Status[200] = 490
	s.Steps[0].Status[503] = 10
	s.Steps[0].Errors[ErrHTTP5xx] = 10
	s.Steps[0].BytesSent, s.Steps[0].BytesReceived = 50000, 90000
	s.Steps[0].PhaseSum[PhaseConnect] = 3 * time.Millisecond
	s.Steps[0].PhaseCount[PhaseConnect] = 3
	s.Steps[1].Requests = 1
	s.Steps[1].Latency.Record(time.Hour)
	s.Steps[1].Errors[ErrAssertion] = 1
	s.Samples = []Sample{{
		Kind: SampleKind(ErrHTTP5xx), Step: 0, StepID: "login", Time: time.Unix(1700000000, 5),
		Method: "POST", URL: "http://x/login", Status: 503, Error: "HTTP 503",
		Duration: 12 * time.Millisecond, RequestHeaders: []Header{{"Authorization", "***"}},
		ResponseHeaders: []Header{{"Content-Type", "text/plain"}}, Body: "busy", BodyTruncated: true,
	}}
	s.ComputeTotal()
	return s
}

func TestBinaryRoundTrip(t *testing.T) {
	s := sampleSnapshot()
	b, err := s.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var got Snapshot
	if err := got.UnmarshalBinary(b); err != nil {
		t.Fatal(err)
	}
	if got.Seq != s.Seq || !got.Time.Equal(s.Time) || got.Duration != s.Duration ||
		got.ActiveVUs != 100 || got.ActiveConns != 98 || got.Target != 100.5 ||
		got.Iterations != 1234 || got.DroppedIterations != 5 || got.OverheadSent != 1000 || got.OverheadReceived != 2000 {
		t.Fatalf("header mismatch: %+v", got)
	}
	if got.SchedDelay == nil || got.SchedDelay.Count() != 1 {
		t.Fatal("sched delay lost")
	}
	if len(got.Steps) != 2 || got.Steps[0].ID != "login" || got.Steps[1].Name != "查询" {
		t.Fatalf("steps = %+v", got.Steps)
	}
	a, bb := s.Steps[0].Latency.Stats(), got.Steps[0].Latency.Stats()
	if a != bb {
		t.Fatalf("latency stats differ: %+v vs %+v", a, bb)
	}
	if got.Steps[0].Status[503] != 10 || got.Steps[0].Errors[ErrHTTP5xx] != 10 || got.Steps[1].Errors[ErrAssertion] != 1 {
		t.Fatal("counters lost")
	}
	if got.Steps[0].PhaseSum[PhaseConnect] != 3*time.Millisecond || got.Steps[0].PhaseCount[PhaseConnect] != 3 {
		t.Fatal("phases lost")
	}
	if got.Total.Requests != 501 || got.Total.Latency.Count() != 501 {
		t.Fatalf("total not rebuilt: %d", got.Total.Requests)
	}
	if len(got.Samples) != 1 || got.Samples[0].Body != "busy" || !got.Samples[0].BodyTruncated || got.Samples[0].RequestHeaders[0].Value != "***" {
		t.Fatalf("samples = %+v", got.Samples)
	}
	// The sparse encoding must be far smaller than the dense histograms.
	if len(b) > 4096 {
		t.Fatalf("encoding too large: %d bytes", len(b))
	}
}

func TestBinaryRejectsCorruptInput(t *testing.T) {
	b, _ := sampleSnapshot().MarshalBinary()
	var s Snapshot
	for i := 0; i < len(b); i++ {
		// Truncations must fail cleanly, never panic.
		if err := s.UnmarshalBinary(b[:i]); err == nil {
			t.Fatalf("truncated input of %d bytes accepted", i)
		}
	}
	bad := append([]byte{}, b...)
	bad[0] = 'X'
	if err := s.UnmarshalBinary(bad); err == nil {
		t.Fatal("bad magic accepted")
	}
	if err := s.UnmarshalBinary(append(b, 0)); err == nil {
		t.Fatal("trailing data accepted")
	}
}

func TestViewJSON(t *testing.T) {
	s := sampleSnapshot()
	data, err := json.Marshal(s.View())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["requests"].(float64) != 501 || m["errors"].(float64) != 11 {
		t.Fatalf("view = %s", data)
	}
	lat := m["latency"].(map[string]any)
	for _, k := range []string{"p50_ms", "p90_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms"} {
		if _, ok := lat[k]; !ok {
			t.Fatalf("latency missing %s: %s", k, data)
		}
	}
	steps := m["steps"].([]any)
	first := steps[0].(map[string]any)
	if first["id"] != "login" || first["status"].(map[string]any)["503"].(float64) != 10 {
		t.Fatalf("step view = %v", first)
	}
	if first["error_kinds"].(map[string]any)["http_5xx"].(float64) != 10 {
		t.Fatalf("error kinds = %v", first["error_kinds"])
	}
	if first["sent_bps"].(float64) != 400000 {
		t.Fatalf("sent_bps = %v", first["sent_bps"])
	}
	if _, ok := m["sched_delay"]; !ok {
		t.Fatal("sched_delay missing")
	}
	if _, ok := m["counts"]; ok {
		t.Fatal("raw histogram must not be in JSON")
	}
}

func TestRedactHeader(t *testing.T) {
	if RedactHeader("authorization", "Bearer x") != "***" || RedactHeader("Cookie", "a=b") != "***" {
		t.Fatal("sensitive header not masked")
	}
	if RedactHeader("Content-Type", "text/plain") != "text/plain" {
		t.Fatal("plain header masked")
	}
}

func TestKindNames(t *testing.T) {
	for k := ErrorKind(0); k < NumErrorKinds; k++ {
		got, ok := ParseErrorKind(k.String())
		if !ok || got != k {
			t.Fatalf("round trip of %v failed", k)
		}
	}
	if k, ok := ParseSampleKind("slow"); !ok || k != SampleSlow {
		t.Fatal("slow kind")
	}
	if ErrLocal.String() != "local" || PhaseTLS.String() != "tls" {
		t.Fatal("names changed")
	}
}

func BenchmarkRecordRequest(b *testing.B) {
	c := NewCollector(1, []StepInfo{{ID: "a"}}, false, 5)
	r := c.Recorder(0)
	rec := RequestRecord{Status: 200, Error: ErrNone, Latency: 150 * time.Microsecond, Sent: 80, Received: 120}
	rec.SetPhase(PhaseFirstByte, 100*time.Microsecond)
	rec.SetPhase(PhaseDownload, 10*time.Microsecond)
	b.ReportAllocs()
	for b.Loop() {
		r.RecordRequest(&rec)
	}
}

func BenchmarkRecordRequestParallel(b *testing.B) {
	c := NewCollector(32, []StepInfo{{ID: "a"}}, false, 5)
	var next int64
	var mu sync.Mutex
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		mu.Lock()
		r := c.Recorder(int(next))
		next++
		mu.Unlock()
		rec := RequestRecord{Status: 200, Error: ErrNone, Latency: 150 * time.Microsecond}
		for pb.Next() {
			r.RecordRequest(&rec)
		}
	})
}

func BenchmarkCollect(b *testing.B) {
	c := NewCollector(32, []StepInfo{{ID: "a"}, {ID: "b"}}, false, 5)
	for i := 0; i < 32; i++ {
		c.Recorder(i).RecordRequest(&RequestRecord{Step: i % 2, Status: 200, Error: ErrNone, Latency: time.Millisecond})
	}
	b.ReportAllocs()
	for b.Loop() {
		s := c.NewSnapshot()
		for i := 0; i < 32; i++ {
			c.Recorder(i).RecordRequest(&RequestRecord{Step: i % 2, Status: 200, Error: ErrNone, Latency: time.Millisecond})
		}
		c.Collect(s, 5)
	}
}
