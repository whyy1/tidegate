package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"tidegate/internal/auth"
	"tidegate/internal/controller"
	"tidegate/internal/store"
)

const (
	maxMCPRequestBytes   = 1 << 20   // 1 MiB JSON-RPC body cap
	maxMCPOutputBytes    = 48 << 10  // 48 KiB deterministic tool output truncation
	defaultRatePerMinute = 120       // per actor sliding window request cap
	defaultQueryTimeout  = 15 * time.Second
	defaultDebugTimeout  = 50 * time.Second
)

// AuditFunc records MCP tool calls into audit_logs.
type AuditFunc func(r *http.Request, action, resource, resourceID string, detail any)

// Options configure an MCP Server instance.
type Options struct {
	Store      *store.Store
	Results    *store.ResultDB
	Controller *controller.Controller
	Auth       *auth.Service
	Version    string
	Audit      AuditFunc
}

// Server exposes Tidegate's authenticated, RBAC-scoped MCP JSON-RPC 2.0 endpoint.
type Server struct {
	store   *store.Store
	results *store.ResultDB
	ctrl    *controller.Controller
	auth    *auth.Service
	version string
	audit   AuditFunc

	tools   []toolDef
	byName  map[string]toolDef
	limiter *rateWindow
}

// NewServer constructs an MCP Server wired with Tidegate subsystems.
func NewServer(opts Options) *Server {
	s := &Server{
		store:   opts.Store,
		results: opts.Results,
		ctrl:    opts.Controller,
		auth:    opts.Auth,
		version: opts.Version,
		audit:   opts.Audit,
		limiter: newRateWindow(defaultRatePerMinute, time.Minute),
	}
	if s.version == "" {
		s.version = "0.1.0-dev"
	}
	defs := buildTools()
	s.tools = defs
	s.byName = make(map[string]toolDef, len(defs))
	for _, d := range defs {
		s.byName[d.spec.Name] = d
	}
	return s
}

// ServeHTTP handles MCP Streamable HTTP JSON-RPC 2.0 requests on /api/v1/mcp.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeHTTPJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "MCP 端点仅支持 POST Streamable HTTP 请求")
		return
	}

	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		auth.WriteError(w, auth.ErrUnauthenticated)
		return
	}
	if err := auth.CheckScope(r.Context(), "mcp"); err != nil {
		auth.WriteError(w, err)
		return
	}

	pol, err := s.ctrl.GetSecurityPolicy(r.Context())
	if err != nil {
		writeHTTPJSONError(w, http.StatusInternalServerError, "internal_error", "读取安全策略失败")
		return
	}
	if pol.MCPDisabled {
		writeHTTPJSONError(w, http.StatusForbidden, "mcp_disabled", "管理员已全局关闭 MCP 服务入口")
		return
	}

	_, _, rateCap := effectiveMCPLimits(r.Context(), pol)
	actorKey := fmt.Sprintf("u:%d:%s", u.ID, hostOnly(r.RemoteAddr))
	if tok, hasTok := auth.TokenFromContext(r.Context()); hasTok {
		actorKey = "tok:" + tok.ID
	}
	if !s.limiter.allowLimit(actorKey, rateCap, time.Now()) {
		w.Header().Set("Retry-After", "10")
		writeHTTPJSONError(w, http.StatusTooManyRequests, "rate_limited", "MCP 请求过于频繁，请稍后再试")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxMCPRequestBytes)
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeRPC(w, http.StatusRequestEntityTooLarge, json.RawMessage("null"), nil, &rpcError{
				Code:    CodeInvalidRequest,
				Message: "MCP 请求体不能超过 1 MiB",
			})
			return
		}
		writeRPC(w, http.StatusBadRequest, json.RawMessage("null"), nil, &rpcError{
			Code:    CodeParseError,
			Message: "读取请求体失败",
		})
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeRPC(w, http.StatusBadRequest, json.RawMessage("null"), nil, &rpcError{
			Code:    CodeParseError,
			Message: "无效的 JSON-RPC 2.0 报文",
		})
		return
	}
	if req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
		id := json.RawMessage("null")
		if len(req.ID) > 0 {
			id = req.ID
		}
		writeRPC(w, http.StatusBadRequest, id, nil, &rpcError{
			Code:    CodeInvalidRequest,
			Message: "jsonrpc 必须为 2.0 且 method 不能为空",
		})
		return
	}

	// JSON-RPC notifications carry no id and receive 202 Accepted per MCP Streamable HTTP.
	if !req.hasID() {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusAccepted)
		return
	}

	result, rpcErr := s.dispatch(r, u, pol, req)
	writeRPC(w, http.StatusOK, req.ID, result, rpcErr)
}

