package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tidegate/internal/auth"
	"tidegate/internal/controller"
	"tidegate/internal/store"
)

func (s *Server) audit(r *http.Request, action, resource, resourceID string, detail any) {
	var u *auth.User
	if r != nil {
		u, _ = auth.UserFromContext(r.Context())
	}
	s.auditAs(r, u, action, resource, resourceID, detail)
}

func (s *Server) auditAs(r *http.Request, u *auth.User, action, resource, resourceID string, detail any) {
	if s.results == nil || r == nil {
		return
	}
	var (
		userID   int64
		username string
		role     string
	)
	if u != nil {
		userID = u.ID
		username = u.Username
		role = string(u.Role)
	}
	var raw json.RawMessage
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			raw = b
		}
	}
	if err := s.results.RecordAudit(r.Context(), store.AuditEntry{
		UserID:     userID,
		Username:   username,
		Role:       role,
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		RemoteAddr: r.RemoteAddr,
		Detail:     raw,
	}); err != nil {
		s.log.Warn("写入审计日志失败", "action", action, "err", err)
	}
}

// ---------------------------------------------------------------------------
// Users (admin-only)

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]userDTO, 0, len(users))
	for i := range users {
		out = append(out, toUserDTO(&users[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string    `json:"username"`
		Password string    `json:"password"`
		Role     auth.Role `json:"role"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	u, err := s.auth.CreateUser(r.Context(), auth.CreateUserRequest{
		Username: body.Username,
		Password: body.Password,
		Role:     body.Role,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "user.create", "user", strconv.FormatInt(u.ID, 10), map[string]any{
		"username": u.Username,
		"role":     u.Role,
	})
	writeJSON(w, http.StatusCreated, map[string]any{"user": toUserDTO(u)})
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	uid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || uid <= 0 {
		writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "用户 ID 无效"))
		return
	}
	var body struct {
		Role auth.Role `json:"role"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	u, err := s.auth.UpdateUserRole(r.Context(), uid, body.Role)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "user.role_update", "user", strconv.FormatInt(uid, 10), map[string]any{
		"username": u.Username,
		"role":     u.Role,
	})
	writeJSON(w, http.StatusOK, map[string]any{"user": toUserDTO(u)})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	uid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || uid <= 0 {
		writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "用户 ID 无效"))
		return
	}
	actor, _ := auth.UserFromContext(r.Context())
	var actorID int64
	if actor != nil {
		actorID = actor.ID
	}
	if err := s.auth.DeleteUser(r.Context(), actorID, uid); err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "user.delete", "user", strconv.FormatInt(uid, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// API Tokens

func (s *Server) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, auth.ErrUnauthenticated)
		return
	}
	list, err := s.auth.ListAPITokens(r.Context(), u.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, auth.ErrUnauthenticated)
		return
	}
	var body struct {
		Name      string    `json:"name"`
		Role      auth.Role `json:"role"`
		ExpiresIn string    `json:"expires_in"`
		TTLDays   int       `json:"ttl_days"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	var ttl time.Duration
	if strings.TrimSpace(body.ExpiresIn) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(body.ExpiresIn))
		if err != nil || d <= 0 {
			writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "expires_in 必须是大于 0 的有效时长，例如 24h"))
			return
		}
		ttl = d
	} else if body.TTLDays > 0 {
		ttl = time.Duration(body.TTLDays) * 24 * time.Hour
	}

	tok, plaintext, err := s.auth.CreateAPIToken(r.Context(), auth.CreateTokenRequest{
		UserID: u.ID,
		Name:   body.Name,
		Role:   body.Role,
		TTL:    ttl,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "token.create", "token", tok.ID, map[string]any{
		"name":   tok.Name,
		"role":   tok.Role,
		"prefix": tok.Prefix,
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":     tok,
		"plaintext": plaintext,
	})
}

func (s *Server) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, auth.ErrUnauthenticated)
		return
	}
	id := r.PathValue("id")
	if err := s.auth.RevokeAPIToken(r.Context(), u.ID, id, u.Role.IsAdmin()); err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "token.revoke", "token", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Security Policy & Audit Logs

func (s *Server) handleGetSecurityPolicy(w http.ResponseWriter, r *http.Request) {
	pol, err := s.ctrl.GetSecurityPolicy(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pol)
}

func (s *Server) handlePutSecurityPolicy(w http.ResponseWriter, r *http.Request) {
	var pol controller.SecurityPolicy
	if err := decodeJSON(w, r, &pol); err != nil {
		writeError(w, err)
		return
	}
	out, err := s.ctrl.SetSecurityPolicy(r.Context(), pol)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "security.update", "security_policy", "default", out)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	q := store.AuditQuery{
		Action:   r.URL.Query().Get("action"),
		Username: r.URL.Query().Get("username"),
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Limit = n
		}
	}
	if v := r.URL.Query().Get("before_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			q.BeforeID = n
		}
	}
	list, err := s.results.ListAuditLogs(r.Context(), q)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
