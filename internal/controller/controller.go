package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tidegate/internal/cluster"
	"tidegate/internal/engine"
	"tidegate/internal/exporter"
	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
	"tidegate/internal/sysmon"
)

// Typed sentinel errors returned by Controller methods for HTTP mapping.
var (
	ErrRunActive    = errors.New("controller: a run is already active")
	ErrRunNotActive = errors.New("controller: run is not active")
	ErrNoAgents     = errors.New("controller: no connected agents")
	ErrInvalidInput = errors.New("controller: invalid input")
)

// UserError wraps an operational failure with a Simplified Chinese message safe
// to return to API clients, paired with an underlying sentinel category error.
type UserError struct {
	Sentinel error
	Message  string
	Cause    error
}

func (e *UserError) Error() string { return e.Message }

func (e *UserError) Unwrap() error {
	if e.Sentinel != nil {
		return e.Sentinel
	}
	return e.Cause
}

func userErr(sentinel error, msg string, cause error) error {
	return &UserError{Sentinel: sentinel, Message: msg, Cause: cause}
}

// Options configure a Controller.
type Options struct {
	Store               *store.Store
	Results             *store.ResultDB
	Version             string
	ClusterListen       string
	ClusterToken        string
	StartLocalAgent     bool
	InProcessLocalAgent bool
	ExecutablePath      string
	Client              engine.ClientKind
	NodeGracePeriod     time.Duration
	StartDelay          time.Duration
	Logger              *slog.Logger
}

type sysEntry struct {
	raw   json.RawMessage
	stats *sysmon.Stats
}

// Controller coordinates cluster nodes, interactive debug runs, load test
// orchestration, snapshot aggregation, and bottleneck alerting.
type Controller struct {
	opts          Options
	store         *store.Store
	results       *store.ResultDB
	log           *slog.Logger
	internalToken string
	clientKind    engine.ClientKind
	gracePeriod   time.Duration
	startDelay    time.Duration

	hub    *eventHub
	sink   *metricsSink
	lis    net.Listener
	srv    *cluster.Server
	sup    *localSupervisor
	srvWG  sync.WaitGroup
	bgWG   sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc

	closing      atomic.Bool
	defaultProj  sync.Mutex
	agentsSignal chan struct{}

	mu      sync.Mutex
	active  *activeRun
	lastSys map[string]sysEntry
}

// DebugAPIRequest configures an interactive single-API debug call.
type DebugAPIRequest struct {
	APIID         string                     `json:"api_id,omitempty"`
	API           *store.API                 `json:"api,omitempty"`
	EnvironmentID string                     `json:"environment_id,omitempty"`
	Variables     map[string]scenario.Scalar `json:"variables,omitempty"`
	HTTP          *scenario.HTTPOptions      `json:"http,omitempty"`
	MaxBodyBytes  int64                      `json:"max_body_bytes,omitempty"`
	SaveHistory   *bool                      `json:"save_history,omitempty"`
}

// DebugAPIResponse is the result of DebugAPI.
type DebugAPIResponse struct {
	RecordID  string                 `json:"record_id,omitempty"`
	Result    engine.DebugStepResult `json:"result"`
	FinalVars map[string]string      `json:"final_vars,omitempty"`
}

// DebugScenarioRequest configures a 1-iteration multi-step scenario dry-run.
type DebugScenarioRequest struct {
	ScenarioID    string                     `json:"scenario_id,omitempty"`
	Scenario      *store.Scenario            `json:"scenario,omitempty"`
	EnvironmentID string                     `json:"environment_id,omitempty"`
	Variables     map[string]scenario.Scalar `json:"variables,omitempty"`
	SkipThink     *bool                      `json:"skip_think,omitempty"`
	StopOnError   bool                       `json:"stop_on_error,omitempty"`
	MaxBodyBytes  int64                      `json:"max_body_bytes,omitempty"`
}

// StartRunRequest starts a distributed or local load test run.
type StartRunRequest struct {
	ScenarioID    string                     `json:"scenario_id,omitempty"`
	Mix           []ScenarioMixItem          `json:"mix,omitempty"`
	APIID         string                     `json:"api_id,omitempty"`
	API           *store.API                 `json:"api,omitempty"`
	EnvironmentID string                     `json:"environment_id,omitempty"`
	Variables     map[string]scenario.Scalar `json:"variables,omitempty"`
	Load          *scenario.Load             `json:"load,omitempty"`
	HTTP          *scenario.HTTPOptions      `json:"http,omitempty"`
	ActorRole     string                     `json:"-"`
}

