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
	err := s.db.QueryRowContext(ctx, `SELECT restricted FROM auth_project_acl WHERE project_id = ?`, pid).Scan(&restricted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("auth: get project acl: %w", err)
	}
	acl.Restricted = restricted != 0

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
		if m.Role != RoleMember && m.Role != RoleViewer {
			return nil, &Error{Code: CodeInvalidRole, Status: http.StatusBadRequest, Message: "项目成员权限只能设置为 member 或 viewer"}
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
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO auth_project_acl (project_id, restricted, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(project_id) DO UPDATE SET restricted = excluded.restricted, updated_at = excluded.updated_at`,
		pid, restrInt, ms); err != nil {
		return nil, fmt.Errorf("auth: upsert project acl: %w", err)
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
		if memberRole != "" && Role(memberRole).Valid() {
			baseRole = Role(memberRole)
		} else if restricted != 0 {
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
