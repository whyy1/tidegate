// Package engine is the Tidegate load generator: it schedules virtual
// users according to a compiled scenario plan, executes their requests and
// aggregates the measurements into per-second snapshots.
//
// Hot path rules: one goroutine per virtual user, no global locks (every
// virtual user records into one of many metrics.Recorder shards), buffers
// owned by the virtual user and reused, response bodies discarded unless an
// extractor or assertion needs them.
package engine

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
)

// ClientKind selects the HTTP client implementation.
type ClientKind string

const (
	// ClientFast is Tidegate's own minimal HTTP/1.1 client (default).
	ClientFast ClientKind = "fast"
	// ClientStd uses net/http's Transport.
	ClientStd ClientKind = "std"
)

// DataSlice selects a disjoint subset of every data file's rows: row i is
// used when i % Count == Index. Reserved for cluster mode, where each node
// gets its own slice so data rows are never reused across nodes.
type DataSlice struct {
	Index int
	Count int
}

// Options configure an engine. The zero value is valid.
type Options struct {
	Client ClientKind
	// SlowThreshold marks successful requests at least this slow for
	// sampling. 0 means 1s; negative disables slow samples.
	SlowThreshold time.Duration
	// SamplesPerSecond limits samples per category and second (default 5).
	SamplesPerSecond int
	// SummarySamples limits samples per category in the summary (default 20).
	SummarySamples int
	// SampleBodyBytes is how much of a response body a sample keeps (default 1024).
	SampleBodyBytes int
	// Shards is the number of metric recorders (default GOMAXPROCS).
	Shards int
	// LoadShare is the fraction of the plan's load this engine runs
	// (0 means 1). Reserved for cluster mode.
	LoadShare float64
	// DataSlice is this engine's share of data rows. Reserved for cluster mode.
	DataSlice DataSlice
}

// Engine runs one plan once.
type Engine struct {
	plan      *scenario.Plan
	opts      Options
	steps     []*stepRT
	collector *metrics.Collector
	shards    int
	dialer    *dialer
	client    clientFactory
	data      []*dataState
	seed      uint64

	prof     atomic.Pointer[profile]
	target   atomic.Uint64 // float64 bits of the current target
	start    int64         // nanotime at start
	started  time.Time
	maxVUs   int
	maxReq   int64
	reqCount atomic.Int64

	hardCtx    context.Context
	hardCancel context.CancelFunc
	stopCh     chan struct{}
	stopOnce   sync.Once
	stopping   atomic.Bool
	aborted    atomic.Bool

	vusMu  sync.Mutex
	vus    map[*vu]struct{}
	wg     sync.WaitGroup
	jobs   chan int64 // open model: planned start times for idle VUs
	nextID int

	activeVUs        atomic.Int64
	activeConns      atomic.Int64
	dropped          atomic.Int64
	lateOverheadSent atomic.Int64
	lateOverheadRecv atomic.Int64

	queue   snapQueue
	out     chan *metrics.Snapshot
	aggStop chan struct{}
	done    chan struct{}
	summary *metrics.Snapshot
	ran     atomic.Bool
}

// vuClient is a virtual user's handle on an HTTP client implementation.
type vuClient interface {
	do(q *hopRequest, r *hopResponse) error
	closeAll()
	abort()
}

// clientFactory creates per virtual user clients.
type clientFactory interface {
	newVU(v *vu) vuClient
	close()
}

type dataState struct {
	ds   *scenario.Dataset
	rows [][]string
	next atomic.Uint64
}

