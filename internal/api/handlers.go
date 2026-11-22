package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tidegate/internal/auth"
	"tidegate/internal/controller"
	"tidegate/internal/curl"
	"tidegate/internal/importer"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

type userDTO struct {
	ID                int64     `json:"id"`
	Username          string    `json:"username"`
	Role              auth.Role `json:"role"`
	CreatedAt         time.Time `json:"created_at"`
	PasswordChangedAt time.Time `json:"password_changed_at"`
}

func toUserDTO(u *auth.User) userDTO {
	return userDTO{
		ID:                u.ID,
		Username:          u.Username,
		Role:              u.Role,
		CreatedAt:         u.CreatedAt,
		PasswordChangedAt: u.PasswordChangedAt,
	}
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	pending, err := s.auth.SetupPending(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_required": pending,
		"version":        s.opts.Version,
		"tls":            s.opts.TLS,
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code     string `json:"code"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	res, err := s.auth.Setup(r.Context(), auth.SetupRequest{
		Code:       body.Code,
		Username:   body.Username,
		Password:   body.Password,
		RemoteAddr: r.RemoteAddr,
		UserAgent:  r.UserAgent(),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.ctrl.EnsureDefaultProject(); err != nil {
		s.log.Warn("初始化默认项目失败", "err", err)
	}
	s.auditAs(r, res.User, "user.setup", "user", strconv.FormatInt(res.User.ID, 10), map[string]any{"username": res.User.Username})
	auth.SetSessionCookie(w, res, s.guard.SecureCookies(r))
	writeJSON(w, http.StatusCreated, map[string]any{"user": toUserDTO(res.User)})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	res, err := s.auth.Login(r.Context(), auth.LoginRequest{
		Username:   body.Username,
		Password:   body.Password,
		RemoteAddr: r.RemoteAddr,
		UserAgent:  r.UserAgent(),
	})
	if err != nil {
		s.audit(r, "user.login_failed", "auth", body.Username, map[string]any{"username": body.Username})
		writeError(w, err)
		return
	}
	s.auditAs(r, res.User, "user.login", "auth", strconv.FormatInt(res.User.ID, 10), nil)
	auth.SetSessionCookie(w, res, s.guard.SecureCookies(r))
	writeJSON(w, http.StatusOK, map[string]any{"user": toUserDTO(res.User)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	tok := auth.SessionToken(r)
	if err := s.auth.Logout(r.Context(), tok); err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "user.logout", "auth", "", nil)
	auth.ClearSessionCookie(w, s.guard.SecureCookies(r))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, auth.ErrUnauthenticated)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": toUserDTO(u)})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, auth.ErrUnauthenticated)
		return
	}
	sess, _ := auth.SessionFromContext(r.Context())
	keepID := ""
	if sess != nil {
		keepID = sess.ID
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	if err := s.auth.ChangePassword(r.Context(), auth.ChangePasswordRequest{
		UserID:          u.ID,
		CurrentPassword: body.CurrentPassword,
		NewPassword:     body.NewPassword,
		KeepSessionID:   keepID,
		RemoteAddr:      r.RemoteAddr,
	}); err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "user.password_change", "user", strconv.FormatInt(u.ID, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

type sessionDTO struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	RemoteAddr string    `json:"remote_addr"`
	UserAgent  string    `json:"user_agent"`
	Current    bool      `json:"current"`
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, auth.ErrUnauthenticated)
		return
	}
	curSess, _ := auth.SessionFromContext(r.Context())
	items, err := s.auth.Sessions(r.Context(), u.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]sessionDTO, 0, len(items))
	for _, it := range items {
		out = append(out, sessionDTO{
			ID:         it.ID,
			CreatedAt:  it.CreatedAt,
			LastSeenAt: it.LastSeenAt,
			ExpiresAt:  it.ExpiresAt,
			RemoteAddr: it.RemoteAddr,
			UserAgent:  it.UserAgent,
			Current:    curSess != nil && it.ID == curSess.ID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, auth.ErrUnauthenticated)
		return
	}
	targetID := r.PathValue("id")
	if err := s.auth.RevokeSession(r.Context(), u.ID, targetID); err != nil {
		writeError(w, err)
		return
	}
	if curSess, _ := auth.SessionFromContext(r.Context()); curSess != nil && curSess.ID == targetID {
		auth.ClearSessionCookie(w, s.guard.SecureCookies(r))
	}
	s.audit(r, "session.revoke", "session", targetID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// deleteVersion extracts an optional version from either ?version= or a JSON body.
func deleteVersion(w http.ResponseWriter, r *http.Request) string {
	if v := r.URL.Query().Get("version"); v != "" {
		return v
	}
	if r.Body != nil && r.ContentLength != 0 {
		var body struct {
			Version string `json:"version"`
		}
		if err := decodeJSON(w, r, &body); err == nil {
			return body.Version
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Projects

func (s *Server) handleListProjects(w http.ResponseWriter, _ *http.Request) {
	list, err := s.store.ListProjects()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var in store.Project
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	out, err := s.store.CreateProject(&in)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "project.create", "project", out.ID, map[string]any{"name": out.Name})
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	pr, err := s.store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pr)
}

func (s *Server) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	cur, err := s.store.GetProject(pid)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Name        optField[string]             `json:"name"`
		Description optField[string]             `json:"description"`
		Variables   optField[store.KeyValueList] `json:"variables"`
		Version     string                       `json:"version"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	upd := store.Project{
		ID:          pid,
		Name:        cur.Name,
		Description: cur.Description,
		Variables:   cur.Variables,
		Version:     body.Version,
	}
	if body.Name.Set {
		upd.Name = body.Name.Value
	}
	if body.Description.Set {
		upd.Description = body.Description.Value
	}
	if body.Variables.Set {
		upd.Variables = body.Variables.Value
	}
	out, err := s.store.UpdateProject(&upd)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "project.update", "project", out.ID, map[string]any{"name": out.Name})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	ver := deleteVersion(w, r)
	if err := s.store.DeleteProject(pid, ver); err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.results.DeleteProjectRuns(r.Context(), pid); err != nil {
		s.log.Warn("删除项目压测记录失败", "project_id", pid, "err", err)
	}
	if err := s.results.ClearDebugHistory(r.Context(), pid); err != nil {
		s.log.Warn("清理项目调试历史失败", "project_id", pid, "err", err)
	}
	s.audit(r, "project.delete", "project", pid, nil)
	w.WriteHeader(http.StatusNoContent)
}

