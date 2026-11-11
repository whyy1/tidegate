package engine

import (
	"math"
	"time"

	"tidegate/internal/scenario"
)

// segment is one linear piece of the load profile: the target moves from
// from to to over [start, start+dur). cum is the integral of the target up
// to start, used by the open model to place iterations.
type segment struct {
	start, dur time.Duration
	from, to   float64
	cum        float64
}

// profile is a piecewise linear target over time, starting at 0.
type profile struct {
	segs []segment
	end  time.Duration
}

func newProfile(stages []scenario.StageSpec, scale float64) *profile {
	p := &profile{}
	prev, t, cum := 0.0, time.Duration(0), 0.0
	for _, st := range stages {
		target := st.Target * scale
		if st.Duration > 0 {
			s := segment{start: t, dur: st.Duration, from: prev, to: target, cum: cum}
			p.segs = append(p.segs, s)
			cum += (prev + target) / 2 * st.Duration.Seconds()
			t += st.Duration
		}
		prev = target
	}
	p.end = t
	return p
}

// withOverride returns a profile that follows p until at and then holds
// value until p's end. Used by SetTarget.
func (p *profile) withOverride(at time.Duration, value float64) *profile {
	q := &profile{end: p.end}
	for _, s := range p.segs {
		if s.start >= at {
			break
		}
		if s.start+s.dur > at {
			s.to = s.valueAt(at)
			s.dur = at - s.start
		}
		q.segs = append(q.segs, s)
	}
	cum := q.cumAt(at)
	if at < p.end {
		q.segs = append(q.segs, segment{start: at, dur: p.end - at, from: value, to: value, cum: cum})
	}
	return q
}

func (s *segment) valueAt(t time.Duration) float64 {
	if s.dur <= 0 {
		return s.to
	}
	f := float64(t-s.start) / float64(s.dur)
	return s.from + (s.to-s.from)*f
}

// targetAt returns the target at elapsed time t; done is true at or after
// the end of the profile.
func (p *profile) targetAt(t time.Duration) (v float64, done bool) {
	if t >= p.end {
		return 0, true
	}
	for i := range p.segs {
		s := &p.segs[i]
		if t < s.start+s.dur {
			return s.valueAt(t), false
		}
	}
	return 0, true
}

// cumAt is the number of iterations the open model should have started by t.
func (p *profile) cumAt(t time.Duration) float64 {
	cum := 0.0
	for i := range p.segs {
		s := &p.segs[i]
		if t <= s.start {
			break
		}
		x := min(t, s.start+s.dur) - s.start
		cum = s.cum + (s.from+s.valueAt(s.start+x))/2*x.Seconds()
	}
	return cum
}

// timeOf returns the time at which the integral of the target reaches k,
// i.e. the planned start of iteration k. ok is false when k is not reached
// before the end of the profile.
func (p *profile) timeOf(k float64) (time.Duration, bool) {
	for i := range p.segs {
		s := &p.segs[i]
		total := (s.from + s.to) / 2 * s.dur.Seconds()
		if k > s.cum+total {
			continue
		}
		c := k - s.cum
		if c <= 0 {
			return s.start, true
		}
		// Solve a*x^2 + b*x = c with a = slope/2, b = from, in the stable
		// form x = 2c / (b + sqrt(b^2 + 4ac)).
		a := (s.to - s.from) / s.dur.Seconds() / 2
		b := s.from
		var x float64
		if a == 0 {
			if b <= 0 {
				continue
			}
			x = c / b
		} else {
			disc := b*b + 4*a*c
			if disc < 0 {
				continue
			}
			den := b + math.Sqrt(disc)
			if den <= 0 {
				continue
			}
			x = 2 * c / den
		}
		d := s.start + time.Duration(x*float64(time.Second))
		if d > s.start+s.dur {
			d = s.start + s.dur
		}
		return d, true
	}
	return 0, false
}