// New prepares an engine for plan.
func New(plan *scenario.Plan, opts Options) (*Engine, error) {
	if plan == nil || len(plan.Steps) == 0 {
		return nil, errors.New("engine: empty plan")
	}
	if opts.Client == "" {
		opts.Client = ClientFast
	}
	if opts.Client != ClientFast && opts.Client != ClientStd {
		return nil, fmt.Errorf("未知的 HTTP 客户端 %q，可选 fast 或 std", opts.Client)
	}
	if opts.SlowThreshold == 0 {
		opts.SlowThreshold = time.Second
	}
	if opts.SamplesPerSecond <= 0 {
		opts.SamplesPerSecond = 5
	}
	if opts.SummarySamples <= 0 {
		opts.SummarySamples = 20
	}
	if opts.SampleBodyBytes <= 0 {
		opts.SampleBodyBytes = 1024
	}
	if opts.Shards <= 0 {
		opts.Shards = runtime.GOMAXPROCS(0)
	}
	if opts.LoadShare <= 0 || opts.LoadShare > 1 {
		opts.LoadShare = 1
	}
	if opts.DataSlice.Count <= 0 {
		opts.DataSlice = DataSlice{Index: 0, Count: 1}
	}
	if opts.DataSlice.Index < 0 || opts.DataSlice.Index >= opts.DataSlice.Count {
		return nil, fmt.Errorf("engine: invalid data slice %d/%d", opts.DataSlice.Index, opts.DataSlice.Count)
	}

	e := &Engine{
		plan:    plan,
		opts:    opts,
		shards:  opts.Shards,
		stopCh:  make(chan struct{}),
		vus:     map[*vu]struct{}{},
		jobs:    make(chan int64),
		out:     make(chan *metrics.Snapshot, 64),
		aggStop: make(chan struct{}),
		done:    make(chan struct{}),
	}
	e.hardCtx, e.hardCancel = context.WithCancel(context.Background())
	var seed [8]byte
	_, _ = crand.Read(seed[:])
	e.seed = binary.LittleEndian.Uint64(seed[:])

	infos := make([]metrics.StepInfo, len(plan.Steps))
	for i, n := range plan.Steps {
		st, ok := n.(*scenario.HTTPStep)
		if !ok {
			return nil, fmt.Errorf("engine: step %d has unsupported kind %d", i, n.Kind())
		}
		e.steps = append(e.steps, newStepRT(st))
		infos[i] = metrics.StepInfo{ID: st.ID, Name: st.Name}
	}
	open := plan.Load.Mode == scenario.LoadRate
	e.collector = metrics.NewCollector(e.shards, infos, open, opts.SamplesPerSecond)
	e.summary = e.collector.NewSnapshot()

	share := opts.LoadShare
	e.prof.Store(newProfile(plan.Load.Stages, share))
	if open {
		e.maxVUs = max(1, int(math.Ceil(float64(plan.Load.MaxVUs)*share)))
	} else {
		e.maxVUs = int(math.Round(plan.Load.PeakTarget * share))
	}
	if plan.Load.MaxRequests > 0 {
		e.maxReq = max(1, int64(math.Ceil(float64(plan.Load.MaxRequests)*share)))
	}

	for _, ds := range plan.Data {
		st := &dataState{ds: ds}
		for i, row := range ds.Rows {
			if i%opts.DataSlice.Count == opts.DataSlice.Index {
				st.rows = append(st.rows, row)
			}
		}
		if ds.Used && len(st.rows) == 0 {
			return nil, fmt.Errorf("数据文件 %q 在当前切片中没有数据行", ds.Name)
		}
		if ds.Used && ds.Mode == scenario.DataUnique && e.maxVUs > len(st.rows) {
			return nil, fmt.Errorf("数据文件 %q 是 unique 模式，只有 %d 行，少于最大虚拟用户数 %d", ds.Name, len(st.rows), e.maxVUs)
		}
		e.data = append(e.data, st)
	}

	e.dialer = newDialer(e, &plan.HTTP)
	switch opts.Client {
	case ClientStd:
		e.client = newStdClient(e, e.dialer)
	default:
		e.client = newFastClient(e, e.dialer)
	}
	e.queue.notify = make(chan struct{}, 1)
	go e.queue.forward(e.out)
	return e, nil
}

