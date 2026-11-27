package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// ProjectMember assigns a per-project role override to one local user.
type ProjectMember struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username,omitempty"`
	Role     Role   `json:"role"`
}

// ProjectACL describes project-level access restriction and explicit member overrides.
type ProjectACL struct {
	ProjectID  string          `json:"project_id"`
	Restricted bool            `json:"restricted"`
	CreatedBy  int64           `json:"created_by,omitempty"`
	Members    []ProjectMember `json:"members"`
}

// GetProjectACL returns the stored ACL configuration and members for projectID.
func (s *Service) GetProjectACL(ctx context.Context, projectID string) (*ProjectACL, error) {
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return nil, &Error{Code: CodeInvalidTokenInput, Status: http.StatusBadRequest, Message: "项目 ID 不能为空"}
	}
	acl := &ProjectACL{
		ProjectID: pid,
		Members:   []ProjectMember{},
	}
	var restricted int
	var createdBy int64
	err := s.db.QueryRowContext(ctx, `SELECT restricted, created_by FROM auth_project_acl WHERE project_id = ?`, pid).Scan(&restricted, &createdBy)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("auth: get project acl: %w", err)
	}
	acl.Restricted = restricted != 0
	acl.CreatedBy = createdBy

	rows, err := s.db.QueryContext(ctx,
		`SELECT m.user_id, u.username, m.role
		   FROM auth_project_members m JOIN auth_users u ON u.id = m.user_id
		  WHERE m.project_id = ?
		  ORDER BY u.id ASC`, pid)
	if err != nil {
		return nil, fmt.Errorf("auth: list project members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			pm   ProjectMember
			role string
		)
		if err := rows.Scan(&pm.UserID, &pm.Username, &role); err != nil {
			return nil, fmt.Errorf("auth: scan project member: %w", err)
		}
		pm.Role = Role(role)
		acl.Members = append(acl.Members, pm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list project members: %w", err)
	}
	return acl, nil
}

// SetProjectACL updates whether projectID requires explicit membership and replaces its member list.
func (s *Service) SetProjectACL(ctx context.Context, projectID string, restricted bool, members []ProjectMember) (*ProjectACL, error) {
	return s.saveProjectACL(ctx, projectID, 0, false, restricted, members)
}

// InitProjectACL initializes a newly created/imported project's creator, visibility mode,
// and initial member list in a single transaction.
func (s *Service) InitProjectACL(ctx context.Context, projectID string, createdBy int64, restricted bool, members []ProjectMember) (*ProjectACL, error) {
	return s.saveProjectACL(ctx, projectID, createdBy, true, restricted, members)
}

// ProjectCreatedBy returns the creator user ID stored for projectID (0 if unknown or legacy).
func (s *Service) ProjectCreatedBy(ctx context.Context, projectID string) (int64, error) {
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return 0, nil
	}
	var createdBy int64
	err := s.db.QueryRowContext(ctx, `SELECT created_by FROM auth_project_acl WHERE project_id = ?`, pid).Scan(&createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("auth: get project creator: %w", err)
	}
	return createdBy, nil
}

// ListRestrictedProjects returns the map of project IDs marked as restricted.
func (s *Service) ListRestrictedProjects(ctx context.Context) (map[string]bool, error) {
	out := make(map[string]bool)
	rows, err := s.db.QueryContext(ctx, `SELECT project_id FROM auth_project_acl WHERE restricted <> 0`)
	if err != nil {
		return nil, fmt.Errorf("auth: list restricted projects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			return nil, fmt.Errorf("auth: scan restricted project: %w", err)
		}
		out[pid] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list restricted projects: %w", err)
	}
	return out, nil
}

