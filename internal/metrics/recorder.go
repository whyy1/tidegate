package metrics

import (
	"sync"
	"sync/atomic"
	"time"
)

// statusSlots covers every three digit status code; index 0 is unused.
const statusSlots = 1000

// RequestRecord is the measurement of one request (one hop when redirects
// are followed). The engine fills it on its hot path; keep it allocation free.
type RequestRecord struct {
	Step     int
	Status   int       // 0 when no response was received
	Error    ErrorKind // ErrNone when successful
	Latency  time.Duration
	Sent     int64
	Received int64
	// Overhead bytes observed while serving this request that belong to no
	// request (TLS handshake, retries); booked under the same lock.
	OverheadSent     int64
	OverheadReceived int64
	Phases           [NumPhases]time.Duration
	Measured         PhaseMask
}

// SetPhase stores the duration of phase p.
func (r *RequestRecord) SetPhase(p Phase, d time.Duration) {
	r.Phases[p] = d
	r.Measured |= 1 << p
}

type stepWindow struct {
	requests   int64
	status     [statusSlots]int64
	errors     [NumErrorKinds]int64
	latency    *Histogram
	sent       int64
	recv       int64
	phaseSum   [NumPhases]time.Duration
	phaseCount [NumPhases]int64
}

type window struct {
	steps        []stepWindow
	schedDelay   *Histogram
	iterations   int64
	overheadSent int64
	overheadRecv int64
	samples      []Sample
}

func newWindow(steps int, schedDelay bool) *window {
	w := &window{steps: make([]stepWindow, steps)}
	for i := range w.steps {
		w.steps[i].latency = NewHistogram()
	}
	if schedDelay {
		w.schedDelay = NewHistogram()
	}
	return w
}

func (w *window) reset() {
	for i := range w.steps {
		s := &w.steps[i]
		if s.requests != 0 {
			h := s.latency
			h.Reset()
			*s = stepWindow{latency: h}
		}
	}
	if w.schedDelay != nil {
		w.schedDelay.Reset()
	}
	w.iterations, w.overheadSent, w.overheadRecv = 0, 0, 0
	clear(w.samples)
	w.samples = w.samples[:0]
}

// Recorder accumulates measurements for one shard of workers. Workers are
// spread over many recorders so the short critical section is practically
// uncontended; there is no global lock on the hot path.
type Recorder struct {
	_           [64]byte // keep the mutex off neighbouring cache lines
	mu          sync.Mutex
	cur         *window
	samplesUsed [NumSampleKinds]atomic.Int32
	sampleLimit int32
	_           [64]byte
}

// RecordRequest adds one request measurement.
func (r *Recorder) RecordRequest(rec *RequestRecord) {
	r.mu.Lock()
	s := &r.cur.steps[rec.Step]
	s.requests++
	if rec.Status > 0 && rec.Status < statusSlots {
		s.status[rec.Status]++
	}
	if rec.Error < NumErrorKinds {
		s.errors[rec.Error]++
	}
	s.latency.Record(rec.Latency)
	s.sent += rec.Sent
	s.recv += rec.Received
	r.cur.overheadSent += rec.OverheadSent
	r.cur.overheadRecv += rec.OverheadReceived
	if m := rec.Measured; m != 0 {
		for p := Phase(0); p < NumPhases; p++ {
			if m.Has(p) {
				s.phaseSum[p] += rec.Phases[p]
				s.phaseCount[p]++
			}
		}
	}
	r.mu.Unlock()
}

// RecordIteration counts one completed scenario iteration.
func (r *Recorder) RecordIteration() {
	r.mu.Lock()
	r.cur.iterations++
	r.mu.Unlock()
}

// RecordSchedDelay records the scheduling delay of one open model iteration.
func (r *Recorder) RecordSchedDelay(d time.Duration) {
	r.mu.Lock()
	if r.cur.schedDelay != nil {
		r.cur.schedDelay.Record(d)
	}
	r.mu.Unlock()
}