// Snapshots delivers one snapshot per second (the last one covers the
// remaining partial second). The channel is closed when Run finishes.
// The consumer must keep reading; snapshots are queued, never dropped.
func (e *Engine) Snapshots() <-chan *metrics.Snapshot { return e.out }

// Summary returns the merge of all snapshots. It is nil until Run returns.
func (e *Engine) Summary() *metrics.Snapshot {
	select {
	case <-e.done:
		return e.summary
	default:
		return nil
	}
}

// Stop starts a graceful stop: no new iterations or steps start, in-flight
// requests may finish within the plan's graceful_stop time.
func (e *Engine) Stop() {
	e.stopOnce.Do(func() {
		e.stopping.Store(true)
		close(e.stopCh)
	})
}

// Abort stops immediately, interrupting in-flight requests. Interrupted
// requests are not recorded.
func (e *Engine) Abort() {
	e.aborted.Store(true)
	e.Stop()
	e.hardCancel()
	e.vusMu.Lock()
	for v := range e.vus {
		v.client.abort()
	}
	e.vusMu.Unlock()
}

// SetTarget overrides the current target (virtual users or iterations per
// second) from now until the end of the planned duration.
func (e *Engine) SetTarget(v float64) error {
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return errors.New("engine: invalid target")
	}
	if e.start == 0 {
		return errors.New("engine: not running")
	}
	v *= e.opts.LoadShare
	if e.plan.Load.Mode == scenario.LoadVUs {
		v = math.Round(v)
	}
	for {
		old := e.prof.Load()
		if e.prof.CompareAndSwap(old, old.withOverride(e.elapsed(), v)) {
			return nil
		}
	}
}

func (e *Engine) elapsed() time.Duration { return time.Duration(nanotime() - e.start) }

// Run executes the plan and blocks until it finishes, ctx is cancelled
// (graceful stop) or Abort is called.
func (e *Engine) Run(ctx context.Context) error {
	if !e.ran.CompareAndSwap(false, true) {
		return errors.New("engine: Run called twice")
	}
	e.started = time.Now()
	e.start = nanotime()
	aggDone := make(chan struct{})
	go e.aggregate(aggDone)
	stopWatch := context.AfterFunc(ctx, e.Stop)

	if e.plan.Load.Mode == scenario.LoadRate {
		e.runOpen()
	} else {
		e.runClosed()
	}
	e.Stop()

	if !waitTimeout(&e.wg, e.plan.Load.GracefulStop) {
		e.Abort()
		waitTimeout(&e.wg, 10*time.Second)
	}
	stopWatch()
	e.client.close()
	close(e.aggStop)
	<-aggDone
	e.hardCancel()
	close(e.done)
	e.queue.close()
	return nil
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	ch := make(chan struct{})
	go func() { wg.Wait(); close(ch) }()
	if d <= 0 {
		d = time.Millisecond
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-t.C:
		return false
	}
}

func (e *Engine) setTarget(v float64) { e.target.Store(math.Float64bits(v)) }

// newVU creates and registers a virtual user with the given id.
func (e *Engine) newVU(id int) *vu {
	v := newVU(e, id)
	e.vusMu.Lock()
	e.vus[v] = struct{}{}
	e.vusMu.Unlock()
	if e.aborted.Load() {
		v.client.abort()
	}
	e.wg.Add(1)
	e.activeVUs.Add(1)
	return v
}

func (e *Engine) vuExited(v *vu) {
	v.client.closeAll()
	e.vusMu.Lock()
	delete(e.vus, v)
	e.vusMu.Unlock()
	e.activeVUs.Add(-1)
	e.wg.Done()
}

// runClosed keeps the number of virtual users at the profile's target.
// Ramp-down stops the newest virtual users at their next step boundary.
func (e *Engine) runClosed() {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	var running []*vu
	for {
		target, done := e.prof.Load().targetAt(e.elapsed())
		if done {
			return
		}
		e.setTarget(target)
		want := int(math.Round(target))
		for len(running) < want && !e.stopping.Load() {
			v := e.newVU(len(running))
			running = append(running, v)
			go v.runClosed()
		}
		for len(running) > want {
			last := len(running) - 1
			running[last].stop()
			running[last] = nil
			running = running[:last]
		}
		select {
		case <-tick.C:
		case <-e.stopCh:
			return
		}
	}
}