func (s *Server) dispatch(r *http.Request, u *auth.User, pol controller.SecurityPolicy, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p InitializeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &p)
		}
		proto := negotiateProtocol(p.ProtocolVersion)
		caps := ServerCapabilities{
			Tools: &ToolsCapability{ListChanged: false},
		}
		if !pol.MCPDisableResources {
			caps.Resources = &ResourcesCapability{ListChanged: false}
		}
		if !pol.MCPDisablePrompts {
			caps.Prompts = &PromptsCapability{ListChanged: false}
		}
		return InitializeResult{
			ProtocolVersion: proto,
			Capabilities:    caps,
			ServerInfo: EntityInfo{
				Name:    "tidegate",
				Version: s.version,
			},
			Instructions: "Tidegate 接口调试、场景编排与分布式压测安全控制台。支持 MCP Tools、Resources (tidegate://...) 与 Prompts；所有敏感变量与请求凭据已自动脱敏，访问受项目级 RBAC、Token Scope、目标白名单及规模护栏限制。",
		}, nil

	case "ping":
		return map[string]any{}, nil

	case "tools/list":
		hasWriteProj := false
		hasWriteChecked := false
		specs := make([]Tool, 0, len(s.tools))
		for _, td := range s.tools {
			if pol.IsMCPToolDisabled(td.spec.Name) {
				continue
			}
			if td.scope != "" {
				if err := auth.CheckScope(r.Context(), td.scope); err != nil {
					continue
				}
			}
			if err := auth.CheckMCPTool(r.Context(), td.spec.Name); err != nil {
				continue
			}
			if !td.readOnly && u != nil && !u.Role.CanWrite() && td.spec.Name != "import_api_spec" {
				if !hasWriteChecked {
					hasWriteProj = s.hasAnyWritableProject(r.Context(), u)
					hasWriteChecked = true
				}
				if !hasWriteProj {
					continue
				}
			}
			specs = append(specs, td.spec)
		}
		return map[string]any{"tools": specs}, nil

	case "tools/call":
		var p CallToolParams
		if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.Name) == "" {
			return nil, &rpcError{Code: CodeInvalidParams, Message: "tools/call 参数无效，缺少 tool name"}
		}
		td, ok := s.byName[p.Name]
		if !ok {
			return nil, &rpcError{Code: CodeInvalidParams, Message: fmt.Sprintf("未知的 MCP 工具 %q", p.Name)}
		}
		args := p.Arguments
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		start := time.Now()
		res := s.invokeTool(r, u, pol, td, args)
		durMs := time.Since(start).Milliseconds()
		if s.audit != nil {
			detail := map[string]any{
				"tool":        td.spec.Name,
				"is_error":    res.IsError,
				"duration_ms": durMs,
			}
			var ids struct {
				ProjectID string `json:"project_id"`
				RunID     string `json:"run_id"`
			}
			if json.Unmarshal(args, &ids) == nil {
				if pid := strings.TrimSpace(ids.ProjectID); pid != "" {
					detail["project_id"] = pid
				}
				if rid := strings.TrimSpace(ids.RunID); rid != "" {
					detail["run_id"] = rid
				}
			}
			if tok, hasTok := auth.TokenFromContext(r.Context()); hasTok {
				detail["token_prefix"] = tok.Prefix
			}
			s.audit(r, "mcp.tool."+td.spec.Name, "mcp_tool", td.spec.Name, detail)
		}
		return res, nil

	case "resources/list":
		if pol.MCPDisableResources {
			return nil, &rpcError{Code: CodeInvalidRequest, Message: "管理员已在安全策略中禁用 MCP 只读资源"}
		}
		items, rpcErr := s.listMCPResources(r.Context(), u)
		if rpcErr != nil {
			return nil, rpcErr
		}
		return map[string]any{"resources": items}, nil

	case "resources/templates/list":
		if pol.MCPDisableResources {
			return nil, &rpcError{Code: CodeInvalidRequest, Message: "管理员已在安全策略中禁用 MCP 只读资源"}
		}
		if err := auth.CheckScope(r.Context(), "read"); err != nil {
			return nil, &rpcError{Code: CodeInvalidRequest, Message: err.Error()}
		}
		return map[string]any{"resourceTemplates": s.listMCPResourceTemplates()}, nil

	case "resources/read":
		if pol.MCPDisableResources {
			return nil, &rpcError{Code: CodeInvalidRequest, Message: "管理员已在安全策略中禁用 MCP 只读资源"}
		}
		start := time.Now()
		out, rpcErr := s.readMCPResource(r.Context(), u, req.Params)
		if s.audit != nil {
			in, _ := decodeArgs[ReadResourceParams](req.Params)
			uri := strings.TrimSpace(in.URI)
			detail := map[string]any{
				"uri":         uri,
				"is_error":    rpcErr != nil,
				"duration_ms": time.Since(start).Milliseconds(),
			}
			if tok, hasTok := auth.TokenFromContext(r.Context()); hasTok {
				detail["token_prefix"] = tok.Prefix
			}
			s.audit(r, "mcp.resource.read", "mcp_resource", uri, detail)
		}
		if rpcErr != nil {
			return nil, rpcErr
		}
		return out, nil

	case "prompts/list":
		if pol.MCPDisablePrompts {
			return nil, &rpcError{Code: CodeInvalidRequest, Message: "管理员已在安全策略中禁用 MCP 提示词模板"}
		}
		return map[string]any{"prompts": s.listMCPPrompts()}, nil

	case "prompts/get":
		if pol.MCPDisablePrompts {
			return nil, &rpcError{Code: CodeInvalidRequest, Message: "管理员已在安全策略中禁用 MCP 提示词模板"}
		}
		start := time.Now()
		out, rpcErr := s.getMCPPrompt(req.Params)
		if s.audit != nil {
			in, _ := decodeArgs[GetPromptParams](req.Params)
			name := strings.TrimSpace(in.Name)
			detail := map[string]any{
				"prompt":      name,
				"is_error":    rpcErr != nil,
				"duration_ms": time.Since(start).Milliseconds(),
			}
			if tok, hasTok := auth.TokenFromContext(r.Context()); hasTok {
				detail["token_prefix"] = tok.Prefix
			}
			s.audit(r, "mcp.prompt.get", "mcp_prompt", name, detail)
		}
		if rpcErr != nil {
			return nil, rpcErr
		}
		return out, nil

	default:
		return nil, &rpcError{Code: CodeMethodNotFound, Message: fmt.Sprintf("不支持的 MCP 方法 %q", req.Method)}
	}
}

