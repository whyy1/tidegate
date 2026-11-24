package api

import (
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"tidegate/internal/auth"
	"tidegate/internal/controller"
	"tidegate/internal/mcp"
	"tidegate/internal/store"
)

// Options configure the Tidegate HTTP and WebSocket API server.
type Options struct {
	Controller        *controller.Controller
	Store             *store.Store
	Results           *store.ResultDB
	Auth              *auth.Service
	Guard             *auth.Guard
	Version           string
	TLS               bool
	ListenLoopback    bool
	AllowedOrigins    []string
	PublicURL         string
	ClusterPublicAddr string
	WebHandler        http.Handler
	Logger            *slog.Logger
}

// Server serves the /api/v1 REST endpoints, /api/v1/ws WebSocket stream,
// and the Web UI entrypoint.
type Server struct {
	opts         Options
	ctrl         *controller.Controller
	mcp          *mcp.Server
	store        *store.Store
	results      *store.ResultDB
	auth         *auth.Service
	guard        *auth.Guard
	log          *slog.Logger
	allowedHosts map[string]bool
	handler      http.Handler

	wsMu  sync.Mutex
	wsSet map[*wsConn]struct{}
}

// NewServer constructs the API HTTP handler.
func NewServer(opts Options) (*Server, error) {
	if opts.Controller == nil || opts.Store == nil || opts.Results == nil || opts.Auth == nil || opts.Guard == nil {
		return nil, newHTTPError(http.StatusInternalServerError, "internal_error", "API 服务缺少必需依赖")
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	allowed := map[string]bool{
		"localhost": true,
		"127.0.0.1": true,
		"::1":       true,
	}
	for _, o := range opts.AllowedOrigins {
		if u, err := url.Parse(strings.TrimSpace(o)); err == nil && u.Hostname() != "" {
			allowed[strings.ToLower(u.Hostname())] = true
		}
	}
	if strings.TrimSpace(opts.PublicURL) != "" {
		if u, err := url.Parse(strings.TrimSpace(opts.PublicURL)); err == nil && u.Hostname() != "" {
			allowed[strings.ToLower(u.Hostname())] = true
		}
	}

	s := &Server{
		opts:         opts,
		ctrl:         opts.Controller,
		store:        opts.Store,
		results:      opts.Results,
		auth:         opts.Auth,
		guard:        opts.Guard,
		log:          log,
		allowedHosts: allowed,
		wsSet:        make(map[*wsConn]struct{}),
	}
	s.mcp = mcp.NewServer(mcp.Options{
		Store:      opts.Store,
		Results:    opts.Results,
		Controller: opts.Controller,
		Auth:       opts.Auth,
		Version:    opts.Version,
		Audit:      s.audit,
	})
	s.handler = s.buildHandler()
	return s, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// Close closes all active WebSocket connections cleanly.
func (s *Server) Close() {
	s.wsMu.Lock()
	conns := make([]*wsConn, 0, len(s.wsSet))
	for wc := range s.wsSet {
		conns = append(conns, wc)
	}
	s.wsMu.Unlock()
	for _, wc := range conns {
		wc.close()
	}
}

func (s *Server) trackWS(wc *wsConn) {
	s.wsMu.Lock()
	s.wsSet[wc] = struct{}{}
	s.wsMu.Unlock()
}

func (s *Server) untrackWS(wc *wsConn) {
	s.wsMu.Lock()
	delete(s.wsSet, wc)
	s.wsMu.Unlock()
}

func (s *Server) buildHandler() http.Handler {
	apiMux := http.NewServeMux()

	authed := func(fn http.HandlerFunc) http.Handler {
		return s.guard.RequireSession(fn)
	}
	scoped := func(scope string, fn http.HandlerFunc) http.Handler {
		return s.guard.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := auth.CheckScope(r.Context(), scope); err != nil {
				writeError(w, err)
				return
			}
			fn(w, r)
		}))
	}
	scopedWriter := func(scope string, fn http.HandlerFunc) http.Handler {
		return s.guard.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := auth.UserFromContext(r.Context())
			if !ok || !u.Role.CanWrite() {
				writeError(w, auth.ErrForbidden)
				return
			}
			if err := auth.CheckScope(r.Context(), scope); err != nil {
				writeError(w, err)
				return
			}
			fn(w, r)
		}))
	}
	adminOnly := func(fn http.HandlerFunc) http.Handler {
		return s.guard.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := auth.UserFromContext(r.Context())
			if !ok || !u.Role.IsAdmin() {
				writeError(w, auth.ErrForbidden)
				return
			}
			if err := auth.CheckScope(r.Context(), "admin"); err != nil {
				writeError(w, err)
				return
			}
			fn(w, r)
		}))
	}
	projAccess := func(scope string, needWrite bool, fn http.HandlerFunc) http.Handler {
		return s.guard.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := auth.CheckScope(r.Context(), scope); err != nil {
				writeError(w, err)
				return
			}
			nextReq, ok := s.authorizeProject(w, r, r.PathValue("pid"), needWrite)
			if !ok {
				return
			}
			fn(w, nextReq)
		}))
	}
	projRead := func(fn http.HandlerFunc) http.Handler {
		return projAccess("read", false, fn)
	}
	projWrite := func(fn http.HandlerFunc) http.Handler {
		return projAccess("write", true, fn)
	}
	runAccess := func(scope string, needWrite bool, fn http.HandlerFunc) http.Handler {
		return s.guard.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := auth.CheckScope(r.Context(), scope); err != nil {
				writeError(w, err)
				return
			}
			run, err := s.results.GetRun(r.Context(), r.PathValue("id"))
			if err != nil {
				writeError(w, err)
				return
			}
			nextReq := r
			if run.ProjectID != "" {
				var ok bool
				nextReq, ok = s.authorizeProject(w, r, run.ProjectID, needWrite)
				if !ok {
					return
				}
			} else if needWrite {
				if u, ok := auth.UserFromContext(r.Context()); !ok || !u.Role.CanWrite() {
					writeError(w, auth.ErrForbidden)
					return
				}
			}
			fn(w, nextReq)
		}))
	}

	// Setup, authentication, users, 2FA, and API tokens.
	apiMux.HandleFunc("GET /api/v1/setup/status", s.handleSetupStatus)
	apiMux.Handle("POST /api/v1/setup", s.guard.RequireSecureTransport(http.HandlerFunc(s.handleSetup)))
	apiMux.Handle("POST /api/v1/auth/login", s.guard.RequireSecureTransport(http.HandlerFunc(s.handleLogin)))
	apiMux.Handle("POST /api/v1/auth/logout", authed(s.handleLogout))
	apiMux.Handle("GET /api/v1/auth/me", authed(s.handleMe))
	apiMux.Handle("POST /api/v1/auth/password", authed(s.handleChangePassword))
	apiMux.Handle("POST /api/v1/auth/totp/enroll", authed(s.handleEnrollTOTP))
	apiMux.Handle("POST /api/v1/auth/totp/confirm", authed(s.handleConfirmTOTP))
	apiMux.Handle("POST /api/v1/auth/totp/disable", authed(s.handleDisableTOTP))
	apiMux.Handle("GET /api/v1/auth/sessions", authed(s.handleListSessions))
	apiMux.Handle("POST /api/v1/auth/sessions/revoke-others", authed(s.handleRevokeOtherSessions))
	apiMux.Handle("DELETE /api/v1/auth/sessions/{id}", authed(s.handleRevokeSession))
	apiMux.Handle("GET /api/v1/auth/tokens", authed(s.handleListAPITokens))
	apiMux.Handle("POST /api/v1/auth/tokens", authed(s.handleCreateAPIToken))
	apiMux.Handle("DELETE /api/v1/auth/tokens/{id}", authed(s.handleRevokeAPIToken))

	apiMux.Handle("GET /api/v1/users", adminOnly(s.handleListUsers))
	apiMux.Handle("POST /api/v1/users", adminOnly(s.handleCreateUser))
	apiMux.Handle("PATCH /api/v1/users/{id}", adminOnly(s.handlePatchUser))
	apiMux.Handle("DELETE /api/v1/users/{id}", adminOnly(s.handleDeleteUser))
	apiMux.Handle("POST /api/v1/users/{id}/password", adminOnly(s.handleAdminResetUserPassword))
	apiMux.Handle("DELETE /api/v1/users/{id}/totp", adminOnly(s.handleAdminResetUserTOTP))
	apiMux.Handle("DELETE /api/v1/users/{id}/sessions", adminOnly(s.handleAdminRevokeUserSessions))
	apiMux.Handle("GET /api/v1/users/{id}/projects", adminOnly(s.handleGetUserProjects))
	apiMux.Handle("PUT /api/v1/users/{id}/projects", adminOnly(s.handlePutUserProjects))
	apiMux.Handle("GET /api/v1/admin/tokens", adminOnly(s.handleAdminListAPITokens))
	apiMux.Handle("GET /api/v1/mcp/catalog", scoped("read", s.handleMCPCatalog))
	apiMux.Handle("GET /api/v1/mcp/activity", scoped("read", s.handleMCPActivity))
	apiMux.Handle("GET /api/v1/settings/security", scoped("read", s.handleGetSecurityPolicy))
	apiMux.Handle("PUT /api/v1/settings/security", adminOnly(s.handlePutSecurityPolicy))
	apiMux.Handle("GET /api/v1/settings/metrics-sink", scoped("read", s.handleGetMetricsSink))
	apiMux.Handle("PUT /api/v1/settings/metrics-sink", adminOnly(s.handlePutMetricsSink))
	apiMux.Handle("POST /api/v1/settings/metrics-sink/test", adminOnly(s.handleTestMetricsSink))
	apiMux.Handle("GET /api/v1/metrics/prometheus", scoped("read", s.handlePrometheusMetrics))
	apiMux.Handle("GET /api/v1/audit-logs", adminOnly(s.handleListAuditLogs))

	// Projects & ACL.
	apiMux.Handle("GET /api/v1/projects", scoped("read", s.handleListProjects))
	apiMux.Handle("POST /api/v1/projects", scopedWriter("write", s.handleCreateProject))
	apiMux.Handle("POST /api/v1/projects/import", scopedWriter("write", s.handleImportProjectArchive))
	apiMux.Handle("GET /api/v1/projects/{pid}", projRead(s.handleGetProject))
	apiMux.Handle("PATCH /api/v1/projects/{pid}", projWrite(s.handlePatchProject))
	apiMux.Handle("DELETE /api/v1/projects/{pid}", projWrite(s.handleDeleteProject))
	apiMux.Handle("GET /api/v1/projects/{pid}/acl", adminOnly(s.handleGetProjectACL))
	apiMux.Handle("PUT /api/v1/projects/{pid}/acl", adminOnly(s.handlePutProjectACL))
	apiMux.Handle("GET /api/v1/projects/{pid}/export", projRead(s.handleExportProject))
	apiMux.Handle("POST /api/v1/projects/{pid}/import/preview", projRead(s.handleImportPreview))
	apiMux.Handle("POST /api/v1/projects/{pid}/import/apply", projWrite(s.handleImportApply))

	// API tree, folders, APIs.
	apiMux.Handle("GET /api/v1/projects/{pid}/tree", projRead(s.handleTree))
	apiMux.Handle("POST /api/v1/projects/{pid}/folders", projWrite(s.handleCreateFolder))
	apiMux.Handle("GET /api/v1/projects/{pid}/folders/{id}", projRead(s.handleGetFolder))
	apiMux.Handle("PATCH /api/v1/projects/{pid}/folders/{id}", projWrite(s.handlePatchFolder))
	apiMux.Handle("DELETE /api/v1/projects/{pid}/folders/{id}", projWrite(s.handleDeleteFolder))
	apiMux.Handle("POST /api/v1/projects/{pid}/apis", projWrite(s.handleCreateAPI))
	apiMux.Handle("POST /api/v1/projects/{pid}/apis/resolve-preview", projRead(s.handleResolvePreviewAPI))
	apiMux.Handle("POST /api/v1/projects/{pid}/apis/to-curl", projRead(s.handleAPIToCurl))
	apiMux.Handle("POST /api/v1/projects/{pid}/grpc/discover", projAccess("debug", false, s.handleDiscoverGRPC))
	apiMux.Handle("GET /api/v1/projects/{pid}/apis/{id}", projRead(s.handleGetAPI))
	apiMux.Handle("PUT /api/v1/projects/{pid}/apis/{id}", projWrite(s.handleUpdateAPI))
	apiMux.Handle("DELETE /api/v1/projects/{pid}/apis/{id}", projWrite(s.handleDeleteAPI))
	apiMux.Handle("POST /api/v1/projects/{pid}/apis/{id}/copy", projWrite(s.handleCopyAPI))
	apiMux.Handle("POST /api/v1/projects/{pid}/move", projWrite(s.handleMoveNode))

	// Environments.
	apiMux.Handle("GET /api/v1/projects/{pid}/environments", projRead(s.handleListEnvironments))
	apiMux.Handle("POST /api/v1/projects/{pid}/environments", projWrite(s.handleCreateEnvironment))
	apiMux.Handle("GET /api/v1/projects/{pid}/environments/{id}", projRead(s.handleGetEnvironment))
	apiMux.Handle("PUT /api/v1/projects/{pid}/environments/{id}", projWrite(s.handleUpdateEnvironment))
	apiMux.Handle("DELETE /api/v1/projects/{pid}/environments/{id}", projWrite(s.handleDeleteEnvironment))

	// Data files.
	apiMux.Handle("GET /api/v1/projects/{pid}/files", projRead(s.handleListFiles))
	apiMux.Handle("POST /api/v1/projects/{pid}/files", projWrite(s.handleUploadFile))
	apiMux.Handle("GET /api/v1/projects/{pid}/files/{name}/preview", projRead(s.handlePreviewFile))
	apiMux.Handle("DELETE /api/v1/projects/{pid}/files/{name}", projWrite(s.handleDeleteFile))

	// Interactive debug, dry-run, collection runner, history, cURL conversion.
	apiMux.Handle("POST /api/v1/projects/{pid}/debug", projAccess("debug", true, s.handleDebugAPI))
	apiMux.Handle("POST /api/v1/projects/{pid}/scenarios/debug", projAccess("debug", true, s.handleDebugScenario))
	apiMux.Handle("POST /api/v1/projects/{pid}/tests/run", projAccess("debug", true, s.handleRunCollectionTest))
	apiMux.Handle("GET /api/v1/projects/{pid}/debug-history", projRead(s.handleListDebugHistory))
	apiMux.Handle("GET /api/v1/projects/{pid}/debug-history/{id}", projRead(s.handleGetDebugRecord))
	apiMux.Handle("DELETE /api/v1/projects/{pid}/debug-history", projAccess("debug", true, s.handleClearDebugHistory))
	apiMux.Handle("POST /api/v1/tools/curl/parse", scoped("read", s.handleParseCurl))

	// Scenarios.
	apiMux.Handle("GET /api/v1/projects/{pid}/scenarios", projRead(s.handleListScenarios))
	apiMux.Handle("POST /api/v1/projects/{pid}/scenarios", projWrite(s.handleCreateScenario))
	apiMux.Handle("GET /api/v1/projects/{pid}/scenarios/{id}", projRead(s.handleGetScenario))
	apiMux.Handle("PUT /api/v1/projects/{pid}/scenarios/{id}", projWrite(s.handleUpdateScenario))
	apiMux.Handle("DELETE /api/v1/projects/{pid}/scenarios/{id}", projWrite(s.handleDeleteScenario))
	apiMux.Handle("GET /api/v1/projects/{pid}/scenarios/{id}/export", projRead(s.handleExportScenario))

	// Runs, snapshots, samples, export.
	apiMux.Handle("POST /api/v1/projects/{pid}/runs", projAccess("run", true, s.handleStartRun))
	apiMux.Handle("POST /api/v1/projects/{pid}/runs/estimate", projRead(s.handleEstimateRun))
	apiMux.Handle("GET /api/v1/projects/{pid}/runs", projRead(s.handleListRuns))
	apiMux.Handle("GET /api/v1/runs/compare", scoped("read", s.handleCompareRuns))
	apiMux.Handle("GET /api/v1/runs/{id}", runAccess("read", false, s.handleGetRun))
	apiMux.Handle("GET /api/v1/runs/{id}/snapshots", runAccess("read", false, s.handleRunSnapshots))
	apiMux.Handle("GET /api/v1/runs/{id}/samples", runAccess("read", false, s.handleRunSamples))
	apiMux.Handle("GET /api/v1/runs/{id}/histogram", runAccess("read", false, s.handleRunHistogram))
	apiMux.Handle("POST /api/v1/runs/{id}/stop", runAccess("run", true, s.handleStopRun))
	apiMux.Handle("POST /api/v1/runs/{id}/target", runAccess("run", true, s.handleSetRunTarget))
	apiMux.Handle("DELETE /api/v1/runs/{id}", runAccess("write", true, s.handleDeleteRun))
	apiMux.Handle("GET /api/v1/runs/{id}/export", runAccess("read", false, s.handleRunExport))

	// Agents, system, WebSocket.
	apiMux.Handle("GET /api/v1/agents", scoped("read", s.handleListAgents))
	apiMux.Handle("GET /api/v1/agents/ca", scoped("read", s.handleGetAgentCA))
	apiMux.Handle("GET /api/v1/agents/join-tokens", adminOnly(s.handleListJoinTokens))
	apiMux.Handle("POST /api/v1/agents/join-tokens", adminOnly(s.handleCreateJoinToken))
	apiMux.Handle("DELETE /api/v1/agents/join-tokens/{id}", adminOnly(s.handleDeleteJoinToken))
	apiMux.Handle("PATCH /api/v1/agents/{id}", adminOnly(s.handlePatchAgent))
	apiMux.Handle("DELETE /api/v1/agents/{id}", adminOnly(s.handleRevokeAgent))
	apiMux.Handle("GET /api/v1/system/info", scoped("read", s.handleSystemInfo))
	apiMux.Handle("GET /api/v1/ws", scoped("read", s.handleWS))

	// Unmatched /api/ paths return JSON 404.
	apiMux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, newHTTPError(http.StatusNotFound, "not_found", "接口路径不存在"))
	})

	// Protect every state-changing /api/ request with CSRF + Origin validation.
	protectedAPI := s.guard.CSRF(apiMux)

	// MCP Streamable HTTP supports standard Bearer clients without browser CSRF
	// headers while still enforcing CSRF/Origin when cookie sessions are used.
	mcpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.BearerToken(r) == "" && auth.SessionToken(r) == "" {
			if err := s.guard.CheckOrigin(r); err != nil {
				writeError(w, err)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="tidegate-mcp"`)
			writeError(w, auth.ErrUnauthenticated)
			return
		}
		s.guard.CSRF(authed(s.mcp.ServeHTTP)).ServeHTTP(w, r)
	})

	rootMux := http.NewServeMux()
	rootMux.Handle("/api/v1/mcp", mcpHandler)
	rootMux.Handle("/mcp", mcpHandler)
	rootMux.Handle("/api/", protectedAPI)
	rootMux.HandleFunc("/", s.handleUI)

	return s.securityMiddleware(rootMux)
}

func (s *Server) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.ListenLoopback && !s.isAllowedHost(r.Host) {
			writeError(w, newHTTPError(http.StatusForbidden, "invalid_host", "拒绝来自不受信任主机名的访问"))
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) isAllowedHost(host string) bool {
	if host == "" {
		return false
	}
	name, _, err := net.SplitHostPort(host)
	if err != nil {
		name = strings.Trim(host, "[]")
	}
	return s.allowedHosts[strings.ToLower(name)]
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if s.opts.WebHandler != nil {
		s.opts.WebHandler.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>Tidegate</title></head>
<body style="font-family:system-ui,sans-serif;max-width:640px;margin:48px auto;padding:0 16px;color:#111">
<h1>Tidegate 服务运行中</h1>
<p>当前版本为纯后端接口与控制器服务（Web 界面将在 S3 阶段集成）。</p>
<p>接口前缀：<code>/api/v1</code>，版本：<code>` + htmlEscape(s.opts.Version) + `</code></p>
</body>
</html>`))
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
