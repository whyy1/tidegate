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
	Version    string
	Audit      AuditFunc
}

// Server exposes Tidegate's authenticated, RBAC-scoped MCP JSON-RPC 2.0 endpoint.
type Server struct {
	store   *store.Store
	results *store.ResultDB
	ctrl    *controller.Controller
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

	pol, err := s.ctrl.GetSecurityPolicy(r.Context())
	if err != nil {
		writeHTTPJSONError(w, http.StatusInternalServerError, "internal_error", "读取安全策略失败")
		return
	}
	if pol.MCPDisabled {
		writeHTTPJSONError(w, http.StatusForbidden, "mcp_disabled", "管理员已全局关闭 MCP 服务入口")
		return
	}

	actorKey := fmt.Sprintf("u:%d:%s", u.ID, hostOnly(r.RemoteAddr))
	if !s.limiter.allow(actorKey, time.Now()) {
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
		return InitializeResult{
			ProtocolVersion: proto,
			Capabilities: ServerCapabilities{
				Tools: &ToolsCapability{ListChanged: false},
			},
			ServerInfo: EntityInfo{
				Name:    "tidegate",
				Version: s.version,
			},
			Instructions: "Tidegate 接口调试、场景编排与分布式压测安全控制台。所有敏感变量与请求凭据已自动脱敏；写入、调试及压测操作受 RBAC 角色、目标白名单及 MCP 压测规模护栏限制。",
		}, nil

	case "ping":
		return map[string]any{}, nil

	case "tools/list":
		specs := make([]Tool, 0, len(s.tools))
		for _, td := range s.tools {
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
			if tok, hasTok := auth.TokenFromContext(r.Context()); hasTok {
				detail["token_prefix"] = tok.Prefix
			}
			s.audit(r, "mcp.tool."+td.spec.Name, "mcp_tool", td.spec.Name, detail)
		}
		return res, nil

	default:
		return nil, &rpcError{Code: CodeMethodNotFound, Message: fmt.Sprintf("不支持的 MCP 方法 %q", req.Method)}
	}
}

func (s *Server) invokeTool(r *http.Request, u *auth.User, pol controller.SecurityPolicy, td toolDef, args json.RawMessage) *CallToolResult {
	if !td.readOnly && !u.Role.CanWrite() {
		return toolErrorResult(fmt.Errorf("权限不足：当前令牌/用户角色为 %s，仅只读工具允许访问（需要 member 或 admin 权限）", u.Role))
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

func (rw *rateWindow) allow(key string, now time.Time) bool {
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
	if cur.count >= rw.limit {
		return false
	}
	cur.count++
	rw.entries[key] = cur
	return true
}
