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

var fixedBandEdgesUs = []int64{
	500,     // <0.5ms
	1000,    // 0.5-1ms
	2000,    // 1-2ms
	5000,    // 2-5ms
	10000,   // 5-10ms
	25000,   // 10-25ms
	50000,   // 25-50ms
	100000,  // 50-100ms
	250000,  // 100-250ms
	500000,  // 250-500ms
	1000000, // 500ms-1s
}

// FixedBands returns counts grouped into 12 fixed logarithmic latency bands in milliseconds:
// <0.5, 0.5-1, 1-2, 2-5, 5-10, 10-25, 25-50, 50-100, 100-250, 250-500, 500-1000, >=1000.
func (h *Histogram) FixedBands() []int64 {
	bands := make([]int64, len(fixedBandEdgesUs)+1)
	if h == nil || h.h.TotalCount() == 0 {
		return bands
	}
	for _, bar := range h.h.Distribution() {
		if bar.Count == 0 {
			continue
		}
		mid := bar.From + (bar.To-bar.From)/2
		idx := len(fixedBandEdgesUs)
		for i, edge := range fixedBandEdgesUs {
			if mid < edge {
				idx = i
				break
			}
		}
		bands[idx] += bar.Count
	}
	return bands
}

// LatencyBucket describes one bucket of a latency histogram distribution.
type LatencyBucket struct {
	LowMs  float64 `json:"low_ms"`
	HighMs float64 `json:"high_ms"`
	Count  int64   `json:"count"`
}

// Distribution buckets recorded latencies into up to maxBins contiguous bins.
func (h *Histogram) Distribution(maxBins int) []LatencyBucket {
	if h == nil || h.h.TotalCount() == 0 {
		return []LatencyBucket{}
	}
	if maxBins <= 0 {
		maxBins = 24
	}
	bars := h.h.Distribution()
	var nonZero []hdr.Bar
	for _, b := range bars {
		if b.Count > 0 {
			nonZero = append(nonZero, b)
		}
	}
	if len(nonZero) == 0 {
		return []LatencyBucket{}
	}
	minUs := nonZero[0].From
	maxUs := nonZero[len(nonZero)-1].To
	if maxUs <= minUs {
		return []LatencyBucket{{
			LowMs:  round3(usToMs(minUs)),
			HighMs: round3(usToMs(maxUs)),
			Count:  h.h.TotalCount(),
		}}
	}
	if len(nonZero) <= maxBins {
		out := make([]LatencyBucket, 0, len(nonZero))
		for _, b := range nonZero {
			out = append(out, LatencyBucket{
				LowMs:  round3(usToMs(b.From)),
				HighMs: round3(usToMs(b.To)),
				Count:  b.Count,
			})
		}
		return out
	}

	step := float64(maxUs-minUs) / float64(maxBins)
	if step <= 0 {
		step = 1
	}
	out := make([]LatencyBucket, maxBins)
	for i := 0; i < maxBins; i++ {
		lo := float64(minUs) + float64(i)*step
		hi := float64(minUs) + float64(i+1)*step
		if i == maxBins-1 {
			hi = float64(maxUs)
		}
		out[i] = LatencyBucket{
			LowMs:  round3(lo / 1000),
			HighMs: round3(hi / 1000),
		}
	}
	for _, b := range nonZero {
		mid := float64(b.From) + float64(b.To-b.From)/2
		idx := int((mid - float64(minUs)) / step)
		if idx < 0 {
			idx = 0
		} else if idx >= maxBins {
			idx = maxBins - 1
		}
		out[idx].Count += b.Count
	}
	return out
}

// histogramFromCounts rebuilds a histogram with the shared geometry.
func histogramFromCounts(counts []int64) *Histogram {
	return &Histogram{h: hdr.Import(&hdr.Snapshot{
		LowestTrackableValue:  HistLowest,
		HighestTrackableValue: HistHighest,
		SignificantFigures:    HistSigFigs,
		Counts:                counts,
	})}
}
