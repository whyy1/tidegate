package controller

import (
	"context"
	"math"

	"tidegate/internal/scenario"
)

// RunEstimate reports pre-run capacity, estimated traffic rate, online cluster
// headroom, and actionable warnings before launching a load test.
type RunEstimate struct {
	LoadMode           string   `json:"load_mode"`
	PeakTarget         float64  `json:"peak_target"`
	MaxVUs             int      `json:"max_vus"`
	StepCount          int      `json:"step_count"`
	AvgRequestBytes    int64    `json:"avg_request_bytes"`
	EstimatedPeakRPS   float64  `json:"estimated_peak_rps"`
	EstimatedUplinkBps float64  `json:"estimated_uplink_bps"`
	OnlineAgents       int      `json:"online_agents"`
	TotalCPUCores      int      `json:"total_cpu_cores"`
	AvailableUplinkBps uint64   `json:"available_uplink_bps"`
	Warnings           []string `json:"warnings,omitempty"`
}

// EstimateRun compiles the requested scenario or API without requiring connected
// agents, estimates peak RPS and wire bandwidth, compares against connected
// agent capacity, and returns actionable warnings.
func (c *Controller) EstimateRun(ctx context.Context, projectID string, req StartRunRequest) (*RunEstimate, error) {
	resolved, _, err := c.resolveRunDefinition(projectID, req)
	if err != nil {
		return nil, err
	}
	plan, err := scenario.Compile(cloneDefinition(resolved.Definition), resolved.CompileOptions())
	if err != nil {
		return nil, userErr(ErrInvalidInput, err.Error(), err)
	}
	pol, err := c.GetSecurityPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if err := ValidatePlanTargets(ctx, plan, pol); err != nil {
		return nil, err
	}

	expectedReqs, leafSteps := estimateNodesProfile(plan.Steps)
	stepCount := len(leafSteps)
	if stepCount == 0 {
		stepCount = len(plan.Steps)
	}
	if expectedReqs < 1 {
		expectedReqs = float64(max(1, stepCount))
	}
	avgReqBytes := estimateLeafAvgRequestBytes(leafSteps)

	var estPeakRPS float64
	if plan.Load.Mode == scenario.LoadRate {
		estPeakRPS = plan.Load.PeakTarget * expectedReqs
	} else {
		cycleSec := math.Max(plan.ThinkMax.Seconds()+0.05, 0.05)
		estPeakRPS = plan.Load.PeakTarget * expectedReqs / cycleSec
	}
	estUplinkBps := estPeakRPS * float64(avgReqBytes) * 8

	agents, err := c.Agents(ctx)
	if err != nil {
		return nil, err
	}
	var totalCPU int
	var availBps uint64
	for _, ag := range agents {
		totalCPU += ag.CPUCores
		if ag.BandwidthBps > 0 {
			availBps += uint64(ag.BandwidthBps)
			continue
		}
		var maxNIC uint64
		for _, iface := range ag.Interfaces {
			if iface.LinkSpeedBps > maxNIC {
				maxNIC = iface.LinkSpeedBps
			}
		}
		availBps += maxNIC
	}

	var warnings []string
	if len(agents) == 0 {
		warnings = append(warnings, "当前没有在线的发压节点，无法直接启动压测")
	} else {
		if availBps == 0 {
			warnings = append(warnings, "未检测到网卡协商速率且未手动设置节点带宽上限，建议在节点管理中填写实际可用带宽")
		} else if estUplinkBps > 0.8*float64(availBps) {
			warnings = append(warnings, "预估上行带宽超过可用上限的 80%，建议降低目标速率、开启限速或增加发压节点")
		}
		if totalCPU > 0 && plan.Load.MaxVUs > totalCPU*5000 {
			warnings = append(warnings, "单核并发虚拟用户数较高，建议留意发压机 CPU 与临时端口占用")
		}
	}

	return &RunEstimate{
		LoadMode:           plan.Load.Mode.String(),
		PeakTarget:         plan.Load.PeakTarget,
		MaxVUs:             plan.Load.MaxVUs,
		StepCount:          stepCount,
		AvgRequestBytes:    avgReqBytes,
		EstimatedPeakRPS:   math.Round(estPeakRPS*100) / 100,
		EstimatedUplinkBps: math.Round(estUplinkBps*100) / 100,
		OnlineAgents:       len(agents),
		TotalCPUCores:      totalCPU,
		AvailableUplinkBps: availBps,
		Warnings:           warnings,
	}, nil
}

func estimateNodesProfile(nodes []scenario.Node) (float64, []*scenario.HTTPStep) {
	var expectedReqs float64
	var leaves []*scenario.HTTPStep
	for _, node := range nodes {
		switch st := node.(type) {
		case *scenario.HTTPStep:
			expectedReqs += 1
			leaves = append(leaves, st)
		case *scenario.GroupNode:
			r, sub := estimateNodesProfile(st.Children)
			expectedReqs += r
			leaves = append(leaves, sub...)
		case *scenario.LoopNode:
			r, sub := estimateNodesProfile(st.Children)
			mult := 1
			if st.Count > 0 {
				mult = min(st.Count, 100)
			}
			expectedReqs += r * float64(mult)
			leaves = append(leaves, sub...)
		case *scenario.IfNode:
			rt, stThen := estimateNodesProfile(st.Then)
			re, stElse := estimateNodesProfile(st.Else)
			leaves = append(leaves, stThen...)
			leaves = append(leaves, stElse...)
			expectedReqs += (rt + re) / 2
		case *scenario.WeightedNode:
			totalW := st.TotalWeight
			if totalW <= 0 {
				totalW = 1
			}
			for _, br := range st.Branches {
				rb, sub := estimateNodesProfile(br.Steps)
				expectedReqs += rb * (br.Weight / totalW)
				leaves = append(leaves, sub...)
			}
		}
	}
	return expectedReqs, leaves
}

func estimateLeafAvgRequestBytes(leaves []*scenario.HTTPStep) int64 {
	if len(leaves) == 0 {
		return 1
	}
	var total int64
	for _, hs := range leaves {
		methodLen := int64(len(hs.Method))
		if methodLen == 0 {
			methodLen = 3
		}
		var uriLen, hostLen int64
		switch {
		case hs.URL.Static != nil:
			uriLen = int64(len(hs.URL.Static.RequestURI))
			hostLen = int64(len(hs.URL.Static.Host))
		case hs.URL.Path != nil:
			uriLen = int64(templateEstLen(hs.URL.Path))
			if hs.URL.Origin != nil {
				hostLen = int64(len(hs.URL.Origin.Host))
			}
		case hs.URL.Full != nil:
			uriLen = int64(templateEstLen(hs.URL.Full))
		}
		if uriLen == 0 {
			uriLen = 1
		}

		// Request line + Host header + default client headers overhead.
		stepBytes := methodLen + 1 + uriLen + int64(len(" HTTP/1.1\r\n")) + int64(len("Host: ")) + hostLen + 2 + 64
		for _, h := range hs.Headers {
			stepBytes += int64(len(h.Name) + 2 + templateEstLen(h.Value) + 2)
		}
		if hs.BasicUser != nil || hs.BasicPass != nil || hs.OAuth2 != nil {
			stepBytes += 48
		}
		if hs.Body != nil {
			stepBytes += int64(templateEstLen(hs.Body)) + 24
		}
		total += stepBytes
	}
	avg := total / int64(len(leaves))
	if avg < 1 {
		return 1
	}
	return avg
}

func templateEstLen(t *scenario.Template) int {
	if t == nil {
		return 0
	}
	if t.Static() {
		return len(t.Literal())
	}
	return len(t.Source())
}
