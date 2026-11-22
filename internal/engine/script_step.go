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

func classifyScriptErr(err error) metrics.ErrorKind {
	if errors.Is(err, scenario.ErrScriptAssert) {
		return metrics.ErrAssertion
	}
	return metrics.ErrOther
}

func applyScriptDebugOutcome(prefix string, out *scenario.ScriptOutcome, err error, res *DebugStepResult, passed bool, firstKind metrics.ErrorKind, firstMsg string) (bool, metrics.ErrorKind, string) {
	if err != nil {
		passed = false
		if firstKind == metrics.ErrNone {
			firstKind = classifyScriptErr(err)
			firstMsg = err.Error()
		}
	}
	if out != nil {
		if len(out.WrittenVars) > 0 {
			if res.ExtractedVars == nil {
				res.ExtractedVars = make(map[string]string, len(out.WrittenVars))
			}
			for k, val := range out.WrittenVars {
				res.ExtractedVars[k] = val
			}
		}
		for _, a := range out.Asserts {
			desc := a.Message
			if prefix != "" {
				desc = prefix + "：" + a.Message
			}
			res.Asserts = append(res.Asserts, DebugAssertResult{
				Type:    "script",
				Desc:    desc,
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
	return passed, firstKind, firstMsg
}

func (v *vu) execStepPreScriptLoad(prog *scenario.ScriptProgram) (metrics.ErrorKind, string) {
	if prog == nil {
		return metrics.ErrNone, ""
	}
	if err := prog.Exec(&v.env); err != nil {
		return classifyScriptErr(err), err.Error()
	}
	return metrics.ErrNone, ""
}

func (v *vu) execStepPostScriptLoad(prog *scenario.ScriptProgram, view *scenario.ResponseView, kind metrics.ErrorKind, msg string) (metrics.ErrorKind, string) {
	if prog == nil {
		return kind, msg
	}
	sr := scenario.ScriptResponse{
		Status: view.Status,
		Body:   view.Body,
		Header: view.Header,
	}
	if err := prog.ExecWithResponse(&v.env, &sr); err != nil && kind == metrics.ErrNone {
		return classifyScriptErr(err), err.Error()
	}
	return kind, msg
}

func (v *vu) execStepPreScriptDebug(prog *scenario.ScriptProgram, res *DebugStepResult) bool {
	if prog == nil {
		return true
	}
	out, err := prog.ExecDebug(&v.env)
	passed, firstKind, firstMsg := applyScriptDebugOutcome("前置脚本", out, err, res, true, metrics.ErrNone, "")
	if !passed {
		res.Passed = false
		res.ErrorKind = firstKind.String()
		res.Error = firstMsg
	}
	return passed
}

func (v *vu) execStepPostScriptDebug(prog *scenario.ScriptProgram, view *scenario.ResponseView, res *DebugStepResult, passed bool, firstKind metrics.ErrorKind, firstMsg string) (bool, metrics.ErrorKind, string) {
	if prog == nil {
		return passed, firstKind, firstMsg
	}
	sr := scenario.ScriptResponse{
		Status: view.Status,
		Body:   view.Body,
		Header: view.Header,
	}
	out, err := prog.ExecDebugWithResponse(&v.env, &sr)
	return applyScriptDebugOutcome("后置脚本", out, err, res, passed, firstKind, firstMsg)
}

func (v *vu) execProtoPreScriptLoad(stepIdx int, stepID, proto string, prog *scenario.ScriptProgram, planned int64) (bool, metrics.ErrorKind) {
	if prog == nil {
		return false, metrics.ErrNone
	}
	kind, msg := v.execStepPreScriptLoad(prog)
	if kind == metrics.ErrNone {
		return false, metrics.ErrNone
	}
	now := nanotime()
	start := now
	if planned != 0 {
		start = planned
	}
	v.recordGeneric(stepIdx, 0, kind, time.Duration(now-start), 0, 0, 0, 0, false, dialTiming{}, 0, 0, now)
	v.maybeSampleGeneric(stepIdx, stepID, proto, "", nil, "", 0, nil, nil, false, kind, msg, start, now)
	v.finishRequestSlot()
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

	passed, firstKind, firstMsg := applyScriptDebugOutcome("", out, err, &res, true, metrics.ErrNone, "")

	res.Passed = passed
	if passed {
		res.Status = 200
	} else {
		res.ErrorKind = firstKind.String()
		res.Error = firstMsg
	}
	return res
}