func (s *Server) invokeTool(r *http.Request, u *auth.User, pol controller.SecurityPolicy, td toolDef, args json.RawMessage) *CallToolResult {
	if pol.IsMCPToolDisabled(td.spec.Name) {
		if td.spec.Name == "manage_load_run" && pol.MCPDisableLoadRuns {
			return toolErrorResult(errors.New("管理员已在安全策略中禁用通过 MCP 控制或启动压测任务"))
		}
		return toolErrorResult(fmt.Errorf("管理员已在安全策略中禁用 MCP 工具 %q", td.spec.Name))
	}
	if td.scope != "" {
		if err := auth.CheckScope(r.Context(), td.scope); err != nil {
			return toolErrorResult(err)
		}
	}
	if err := auth.CheckMCPTool(r.Context(), td.spec.Name); err != nil {
		return toolErrorResult(err)
	}
	if !td.readOnly && !u.Role.CanWrite() && s.auth == nil {
		return toolErrorResult(fmt.Errorf("权限不足：当前令牌/用户角色为 %s，仅只读工具允许访问（需要 member 或 admin 权限）", u.Role))
	}
	if !td.readOnly && !u.Role.CanWrite() && td.spec.Name != "import_api_spec" {
		// Global viewers cannot write unless an explicit per-project ACL grants member;
		// reject early when caller has no project_id or verify via authorizeProject below.
		var check struct {
			ProjectID string `json:"project_id"`
			RunID     string `json:"run_id"`
		}
		_ = json.Unmarshal(args, &check)
		if strings.TrimSpace(check.ProjectID) == "" && strings.TrimSpace(check.RunID) == "" {
			return toolErrorResult(fmt.Errorf("权限不足：当前令牌/用户角色为 %s，仅只读工具允许访问（需要 member 或 admin 权限）", u.Role))
		}
	}

	timeout := defaultQueryTimeout
	if td.longOp {
		timeout = defaultDebugTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	out, err := td.run(s, ctx, u, pol, args)
	if err != nil {
		return toolErrorResult(err)
	}
	return formatToolSuccess(out)
}

func (s *Server) accessibleProjects(ctx context.Context, u *auth.User) ([]*store.Project, error) {
	list, err := s.store.ListProjects()
	if err != nil {
		return nil, err
	}
	if s.auth == nil || u == nil {
		return list, nil
	}
	tok, _ := auth.TokenFromContext(ctx)
	out := make([]*store.Project, 0, len(list))
	for _, pr := range list {
		if _, allowed, err := s.auth.ResolveProjectRole(ctx, u, tok, pr.ID); err == nil && allowed {
			out = append(out, pr)
		}
	}
	return out, nil
}

// ToolCatalogItem describes an MCP tool's metadata, scope, and global policy availability.
type ToolCatalogItem struct {
	Name            string `json:"name"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Category        string `json:"category"` // read | write | debug | run
	Scope           string `json:"scope"`
	PreviewReadOnly bool   `json:"preview_read_only,omitempty"`
	ReadOnly        bool   `json:"read_only"`
	Destructive     bool   `json:"destructive"`
	OpenWorld       bool   `json:"open_world"`
	GloballyEnabled bool   `json:"globally_enabled"`
}

// ResourceCatalogItem summarizes an MCP Resource or Resource Template for console inspection.
type ResourceCatalogItem struct {
	URI             string `json:"uri"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Scope           string `json:"scope"`
	GloballyEnabled bool   `json:"globally_enabled"`
}

// PromptCatalogItem summarizes a registered MCP Prompt template and its arguments.
type PromptCatalogItem struct {
	Name            string           `json:"name"`
	Description     string           `json:"description"`
	Arguments       []PromptArgument `json:"arguments,omitempty"`
	GloballyEnabled bool             `json:"globally_enabled"`
}

// ResourcesCatalog returns all static and templated tidegate:// MCP resources.
func (s *Server) ResourcesCatalog(pol controller.SecurityPolicy) []ResourceCatalogItem {
	enabled := !pol.MCPDisabled && !pol.MCPDisableResources
	out := []ResourceCatalogItem{
		{
			URI:             "tidegate://projects",
			Name:            "Tidegate 项目列表（已脱敏）",
			Description:     "当前账号/令牌有权访问的全部项目元数据及脱敏环境变量列表。",
			Scope:           "read",
			GloballyEnabled: enabled,
		},
	}
	for _, tpl := range s.listMCPResourceTemplates() {
		out = append(out, ResourceCatalogItem{
			URI:             tpl.URITemplate,
			Name:            tpl.Name,
			Description:     tpl.Description,
			Scope:           "read",
			GloballyEnabled: enabled,
		})
	}
	return out
}

// PromptsCatalog returns all built-in diagnostic and load-design prompt templates.
func (s *Server) PromptsCatalog(pol controller.SecurityPolicy) []PromptCatalogItem {
	enabled := !pol.MCPDisabled && !pol.MCPDisablePrompts
	raw := s.listMCPPrompts()
	out := make([]PromptCatalogItem, 0, len(raw))
	for _, p := range raw {
		out = append(out, PromptCatalogItem{
			Name:            p.Name,
			Description:     p.Description,
			Arguments:       p.Arguments,
			GloballyEnabled: enabled,
		})
	}
	return out
}

func effectiveMCPLimits(ctx context.Context, pol controller.SecurityPolicy) (maxVUs, maxDurSec, ratePerMin int) {
	maxVUs = pol.EffectiveMCPMaxVUs()
	maxDurSec = int(pol.EffectiveMCPMaxDuration().Seconds())
	ratePerMin = pol.EffectiveMCPRatePerMin()
	if tok, ok := auth.TokenFromContext(ctx); ok && tok != nil {
		if tok.MCPMaxVUs > 0 && tok.MCPMaxVUs < maxVUs {
			maxVUs = tok.MCPMaxVUs
		}
		if tok.MCPMaxDurationSec > 0 && tok.MCPMaxDurationSec < maxDurSec {
			maxDurSec = tok.MCPMaxDurationSec
		}
		if tok.RatePerMin > 0 && tok.RatePerMin < ratePerMin {
			ratePerMin = tok.RatePerMin
		}
	}
	return maxVUs, maxDurSec, ratePerMin
}

func toolCategory(td toolDef) string {
	switch {
	case td.readOnly:
		return "read"
	case td.spec.Name == "save_api" || td.spec.Name == "import_api_spec" || td.spec.Name == "save_scenario":
		return "write"
	case td.scope == "run":
		return "run"
	default:
		return "debug"
	}
}

// Catalog returns metadata for all registered MCP tools evaluated against pol.
func (s *Server) Catalog(pol controller.SecurityPolicy) []ToolCatalogItem {
	out := make([]ToolCatalogItem, 0, len(s.tools))
	for _, td := range s.tools {
		title := td.spec.Name
		var destructive, openWorld bool
		if td.spec.Annotations != nil {
			if td.spec.Annotations.Title != "" {
				title = td.spec.Annotations.Title
			}
			if td.spec.Annotations.DestructiveHint != nil {
				destructive = *td.spec.Annotations.DestructiveHint
			}
			if td.spec.Annotations.OpenWorldHint != nil {
				openWorld = *td.spec.Annotations.OpenWorldHint
			}
		}
		scope := td.scope
		previewRO := false
		if td.spec.Name == "import_api_spec" {
			scope = "write"
			previewRO = true
		}
		out = append(out, ToolCatalogItem{
			Name:            td.spec.Name,
			Title:           title,
			Description:     td.spec.Description,
			Category:        toolCategory(td),
			Scope:           scope,
			PreviewReadOnly: previewRO,
			ReadOnly:        td.readOnly,
			Destructive:     destructive,
			OpenWorld:       openWorld,
			GloballyEnabled: !pol.IsMCPToolDisabled(td.spec.Name),
		})
	}
	return out
}

func (s *Server) hasAnyWritableProject(ctx context.Context, u *auth.User) bool {
	if s.auth == nil || u == nil {
		return true
	}
	if u.Role.CanWrite() {
		return true
	}
	list, err := s.store.ListProjects()
	if err != nil {
		return false
	}
	tok, _ := auth.TokenFromContext(ctx)
	for _, pr := range list {
		if role, allowed, err := s.auth.ResolveProjectRole(ctx, u, tok, pr.ID); err == nil && allowed && role.CanWrite() {
			return true
		}
	}
	return false
}

func (s *Server) authorizeProject(ctx context.Context, u *auth.User, pid string, needWrite bool) (auth.Role, error) {
	if s.auth == nil {
		if u != nil && needWrite && !u.Role.CanWrite() {
			return u.Role, auth.ErrForbidden
		}
		if u != nil {
			return u.Role, nil
		}
		return auth.RoleAdmin, nil
	}
	tok, _ := auth.TokenFromContext(ctx)
	role, allowed, err := s.auth.ResolveProjectRole(ctx, u, tok, pid)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", fmt.Errorf("%w：无权访问项目 %s", auth.ErrForbidden, pid)
	}
	if needWrite && !role.CanWrite() {
		return role, fmt.Errorf("%w：在项目 %s 中仅具有只读角色 %s", auth.ErrForbidden, pid, role)
	}
	return role, nil
}

