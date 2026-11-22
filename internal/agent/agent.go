// Package agent implements the Tidegate load generator node runtime: it
// connects to the controller via cluster, streams system health statistics,
// and executes assigned load test scenarios with synchronized start times.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"tidegate/internal/cluster"
	"tidegate/internal/engine"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
	"tidegate/internal/sysmon"
)

// Options configure an Agent.
type Options struct {
	JoinAddr       string
	Token          string
	Name           string
	Version        string
	WorkDir        string
	SysInterval    time.Duration
	Client         engine.ClientKind
	Logger         *slog.Logger
	ClusterOptions cluster.ClientOptions
}

// Agent runs on a load generator machine, maintaining a cluster connection to
// the controller and executing load tests.
type Agent struct {
	opts        Options
	workDir     string
	sysInterval time.Duration
	clientKind  engine.ClientKind
	log         *slog.Logger
	cl          *cluster.Client

	parentCtx context.Context
	mu        sync.Mutex
	runs      map[string]*agentRun
	wg        sync.WaitGroup
}

type agentRun struct {
	id       string
	ctx      context.Context
	cancel   context.CancelFunc
	stopCh   chan struct{}
	stopOnce sync.Once

	mu      sync.Mutex
	eng     *engine.Engine
	stopped bool
	aborted bool
}

// New creates an Agent. Call Run to connect and start serving.
func New(opts Options) (*Agent, error) {
	if opts.JoinAddr == "" {
		return nil, errors.New("未指定控制器接入地址（--join）")
	}
	if opts.Token == "" {
		return nil, errors.New("未指定节点接入令牌（--token）")
	}
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = os.TempDir()
	}
	sysInterval := opts.SysInterval
	if sysInterval <= 0 {
		sysInterval = time.Second
	}
	clientKind := opts.Client
	if clientKind == "" {
		clientKind = engine.ClientFast
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	host := sysmon.Host()
	ifaces := make([]cluster.NetInterface, 0, len(host.NICs))
	for _, nic := range host.NICs {
		ifaces = append(ifaces, cluster.NetInterface{
			Name:         nic.Name,
			LinkSpeedBps: nic.LinkSpeedBitsPerSec,
		})
	}

	clOpts := opts.ClusterOptions
	clOpts.Addr = opts.JoinAddr
	clOpts.Token = opts.Token
	if opts.Name != "" {
		clOpts.Name = opts.Name
	}
	if opts.Version != "" {
		clOpts.Version = opts.Version
	}
	if clOpts.Hello == nil {
		clOpts.Hello = func() cluster.Hello {
			return cluster.Hello{
				OS:          host.OS,
				Arch:        host.Arch,
				CPUCores:    host.CPUCores,
				MemoryBytes: host.MemoryTotalBytes,
				Interfaces:  slices.Clone(ifaces),
			}
		}
	}
	if clOpts.Logf == nil {
		clOpts.Logf = func(format string, args ...any) {
			log.Info(fmt.Sprintf(format, args...))
		}
	}

	return &Agent{
		opts:        opts,
		workDir:     workDir,
		sysInterval: sysInterval,
		clientKind:  clientKind,
		log:         log,
		cl:          cluster.NewClient(clOpts),
		runs:        make(map[string]*agentRun),
	}, nil
}

// Client returns the underlying cluster client.
func (a *Agent) Client() *cluster.Client { return a.cl }

// Run connects to the controller and serves until ctx is cancelled or the
// controller rejects the node.
func (a *Agent) Run(ctx context.Context) error {
	a.mu.Lock()
	a.parentCtx = ctx
	a.mu.Unlock()

	sysCtx, sysCancel := context.WithCancel(ctx)
	var sysWG sync.WaitGroup
	mon, err := sysmon.New(sysmon.Options{Interval: a.sysInterval})
	if err != nil {
		a.log.Warn("初始化系统监控失败，将继续运行但不上报系统指标", "err", err)
	} else {
		mon.Start(sysCtx)
		sysWG.Add(1)
		go func() {
			defer sysWG.Done()
			for st := range mon.Stats() {
				b, err := json.Marshal(st)
				if err != nil {
					continue
				}
				_ = a.cl.SendSysStats(st.Time, b)
			}
		}()
	}

	runErr := a.cl.Run(ctx, &handler{agent: a})
	sysCancel()
	sysWG.Wait()
	a.abortAllRuns()
	a.wg.Wait()
	return runErr
}

func (a *Agent) abortAllRuns() {
	a.mu.Lock()
	active := make([]*agentRun, 0, len(a.runs))
	for _, r := range a.runs {
		active = append(active, r)
	}
	a.mu.Unlock()
	for _, r := range active {
		r.stop(cluster.StopAbort)
	}
}

type handler struct {
	agent *Agent
}

func (h *handler) OnWelcome(w cluster.WelcomeInfo) {
	h.agent.log.Info("已连接控制器", "node_id", w.NodeID, "resumed", w.Resumed, "rtt", w.RTT)
}

func (h *handler) OnStartRun(r *cluster.StartRun) {
	a := h.agent
	if r == nil || r.RunID == "" {
		return
	}

	a.mu.Lock()
	if _, exists := a.runs[r.RunID]; exists {
		a.mu.Unlock()
		return
	}
	parent := a.parentCtx
	if parent == nil {
		parent = context.Background()
	}
	runCtx, cancel := context.WithCancel(parent)
	ar := &agentRun{
		id:     r.RunID,
		ctx:    runCtx,
		cancel: cancel,
		stopCh: make(chan struct{}),
	}
	a.runs[r.RunID] = ar
	a.wg.Add(1)
	a.mu.Unlock()

	go a.executeRun(ar, r)
}

