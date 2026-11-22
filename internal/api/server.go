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
	"tidegate/internal/store"
)

// Options configure the Tidegate HTTP and WebSocket API server.
type Options struct {
	Controller     *controller.Controller
	Store          *store.Store
	Results        *store.ResultDB
	Auth           *auth.Service
	Guard          *auth.Guard
	Version        string
	TLS            bool
	ListenLoopback bool
	AllowedOrigins []string
	WebHandler     http.Handler
	Logger         *slog.Logger
}

// Server serves the /api/v1 REST endpoints, /api/v1/ws WebSocket stream,
// and the Web UI entrypoint.
type Server struct {
	opts         Options
	ctrl         *controller.Controller
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
	requireRole := func(allow func(auth.Role) bool, fn http.HandlerFunc) http.Handler {
		return s.guard.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := auth.UserFromContext(r.Context())
			if !ok || !allow(u.Role) {
				writeError(w, auth.ErrForbidden)
				return
			}
			fn(w, r)
		}))
	}
	writer := func(fn http.HandlerFunc) http.Handler {
		return requireRole(auth.Role.CanWrite, fn)
	}
	adminOnly := func(fn http.HandlerFunc) http.Handler {
		return requireRole(auth.Role.IsAdmin, fn)
	}

	// Setup, authentication, users, and API tokens.
	apiMux.HandleFunc("GET /api/v1/setup/status", s.handleSetupStatus)
	apiMux.Handle("POST /api/v1/setup", s.guard.RequireSecureTransport(http.HandlerFunc(s.handleSetup)))
	apiMux.Handle("POST /api/v1/auth/login", s.guard.RequireSecureTransport(http.HandlerFunc(s.handleLogin)))
	apiMux.Handle("POST /api/v1/auth/logout", authed(s.handleLogout))
	apiMux.Handle("GET /api/v1/auth/me", authed(s.handleMe))
	apiMux.Handle("POST /api/v1/auth/password", authed(s.handleChangePassword))
	apiMux.Handle("GET /api/v1/auth/sessions", authed(s.handleListSessions))
	apiMux.Handle("DELETE /api/v1/auth/sessions/{id}", authed(s.handleRevokeSession))
	apiMux.Handle("GET /api/v1/auth/tokens", authed(s.handleListAPITokens))
	apiMux.Handle("POST /api/v1/auth/tokens", authed(s.handleCreateAPIToken))
	apiMux.Handle("DELETE /api/v1/auth/tokens/{id}", authed(s.handleRevokeAPIToken))

	apiMux.Handle("GET /api/v1/users", adminOnly(s.handleListUsers))
	apiMux.Handle("POST /api/v1/users", adminOnly(s.handleCreateUser))
	apiMux.Handle("PATCH /api/v1/users/{id}", adminOnly(s.handlePatchUser))
	apiMux.Handle("DELETE /api/v1/users/{id}", adminOnly(s.handleDeleteUser))
	apiMux.Handle("GET /api/v1/settings/security", authed(s.handleGetSecurityPolicy))
	apiMux.Handle("PUT /api/v1/settings/security", adminOnly(s.handlePutSecurityPolicy))
	apiMux.Handle("GET /api/v1/audit-logs", adminOnly(s.handleListAuditLogs))

	// Projects.
	apiMux.Handle("GET /api/v1/projects", authed(s.handleListProjects))
	apiMux.Handle("POST /api/v1/projects", writer(s.handleCreateProject))
	apiMux.Handle("POST /api/v1/projects/import", writer(s.handleImportProjectArchive))
	apiMux.Handle("GET /api/v1/projects/{pid}", authed(s.handleGetProject))
	apiMux.Handle("PATCH /api/v1/projects/{pid}", writer(s.handlePatchProject))
	apiMux.Handle("DELETE /api/v1/projects/{pid}", writer(s.handleDeleteProject))
	apiMux.Handle("GET /api/v1/projects/{pid}/export", authed(s.handleExportProject))
	apiMux.Handle("POST /api/v1/projects/{pid}/import/preview", authed(s.handleImportPreview))
	apiMux.Handle("POST /api/v1/projects/{pid}/import/apply", writer(s.handleImportApply))

	// API tree, folders, APIs.
	apiMux.Handle("GET /api/v1/projects/{pid}/tree", authed(s.handleTree))
	apiMux.Handle("POST /api/v1/projects/{pid}/folders", writer(s.handleCreateFolder))
	apiMux.Handle("GET /api/v1/projects/{pid}/folders/{id}", authed(s.handleGetFolder))
	apiMux.Handle("PATCH /api/v1/projects/{pid}/folders/{id}", writer(s.handlePatchFolder))
	apiMux.Handle("DELETE /api/v1/projects/{pid}/folders/{id}", writer(s.handleDeleteFolder))
	apiMux.Handle("POST /api/v1/projects/{pid}/apis", writer(s.handleCreateAPI))
	apiMux.Handle("POST /api/v1/projects/{pid}/apis/resolve-preview", authed(s.handleResolvePreviewAPI))
	apiMux.Handle("POST /api/v1/projects/{pid}/apis/to-curl", authed(s.handleAPIToCurl))
	apiMux.Handle("GET /api/v1/projects/{pid}/apis/{id}", authed(s.handleGetAPI))
	apiMux.Handle("PUT /api/v1/projects/{pid}/apis/{id}", writer(s.handleUpdateAPI))
	apiMux.Handle("DELETE /api/v1/projects/{pid}/apis/{id}", writer(s.handleDeleteAPI))
	apiMux.Handle("POST /api/v1/projects/{pid}/apis/{id}/copy", writer(s.handleCopyAPI))
	apiMux.Handle("POST /api/v1/projects/{pid}/move", writer(s.handleMoveNode))

	// Environments.
	apiMux.Handle("GET /api/v1/projects/{pid}/environments", authed(s.handleListEnvironments))
	apiMux.Handle("POST /api/v1/projects/{pid}/environments", writer(s.handleCreateEnvironment))
	apiMux.Handle("GET /api/v1/projects/{pid}/environments/{id}", authed(s.handleGetEnvironment))
	apiMux.Handle("PUT /api/v1/projects/{pid}/environments/{id}", writer(s.handleUpdateEnvironment))
	apiMux.Handle("DELETE /api/v1/projects/{pid}/environments/{id}", writer(s.handleDeleteEnvironment))

	// Data files.
	apiMux.Handle("GET /api/v1/projects/{pid}/files", authed(s.handleListFiles))
	apiMux.Handle("POST /api/v1/projects/{pid}/files", writer(s.handleUploadFile))
	apiMux.Handle("GET /api/v1/projects/{pid}/files/{name}/preview", authed(s.handlePreviewFile))
	apiMux.Handle("DELETE /api/v1/projects/{pid}/files/{name}", writer(s.handleDeleteFile))

	// Interactive debug, dry-run, history, cURL conversion.
	apiMux.Handle("POST /api/v1/projects/{pid}/debug", writer(s.handleDebugAPI))
	apiMux.Handle("POST /api/v1/projects/{pid}/scenarios/debug", writer(s.handleDebugScenario))
	apiMux.Handle("GET /api/v1/projects/{pid}/debug-history", authed(s.handleListDebugHistory))
	apiMux.Handle("GET /api/v1/projects/{pid}/debug-history/{id}", authed(s.handleGetDebugRecord))
	apiMux.Handle("DELETE /api/v1/projects/{pid}/debug-history", writer(s.handleClearDebugHistory))
	apiMux.Handle("POST /api/v1/tools/curl/parse", authed(s.handleParseCurl))

	// Scenarios.
	apiMux.Handle("GET /api/v1/projects/{pid}/scenarios", authed(s.handleListScenarios))
	apiMux.Handle("POST /api/v1/projects/{pid}/scenarios", writer(s.handleCreateScenario))
	apiMux.Handle("GET /api/v1/projects/{pid}/scenarios/{id}", authed(s.handleGetScenario))
	apiMux.Handle("PUT /api/v1/projects/{pid}/scenarios/{id}", writer(s.handleUpdateScenario))
	apiMux.Handle("DELETE /api/v1/projects/{pid}/scenarios/{id}", writer(s.handleDeleteScenario))
	apiMux.Handle("GET /api/v1/projects/{pid}/scenarios/{id}/export", authed(s.handleExportScenario))

	// Runs, snapshots, samples, export.
	apiMux.Handle("POST /api/v1/projects/{pid}/runs", writer(s.handleStartRun))
	apiMux.Handle("POST /api/v1/projects/{pid}/runs/estimate", authed(s.handleEstimateRun))
	apiMux.Handle("GET /api/v1/projects/{pid}/runs", authed(s.handleListRuns))
	apiMux.Handle("GET /api/v1/runs/compare", authed(s.handleCompareRuns))
	apiMux.Handle("GET /api/v1/runs/{id}", authed(s.handleGetRun))
	apiMux.Handle("GET /api/v1/runs/{id}/snapshots", authed(s.handleRunSnapshots))
	apiMux.Handle("GET /api/v1/runs/{id}/samples", authed(s.handleRunSamples))
	apiMux.Handle("POST /api/v1/runs/{id}/stop", writer(s.handleStopRun))
	apiMux.Handle("POST /api/v1/runs/{id}/target", writer(s.handleSetRunTarget))
	apiMux.Handle("DELETE /api/v1/runs/{id}", writer(s.handleDeleteRun))
	apiMux.Handle("GET /api/v1/runs/{id}/export", authed(s.handleRunExport))

	// Agents, system, WebSocket.
	apiMux.Handle("GET /api/v1/agents", authed(s.handleListAgents))
	apiMux.Handle("PATCH /api/v1/agents/{id}", adminOnly(s.handlePatchAgent))
	apiMux.Handle("GET /api/v1/system/info", authed(s.handleSystemInfo))
	apiMux.Handle("GET /api/v1/ws", authed(s.handleWS))

	// Unmatched /api/ paths return JSON 404.
	apiMux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, newHTTPError(http.StatusNotFound, "not_found", "接口路径不存在"))
	})

	// Protect every state-changing /api/ request with CSRF + Origin validation.
	protectedAPI := s.guard.CSRF(apiMux)

	rootMux := http.NewServeMux()
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