const maxImportUploadBytes = 64 << 20

func readImportInput(w http.ResponseWriter, r *http.Request) (importer.PreviewInput, string, error) {
	var in importer.PreviewInput
	var nameOverride string
	q := r.URL.Query()
	ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))

	if strings.HasPrefix(ct, "multipart/form-data") {
		mr, err := r.MultipartReader()
		if err != nil {
			return in, "", newHTTPError(http.StatusBadRequest, "invalid_input", "无效的 multipart 表单请求")
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return in, "", newHTTPError(http.StatusBadRequest, "invalid_input", "读取上传数据失败")
			}
			fn := part.FormName()
			switch fn {
			case "file", "content":
				data, rerr := io.ReadAll(io.LimitReader(part, maxImportUploadBytes+1))
				_ = part.Close()
				if rerr != nil {
					return in, "", newHTTPError(http.StatusBadRequest, "invalid_input", "读取导入文件内容失败")
				}
				if int64(len(data)) > maxImportUploadBytes {
					return in, "", newHTTPError(http.StatusRequestEntityTooLarge, "body_too_large", "导入文件不能超过 64 MB")
				}
				in.Content = data
				if fn == "file" && in.Filename == "" {
					in.Filename = part.FileName()
				}
			case "format":
				b, _ := io.ReadAll(io.LimitReader(part, 256))
				in.Format = strings.TrimSpace(string(b))
				_ = part.Close()
			case "filename":
				b, _ := io.ReadAll(io.LimitReader(part, 1024))
				if s := strings.TrimSpace(string(b)); s != "" {
					in.Filename = s
				}
				_ = part.Close()
			case "strategy":
				b, _ := io.ReadAll(io.LimitReader(part, 256))
				in.Strategy = strings.TrimSpace(string(b))
				_ = part.Close()
			case "target_folder_id":
				b, _ := io.ReadAll(io.LimitReader(part, 256))
				in.TargetFolderID = strings.TrimSpace(string(b))
				_ = part.Close()
			case "name":
				b, _ := io.ReadAll(io.LimitReader(part, 1024))
				nameOverride = strings.TrimSpace(string(b))
				_ = part.Close()
			default:
				_ = part.Close()
			}
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, maxImportUploadBytes)
		var body struct {
			Format         string `json:"format"`
			Filename       string `json:"filename"`
			Content        string `json:"content"`
			Strategy       string `json:"strategy"`
			TargetFolderID string `json:"target_folder_id"`
			Name           string `json:"name"`
		}
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&body); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return in, "", newHTTPError(http.StatusRequestEntityTooLarge, "body_too_large", "请求体过大")
			}
			if errors.Is(err, io.EOF) {
				return in, "", newHTTPError(http.StatusBadRequest, "bad_json", "请求体不能为空，需要合法的 JSON")
			}
			return in, "", newHTTPError(http.StatusBadRequest, "bad_json", "请求体不是合法的 JSON")
		}
		in.Format = strings.TrimSpace(body.Format)
		in.Filename = strings.TrimSpace(body.Filename)
		in.Content = []byte(body.Content)
		in.Strategy = strings.TrimSpace(body.Strategy)
		in.TargetFolderID = strings.TrimSpace(body.TargetFolderID)
		nameOverride = strings.TrimSpace(body.Name)
	}

	if in.Format == "" {
		in.Format = strings.TrimSpace(q.Get("format"))
	}
	if in.Strategy == "" {
		in.Strategy = strings.TrimSpace(q.Get("strategy"))
	}
	if in.TargetFolderID == "" {
		in.TargetFolderID = strings.TrimSpace(q.Get("target_folder_id"))
	}
	if nameOverride == "" {
		nameOverride = strings.TrimSpace(q.Get("name"))
	}
	if len(in.Content) == 0 {
		return in, "", newHTTPError(http.StatusBadRequest, "invalid_input", "导入内容不能为空")
	}
	return in, nameOverride, nil
}

