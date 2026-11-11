package metrics

import (
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
)

// Histogram geometry shared by every latency histogram so that any two can
// be merged: 1 microsecond to 1 hour with 3 significant digits.
const (
	HistLowest  = 1             // microseconds
	HistHighest = 3_600_000_000 // one hour in microseconds
	HistSigFigs = 3
)

// Histogram is an HDR latency histogram in microseconds. It is not safe for
// concurrent use.
type Histogram struct {
	h *hdr.Histogram
}

// NewHistogram returns an empty histogram with the shared geometry.
func NewHistogram() *Histogram {
	return &Histogram{h: hdr.New(HistLowest, HistHighest, HistSigFigs)}
}

// RecordMicros records one value in microseconds, clamping it into range.
func (h *Histogram) RecordMicros(us int64) {
	if us < HistLowest {
		us = HistLowest
	} else if us > HistHighest {
		us = HistHighest
	}
	_ = h.h.RecordValue(us)
}

// Record records one duration.
func (h *Histogram) Record(d time.Duration) {
	h.RecordMicros(int64(d / time.Microsecond))
}

// Merge adds all values of o into h.
func (h *Histogram) Merge(o *Histogram) {
	if o == nil || o.h.TotalCount() == 0 {
		return
	}
	h.h.Merge(o.h)
}

// Count returns the number of recorded values.
func (h *Histogram) Count() int64 { return h.h.TotalCount() }

// Reset removes all values.
func (h *Histogram) Reset() {
	if h.h.TotalCount() != 0 {
		h.h.Reset()
	}
}

// Clone returns an independent copy.
func (h *Histogram) Clone() *Histogram { return &Histogram{h: h.h.Clone()} }

// ValueAtPercentile returns the value at percentile p (0-100) in microseconds.
func (h *Histogram) ValueAtPercentile(p float64) int64 {
	if h.h.TotalCount() == 0 {
		return 0
	}
	return h.h.ValueAtPercentile(p)
}

// Max returns the largest recorded value in microseconds.
func (h *Histogram) Max() int64 {
	if h.h.TotalCount() == 0 {
		return 0
	}
	return h.h.Max()
}

// Mean returns the mean in microseconds.
func (h *Histogram) Mean() float64 { return h.h.Mean() }

// LatencyStats are the computed values reported in JSON and summaries.
// All durations are in milliseconds.
type LatencyStats struct {
	Count int64   `json:"count"`
	P50   float64 `json:"p50_ms"`
	P90   float64 `json:"p90_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
	Mean  float64 `json:"mean_ms"`
}

var statPercentiles = []float64{50, 90, 95, 99}

// Stats computes percentiles, max and mean in one pass over the histogram.
func (h *Histogram) Stats() LatencyStats {
	n := h.h.TotalCount()
	if n == 0 {
		return LatencyStats{}
	}
	v := h.h.ValueAtPercentilesSlice(statPercentiles)
	return LatencyStats{
		Count: n,
		P50:   usToMs(v[0]),
		P90:   usToMs(v[1]),
		P95:   usToMs(v[2]),
		P99:   usToMs(v[3]),
		Max:   usToMs(h.h.Max()),
		Mean:  float64(int64(h.h.Mean()*1000)) / 1e6,
	}
}

func usToMs(us int64) float64 { return float64(us) / 1000 }

// counts exposes the dense counts for encoding. The slice is a copy.
func (h *Histogram) counts() []int64 { return h.h.Export().Counts }

// histogramFromCounts rebuilds a histogram with the shared geometry.
func histogramFromCounts(counts []int64) *Histogram {
	return &Histogram{h: hdr.Import(&hdr.Snapshot{
		LowestTrackableValue:  HistLowest,
		HighestTrackableValue: HistHighest,
		SignificantFigures:    HistSigFigs,
		Counts:                counts,
	})}
}