// New starts the cluster listener, cleans up interrupted runs, optionally starts
// a local agent, and returns a ready Controller.
func New(ctx context.Context, opts Options) (*Controller, error) {
	if opts.Store == nil {
		return nil, errors.New("controller: nil store")
	}
	if opts.Results == nil {
		return nil, errors.New("controller: nil results")
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts.ClusterListen == "" {
		opts.ClusterListen = "127.0.0.1:7443"
	}
	clientKind := opts.Client
	if clientKind == "" {
		clientKind = engine.ClientFast
	}
	grace := opts.NodeGracePeriod
	if grace <= 0 {
		grace = 3 * time.Second
	}
	startDelay := opts.StartDelay

	var tokBuf [32]byte
	if _, err := rand.Read(tokBuf[:]); err != nil {
		return nil, fmt.Errorf("生成节点认证令牌失败：%w", err)
	}
	internalTok := hex.EncodeToString(tokBuf[:])

	if _, err := opts.Results.MarkInterrupted(ctx, ""); err != nil {
		return nil, fmt.Errorf("恢复中断的压测记录失败：%w", err)
	}

	lis, err := net.Listen("tcp", opts.ClusterListen)
	if err != nil {
		return nil, fmt.Errorf("监听集群地址 %s 失败：%w", opts.ClusterListen, err)
	}

	bgCtx, cancel := context.WithCancel(context.Background())
	c := &Controller{
		opts:          opts,
		store:         opts.Store,
		results:       opts.Results,
		log:           log,
		internalToken: internalTok,
		clientKind:    clientKind,
		gracePeriod:   grace,
		startDelay:    startDelay,
		hub:           newEventHub(),
		lis:           lis,
		ctx:           bgCtx,
		cancel:        cancel,
		agentsSignal:  make(chan struct{}, 1),
		lastSys:       make(map[string]sysEntry),
	}

	srv := cluster.NewServer(cluster.ServerOptions{
		Version: opts.Version,
		Authenticate: func(tok string) bool {
			if cluster.TokenEqual(tok, c.internalToken) {
				return true
			}
			return opts.ClusterToken != "" && cluster.TokenEqual(tok, opts.ClusterToken)
		},
		Handler: &clusterHandler{ctrl: c},
		Logf: func(format string, args ...any) {
			log.Info(fmt.Sprintf(format, args...))
		},
	})
	c.srv = srv

	c.srvWG.Add(1)
	go func() {
		defer c.srvWG.Done()
		if err := srv.Serve(lis); err != nil && !c.closing.Load() {
			log.Error("集群服务异常退出", "err", err)
		}
	}()

	c.bgWG.Add(1)
	go c.agentsBroadcastLoop()

	c.sink = newMetricsSink(c)
	c.reloadMetricsSink(ctx)
	c.bgWG.Add(1)
	go c.sink.run()

	if opts.StartLocalAgent || opts.InProcessLocalAgent {
		sup, err := startLocalAgentSupervisor(
			bgCtx,
			lis.Addr().String(),
			c.internalToken,
			opts.Version,
			opts.InProcessLocalAgent,
			opts.ExecutablePath,
			clientKind,
			log,
		)
		if err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("启动本机发压节点失败：%w", err)
		}
		c.sup = sup
	}

	return c, nil
}

// ClusterAddr returns the controller's cluster gRPC listening address.
func (c *Controller) ClusterAddr() string {
	return c.lis.Addr().String()
}

// Close shuts down the local agent supervisor, cluster server, and event hub.
func (c *Controller) Close() error {
	c.closing.Store(true)
	c.cancel()
	if c.sup != nil {
		c.sup.close()
	}

	c.mu.Lock()
	active := c.active
	c.active = nil
	c.mu.Unlock()
	if active != nil {
		active.stopLocalTimers()
	}

	c.srv.Stop()
	_ = c.lis.Close()
	c.srvWG.Wait()
	c.bgWG.Wait()
	c.sink.close()
	c.hub.closeAll()
	return nil
}

// Subscribe creates a new event stream subscriber.
func (c *Controller) Subscribe() *Subscriber {
	return c.hub.subscribe()
}

// EnsureDefaultProject creates a default project when the store currently has
// zero projects, returning the created or first existing project.
func (c *Controller) EnsureDefaultProject() (*store.Project, error) {
	c.defaultProj.Lock()
	defer c.defaultProj.Unlock()

	list, err := c.store.ListProjects()
	if err != nil {
		return nil, err
	}
	if len(list) > 0 {
		return list[0], nil
	}
	return c.store.CreateProject(&store.Project{
		Name:        "默认项目",
		Description: "默认工作区，可直接新建接口或粘贴 cURL 调试",
	})
}

func (c *Controller) notifyAgentsChanged() {
	select {
	case c.agentsSignal <- struct{}{}:
	default:
	}
}

func (c *Controller) agentsBroadcastLoop() {
	defer c.bgWG.Done()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.agentsSignal:
			list, err := c.Agents(c.ctx)
			if err != nil {
				continue
			}
			c.hub.publish(Event{
				Type:   EventAgents,
				Agents: list,
			})
		}
	}
}

// Agents returns all currently connected load generator nodes with latest
// system metrics and configured bandwidth limits.
func (c *Controller) Agents(ctx context.Context) ([]AgentView, error) {
	nodes := c.srv.Nodes()
	settingsList, err := c.results.ListAgentSettings(ctx)
	if err != nil {
		return nil, err
	}
	bwMap := make(map[string]int64, len(settingsList))
	for _, s := range settingsList {
		bwMap[s.Name] = s.BandwidthBps
	}

	c.mu.Lock()
	sysSnapshot := make(map[string]*sysmon.Stats, len(c.lastSys))
	for id, entry := range c.lastSys {
		if entry.stats != nil {
			cp := *entry.stats
			sysSnapshot[id] = &cp
		}
	}
	c.mu.Unlock()

	out := make([]AgentView, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, buildAgentView(n, bwMap[n.Name], sysSnapshot[n.ID]))
	}
	return out, nil
}

// UpdateAgentSettings updates the configured bandwidth ceiling (bps) of an
// agent identified by node ID or node name and broadcasts the updated node list.
func (c *Controller) UpdateAgentSettings(ctx context.Context, idOrName string, bandwidthBps int64) (*AgentView, error) {
	targetName := strings.TrimSpace(idOrName)
	if targetName == "" {
		return nil, userErr(ErrInvalidInput, "节点标识不能为空", nil)
	}
	for _, n := range c.srv.Nodes() {
		if n.ID == idOrName || n.Name == idOrName {
			targetName = n.Name
			break
		}
	}
	if err := c.results.SetAgentSettings(ctx, store.AgentSettings{
		Name:         targetName,
		BandwidthBps: bandwidthBps,
	}); err != nil {
		return nil, err
	}
	c.notifyAgentsChanged()

	agents, err := c.Agents(ctx)
	if err != nil {
		return nil, err
	}
	for i := range agents {
		if agents[i].ID == idOrName || agents[i].Name == targetName {
			return &agents[i], nil
		}
	}
	return &AgentView{
		ID:           idOrName,
		Name:         targetName,
		BandwidthBps: bandwidthBps,
	}, nil
}

