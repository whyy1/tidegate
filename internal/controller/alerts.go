package controller

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
	"tidegate/internal/sysmon"
)

const (
	cpuThreshold        = 0.85
	memoryThreshold     = 0.85
	portThreshold       = 0.90
	bandwidthThreshold  = 0.80
	schedDelayP95Ms     = 100.0
	alertClearIntervals = 2
)

type alertKey struct {
	agent string
	kind  string
}

type activeAlert struct {
	alert    store.Alert
	okStreak int
}

type targetEgress struct {
	hasNonLoopback bool
	egressIndex    int
	egressName     string
}

// alertEvaluator tracks active bottleneck alerts during a single run.
type alertEvaluator struct {
	runID   string
	results *store.ResultDB
	hub     *eventHub
	egress  targetEgress

	mu     sync.Mutex
	active map[alertKey]*activeAlert
	closed bool
}

func newAlertEvaluator(runID string, results *store.ResultDB, hub *eventHub, egress targetEgress) *alertEvaluator {
	return &alertEvaluator{
		runID:   runID,
		results: results,
		hub:     hub,
		egress:  egress,
		active:  make(map[alertKey]*activeAlert),
	}
}

func (a *alertEvaluator) finish() {
	a.mu.Lock()
	a.closed = true
	clear(a.active)
	a.mu.Unlock()
}

func (a *alertEvaluator) observeCondition(ctx context.Context, agent, kind, level, msg string, val, threshold float64, triggered bool, at time.Time) {
	if at.IsZero() {
		at = time.Now()
	}
	key := alertKey{agent: agent, kind: kind}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	cur := a.active[key]
	if triggered {
		if cur != nil {
			cur.okStreak = 0
			a.mu.Unlock()
			return
		}
		al := store.Alert{
			RunID:     a.runID,
			Agent:     agent,
			Kind:      kind,
			Level:     level,
			Message:   msg,
			Value:     val,
			Threshold: threshold,
			StartedAt: at,
		}
		a.mu.Unlock()

		if err := a.results.AddAlert(ctx, &al); err != nil {
			return
		}
		a.mu.Lock()
		if !a.closed {
			a.active[key] = &activeAlert{alert: al}
			a.mu.Unlock()
			a.hub.publish(Event{
				Type:  EventAlert,
				RunID: a.runID,
				Alert: &al,
			})
			return
		}
		a.mu.Unlock()
		return
	}

	if cur == nil {
		a.mu.Unlock()
		return
	}
	cur.okStreak++
	if cur.okStreak < alertClearIntervals {
		a.mu.Unlock()
		return
	}
	closed := cur.alert
	delete(a.active, key)
	a.mu.Unlock()

	if err := a.results.EndAlert(ctx, closed.ID, at); err == nil {
		closed.EndedAt = at
		a.hub.publish(Event{
			Type:  EventAlert,
			RunID: a.runID,
			Alert: &closed,
		})
	}
}

func (a *alertEvaluator) evaluateSys(ctx context.Context, agentName string, st *sysmon.Stats, manualBandwidthBps int64) {
	if st == nil {
		return
	}
	now := st.Time
	if now.IsZero() {
		now = time.Now()
	}

	a.observeCondition(
		ctx, agentName, "cpu", store.AlertWarning,
		fmt.Sprintf("发压机 %s CPU 使用率过高（%.1f%%）", agentName, st.CPU.Machine*100),
		st.CPU.Machine, cpuThreshold,
		st.CPU.Machine > cpuThreshold, now,
	)

	var memRatio float64
	if st.Memory.TotalBytes > 0 {
		memRatio = float64(st.Memory.UsedBytes) / float64(st.Memory.TotalBytes)
	}
	a.observeCondition(
		ctx, agentName, "memory", store.AlertWarning,
		fmt.Sprintf("发压机 %s 内存使用率过高（%.1f%%）", agentName, memRatio*100),
		memRatio, memoryThreshold,
		st.Memory.TotalBytes > 0 && memRatio > memoryThreshold, now,
	)

	var portRatio float64
	if st.Ports.EphemeralCount > 0 {
		portRatio = float64(st.Ports.TCPConns) / float64(st.Ports.EphemeralCount)
	}
	a.observeCondition(
		ctx, agentName, "ports", store.AlertWarning,
		fmt.Sprintf("发压机 %s 临时端口占用接近上限（%.1f%%）", agentName, portRatio*100),
		portRatio, portThreshold,
		st.Ports.EphemeralCount > 0 && portRatio > portThreshold, now,
	)

	if a.egress.hasNonLoopback || manualBandwidthBps > 0 {
		nic, ok := selectEgressNIC(st.NICs, a.egress)
		if ok && !nic.Loopback {
			limit := uint64(0)
			if manualBandwidthBps > 0 {
				limit = uint64(manualBandwidthBps)
			} else {
				limit = nic.LinkSpeedBitsPerSec
			}
			if limit > 0 {
				usedBps := float64(max(nic.TxBytesPerSec, nic.RxBytesPerSec) * 8)
				thresh := bandwidthThreshold * float64(limit)
				a.observeCondition(
					ctx, agentName, "bandwidth", store.AlertWarning,
					fmt.Sprintf("发压机 %s 网卡 %s 带宽达到 %.1f%%", agentName, nic.Name, usedBps/float64(limit)*100),
					usedBps, thresh,
					usedBps > thresh, now,
				)
			}
		}
	}
}

