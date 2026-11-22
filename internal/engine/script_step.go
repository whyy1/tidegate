package engine

import (
	"errors"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
)

func (v *vu) runScript(s *scenario.ScriptStepPlan, planned int64) (bool, metrics.ErrorKind) {
	if !s.IsGroup && !v.takeRequestSlot() {
		return false, metrics.ErrNone
	}
	start := nanotime()
	if planned != 0 {
		start = planned
	}
	err := s.Prog.Exec(&v.env)
	end := nanotime()
	if v.eng.aborted.Load() {
		if !s.IsGroup {
			v.finishRequestSlot()
		}
		return false, metrics.ErrNone
	}

	kind := metrics.ErrNone
	status := 200
	var msg string
	if err != nil {
		status = 0
		msg = err.Error()
		if errors.Is(err, scenario.ErrScriptAssert) {
			kind = metrics.ErrAssertion
		} else {
			kind = metrics.ErrOther
		}
	}

	v.recordGeneric(s.Index, status, kind, time.Duration(max(0, end-start)), 0, 0, 0, 0, false, dialTiming{}, 0, 0, end)
	if kind != metrics.ErrNone || (v.eng.opts.SlowThreshold >= 0 && time.Duration(end-start) >= v.eng.opts.SlowThreshold) {
		v.maybeSampleGeneric(s.Index, s.ID, "SCRIPT", "", nil, s.Prog.Source, status, nil, nil, false, kind, msg, start, end)
	}
	if !s.IsGroup {
		v.finishRequestSlot()
	}
	return true, kind
}

func (v *vu) runScriptDebug(s *scenario.ScriptStepPlan) DebugStepResult {
	res := DebugStepResult{
		StepID:   s.ID,
		StepName: s.Name,
		ActualRequest: DebugActualRequest{
			Method: "SCRIPT",
			Body:   s.Prog.Source,
		},
	}
	start := nanotime()
	out, err := s.Prog.ExecDebug(&v.env)
	end := nanotime()
	res.Timing = genericDebugTiming(start, 0, 0, end, false, dialTiming{}, err)

	passed := err == nil
	firstKind := metrics.ErrNone
	var firstMsg string
	if err != nil {
		firstKind = metrics.ErrOther
		if errors.Is(err, scenario.ErrScriptAssert) {
			firstKind = metrics.ErrAssertion
		}
		firstMsg = err.Error()
	}

	if out != nil {
		if len(out.WrittenVars) > 0 {
			res.ExtractedVars = out.WrittenVars
		}
		if len(out.Asserts) > 0 {
			res.Asserts = make([]DebugAssertResult, 0, len(out.Asserts))
			for _, a := range out.Asserts {
				res.Asserts = append(res.Asserts, DebugAssertResult{
					Type:    "script",
					Desc:    a.Message,
					Passed:  a.Passed,
					Message: a.Message,
				})
				if !a.Passed {
					passed = false
					if firstKind == metrics.ErrNone {
						firstKind = metrics.ErrAssertion
						firstMsg = "脚本断言失败：" + a.Message
					}
				}
			}
		}
	}

	res.Passed = passed
	if passed {
		res.Status = 200
	} else {
		res.ErrorKind = firstKind.String()
		res.Error = firstMsg
	}
	return res
}