func buildAgentView(n cluster.NodeInfo, bwBps int64, st *sysmon.Stats) AgentView {
	ifaces := make([]AgentInterface, 0, len(n.Interfaces))
	for _, ni := range n.Interfaces {
		ifaces = append(ifaces, AgentInterface{
			Name:         ni.Name,
			LinkSpeedBps: ni.LinkSpeedBps,
		})
	}
	return AgentView{
		ID:            n.ID,
		Name:          n.Name,
		Version:       n.Version,
		OS:            n.OS,
		Arch:          n.Arch,
		CPUCores:      n.CPUCores,
		MemoryBytes:   n.MemoryBytes,
		Interfaces:    ifaces,
		RemoteAddr:    n.RemoteAddr,
		ConnectedAt:   n.ConnectedAt,
		LastSeen:      n.LastSeen,
		RTTMs:         float64(n.RTT) / 1e6,
		ClockOffsetMs: float64(n.ClockOffset) / 1e6,
		BandwidthBps:  bwBps,
		Stats:         st,
	}
}

// DebugAPI executes one single-API request directly inside the controller
// process, saves it to debug_history, and returns detailed timing, request,
// response, extraction, and assertion output.
func (c *Controller) DebugAPI(ctx context.Context, projectID string, req DebugAPIRequest) (*DebugAPIResponse, error) {
	if req.API == nil && strings.TrimSpace(req.APIID) == "" {
		return nil, userErr(ErrInvalidInput, "请提供接口 ID 或接口定义", nil)
	}

	var httpOpts scenario.HTTPOptions
	if req.HTTP != nil {
		httpOpts = *req.HTTP
	}
	apiOpts := store.APIRunOptions{
		ResolveOptions: store.ResolveOptions{
			EnvironmentID: req.EnvironmentID,
			Variables:     req.Variables,
		},
		HTTP: httpOpts,
	}

	var (
		resolved *store.Resolved
		err      error
		apiID    = strings.TrimSpace(req.APIID)
	)
	if req.API != nil {
		draft := *req.API
		if draft.ID == "" && apiID != "" {
			draft.ID = apiID
		} else if apiID == "" {
			apiID = draft.ID
		}
		resolved, err = c.store.ResolveAPIDraft(projectID, &draft, apiOpts)
	} else {
		resolved, err = c.store.ResolveAPI(projectID, apiID, apiOpts)
	}
	if err != nil {
		return nil, err
	}

	plan, err := resolved.Compile()
	if err != nil {
		return nil, userErr(ErrInvalidInput, err.Error(), err)
	}
	targetCheck, err := c.enforceDebugSecurity(ctx, plan)
	if err != nil {
		return nil, err
	}

	runRes, err := engine.RunDebug(ctx, plan, engine.DebugOptions{
		Client:       c.clientKind,
		MaxBodyBytes: req.MaxBodyBytes,
		SkipThink:    true,
		TargetCheck:  targetCheck,
	})
	if err != nil {
		return nil, err
	}
	if len(runRes.Steps) == 0 {
		return nil, errors.New("调试未执行任何步骤")
	}

	step0 := runRes.Steps[0]
	resp := &DebugAPIResponse{
		Result:    step0,
		FinalVars: runRes.FinalVars,
	}

	save := req.SaveHistory == nil || *req.SaveHistory
	if save {
		payload, err := json.Marshal(struct {
			EnvironmentID string                 `json:"environment_id,omitempty"`
			Result        engine.DebugStepResult `json:"result"`
			FinalVars     map[string]string      `json:"final_vars,omitempty"`
		}{
			EnvironmentID: req.EnvironmentID,
			Result:        step0,
			FinalVars:     runRes.FinalVars,
		})
		if err == nil {
			rec := store.DebugRecord{
				ProjectID:  projectID,
				APIID:      apiID,
				Method:     step0.ActualRequest.Method,
				URL:        step0.ActualRequest.URL,
				Status:     step0.Status,
				DurationMs: step0.Timing.TotalMs,
				Data:       payload,
			}
			if err := c.results.SaveDebugRecord(ctx, &rec); err == nil {
				resp.RecordID = rec.ID
			}
		}
	}
	return resp, nil
}

// DebugScenario runs one functional dry-run iteration across a saved or draft
// multi-step scenario, propagating extracted variables step-to-step.
func (c *Controller) DebugScenario(ctx context.Context, projectID string, req DebugScenarioRequest) (*engine.DebugRunResult, error) {
	if req.Scenario == nil && strings.TrimSpace(req.ScenarioID) == "" {
		return nil, userErr(ErrInvalidInput, "请提供场景 ID 或场景定义", nil)
	}
	opts := store.ResolveOptions{
		EnvironmentID: req.EnvironmentID,
		Variables:     req.Variables,
	}

	var (
		resolved *store.Resolved
		err      error
	)
	if req.Scenario != nil {
		resolved, err = c.store.ResolveScenarioDraft(projectID, req.Scenario, opts)
	} else {
		resolved, err = c.store.ResolveScenario(projectID, strings.TrimSpace(req.ScenarioID), opts)
	}
	if err != nil {
		return nil, err
	}

	// Ensure dry-run compiles cleanly even when a draft scenario has empty load stages.
	resolved.Definition.Load = scenario.Load{
		Mode: "vus",
		Stages: []scenario.Stage{{
			Duration: scenario.Duration(time.Second),
			Target:   1,
		}},
		MaxRequests: int64(len(resolved.Definition.Steps)),
	}

	plan, err := resolved.Compile()
	if err != nil {
		return nil, userErr(ErrInvalidInput, err.Error(), err)
	}
	targetCheck, err := c.enforceDebugSecurity(ctx, plan)
	if err != nil {
		return nil, err
	}

	skipThink := req.SkipThink == nil || *req.SkipThink
	return engine.RunDebug(ctx, plan, engine.DebugOptions{
		Client:       c.clientKind,
		MaxBodyBytes: req.MaxBodyBytes,
		SkipThink:    skipThink,
		StopOnError:  req.StopOnError,
		TargetCheck:  targetCheck,
	})
}

