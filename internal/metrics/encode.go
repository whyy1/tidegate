package metrics

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// Binary snapshot format (little endian varints), used for node reports:
//
//	magic "TGS" + version byte
//	header fields, optional scheduling delay histogram, overhead bytes
//	steps: id, name, counters, sparse status codes and error kinds,
//	       sparse histogram, bytes, phases
//	samples
//
// Snapshot.Total is not encoded; it is rebuilt from the steps on decode.
// Histograms are stored as (index delta, count) pairs of non-zero buckets.

const encodingVersion = 1

var magic = [3]byte{'T', 'G', 'S'}

var histCountsLen = sync.OnceValue(func() int { return len(NewHistogram().counts()) })

// MarshalBinary encodes the snapshot.
func (s *Snapshot) MarshalBinary() ([]byte, error) {
	return s.AppendBinary(make([]byte, 0, 1024))
}

// AppendBinary appends the encoded snapshot to b.
func (s *Snapshot) AppendBinary(b []byte) ([]byte, error) {
	b = append(b, magic[:]...)
	b = append(b, encodingVersion)
	b = binary.AppendVarint(b, s.Seq)
	b = appendTime(b, s.Time)
	b = binary.AppendVarint(b, int64(s.Duration))
	b = binary.AppendVarint(b, s.ActiveVUs)
	b = binary.AppendVarint(b, s.ActiveConns)
	b = binary.LittleEndian.AppendUint64(b, math.Float64bits(s.Target))
	b = binary.AppendVarint(b, s.Iterations)
	b = binary.AppendVarint(b, s.DroppedIterations)
	if s.SchedDelay != nil {
		b = append(b, 1)
		b = appendHist(b, s.SchedDelay)
	} else {
		b = append(b, 0)
	}
	b = binary.AppendVarint(b, s.OverheadSent)
	b = binary.AppendVarint(b, s.OverheadReceived)
	b = binary.AppendUvarint(b, uint64(len(s.Steps)))
	for i := range s.Steps {
		b = appendStep(b, &s.Steps[i])
	}
	b = binary.AppendUvarint(b, uint64(len(s.Samples)))
	for i := range s.Samples {
		b = appendSample(b, &s.Samples[i])
	}
	return b, nil
}

func appendTime(b []byte, t time.Time) []byte {
	if t.IsZero() {
		return binary.AppendVarint(b, 0)
	}
	return binary.AppendVarint(b, t.UnixNano())
}

func appendString(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

func appendHist(b []byte, h *Histogram) []byte {
	counts := h.counts()
	n := 0
	for _, c := range counts {
		if c != 0 {
			n++
		}
	}
	b = binary.AppendUvarint(b, uint64(n))
	last := 0
	for i, c := range counts {
		if c != 0 {
			b = binary.AppendUvarint(b, uint64(i-last))
			b = binary.AppendUvarint(b, uint64(c))
			last = i
		}
	}
	return b
}

func appendStep(b []byte, s *StepStats) []byte {
	b = appendString(b, s.ID)
	b = appendString(b, s.Name)
	b = binary.AppendVarint(b, s.Requests)
	b = binary.AppendUvarint(b, uint64(len(s.Status)))
	for code, n := range s.Status {
		b = binary.AppendUvarint(b, uint64(code))
		b = binary.AppendVarint(b, n)
	}
	b = binary.AppendUvarint(b, uint64(NumErrorKinds))
	for _, n := range s.Errors {
		b = binary.AppendVarint(b, n)
	}
	b = appendHist(b, s.Latency)
	b = binary.AppendVarint(b, s.BytesSent)
	b = binary.AppendVarint(b, s.BytesReceived)
	b = binary.AppendUvarint(b, uint64(NumPhases))
	for p := range s.PhaseSum {
		b = binary.AppendVarint(b, int64(s.PhaseSum[p]))
		b = binary.AppendVarint(b, s.PhaseCount[p])
	}
	return b
}

func appendHeaders(b []byte, hs []Header) []byte {
	b = binary.AppendUvarint(b, uint64(len(hs)))
	for _, h := range hs {
		b = appendString(b, h.Name)
		b = appendString(b, h.Value)
	}
	return b
}

func appendSample(b []byte, s *Sample) []byte {
	b = append(b, byte(s.Kind))
	b = binary.AppendVarint(b, int64(s.Step))
	b = appendString(b, s.StepID)
	b = appendTime(b, s.Time)
	b = appendString(b, s.Method)
	b = appendString(b, s.URL)
	b = binary.AppendVarint(b, int64(s.Status))
	b = appendString(b, s.Error)
	b = binary.AppendVarint(b, int64(s.Duration))
	b = appendHeaders(b, s.RequestHeaders)
	b = appendHeaders(b, s.ResponseHeaders)
	b = appendString(b, s.Body)
	if s.BodyTruncated {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	return b
}

// ErrBadEncoding is returned for malformed binary snapshots.
var ErrBadEncoding = errors.New("metrics: malformed snapshot encoding")

type decoder struct {
	b   []byte
	err error
}

func (d *decoder) fail() {
	if d.err == nil {
		d.err = ErrBadEncoding
	}
}

func (d *decoder) byte() byte {
	if d.err != nil || len(d.b) < 1 {
		d.fail()
		return 0
	}
	v := d.b[0]
	d.b = d.b[1:]
	return v
}

func (d *decoder) varint() int64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Varint(d.b)
	if n <= 0 {
		d.fail()
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *decoder) uvarint() uint64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.fail()
		return 0
	}
	d.b = d.b[n:]
	return v
}