func (s *Service) saveProjectACL(ctx context.Context, projectID string, createdBy int64, setCreator bool, restricted bool, members []ProjectMember) (*ProjectACL, error) {
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return nil, &Error{Code: CodeInvalidTokenInput, Status: http.StatusBadRequest, Message: "项目 ID 不能为空"}
	}

	seen := make(map[int64]struct{}, len(members))
	cleaned := make([]ProjectMember, 0, len(members))
	for _, m := range members {
		if m.UserID <= 0 {
			return nil, &Error{Code: CodeUserNotFound, Status: http.StatusBadRequest, Message: "项目成员用户 ID 无效"}
		}
		if _, dup := seen[m.UserID]; dup {
			continue
		}
		seen[m.UserID] = struct{}{}
		if m.Role != RoleMember && m.Role != RoleViewer && m.Role != RoleNone {
			return nil, &Error{Code: CodeInvalidRole, Status: http.StatusBadRequest, Message: "项目成员权限只能设置为 member、viewer 或 none"}
		}
		cleaned = append(cleaned, ProjectMember{UserID: m.UserID, Role: m.Role})
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("auth: set project acl: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ms := s.clock().UnixMilli()
	restrInt := 0
	if restricted {
		restrInt = 1
	}
	if setCreator {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO auth_project_acl (project_id, restricted, created_by, updated_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT(project_id) DO UPDATE SET restricted = excluded.restricted, created_by = excluded.created_by, updated_at = excluded.updated_at`,
			pid, restrInt, createdBy, ms); err != nil {
			return nil, fmt.Errorf("auth: upsert project acl: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO auth_project_acl (project_id, restricted, updated_at) VALUES (?, ?, ?)
			 ON CONFLICT(project_id) DO UPDATE SET restricted = excluded.restricted, updated_at = excluded.updated_at`,
			pid, restrInt, ms); err != nil {
			return nil, fmt.Errorf("auth: upsert project acl: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_project_members WHERE project_id = ?`, pid); err != nil {
		return nil, fmt.Errorf("auth: clear project members: %w", err)
	}
	for _, m := range cleaned {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM auth_users WHERE id = ?`, m.UserID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrUserNotFound
			}
			return nil, fmt.Errorf("auth: verify project member user: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO auth_project_members (project_id, user_id, role, updated_at) VALUES (?, ?, ?, ?)`,
			pid, m.UserID, string(m.Role), ms); err != nil {
			return nil, fmt.Errorf("auth: insert project member: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("auth: commit project acl: %w", err)
	}
	return s.GetProjectACL(ctx, pid)
}

// DeleteProjectACL removes stored ACL rules for a deleted project.
func (s *Service) DeleteProjectACL(ctx context.Context, projectID string) error {
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_project_members WHERE project_id = ?`, pid); err != nil {
		return fmt.Errorf("auth: delete project members: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_project_acl WHERE project_id = ?`, pid); err != nil {
		return fmt.Errorf("auth: delete project acl: %w", err)
	}
	return nil
}

// ResolveProjectRole evaluates u (and optional Bearer token tok) against per-project
// ACLs and token project_ids bindings, returning (effectiveRole, allowed, err).
func (s *Service) ResolveProjectRole(ctx context.Context, u *User, tok *APIToken, projectID string) (Role, bool, error) {
	if u == nil {
		return "", false, nil
	}
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return "", false, nil
	}
	if tok != nil && len(tok.ProjectIDs) > 0 && !slices.Contains(tok.ProjectIDs, pid) {
		return "", false, nil
	}

	rawRole := u.RawRole
	if rawRole == "" {
		rawRole = u.Role
	}

	var baseRole Role
	if rawRole == RoleAdmin {
		baseRole = RoleAdmin
	} else {
		var restricted int
		err := s.db.QueryRowContext(ctx, `SELECT restricted FROM auth_project_acl WHERE project_id = ?`, pid).Scan(&restricted)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("auth: resolve project acl: %w", err)
		}
		var memberRole string
		mErr := s.db.QueryRowContext(ctx, `SELECT role FROM auth_project_members WHERE project_id = ? AND user_id = ?`, pid, u.ID).Scan(&memberRole)
		if mErr != nil && !errors.Is(mErr, sql.ErrNoRows) {
			return "", false, fmt.Errorf("auth: resolve project member: %w", mErr)
		}
		if memberRole == string(RoleNone) {
			return "", false, nil
		}
		if memberRole != "" && Role(memberRole).Valid() {
			baseRole = Role(memberRole)
		} else if restricted != 0 || u.DefaultProjectAccess == DefaultAccessDeny {
			return "", false, nil
		} else {
			baseRole = rawRole
		}
	}

	if tok != nil {
		baseRole = EffectiveRole(baseRole, tok.Role)
	}
	return baseRole, true, nil
}

// ListUserProjectOverrides returns all project restriction flags and explicit
// per-project member roles assigned to userID.
func (s *Service) ListUserProjectOverrides(ctx context.Context, userID int64) (restricted map[string]bool, overrides map[string]Role, err error) {
	restricted = make(map[string]bool)
	overrides = make(map[string]Role)

	rows, err := s.db.QueryContext(ctx, `SELECT project_id, restricted FROM auth_project_acl`)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: list restricted projects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pid string
		var r int
		if err := rows.Scan(&pid, &r); err != nil {
			return nil, nil, fmt.Errorf("auth: scan restricted project: %w", err)
		}
		if r != 0 {
			restricted[pid] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("auth: list restricted projects: %w", err)
	}

	mrows, err := s.db.QueryContext(ctx, `SELECT project_id, role FROM auth_project_members WHERE user_id = ?`, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: list user project memberships: %w", err)
	}
	defer mrows.Close()
	for mrows.Next() {
		var pid, role string
		if err := mrows.Scan(&pid, &role); err != nil {
			return nil, nil, fmt.Errorf("auth: scan user project membership: %w", err)
		}
		if Role(role) == RoleNone || Role(role).Valid() {
			overrides[pid] = Role(role)
		}
	}
	if err := mrows.Err(); err != nil {
		return nil, nil, fmt.Errorf("auth: list user project memberships: %w", err)
	}
	return restricted, overrides, nil
}

// SetUserProjectOverrides replaces userID's explicit per-project roles (entries
// with empty role clear that project override so the user falls back to their global role or restricted denial).
func (s *Service) SetUserProjectOverrides(ctx context.Context, userID int64, roles map[string]Role) error {
	return s.SetUserProjectAccess(ctx, userID, "", roles)
}

// SetUserProjectAccess atomically updates userID's optional default_project_access policy
// and replaces explicit per-project role overrides.
func (s *Service) SetUserProjectAccess(ctx context.Context, userID int64, defaultAccess DefaultProjectAccess, roles map[string]Role) error {
	if defaultAccess != "" && !defaultAccess.Valid() {
		return &Error{Code: CodeInvalidRole, Status: http.StatusBadRequest, Message: "默认项目访问策略只能是 inherit 或 deny"}
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM auth_users WHERE id = ?`, userID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		return fmt.Errorf("auth: verify user: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("auth: set user project overrides: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ms := s.clock().UnixMilli()
	if defaultAccess != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE auth_users SET default_project_access = ?, updated_at = ? WHERE id = ?`,
			string(defaultAccess), ms, userID); err != nil {
			return fmt.Errorf("auth: update default project access: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_project_members WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("auth: clear user project overrides: %w", err)
	}
	for rawPID, r := range roles {
		pid := strings.TrimSpace(rawPID)
		if pid == "" || r == "" {
			continue
		}
		if r != RoleMember && r != RoleViewer && r != RoleNone {
			return &Error{Code: CodeInvalidRole, Status: http.StatusBadRequest, Message: "项目成员权限只能设置为 member、viewer 或 none"}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO auth_project_members (project_id, user_id, role, updated_at) VALUES (?, ?, ?, ?)`,
			pid, userID, string(r), ms); err != nil {
			return fmt.Errorf("auth: save user project override: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("auth: commit user project overrides: %w", err)
	}
	return nil
}