func (c *Controller) enforceDebugSecurity(ctx context.Context, plan *scenario.Plan) (func(host string, ip net.IP) error, error) {
	pol, err := c.GetSecurityPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if err := ValidatePlanTargets(ctx, plan, pol); err != nil {
		return nil, err
	}
	if !pol.AllowlistEnabled {
		return nil, nil
	}
	check, err := CompileTargetMatcher(pol.AllowedTargets)
	if err != nil {
		return nil, userErr(ErrInvalidInput, err.Error(), err)
	}
	return check, nil
}

func (c *Controller) resolveRunDefinition(projectID string, req StartRunRequest) (*store.Resolved, store.Run, error) {
	var (
		resolved *store.Resolved
		err      error
		runMeta  = store.Run{
			ProjectID:     projectID,
			ScenarioID:    req.ScenarioID,
			APIID:         req.APIID,
			EnvironmentID: req.EnvironmentID,
		}
	)
	if len(req.Mix) > 0 {
		return c.resolveMixRun(projectID, req)
	}
	if req.ScenarioID == "" && req.APIID == "" && req.API == nil {
		return nil, runMeta, userErr(ErrInvalidInput, "请指定压测场景或接口", nil)
	}
	switch {
	case req.ScenarioID != "":
		resolved, err = c.store.ResolveScenario(projectID, req.ScenarioID, store.ResolveOptions{
			EnvironmentID: req.EnvironmentID,
			Variables:     req.Variables,
		})
	case req.API != nil:
		if runMeta.APIID == "" {
			runMeta.APIID = req.API.ID
		}
		var apiOpts store.APIRunOptions
		apiOpts.EnvironmentID = req.EnvironmentID
		apiOpts.Variables = req.Variables
		if req.Load != nil {
			apiOpts.Load = *req.Load
		}
		if req.HTTP != nil {
			apiOpts.HTTP = *req.HTTP
		}
		resolved, err = c.store.ResolveAPIDraft(projectID, req.API, apiOpts)
	default:
		var apiOpts store.APIRunOptions
		apiOpts.EnvironmentID = req.EnvironmentID
		apiOpts.Variables = req.Variables
		if req.Load != nil {
			apiOpts.Load = *req.Load
		}
		if req.HTTP != nil {
			apiOpts.HTTP = *req.HTTP
		}
		resolved, err = c.store.ResolveAPI(projectID, req.APIID, apiOpts)
	}
	if err != nil {
		return nil, runMeta, err
	}
	if req.ScenarioID != "" && req.Load != nil {
		resolved.Definition.Load = *req.Load
	}
	if req.ScenarioID != "" && req.HTTP != nil {
		resolved.Definition.HTTP = *req.HTTP
	}
	return resolved, runMeta, nil
}