// count reads a length prefix and checks it against the remaining input so
// a corrupt prefix cannot trigger a huge allocation.
func (d *decoder) count(minBytesPerItem int) int {
	n := d.uvarint()
	if d.err != nil {
		return 0
	}
	if n > uint64(len(d.b)/max(minBytesPerItem, 1)) {
		d.fail()
		return 0
	}
	return int(n)
}

func (d *decoder) string() string {
	n := d.count(1)
	if d.err != nil {
		return ""
	}
	s := string(d.b[:n])
	d.b = d.b[n:]
	return s
}

func (d *decoder) time() time.Time {
	v := d.varint()
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(0, v)
}

func (d *decoder) hist() *Histogram {
	n := d.count(2)
	if d.err != nil {
		return nil
	}
	counts := make([]int64, histCountsLen())
	idx := 0
	for i := 0; i < n; i++ {
		delta := d.uvarint()
		c := d.uvarint()
		if d.err != nil {
			return nil
		}
		if delta > uint64(len(counts)) || (i > 0 && delta == 0) || c > math.MaxInt64 {
			d.fail()
			return nil
		}
		idx += int(delta)
		if idx >= len(counts) {
			d.fail()
			return nil
		}
		counts[idx] = int64(c)
	}
	return histogramFromCounts(counts)
}

func (d *decoder) headers() []Header {
	n := d.count(2)
	if n == 0 {
		return nil
	}
	hs := make([]Header, 0, n)
	for i := 0; i < n && d.err == nil; i++ {
		hs = append(hs, Header{Name: d.string(), Value: d.string()})
	}
	return hs
}

// UnmarshalBinary decodes a snapshot produced by MarshalBinary.
func (s *Snapshot) UnmarshalBinary(data []byte) error {
	if len(data) < 4 || [3]byte(data[:3]) != magic {
		return ErrBadEncoding
	}
	if data[3] != encodingVersion {
		return fmt.Errorf("metrics: unsupported snapshot encoding version %d", data[3])
	}
	d := &decoder{b: data[4:]}
	out := Snapshot{}
	out.Seq = d.varint()
	out.Time = d.time()
	out.Duration = time.Duration(d.varint())
	out.ActiveVUs = d.varint()
	out.ActiveConns = d.varint()
	if len(d.b) < 8 {
		return ErrBadEncoding
	}
	out.Target = math.Float64frombits(binary.LittleEndian.Uint64(d.b))
	d.b = d.b[8:]
	out.Iterations = d.varint()
	out.DroppedIterations = d.varint()
	if d.byte() == 1 {
		out.SchedDelay = d.hist()
	}
	out.OverheadSent = d.varint()
	out.OverheadReceived = d.varint()
	nsteps := d.count(8)
	out.Steps = make([]StepStats, 0, nsteps)
	for i := 0; i < nsteps && d.err == nil; i++ {
		st := newStepStats(d.string(), d.string())
		st.Requests = d.varint()
		ns := d.count(2)
		for j := 0; j < ns && d.err == nil; j++ {
			code := d.uvarint()
			n := d.varint()
			st.Status[int(code)] = n
		}
		nk := d.count(1)
		for k := 0; k < nk && d.err == nil; k++ {
			n := d.varint()
			if k < int(NumErrorKinds) {
				st.Errors[k] = n
			}
		}
		if h := d.hist(); h != nil {
			st.Latency = h
		}
		st.BytesSent = d.varint()
		st.BytesReceived = d.varint()
		np := d.count(2)
		for p := 0; p < np && d.err == nil; p++ {
			sum, cnt := d.varint(), d.varint()
			if p < int(NumPhases) {
				st.PhaseSum[p] = time.Duration(sum)
				st.PhaseCount[p] = cnt
			}
		}
		out.Steps = append(out.Steps, st)
	}
	nsamples := d.count(10)
	for i := 0; i < nsamples && d.err == nil; i++ {
		var sm Sample
		sm.Kind = SampleKind(d.byte())
		sm.Step = int(d.varint())
		sm.StepID = d.string()
		sm.Time = d.time()
		sm.Method = d.string()
		sm.URL = d.string()
		sm.Status = int(d.varint())
		sm.Error = d.string()
		sm.Duration = time.Duration(d.varint())
		sm.RequestHeaders = d.headers()
		sm.ResponseHeaders = d.headers()
		sm.Body = d.string()
		sm.BodyTruncated = d.byte() == 1
		if int(sm.Kind) >= NumSampleKinds {
			d.fail()
		}
		out.Samples = append(out.Samples, sm)
	}
	if d.err != nil {
		return d.err
	}
	if len(d.b) != 0 {
		return ErrBadEncoding
	}
	out.ComputeTotal()
	*s = out
	return nil
}
