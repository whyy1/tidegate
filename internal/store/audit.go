package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MaxAuditLogEntries caps retained audit log rows in SQLite.
const MaxAuditLogEntries = 10000

// AuditEntry describes one security or lifecycle event recorded by the API.
type AuditEntry struct {
	ID         int64           `json:"id"`
	CreatedAt  time.Time       `json:"created_at"`
	UserID     int64           `json:"user_id"`
	Username   string          `json:"username"`
	Role       string          `json:"role"`
	Action     string          `json:"action"`
	Resource   string          `json:"resource,omitempty"`
	ResourceID string          `json:"resource_id,omitempty"`
	RemoteAddr string          `json:"remote_addr,omitempty"`
	Detail     json.RawMessage `json:"detail,omitempty"`
}

// AuditQuery filters audit log listings.
type AuditQuery struct {
	Limit    int
	BeforeID int64
	Action   string
	Username string
}

// RecordAudit writes one audit entry and prunes rows beyond MaxAuditLogEntries.
func (r *ResultDB) RecordAudit(ctx context.Context, e AuditEntry) error {
	e.Action = strings.TrimSpace(e.Action)
	if e.Action == "" {
		return invalid("action", "审计动作不能为空")
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	e.CreatedAt = fromMS(ms(e.CreatedAt))
	if len(e.Detail) == 0 {
		e.Detail = json.RawMessage("{}")
	}
	if err := validJSON("detail", e.Detail); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_logs (created_at, user_id, username, role, action, resource, resource_id, remote_addr, detail_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ms(e.CreatedAt), e.UserID, e.Username, e.Role, e.Action, e.Resource, e.ResourceID, e.RemoteAddr, string(e.Detail)); err != nil {
		return fmt.Errorf("写入审计日志失败：%w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM audit_logs WHERE id <= COALESCE((SELECT id FROM audit_logs ORDER BY id DESC LIMIT 1 OFFSET ?), 0)`,
		MaxAuditLogEntries); err != nil {
		return fmt.Errorf("清理历史审计日志失败：%w", err)
	}
	return tx.Commit()
}

// ListAuditLogs returns audit entries newest first matching q.
func (r *ResultDB) ListAuditLogs(ctx context.Context, q AuditQuery) ([]AuditEntry, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	var (
		conds []string
		args  []any
	)
	if q.BeforeID > 0 {
		conds = append(conds, "id < ?")
		args = append(args, q.BeforeID)
	}
	if a := strings.TrimSpace(q.Action); a != "" {
		conds = append(conds, "action = ?")
		args = append(args, a)
	}
	if u := strings.TrimSpace(q.Username); u != "" {
		conds = append(conds, "LOWER(username) = LOWER(?)")
		args = append(args, u)
	}

	query := `SELECT id, created_at, user_id, username, role, action, resource, resource_id, remote_addr, detail_json FROM audit_logs`
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, " AND ")
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取审计日志失败：%w", err)
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var (
			ent       AuditEntry
			createdMS int64
			detail    string
		)
		if err := rows.Scan(&ent.ID, &createdMS, &ent.UserID, &ent.Username, &ent.Role,
			&ent.Action, &ent.Resource, &ent.ResourceID, &ent.RemoteAddr, &detail); err != nil {
			return nil, fmt.Errorf("读取审计日志失败：%w", err)
		}
		ent.CreatedAt = fromMS(createdMS)
		if detail != "" {
			ent.Detail = json.RawMessage(detail)
		}
		out = append(out, ent)
	}
	return out, rows.Err()
}