func (h *handler) OnStopRun(r *cluster.StopRun) {
	if r == nil || r.RunID == "" {
		return
	}
	a := h.agent
	a.mu.Lock()
	ar := a.runs[r.RunID]
	a.mu.Unlock()
	if ar != nil {
		ar.stop(r.Mode)
	}
}

func (h *handler) OnSetTarget(t *cluster.SetTarget) {
	if t == nil || t.RunID == "" {
		return
	}
	a := h.agent
	a.mu.Lock()
	ar := a.runs[t.RunID]
	a.mu.Unlock()
	if ar == nil {
		return
	}
	ar.mu.Lock()
	eng := ar.eng
	ar.mu.Unlock()
	if eng != nil {
		if err := eng.SetTarget(t.Value); err != nil {
			a.log.Warn("动态调整并发失败", "run_id", t.RunID, "value", t.Value, "err", err)
		}
	}
}

func (h *handler) OnDisconnected(err error) {
	h.agent.log.Warn("与控制器连接断开", "err", err)
}

func (r *agentRun) stop(mode cluster.StopMode) {
	r.mu.Lock()
	r.stopped = true
	if mode == cluster.StopAbort {
		r.aborted = true
	}
	eng := r.eng
	r.mu.Unlock()

	r.stopOnce.Do(func() { close(r.stopCh) })
	if eng == nil {
		r.cancel()
		return
	}
	if mode == cluster.StopAbort {
		r.cancel()
		eng.Abort()
	} else {
		eng.Stop()
	}
}

func (a *Agent) executeRun(ar *agentRun, r *cluster.StartRun) {
	defer func() {
		ar.cancel()
		a.mu.Lock()
		delete(a.runs, r.RunID)
		a.mu.Unlock()
		a.wg.Done()
	}()

	finishErr := func(msg string) {
		_ = a.cl.SendRunFinished(r.RunID, nil, msg)
	}

	runDir, err := os.MkdirTemp(a.workDir, "tidegate-run-*")
	if err != nil {
		finishErr(fmt.Sprintf("创建临时工作目录失败：%v", err))
		return
	}
	cleanupDir := func() {
		if runDir != "" {
			_ = os.RemoveAll(runDir)
			runDir = ""
		}
	}
	defer cleanupDir()

	for _, df := range r.DataFiles {
		if err := store.ValidateFileName(df.Name); err != nil {
			cleanupDir()
			finishErr(fmt.Sprintf("数据文件名无效 %q：%v", df.Name, err))
			return
		}
		target := filepath.Join(runDir, df.Name)
		if err := os.WriteFile(target, df.Content, 0o600); err != nil {
			cleanupDir()
			finishErr(fmt.Sprintf("写入数据文件 %q 失败：%v", df.Name, err))
			return
		}
	}

	def, err := scenario.Parse(r.Scenario, scenario.FormatJSON)
	if err != nil {
		cleanupDir()
		finishErr(fmt.Sprintf("解析压测场景失败：%v", err))
		return
	}
	plan, err := scenario.Compile(def, scenario.CompileOptions{BaseDir: runDir})
	cleanupDir()
	if err != nil {
		finishErr(fmt.Sprintf("编译压测场景失败：%v", err))
		return
	}

	eng, err := engine.New(plan, engine.Options{
		Client:    a.clientKind,
		LoadShare: r.LoadShare,
		DataSlice: engine.DataSlice{Index: r.SliceIndex, Count: r.SliceCount},
	})
	if err != nil {
		finishErr(fmt.Sprintf("初始化发压引擎失败：%v", err))
		return
	}

	ar.mu.Lock()
	ar.eng = eng
	stopped := ar.stopped
	aborted := ar.aborted
	ar.mu.Unlock()

	if aborted || ar.ctx.Err() != nil {
		finishErr("压测已停止")
		return
	}

	if !r.StartTime.IsZero() {
		startAt := a.cl.ControllerToLocal(r.StartTime)
		if d := time.Until(startAt); d > 0 {
			timer := time.NewTimer(d)
			select {
			case <-timer.C:
			case <-ar.stopCh:
				timer.Stop()
				finishErr("压测在启动前已停止")
				return
			case <-ar.ctx.Done():
				timer.Stop()
				finishErr("压测在启动前已停止")
				return
			}
		}
	}

	ar.mu.Lock()
	stopped = ar.stopped
	ar.mu.Unlock()
	if stopped {
		finishErr("压测在启动前已停止")
		return
	}

	engCtx, engCancel := context.WithCancel(ar.ctx)
	defer engCancel()

	errc := make(chan error, 1)
	go func() {
		errc <- eng.Run(engCtx)
	}()

	for snap := range eng.Snapshots() {
		bin, err := snap.MarshalBinary()
		if err != nil {
			a.log.Error("序列化快照失败", "run_id", r.RunID, "seq", snap.Seq, "err", err)
			continue
		}
		if err := a.cl.SendRunSnapshot(r.RunID, snap.Seq, bin); err != nil {
			a.log.Error("发送快照失败", "run_id", r.RunID, "seq", snap.Seq, "err", err)
		}
	}

	runErr := <-errc
	var sumBytes []byte
	if sum := eng.Summary(); sum != nil {
		sumBytes, _ = sum.MarshalBinary()
	}
	var errMsg string
	if runErr != nil {
		errMsg = runErr.Error()
	}
	_ = a.cl.SendRunFinished(r.RunID, sumBytes, errMsg)
}