// StartRun resolves a scenario or single API, creates the run record, partitions
// load evenly across connected agents, and dispatches synchronized StartRun commands.
func (c *Controller) StartRun(ctx context.Context, projectID string, req StartRunRequest) (*store.Run, error) {
	resolved, runMeta, err := c.resolveRunDefinition(projectID, req)
	if err != nil {
		return nil, err
	}

	// Validate compilation, security allowlist, and role scale limits before admitting the run.
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
	if err := ValidateRoleScale(plan, req.ActorRole, pol); err != nil {
		return nil, err
	}

	execDef := cloneDefinition(resolved.Definition)
	if pol.AllowlistEnabled {
		execDef.HTTP.AllowedTargets = append([]string(nil), pol.AllowedTargets...)
	}
	scenJSON, err := json.Marshal(execDef)
	if err != nil {
		return nil, fmt.Errorf("序列化场景失败：%w", err)
	}
	scrubbedDef := scrubDefinitionSecrets(resolved.Definition, resolved.SecretNames)
	scrubbedJSON, err := json.Marshal(scrubbedDef)
	if err != nil {
		return nil, fmt.Errorf("序列化脱敏场景失败：%w", err)
	}

	nodes := c.srv.Nodes()
	if len(nodes) == 0 {
		return nil, userErr(ErrNoAgents, "当前没有可用的发压节点，请等待本机或远程节点接入后重试", nil)
	}

	dataFiles := make([]cluster.DataFile, 0, len(resolved.Files))
	for _, rf := range resolved.Files {
		b, err := os.ReadFile(rf.Path)
		if err != nil {
			return nil, fmt.Errorf("读取数据文件 %q 失败：%w", rf.Name, err)
		}
		dataFiles = append(dataFiles, cluster.DataFile{Name: rf.Name, Content: b})
	}

	// Atomically claim the single active run slot.
	c.mu.Lock()
	if c.active != nil {
		c.mu.Unlock()
		return nil, userErr(ErrRunActive, "当前已有正在运行的压测任务，请等待结束或先停止", nil)
	}
	c.mu.Unlock()

	runMeta.ScenarioName = resolved.Definition.Name
	runMeta.Scenario = scrubbedJSON
	runMeta.Status = store.RunPreparing
	runMeta.CreatedAt = time.Now()
	if err := c.results.CreateRun(ctx, &runMeta); err != nil {
		return nil, err
	}

	egress := inspectTargetEgress(ctx, resolved.Definition)
	ar := newActiveRun(c, runMeta.ID, runMeta.ProjectID, runMeta.ScenarioName, plan.Load.Mode, egress, resolved.Definition.Load.Thresholds)

	c.mu.Lock()
	if c.active != nil {
		c.mu.Unlock()
		_ = c.results.DeleteRun(ctx, runMeta.ID)
		return nil, userErr(ErrRunActive, "当前已有正在运行的压测任务，请等待结束或先停止", nil)
	}
	ar.initNodes(nodes)
	c.active = ar
	c.mu.Unlock()

	c.hub.publish(Event{
		Type:   EventRunStatus,
		RunID:  runMeta.ID,
		Status: store.RunPreparing,
	})

	startDelay := c.startDelay
	if startDelay <= 0 {
		if len(nodes) == 1 {
			startDelay = 120 * time.Millisecond
		} else {
			startDelay = 350 * time.Millisecond
		}
	}
	commonStart := time.Now().Add(startDelay)

	share := 1.0 / float64(len(nodes))
	dispatched := 0
	var failedDispatch []string
	for i, ni := range nodes {
		node := c.srv.Node(ni.ID)
		if node == nil {
			ar.markDispatchFailed(ni.ID, "节点已离线")
			failedDispatch = append(failedDispatch, ni.Name)
			continue
		}
		err := node.StartRun(&cluster.StartRun{
			RunID:      runMeta.ID,
			Scenario:   scenJSON,
			DataFiles:  dataFiles,
			LoadShare:  share,
			SliceIndex: i,
			SliceCount: len(nodes),
			StartTime:  commonStart,
		})
		if err != nil {
			ar.markDispatchFailed(ni.ID, err.Error())
			failedDispatch = append(failedDispatch, fmt.Sprintf("%s（%v）", ni.Name, err))
			continue
		}
		dispatched++
	}

	if dispatched == 0 {
		errMsg := "下发压测任务到发压节点失败：" + strings.Join(failedDispatch, "；")
		c.clearActive(ar)
		ar.stopLocalTimers()
		ar.alerts.finish()
		_ = c.results.FinishRun(ctx, runMeta.ID, store.RunFinish{
			Status:     store.RunFailed,
			FinishedAt: time.Now(),
			Error:      errMsg,
		})
		c.hub.publish(Event{
			Type:   EventRunStatus,
			RunID:  runMeta.ID,
			Status: store.RunFailed,
			Error:  errMsg,
		})
		runMeta.Status = store.RunFailed
		runMeta.Error = errMsg
		return &runMeta, userErr(ErrNoAgents, errMsg, nil)
	}

	now := time.Now()
	ar.setStartedAt(commonStart)
	runMeta.Status = store.RunRunning
	runMeta.StartedAt = now
	if err := c.results.UpdateRun(ctx, runMeta.ID, store.RunUpdate{
		Status:    store.RunRunning,
		StartedAt: now,
	}); err != nil {
		c.log.Warn("更新压测运行状态失败", "run_id", runMeta.ID, "err", err)
	}
	c.hub.publish(Event{
		Type:   EventRunStatus,
		RunID:  runMeta.ID,
		Status: store.RunRunning,
	})
	ar.startFlushLoop()

	return &runMeta, nil
}

// StopRun sends a graceful or immediate stop command to all nodes participating
// in runID.
func (c *Controller) StopRun(ctx context.Context, runID string, abort bool) (*store.Run, error) {
	c.mu.Lock()
	ar := c.active
	if ar != nil && ar.id != runID {
		ar = nil
	}
	c.mu.Unlock()

	if ar == nil {
		r, err := c.results.GetRun(ctx, runID)
		if err != nil {
			return nil, err
		}
		if !r.Status.Active() {
			return nil, userErr(ErrRunNotActive, "该压测任务已结束", nil)
		}
		return r, nil
	}

	nodeIDs := ar.nodeIDs()
	mode := cluster.StopGraceful
	if abort {
		mode = cluster.StopAbort
	}
	for _, id := range nodeIDs {
		if node := c.srv.Node(id); node != nil {
			_ = node.StopRun(runID, mode)
		}
	}

	_ = c.results.UpdateRun(ctx, runID, store.RunUpdate{Status: store.RunStopping})
	c.hub.publish(Event{
		Type:   EventRunStatus,
		RunID:  runID,
		Status: store.RunStopping,
	})
	return c.results.GetRun(ctx, runID)
}

// SetRunTarget dynamically adjusts the cluster-wide load target (VUs or RPS)
// of an active run.
func (c *Controller) SetRunTarget(ctx context.Context, runID string, target float64) error {
	return c.SetRunTargetWithRole(ctx, runID, target, "")
}