func (a *alertEvaluator) evaluateSnapshot(ctx context.Context, snap *metrics.Snapshot) {
	if snap == nil {
		return
	}
	now := snap.Time
	if now.IsZero() {
		now = time.Now()
	}

	localErrs := float64(snap.Total.Errors[metrics.ErrLocal])
	a.observeCondition(
		ctx, "", "local_error", store.AlertCritical,
		fmt.Sprintf("发压机出现 %d 次本地网络错误（端口耗尽或句柄不足）", int64(localErrs)),
		localErrs, 1,
		localErrs > 0, now,
	)

	dropped := float64(snap.DroppedIterations)
	a.observeCondition(
		ctx, "", "dropped_iterations", store.AlertWarning,
		fmt.Sprintf("开环速率下虚拟用户不足，本秒丢弃 %d 次迭代", int64(dropped)),
		dropped, 1,
		dropped > 0, now,
	)

	var p95 float64
	if snap.SchedDelay != nil && snap.SchedDelay.Count() > 0 {
		p95 = snap.SchedDelay.Stats().P95
	}
	a.observeCondition(
		ctx, "", "sched_delay", store.AlertWarning,
		fmt.Sprintf("发压机调度延迟 P95 过高（%.1f ms）", p95),
		p95, schedDelayP95Ms,
		p95 > schedDelayP95Ms, now,
	)
}

func selectEgressNIC(nics []sysmon.NICStats, eg targetEgress) (sysmon.NICStats, bool) {
	if len(nics) == 0 {
		return sysmon.NICStats{}, false
	}
	if eg.egressIndex != 0 || eg.egressName != "" {
		for _, nic := range nics {
			if (eg.egressIndex != 0 && nic.Index == eg.egressIndex) || (eg.egressName != "" && nic.Name == eg.egressName) {
				return nic, true
			}
		}
	}
	var best sysmon.NICStats
	found := false
	for _, nic := range nics {
		if !nic.Up || nic.Loopback {
			continue
		}
		if !found || max(nic.TxBytesPerSec, nic.RxBytesPerSec) > max(best.TxBytesPerSec, best.RxBytesPerSec) {
			best = nic
			found = true
		}
	}
	return best, found
}

func inspectTargetEgress(ctx context.Context, def *scenario.Definition) targetEgress {
	if def == nil {
		return targetEgress{}
	}
	var res targetEgress
	seen := map[string]bool{}
	inspectStepsEgress(ctx, def.Steps, seen, &res)
	return res
}

func inspectStepsEgress(ctx context.Context, steps []scenario.Step, seen map[string]bool, res *targetEgress) bool {
	for _, st := range steps {
		if st.Group != nil {
			if inspectStepsEgress(ctx, st.Group.Children, seen, res) {
				return true
			}
			continue
		}
		if st.Loop != nil {
			if inspectStepsEgress(ctx, st.Loop.Children, seen, res) {
				return true
			}
			continue
		}
		if st.If != nil {
			if inspectStepsEgress(ctx, st.If.Then, seen, res) || inspectStepsEgress(ctx, st.If.Else, seen, res) {
				return true
			}
			continue
		}
		if st.Weighted != nil {
			for _, br := range st.Weighted.Branches {
				if inspectStepsEgress(ctx, br.Steps, seen, res) {
					return true
				}
			}
			continue
		}
		if st.Request == nil {
			continue
		}
		raw := strings.TrimSpace(st.Request.URL)
		if raw == "" || strings.Contains(raw, "{{") {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		if seen[u.Host] {
			continue
		}
		seen[u.Host] = true

		host := strings.ToLower(u.Hostname())
		if host == "localhost" {
			continue
		}
		if ip, err := netip.ParseAddr(host); err == nil && (ip.IsLoopback() || ip.IsUnspecified()) {
			continue
		}

		lookupCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		eg, err := sysmon.EgressInterfaceContext(lookupCtx, raw)
		cancel()
		if err != nil {
			// Remote address lookup failed or remote agent may use a non-loopback path.
			res.hasNonLoopback = true
			return true
		}
		if !eg.Loopback {
			res.hasNonLoopback = true
			res.egressIndex = eg.Index
			res.egressName = eg.Interface
			return true
		}
	}
	return false
}