// runOpen starts iterations at the profile's rate. Each iteration has a
// planned start time; latency of its first request is measured from that
// time, so a slow target or a busy generator cannot hide queueing delay.
func (e *Engine) runOpen() {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	nvus := 0
	k := 0.0
	for !e.stopping.Load() {
		p := e.prof.Load()
		at, ok := p.timeOf(k)
		if !ok {
			// No more iterations: let the last ones run until the end of
			// the profile instead of stopping right after dispatching them.
			if rest := p.end - e.elapsed(); rest > 0 {
				timer.Reset(rest)
				select {
				case <-timer.C:
				case <-e.stopCh:
				}
			}
			return
		}
		now := e.elapsed()
		if v, done := p.targetAt(now); !done {
			e.setTarget(v)
		}
		if at > now {
			timer.Reset(at - now)
			select {
			case <-timer.C:
			case <-e.stopCh:
				return
			}
			continue
		}
		planned := e.start + int64(at)
		k++
		select {
		case e.jobs <- planned:
			continue
		default:
		}
		if nvus < e.maxVUs {
			v := e.newVU(nvus)
			nvus++
			go v.runOpen(planned)
		} else {
			e.dropped.Add(1)
		}
	}
}

// aggregate turns recorder windows into one snapshot per second.
func (e *Engine) aggregate(done chan struct{}) {
	defer close(done)
	seq := int64(0)
	last := e.start
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	emit := func(final bool) {
		now := nanotime()
		snap := e.collector.NewSnapshot()
		snap.Seq = seq
		snap.Time = e.started.Add(time.Duration(last - e.start))
		snap.Duration = time.Duration(now - last)
		snap.ActiveVUs = e.activeVUs.Load()
		snap.ActiveConns = e.activeConns.Load()
		snap.Target = math.Float64frombits(e.target.Load())
		snap.DroppedIterations = e.dropped.Swap(0)
		e.collector.Collect(snap, e.opts.SamplesPerSecond)
		snap.OverheadSent += e.lateOverheadSent.Swap(0)
		snap.OverheadReceived += e.lateOverheadRecv.Swap(0)
		last = now
		seq++
		if final && snap.Total.Requests == 0 && snap.Iterations == 0 && snap.DroppedIterations == 0 && snap.Duration < 200*time.Millisecond {
			// Do not report an empty sliver at the end.
			e.summary.Merge(snap, metrics.MergeIntervals)
			return
		}
		_ = e.summary.Merge(snap, metrics.MergeIntervals)
		e.summary.TrimSamples(e.opts.SummarySamples)
		e.queue.push(snap)
	}
	for {
		select {
		case <-timer.C:
			emit(false)
			next := time.Duration(seq+1)*time.Second - e.elapsed()
			timer.Reset(max(next, time.Millisecond))
		case <-e.aggStop:
			emit(true)
			return
		}
	}
}

// snapQueue decouples the aggregator from a slow snapshot consumer.
type snapQueue struct {
	mu     sync.Mutex
	items  []*metrics.Snapshot
	closed bool
	notify chan struct{}
}

func (q *snapQueue) push(s *metrics.Snapshot) {
	q.mu.Lock()
	q.items = append(q.items, s)
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *snapQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *snapQueue) forward(out chan<- *metrics.Snapshot) {
	for {
		q.mu.Lock()
		items, closed := q.items, q.closed
		q.items = nil
		q.mu.Unlock()
		for _, s := range items {
			out <- s
		}
		if closed && len(items) == 0 {
			close(out)
			return
		}
		if len(items) == 0 {
			<-q.notify
		}
	}
}