func (s *Server) authorizeRun(ctx context.Context, u *auth.User, runID string, needWrite bool) (*store.Run, auth.Role, error) {
	run, err := s.results.GetRun(ctx, runID)
	if err != nil {
		return nil, "", err
	}
	var effRole auth.Role
	if u != nil {
		effRole = u.Role
	}
	if run.ProjectID != "" {
		r, err := s.authorizeProject(ctx, u, run.ProjectID, needWrite)
		if err != nil {
			return nil, r, err
		}
		effRole = r
	} else if needWrite && (u == nil || !u.Role.CanWrite()) {
		return nil, effRole, auth.ErrForbidden
	}
	return run, effRole, nil
}

func (s *Server) buildProjectWorkspace(pid string) (map[string]any, error) {
	proj, err := s.store.GetProject(pid)
	if err != nil {
		return nil, err
	}
	tree, err := s.store.Tree(pid)
	if err != nil {
		return nil, err
	}
	envs, err := s.store.ListEnvironments(pid)
	if err != nil {
		return nil, err
	}
	scens, err := s.store.ListScenarios(pid)
	if err != nil {
		return nil, err
	}
	files, err := s.store.ListFiles(pid)
	if err != nil {
		return nil, err
	}
	scenSummaries := make([]map[string]any, 0, len(scens))
	for _, sc := range scens {
		scenSummaries = append(scenSummaries, map[string]any{
			"id":             sc.ID,
			"name":           sc.Name,
			"description":    sc.Description,
			"environment_id": sc.EnvironmentID,
			"step_count":     len(sc.Steps),
			"load_mode":      sc.Load.Mode,
		})
	}
	return map[string]any{
		"project":      proj,
		"tree":         redactTreeSecrets(tree),
		"environments": envs,
		"scenarios":    scenSummaries,
		"data_files":   files,
	}, nil
}