func writeImportError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if _, ok := auth.AsError(err); ok {
		writeError(w, err)
		return
	}
	var he *httpError
	var ve *store.ValidationError
	var ce *store.ConflictError
	var nf *store.NotFoundError
	if errors.As(err, &he) || errors.As(err, &ve) || errors.As(err, &ce) || errors.As(err, &nf) ||
		errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
		writeError(w, err)
		return
	}
	writeError(w, newHTTPError(http.StatusBadRequest, "invalid_import", err.Error()))
}

func (s *Server) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	in, _, err := readImportInput(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := importer.Preview(s.store, r.PathValue("pid"), in)
	if err != nil {
		writeImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleImportApply(w http.ResponseWriter, r *http.Request) {
	in, _, err := readImportInput(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	pid := r.PathValue("pid")
	res, err := importer.Apply(s.store, pid, in)
	if err != nil {
		writeImportError(w, err)
		return
	}
	if res.Report != nil {
		s.audit(r, "api.import", "project", pid, map[string]any{
			"format":       res.Report.Format,
			"apis_created": res.Report.APIsCreated,
			"apis_updated": res.Report.APIsUpdated,
		})
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleImportProjectArchive(w http.ResponseWriter, r *http.Request) {
	in, nameOverride, err := readImportInput(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	pr, report, err := importer.ImportProjectArchive(s.store, in.Content, nameOverride)
	if err != nil {
		writeImportError(w, err)
		return
	}
	s.audit(r, "project.import", "project", pr.ID, map[string]any{"name": pr.Name})
	writeJSON(w, http.StatusCreated, map[string]any{
		"project": pr,
		"report":  report,
	})
}

// ---------------------------------------------------------------------------
// Tree, Folders, APIs

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.Tree(r.PathValue("pid"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var in store.Folder
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	out, err := s.store.CreateFolder(r.PathValue("pid"), &in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleGetFolder(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.GetFolder(r.PathValue("pid"), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePatchFolder(w http.ResponseWriter, r *http.Request) {
	pid, id := r.PathValue("pid"), r.PathValue("id")
	cur, err := s.store.GetFolder(pid, id)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Name    optField[string]             `json:"name"`
		Headers optField[store.KeyValueList] `json:"headers"`
		Auth    optField[*store.Auth]        `json:"auth"`
		Version string                       `json:"version"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	upd := store.Folder{
		ID:      id,
		Name:    cur.Name,
		Headers: cur.Headers,
		Auth:    cur.Auth,
		Version: body.Version,
	}
	if body.Name.Set {
		upd.Name = body.Name.Value
	}
	if body.Headers.Set {
		upd.Headers = body.Headers.Value
	}
	if body.Auth.Set {
		upd.Auth = body.Auth.Value
	}
	out, err := s.store.UpdateFolder(pid, &upd)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteFolder(r.PathValue("pid"), r.PathValue("id"), deleteVersion(w, r)); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateAPI(w http.ResponseWriter, r *http.Request) {
	var in store.API
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	out, err := s.store.CreateAPI(r.PathValue("pid"), &in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleGetAPI(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.GetAPI(r.PathValue("pid"), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpdateAPI(w http.ResponseWriter, r *http.Request) {
	var in store.API
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	in.ID = r.PathValue("id")
	out, err := s.store.UpdateAPI(r.PathValue("pid"), &in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteAPI(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAPI(r.PathValue("pid"), r.PathValue("id"), deleteVersion(w, r)); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCopyAPI(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.CopyAPI(r.PathValue("pid"), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleMoveNode(w http.ResponseWriter, r *http.Request) {
	var req store.MoveRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	out, err := s.store.Move(r.PathValue("pid"), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleResolvePreviewAPI(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var body struct {
		APIID         string                     `json:"api_id"`
		API           *store.API                 `json:"api"`
		EnvironmentID string                     `json:"environment_id"`
		Variables     map[string]scenario.Scalar `json:"variables"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	api := body.API
	if api == nil {
		if body.APIID == "" {
			writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "请提供接口 ID 或接口定义"))
			return
		}
		var err error
		api, err = s.store.GetAPI(pid, body.APIID)
		if err != nil {
			writeError(w, err)
			return
		}
	} else if api.ID == "" && body.APIID != "" {
		cp := *api
		cp.ID = body.APIID
		api = &cp
	}

	out, err := s.store.PreviewEffectiveAPI(pid, api, store.ResolveOptions{
		EnvironmentID: body.EnvironmentID,
		Variables:     body.Variables,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Environments

func (s *Server) handleListEnvironments(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListEnvironments(r.PathValue("pid"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateEnvironment(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var in store.Environment
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	out, err := s.store.CreateEnvironment(pid, &in)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "environment.create", "environment", out.ID, map[string]any{"project_id": pid, "name": out.Name})
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleGetEnvironment(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.GetEnvironment(r.PathValue("pid"), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpdateEnvironment(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var in store.Environment
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	in.ID = r.PathValue("id")
	out, err := s.store.UpdateEnvironment(pid, &in)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "environment.update", "environment", out.ID, map[string]any{"project_id": pid, "name": out.Name})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	pid, id := r.PathValue("pid"), r.PathValue("id")
	if err := s.store.DeleteEnvironment(pid, id, deleteVersion(w, r)); err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "environment.delete", "environment", id, map[string]any{"project_id": pid})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Data Files

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListFiles(r.PathValue("pid"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	r.Body = http.MaxBytesReader(w, r.Body, store.MaxFileSize+(2<<20))

	q := r.URL.Query()
	name := q.Get("name")
	version := q.Get("version")
	ct := r.Header.Get("Content-Type")

	if strings.HasPrefix(strings.ToLower(ct), "multipart/form-data") {
		mr, err := r.MultipartReader()
		if err != nil {
			writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "无效的 multipart 表单请求"))
			return
		}
		var saved *store.DataFile
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "读取上传数据失败"))
				return
			}
			fn := part.FormName()
			switch fn {
			case "name":
				b, _ := io.ReadAll(io.LimitReader(part, 1024))
				if s := strings.TrimSpace(string(b)); s != "" {
					name = s
				}
				_ = part.Close()
			case "version":
				b, _ := io.ReadAll(io.LimitReader(part, 256))
				version = strings.TrimSpace(string(b))
				_ = part.Close()
			case "file":
				if name == "" {
					name = part.FileName()
				}
				saved, err = s.store.SaveFile(pid, name, part, version)
				_ = part.Close()
				if err != nil {
					writeError(w, err)
					return
				}
				s.audit(r, "datafile.upload", "datafile", saved.Name, map[string]any{"project_id": pid, "size": saved.Size})
				writeJSON(w, http.StatusCreated, saved)
				return
			default:
				_ = part.Close()
			}
		}
		writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "上传请求缺少 file 字段"))
		return
	}

	if name == "" {
		writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "请通过查询参数 ?name= 指定文件名"))
		return
	}
	df, err := s.store.SaveFile(pid, name, r.Body, version)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "datafile.upload", "datafile", df.Name, map[string]any{"project_id": pid, "size": df.Size})
	writeJSON(w, http.StatusCreated, df)
}

func (s *Server) handlePreviewFile(w http.ResponseWriter, r *http.Request) {
	maxRows := 20
	if raw := r.URL.Query().Get("rows"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			maxRows = n
		}
	}
	pv, err := s.store.PreviewFile(r.PathValue("pid"), r.PathValue("name"), maxRows)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	pid, name := r.PathValue("pid"), r.PathValue("name")
	if err := s.store.DeleteFile(pid, name, deleteVersion(w, r)); err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "datafile.delete", "datafile", name, map[string]any{"project_id": pid})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Debug, Dry-Run, cURL Tools

func (s *Server) handleDebugAPI(w http.ResponseWriter, r *http.Request) {
	var req controller.DebugAPIRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	out, err := s.ctrl.DebugAPI(r.Context(), r.PathValue("pid"), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDebugScenario(w http.ResponseWriter, r *http.Request) {
	var req controller.DebugScenarioRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	out, err := s.ctrl.DebugScenario(r.Context(), r.PathValue("pid"), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRunCollectionTest(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var req controller.CollectionTestRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	out, err := s.ctrl.RunCollectionTest(r.Context(), pid, req)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "collection.test", "project", pid, map[string]any{
		"folder_id": req.FolderID,
		"total":     out.Total,
		"passed":    out.PassedCount,
		"failed":    out.FailedCount,
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListDebugHistory(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	if _, err := s.store.GetProject(pid); err != nil {
		writeError(w, err)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	includeData := r.URL.Query().Get("include_data") == "true"
	out, err := s.results.ListDebugHistory(r.Context(), pid, limit, includeData)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetDebugRecord(w http.ResponseWriter, r *http.Request) {
	pid, id := r.PathValue("pid"), r.PathValue("id")
	rec, err := s.results.GetDebugRecord(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if rec.ProjectID != pid {
		writeError(w, newHTTPError(http.StatusNotFound, "not_found", "调试记录不存在"))
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleClearDebugHistory(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	if _, err := s.store.GetProject(pid); err != nil {
		writeError(w, err)
		return
	}
	if err := s.results.ClearDebugHistory(r.Context(), pid); err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "debug_history.clear", "project", pid, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleParseCurl(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Command string `json:"command"`
		Curl    string `json:"curl"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	raw := body.Command
	if raw == "" {
		raw = body.Curl
	}
	api, err := curl.Parse(raw)
	if err != nil {
		writeError(w, newHTTPError(http.StatusBadRequest, "invalid_curl", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api": api})
}

func (s *Server) handleAPIToCurl(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var body struct {
		APIID         string                     `json:"api_id"`
		API           *store.API                 `json:"api"`
		EnvironmentID string                     `json:"environment_id"`
		Variables     map[string]scenario.Scalar `json:"variables"`
		Mode          string                     `json:"mode"`
		Shell         string                     `json:"shell"`
		RedactSecrets bool                       `json:"redact_secrets"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	api := body.API
	if api == nil {
		if body.APIID == "" {
			writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", "请提供接口 ID 或接口定义"))
			return
		}
		var err error
		api, err = s.store.GetAPI(pid, body.APIID)
		if err != nil {
			writeError(w, err)
			return
		}
	} else if api.ID == "" && body.APIID != "" {
		cp := *api
		cp.ID = body.APIID
		api = &cp
	}

	u, _ := auth.UserFromContext(r.Context())
	redact := body.RedactSecrets || u == nil || !u.Role.CanWrite()

	preview, err := s.store.PreviewEffectiveAPI(pid, api, store.ResolveOptions{
		EnvironmentID:  body.EnvironmentID,
		Variables:      body.Variables,
		IncludeSecrets: !redact,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := curl.FormatAPI(api, preview, body.Mode, body.Shell, redact)
	if err != nil {
		writeError(w, newHTTPError(http.StatusBadRequest, "invalid_input", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"curl": out})
}

// ---------------------------------------------------------------------------
// Scenarios

func (s *Server) handleListScenarios(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListScenarios(r.PathValue("pid"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateScenario(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var in store.Scenario
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	out, err := s.store.CreateScenario(pid, &in)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "scenario.create", "scenario", out.ID, map[string]any{"project_id": pid, "name": out.Name})
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleGetScenario(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.GetScenario(r.PathValue("pid"), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpdateScenario(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var in store.Scenario
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	in.ID = r.PathValue("id")
	out, err := s.store.UpdateScenario(pid, &in)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "scenario.update", "scenario", out.ID, map[string]any{"project_id": pid, "name": out.Name})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteScenario(w http.ResponseWriter, r *http.Request) {
	pid, id := r.PathValue("pid"), r.PathValue("id")
	if err := s.store.DeleteScenario(pid, id, deleteVersion(w, r)); err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "scenario.delete", "scenario", id, map[string]any{"project_id": pid})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Runs, Agents, System

func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	var req controller.StartRunRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if u, ok := auth.UserFromContext(r.Context()); ok {
		req.ActorRole = string(u.Role)
	}
	out, err := s.ctrl.StartRun(r.Context(), r.PathValue("pid"), req)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "run.start", "run", out.ID, map[string]any{
		"project_id":    r.PathValue("pid"),
		"scenario_id":   req.ScenarioID,
		"scenario_name": out.ScenarioName,
	})
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleEstimateRun(w http.ResponseWriter, r *http.Request) {
	var req controller.StartRunRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	out, err := s.ctrl.EstimateRun(r.Context(), r.PathValue("pid"), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	if _, err := s.store.GetProject(pid); err != nil {
		writeError(w, err)
		return
	}
	q := store.RunQuery{Limit: 50}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Offset = n
		}
	}
	out, err := s.results.ListRuns(r.Context(), pid, q)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.results.GetRun(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	alerts, err := s.results.Alerts(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	thresholds, thresholdsPassed := controller.EvaluateRunThresholds(run)
	writeJSON(w, http.StatusOK, struct {
		*store.Run
		Alerts           []store.Alert              `json:"alerts"`
		Thresholds       []scenario.ThresholdResult `json:"thresholds,omitempty"`
		ThresholdsPassed *bool                      `json:"thresholds_passed,omitempty"`
	}{
		Run:              run,
		Alerts:           alerts,
		Thresholds:       thresholds,
		ThresholdsPassed: thresholdsPassed,
	})
}

func (s *Server) handleRunSnapshots(w http.ResponseWriter, r *http.Request) {
	var from int64
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			from = n
		}
	}
	var limit int
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	out, err := s.results.Snapshots(r.Context(), r.PathValue("id"), from, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRunSamples(w http.ResponseWriter, r *http.Request) {
	q := store.SampleQuery{
		Kind: r.URL.Query().Get("kind"),
		Step: r.URL.Query().Get("step"),
	}
	if v := r.URL.Query().Get("after_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			q.AfterID = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Limit = n
		}
	}
	out, err := s.results.Samples(r.Context(), r.PathValue("id"), q)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStopRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, err)
			return
		}
	}
	abort := strings.EqualFold(strings.TrimSpace(body.Mode), "abort")
	out, err := s.ctrl.StopRun(r.Context(), r.PathValue("id"), abort)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "run.stop", "run", r.PathValue("id"), map[string]any{"abort": abort})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetRunTarget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target float64 `json:"target"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	var actorRole string
	if u, ok := auth.UserFromContext(r.Context()); ok {
		actorRole = string(u.Role)
	}
	if err := s.ctrl.SetRunTargetWithRole(r.Context(), r.PathValue("id"), body.Target, actorRole); err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "run.target", "run", r.PathValue("id"), map[string]any{"target": body.Target})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.results.GetRun(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if run.Status.Active() {
		writeError(w, newHTTPError(http.StatusConflict, "run_active", "运行中的压测记录不能直接删除，请先停止"))
		return
	}
	if err := s.results.DeleteRun(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "run.delete", "run", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	out, err := s.ctrl.Agents(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePatchAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BandwidthBps *int64   `json:"bandwidth_bps"`
		Weight       *float64 `json:"weight"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	out, err := s.ctrl.PatchAgentSettings(r.Context(), r.PathValue("id"), controller.PatchAgentSettingsRequest{
		BandwidthBps: body.BandwidthBps,
		Weight:       body.Weight,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "agent.update", "agent", r.PathValue("id"), map[string]any{
		"bandwidth_bps": out.BandwidthBps,
		"weight":        out.Weight,
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevokeAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.ctrl.RevokeAgent(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "agent.revoke", "agent", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetAgentCA(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("format") == "pem" {
		pemBytes := s.ctrl.ClusterCACertPEM()
		h := w.Header()
		h.Set("Content-Type", "application/x-pem-file")
		h.Set("Content-Disposition", `attachment; filename="tidegate-cluster-ca.pem"`)
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pemBytes)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"fingerprint": s.ctrl.ClusterCAFingerprint(),
		"cert_pem":    string(s.ctrl.ClusterCACertPEM()),
	})
}

func (s *Server) handleListJoinTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.ctrl.ListJoinTokens(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateJoinToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		TTLHours  int    `json:"ttl_hours"`
		SingleUse *bool  `json:"single_use"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	ttl := 24 * time.Hour
	if body.TTLHours > 0 {
		ttl = time.Duration(body.TTLHours) * time.Hour
	}
	singleUse := true
	if body.SingleUse != nil {
		singleUse = *body.SingleUse
	}
	createdBy := ""
	if u, ok := auth.UserFromContext(r.Context()); ok {
		createdBy = u.Username
	}
	out, err := s.ctrl.CreateJoinToken(r.Context(), body.Name, createdBy, ttl, singleUse)
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "agent.join_token.create", "join_token", out.Item.ID, map[string]any{
		"name":       out.Item.Name,
		"single_use": out.Item.SingleUse,
	})
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleDeleteJoinToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.ctrl.DeleteJoinToken(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	s.audit(r, "agent.join_token.delete", "join_token", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRunHistogram(w http.ResponseWriter, r *http.Request) {
	out, err := s.ctrl.RunHistogram(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":                s.opts.Version,
		"tls":                    s.opts.TLS,
		"cluster_addr":           s.ctrl.ClusterAddr(),
		"cluster_ca_fingerprint": s.ctrl.ClusterCAFingerprint(),
	})
}

func (s *Server) handleDiscoverGRPC(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var body struct {
		EnvironmentID      string            `json:"environment_id,omitempty"`
		Target             string            `json:"target,omitempty"`
		TLS                bool              `json:"tls,omitempty"`
		InsecureSkipVerify bool              `json:"insecure_skip_verify,omitempty"`
		ProtoFile          string            `json:"proto_file,omitempty"`
		Metadata           map[string]string `json:"metadata,omitempty"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	cat, err := s.ctrl.DiscoverGRPC(r.Context(), controller.DiscoverGRPCRequest{
		ProjectID:          pid,
		EnvironmentID:      body.EnvironmentID,
		Target:             body.Target,
		TLS:                body.TLS,
		InsecureSkipVerify: body.InsecureSkipVerify,
		ProtoFile:          body.ProtoFile,
		Metadata:           body.Metadata,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cat)
}