// SetRunTargetWithRole dynamically adjusts the load target while enforcing role
// limits defined in SecurityPolicy.
func (c *Controller) SetRunTargetWithRole(ctx context.Context, runID string, target float64, actorRole string) error {
	if target <= 0 {
		return userErr(ErrInvalidInput, "目标负载必须大于 0", nil)
	}
	c.mu.Lock()
	ar := c.active
	if ar != nil && ar.id != runID {
		ar = nil
	}
	c.mu.Unlock()
	if ar == nil {
		return userErr(ErrRunNotActive, "当前没有正在运行的指定压测任务", nil)
	}

	role := strings.ToLower(strings.TrimSpace(actorRole))
	if role != "" && role != "admin" {
		if role == "viewer" {
			return userErr(ErrScaleLimitExceeded, "只读角色无权调整压测负载", nil)
		}
		pol, err := c.GetSecurityPolicy(ctx)
		if err != nil {
			return err
		}
		if ar.loadMode == scenario.LoadRate {
			if pol.MemberMaxRPS > 0 && target > pol.MemberMaxRPS {
				return userErr(ErrScaleLimitExceeded, fmt.Sprintf("目标速率 %.0f RPS 超出当前角色上限 %.0f RPS", target, pol.MemberMaxRPS), nil)
			}
		} else {
			if pol.MemberMaxVUs > 0 && int(target) > pol.MemberMaxVUs {
				return userErr(ErrScaleLimitExceeded, fmt.Sprintf("并发虚拟用户数 %.0f 超出当前角色上限 %d", target, pol.MemberMaxVUs), nil)
			}
		}
	}

	for _, id := range ar.nodeIDs() {
		if node := c.srv.Node(id); node != nil {
			if err := node.SetTarget(runID, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Controller) clearActive(ar *activeRun) {
	c.mu.Lock()
	if c.active == ar {
		c.active = nil
	}
	c.mu.Unlock()
}

type clusterHandler struct {
	ctrl *Controller
}

func (h *clusterHandler) OnNodeJoined(n *cluster.Node) {
	c := h.ctrl
	c.mu.Lock()
	ar := c.active
	c.mu.Unlock()
	if ar != nil {
		ar.onNodeJoined(n.ID())
	}
	c.notifyAgentsChanged()
}

func (h *clusterHandler) OnNodeLeft(n *cluster.Node, reason error) {
	c := h.ctrl
	c.mu.Lock()
	delete(c.lastSys, n.ID())
	ar := c.active
	c.mu.Unlock()

	if ar != nil && !c.closing.Load() {
		ar.onNodeLeft(n.ID(), n.Info().Name, reason)
	}
	c.notifyAgentsChanged()
}

func (h *clusterHandler) OnSysStats(n *cluster.Node, s *cluster.SysStats) {
	if s == nil {
		return
	}
	c := h.ctrl
	var parsed *sysmon.Stats
	var st sysmon.Stats
	if err := json.Unmarshal(s.JSON, &st); err == nil {
		parsed = &st
	}

	rawCopy := json.RawMessage(append([]byte(nil), s.JSON...))
	c.mu.Lock()
	c.lastSys[n.ID()] = sysEntry{raw: rawCopy, stats: parsed}
	ar := c.active
	c.mu.Unlock()

	c.hub.publish(Event{
		Type:    EventSys,
		AgentID: n.ID(),
		Sys:     rawCopy,
	})

	if ar != nil && parsed != nil {
		name := n.Info().Name
		settings, _ := c.results.AgentSettings(c.ctx, name)
		ar.alerts.evaluateSys(c.ctx, name, parsed, settings.BandwidthBps)
	}
}

func (h *clusterHandler) OnRunSnapshot(n *cluster.Node, s *cluster.RunSnapshot) {
	if s == nil {
		return
	}
	c := h.ctrl
	c.mu.Lock()
	ar := c.active
	if ar != nil && ar.id != s.RunID {
		ar = nil
	}
	c.mu.Unlock()
	if ar == nil {
		return
	}
	ar.onSnapshot(n.ID(), s)
}

func (h *clusterHandler) OnRunFinished(n *cluster.Node, f *cluster.RunFinished) {
	if f == nil {
		return
	}
	c := h.ctrl
	c.mu.Lock()
	ar := c.active
	if ar != nil && ar.id != f.RunID {
		ar = nil
	}
	c.mu.Unlock()
	if ar == nil {
		return
	}
	ar.onNodeFinished(n.ID(), f)
}

type runNodeState struct {
	id         string
	name       string
	finished   bool
	lost       bool
	errText    string
	summary    *metrics.Snapshot
	graceTimer *time.Timer
}

type seqBucket struct {
	seq       int64
	snap      *metrics.Snapshot
	seen      map[string]bool
	createdAt time.Time
}

type activeRun struct {
	ctrl         *Controller
	id           string
	projectID    string
	scenarioName string
	loadMode     scenario.LoadMode
	alerts       *alertEvaluator
	thresholds   []scenario.Threshold

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	mu                sync.Mutex
	startedAt         time.Time
	thresholdAborted  bool
	thresholdAbortMsg string
	nodes             map[string]*runNodeState
	buckets           map[int64]*seqBucket
	nextFlushSeq      int64
	finalized         bool

	latestView  *metrics.SnapshotView
	totalReqs   int64
	totalErrors int64
	stepReqs    map[string]int64
	stepErrors  map[string]int64
}

func newActiveRun(ctrl *Controller, id, projectID, scenarioName string, loadMode scenario.LoadMode, egress targetEgress, thresholds []scenario.Threshold) *activeRun {
	return &activeRun{
		ctrl:         ctrl,
		id:           id,
		projectID:    projectID,
		scenarioName: scenarioName,
		loadMode:     loadMode,
		alerts:       newAlertEvaluator(id, ctrl.results, ctrl.hub, egress),
		thresholds:   append([]scenario.Threshold(nil), thresholds...),
		stopCh:       make(chan struct{}),
		nodes:        make(map[string]*runNodeState),
		buckets:      make(map[int64]*seqBucket),
		stepReqs:     make(map[string]int64),
		stepErrors:   make(map[string]int64),
	}
}

func (r *activeRun) recordPrometheusTick(view *metrics.SnapshotView) exporter.PrometheusRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	if view != nil {
		r.latestView = view
		r.totalReqs += view.Requests
		r.totalErrors += view.Errors
		for _, step := range view.Steps {
			r.stepReqs[step.ID] += step.Requests
			r.stepErrors[step.ID] += step.Errors
		}
	}
	return r.prometheusRunLocked()
}

func (r *activeRun) prometheusRun() exporter.PrometheusRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prometheusRunLocked()
}

func (r *activeRun) prometheusRunLocked() exporter.PrometheusRun {
	stepReqs := make(map[string]int64, len(r.stepReqs))
	for k, v := range r.stepReqs {
		stepReqs[k] = v
	}
	stepErrs := make(map[string]int64, len(r.stepErrors))
	for k, v := range r.stepErrors {
		stepErrs[k] = v
	}
	return exporter.PrometheusRun{
		Active:            !r.finalized,
		RunID:             r.id,
		ProjectID:         r.projectID,
		ScenarioName:      r.scenarioName,
		Snapshot:          r.latestView,
		RequestsTotal:     r.totalReqs,
		ErrorsTotal:       r.totalErrors,
		StepRequestsTotal: stepReqs,
		StepErrorsTotal:   stepErrs,
	}
}

func (r *activeRun) setStartedAt(t time.Time) {
	r.mu.Lock()
	r.startedAt = t
	r.mu.Unlock()
}

func (r *activeRun) initNodes(nodes []cluster.NodeInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range nodes {
		r.nodes[n.ID] = &runNodeState{id: n.ID, name: n.Name}
	}
}

func (r *activeRun) markDispatchFailed(nodeID, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st := r.nodes[nodeID]; st != nil {
		st.lost = true
		st.finished = true
		st.errText = reason
	}
}

func (r *activeRun) nodeIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.nodes))
	for id := range r.nodes {
		out = append(out, id)
	}
	return out
}