// RecordOverhead adds bytes that belong to no request.
func (r *Recorder) RecordOverhead(sent, recv int64) {
	if sent == 0 && recv == 0 {
		return
	}
	r.mu.Lock()
	r.cur.overheadSent += sent
	r.cur.overheadRecv += recv
	r.mu.Unlock()
}

// WantSample reports whether a sample of kind k would still be kept in the
// current interval. It is a cheap pre-check so callers only build samples
// that are likely to be stored.
func (r *Recorder) WantSample(k SampleKind) bool {
	return r.samplesUsed[k].Load() < r.sampleLimit
}

// AddSample stores a sample if the per-interval budget of its kind allows.
func (r *Recorder) AddSample(s *Sample) {
	if r.samplesUsed[s.Kind].Add(1) > r.sampleLimit {
		return
	}
	r.mu.Lock()
	r.cur.samples = append(r.cur.samples, *s)
	r.mu.Unlock()
}

// Collector owns a set of recorders and turns their windows into snapshots.
// Collect must only be called from one goroutine.
type Collector struct {
	recorders  []*Recorder
	spares     []*window
	steps      []StepInfo
	schedDelay bool
}

// NewCollector creates n recorders for the given steps. samplesPerKind
// limits how many samples of each kind one interval keeps.
func NewCollector(n int, steps []StepInfo, schedDelay bool, samplesPerKind int) *Collector {
	c := &Collector{steps: steps, schedDelay: schedDelay}
	for i := 0; i < n; i++ {
		r := &Recorder{cur: newWindow(len(steps), schedDelay), sampleLimit: int32(samplesPerKind)}
		c.recorders = append(c.recorders, r)
		c.spares = append(c.spares, newWindow(len(steps), schedDelay))
	}
	return c
}

// Recorder returns recorder i modulo the number of recorders.
func (c *Collector) Recorder(i int) *Recorder { return c.recorders[i%len(c.recorders)] }

// Len returns the number of recorders.
func (c *Collector) Len() int { return len(c.recorders) }

// NewSnapshot returns an empty snapshot laid out like this collector.
func (c *Collector) NewSnapshot() *Snapshot { return NewSnapshot(c.steps, c.schedDelay) }

// Collect closes the current interval of every recorder and adds it to dst.
// samplesPerKind caps the merged samples of each kind.
func (c *Collector) Collect(dst *Snapshot, samplesPerKind int) {
	for i, r := range c.recorders {
		spare := c.spares[i]
		r.mu.Lock()
		w := r.cur
		r.cur = spare
		r.mu.Unlock()
		for k := range r.samplesUsed {
			r.samplesUsed[k].Store(0)
		}
		dst.addWindow(w)
		w.reset()
		c.spares[i] = w
	}
	dst.TrimSamples(samplesPerKind)
	dst.ComputeTotal()
}

func (s *Snapshot) addWindow(w *window) {
	for i := range w.steps {
		src := &w.steps[i]
		if src.requests == 0 {
			continue
		}
		dst := &s.Steps[i]
		dst.Requests += src.requests
		for code, n := range src.status {
			if n != 0 {
				dst.Status[code] += n
			}
		}
		for k, n := range src.errors {
			dst.Errors[k] += n
		}
		dst.Latency.Merge(src.latency)
		dst.BytesSent += src.sent
		dst.BytesReceived += src.recv
		for p := range src.phaseSum {
			dst.PhaseSum[p] += src.phaseSum[p]
			dst.PhaseCount[p] += src.phaseCount[p]
		}
	}
	if w.schedDelay != nil && s.SchedDelay != nil {
		s.SchedDelay.Merge(w.schedDelay)
	}
	s.Iterations += w.iterations
	s.OverheadSent += w.overheadSent
	s.OverheadReceived += w.overheadRecv
	s.Samples = append(s.Samples, w.samples...)
}