func (s *Server) buildRunReport(ctx context.Context, u *auth.User, id string, sampleLimit int) (map[string]any, error) {
	run, _, err := s.authorizeRun(ctx, u, id, false)
	if err != nil {
		return nil, err
	}
	alerts, err := s.results.Alerts(ctx, id)
	if err != nil {
		return nil, err
	}
	hist, _ := s.ctrl.RunHistogram(ctx, id)
	thresholds, thresholdsPassed := controller.EvaluateRunThresholds(run)
	var samples []store.SampleRecord
	if sampleLimit > 0 {
		samples, err = s.results.Samples(ctx, id, store.SampleQuery{Limit: sampleLimit})
		if err != nil {
			return nil, err
		}
		for i := range samples {
			samples[i].Data = redactSampleRecordData(samples[i].Data)
		}
	}
	return map[string]any{
		"run":               run,
		"thresholds":        thresholds,
		"thresholds_passed": thresholdsPassed,
		"alerts":            alerts,
		"histogram":         hist,
		"samples":           samples,
	}, nil
}

func negotiateProtocol(clientVer string) string {
	v := strings.TrimSpace(clientVer)
	for _, sup := range SupportedProtocolVersions {
		if v == sup {
			return v
		}
	}
	return LatestProtocolVersion
}