func (r *activeRun) stopLocalTimers() {
	r.stopOnce.Do(func() { close(r.stopCh) })
	r.mu.Lock()
	for _, st := range r.nodes {
		if st.graceTimer != nil {
			st.graceTimer.Stop()
			st.graceTimer = nil
		}
	}
	r.mu.Unlock()
}

func (r *activeRun) startFlushLoop() {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(400 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-r.stopCh:
				return
			case <-r.ctrl.ctx.Done():
				return
			case <-t.C:
				ready := r.takeReadyBuckets(false)
				r.persistAndPublish(ready)
			}
		}
	}()
}

func (r *activeRun) allPendingSeenLocked(b *seqBucket) bool {
	for id, st := range r.nodes {
		if !st.finished && !st.lost && !b.seen[id] {
			return false
		}
	}
	return true
}

func (r *activeRun) onSnapshot(nodeID string, rs *cluster.RunSnapshot) {
	var snap metrics.Snapshot
	if err := snap.UnmarshalBinary(rs.Snapshot); err != nil {
		r.ctrl.log.Warn("解析节点快照失败", "run_id", r.id, "node_id", nodeID, "seq", rs.Seq, "err", err)
		return
	}

	r.mu.Lock()
	if r.finalized {
		r.mu.Unlock()
		return
	}
	b := r.buckets[rs.Seq]
	if b == nil {
		b = &seqBucket{
			seq:       rs.Seq,
			snap:      &snap,
			seen:      map[string]bool{nodeID: true},
			createdAt: time.Now(),
		}
		r.buckets[rs.Seq] = b
	} else if !b.seen[nodeID] {
		if err := b.snap.Merge(&snap, metrics.MergeNodes); err == nil {
			b.seen[nodeID] = true
		}
	}
	r.mu.Unlock()

	ready := r.takeReadyBuckets(false)
	r.persistAndPublish(ready)
}

func (r *activeRun) takeReadyBuckets(flushAll bool) []*metrics.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buckets) == 0 {
		return nil
	}
	if flushAll {
		seqs := make([]int64, 0, len(r.buckets))
		for seq := range r.buckets {
			seqs = append(seqs, seq)
		}
		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		out := make([]*metrics.Snapshot, 0, len(seqs))
		for _, seq := range seqs {
			b := r.buckets[seq]
			delete(r.buckets, seq)
			out = append(out, b.snap)
			if seq >= r.nextFlushSeq {
				r.nextFlushSeq = seq + 1
			}
		}
		return out
	}

	now := time.Now()
	var out []*metrics.Snapshot
	for {
		b := r.buckets[r.nextFlushSeq]
		if b == nil {
			break
		}
		if !r.allPendingSeenLocked(b) && now.Sub(b.createdAt) < 1500*time.Millisecond {
			break
		}
		delete(r.buckets, r.nextFlushSeq)
		out = append(out, b.snap)
		r.nextFlushSeq++
	}
	return out
}

func (r *activeRun) persistAndPublish(snaps []*metrics.Snapshot) {
	for _, s := range snaps {
		view := s.View()
		if err := r.ctrl.results.AppendSnapshot(r.ctrl.ctx, r.id, view); err != nil {
			r.ctrl.log.Warn("保存每秒快照失败", "run_id", r.id, "seq", s.Seq, "err", err)
		}
		r.alerts.evaluateSnapshot(r.ctrl.ctx, s)
		promRun := r.recordPrometheusTick(view)
		if r.ctrl.sink != nil {
			if cfg := r.ctrl.sink.cfg.Load(); cfg != nil && cfg.Enabled {
				r.ctrl.sink.enqueue(sinkBatch{
					runID:        r.id,
					projectID:    r.projectID,
					scenarioName: r.scenarioName,
					view:         view,
					totalReqs:    promRun.RequestsTotal,
					totalErrors:  promRun.ErrorsTotal,
					promState:    exporter.PrometheusState{Run: promRun},
				})
			}
		}
		r.ctrl.hub.publish(Event{
			Type:     EventSnapshot,
			RunID:    r.id,
			Snapshot: view,
		})
		r.checkAbortThresholds(s.Time, view)
	}
}

func (r *activeRun) checkAbortThresholds(snapTime time.Time, view *metrics.SnapshotView) {
	if len(r.thresholds) == 0 || view == nil {
		return
	}
	r.mu.Lock()
	already := r.thresholdAborted
	startedAt := r.startedAt
	r.mu.Unlock()
	if already {
		return
	}
	if startedAt.IsZero() {
		startedAt = snapTime
	}
	elapsed := snapTime.Sub(startedAt)
	if snapTime.IsZero() {
		elapsed = time.Since(startedAt)
	}
	if elapsed < 0 {
		elapsed = 0
	}
	msg, hit := scenario.CheckAbortThresholds(r.thresholds, view, elapsed)
	if !hit {
		return
	}
	fullMsg := "触发中止阈值：" + msg

	r.mu.Lock()
	if r.thresholdAborted {
		r.mu.Unlock()
		return
	}
	r.thresholdAborted = true
	r.thresholdAbortMsg = fullMsg
	r.mu.Unlock()

	r.alerts.observeCondition(r.ctrl.ctx, "", "threshold", store.AlertCritical, fullMsg, 0, 0, true, snapTime)
	go func() {
		_, _ = r.ctrl.StopRun(r.ctrl.ctx, r.id, false)
	}()
}

func (r *activeRun) onNodeJoined(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.nodes[nodeID]
	if st != nil && st.graceTimer != nil {
		st.graceTimer.Stop()
		st.graceTimer = nil
	}
}

func (r *activeRun) onNodeLeft(nodeID, nodeName string, reason error) {
	r.mu.Lock()
	st := r.nodes[nodeID]
	if st == nil || st.finished || st.lost || r.finalized {
		r.mu.Unlock()
		return
	}
	if st.graceTimer != nil {
		st.graceTimer.Stop()
	}
	st.graceTimer = time.AfterFunc(r.ctrl.gracePeriod, func() {
		r.expireNode(nodeID, nodeName, reason)
	})
	r.mu.Unlock()
}

func (r *activeRun) expireNode(nodeID, nodeName string, reason error) {
	r.mu.Lock()
	st := r.nodes[nodeID]
	if st == nil || st.finished || st.lost || r.finalized {
		r.mu.Unlock()
		return
	}
	st.lost = true
	st.graceTimer = nil
	msg := fmt.Sprintf("节点 %s 连接断开", nodeName)
	if reason != nil {
		msg += "：" + reason.Error()
	}
	st.errText = msg
	allDone := r.allNodesDoneLocked()
	if allDone {
		r.finalized = true
	}
	r.mu.Unlock()

	if allDone {
		r.finalize()
	} else {
		ready := r.takeReadyBuckets(false)
		r.persistAndPublish(ready)
	}
}

func (r *activeRun) onNodeFinished(nodeID string, f *cluster.RunFinished) {
	var sum *metrics.Snapshot
	if len(f.Summary) > 0 {
		var s metrics.Snapshot
		if err := s.UnmarshalBinary(f.Summary); err == nil {
			sum = &s
		}
	}

	r.mu.Lock()
	if r.finalized {
		r.mu.Unlock()
		return
	}
	st := r.nodes[nodeID]
	if st == nil {
		st = &runNodeState{id: nodeID, name: nodeID}
		r.nodes[nodeID] = st
	}
	if st.graceTimer != nil {
		st.graceTimer.Stop()
		st.graceTimer = nil
	}
	st.finished = true
	st.errText = f.Error
	st.summary = sum
	allDone := r.allNodesDoneLocked()
	if allDone {
		r.finalized = true
	}
	r.mu.Unlock()

	if allDone {
		r.finalize()
	} else {
		ready := r.takeReadyBuckets(false)
		r.persistAndPublish(ready)
	}
}

func (r *activeRun) allNodesDoneLocked() bool {
	for _, st := range r.nodes {
		if !st.finished && !st.lost {
			return false
		}
	}
	return true
}

func (r *activeRun) finalize() {
	r.stopLocalTimers()
	r.wg.Wait()

	remaining := r.takeReadyBuckets(true)
	r.persistAndPublish(remaining)
	r.alerts.finish()

	r.mu.Lock()
	var merged *metrics.Snapshot
	var errs []string
	var nodeOrder []*runNodeState
	for _, st := range r.nodes {
		nodeOrder = append(nodeOrder, st)
	}
	sort.Slice(nodeOrder, func(i, j int) bool { return nodeOrder[i].id < nodeOrder[j].id })
	for _, st := range nodeOrder {
		if st.errText != "" {
			errs = append(errs, st.errText)
		}
		if st.summary != nil {
			if merged == nil {
				merged = st.summary
			} else {
				_ = merged.Merge(st.summary, metrics.MergeNodes)
			}
		}
	}
	if r.thresholdAbortMsg != "" {
		errs = append(errs, r.thresholdAbortMsg)
	}
	r.mu.Unlock()

	finalStatus := store.RunFinished
	errText := strings.Join(errs, "；")
	if errText != "" {
		finalStatus = store.RunFailed
	}

	var sumJSON json.RawMessage
	var mergedBin []byte
	if merged != nil {
		merged.TrimSamples(store.DefaultMaxSamplesPerKind)
		if b, err := merged.MarshalBinary(); err == nil {
			mergedBin = b
		}
		if b, err := json.Marshal(merged.View()); err == nil {
			sumJSON = b
		}
	}

	if err := r.ctrl.results.FinishRun(r.ctrl.ctx, r.id, store.RunFinish{
		Status:     finalStatus,
		FinishedAt: time.Now(),
		Error:      errText,
		Summary:    sumJSON,
		Merged:     mergedBin,
	}); err != nil {
		r.ctrl.log.Error("保存压测汇总失败", "run_id", r.id, "err", err)
	}

	r.ctrl.clearActive(r)
	r.ctrl.hub.publish(Event{
		Type:   EventRunStatus,
		RunID:  r.id,
		Status: finalStatus,
		Error:  errText,
	})
}

func cloneDefinition(def *scenario.Definition) *scenario.Definition {
	if def == nil {
		return nil
	}
	b, err := json.Marshal(def)
	if err != nil {
		cp := *def
		return &cp
	}
	cp, err := scenario.Parse(b, scenario.FormatJSON)
	if err != nil {
		c := *def
		return &c
	}
	return cp
}