func formatToolSuccess(v any) *CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolErrorResult(fmt.Errorf("序列化结果失败：%w", err))
	}
	text := truncateUTF8Bytes(string(b), maxMCPOutputBytes)
	res := &CallToolResult{
		Content: []ContentBlock{{Type: "text", Text: text}},
	}
	if len(b) <= maxMCPOutputBytes {
		res.StructuredContent = v
	}
	return res
}

func toolErrorResult(err error) *CallToolResult {
	msg := "执行失败"
	if err != nil {
		msg = err.Error()
	}
	return &CallToolResult{
		Content: []ContentBlock{{Type: "text", Text: msg}},
		IsError: true,
	}
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n...[输出已按 48 KiB 安全上限截断]"
}

func writeRPC(w http.ResponseWriter, status int, id json.RawMessage, result any, err *rpcError) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
		Error:   err,
	})
}

func writeHTTPJSONError(w http.ResponseWriter, status int, code, message string) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func hostOnly(remoteAddr string) string {
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return h
	}
	return strings.Trim(remoteAddr, "[]")
}

// rateWindow tracks sliding 1-minute request counts per actor key.
type rateWindow struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string]windowCounter
}

type windowCounter struct {
	resetAt time.Time
	count   int
}

func newRateWindow(limit int, window time.Duration) *rateWindow {
	return &rateWindow{
		limit:   limit,
		window:  window,
		entries: make(map[string]windowCounter),
	}
}

func (rw *rateWindow) allowLimit(key string, limit int, now time.Time) bool {
	if limit <= 0 {
		limit = rw.limit
	}
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if len(rw.entries) > 2048 {
		for k, v := range rw.entries {
			if now.After(v.resetAt) {
				delete(rw.entries, k)
			}
		}
	}
	cur, ok := rw.entries[key]
	if !ok || now.After(cur.resetAt) {
		rw.entries[key] = windowCounter{resetAt: now.Add(rw.window), count: 1}
		return true
	}
	if cur.count >= limit {
		return false
	}
	cur.count++
	rw.entries[key] = cur
	return true
}
