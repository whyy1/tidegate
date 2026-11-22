package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AgentJoinToken represents a controller-issued enrollment token exchanged by an
// agent for a signed Cluster CA client certificate.
type AgentJoinToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	SingleUse bool      `json:"single_use"`
	UsedCount int       `json:"used_count"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	UsedAt    time.Time `json:"used_at,omitzero"`
}

// CreateJoinToken persists a new join token identified by tokenHash.
func (r *ResultDB) CreateJoinToken(ctx context.Context, tok *AgentJoinToken, tokenHash string) error {
	if tok == nil {
		return invalid("join_token", "令牌对象不能为空")
	}
	if strings.TrimSpace(tokenHash) == "" {
		return invalid("token_hash", "令牌哈希不能为空")
	}
	if tok.ID == "" {
		tok.ID = newID()
	}
	if tok.CreatedAt.IsZero() {
		tok.CreatedAt = time.Now()
	}
	tok.CreatedAt = fromMS(ms(tok.CreatedAt))
	if tok.ExpiresAt.IsZero() {
		tok.ExpiresAt = tok.CreatedAt.Add(24 * time.Hour)
	}
	tok.ExpiresAt = fromMS(ms(tok.ExpiresAt))
	single := 0
	if tok.SingleUse {
		single = 1
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO agent_join_tokens (id, name, token_hash, single_use, used_count, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?)`,
		tok.ID, strings.TrimSpace(tok.Name), tokenHash, single, tok.CreatedBy, ms(tok.CreatedAt), ms(tok.ExpiresAt))
	if err != nil {
		return fmt.Errorf("保存节点加入令牌失败：%w", err)
	}
	return nil
}

// ListJoinTokens returns configured join tokens ordered newest first.
func (r *ResultDB) ListJoinTokens(ctx context.Context) ([]AgentJoinToken, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, single_use, used_count, created_by, created_at, expires_at, used_at
		FROM agent_join_tokens ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("读取节点加入令牌失败：%w", err)
	}
	defer rows.Close()
	out := []AgentJoinToken{}
	for rows.Next() {
		var t AgentJoinToken
		var single int
		var created, expires int64
		var used sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &single, &t.UsedCount, &t.CreatedBy, &created, &expires, &used); err != nil {
			return nil, err
		}
		t.SingleUse = single != 0
		t.CreatedAt = fromMS(created)
		t.ExpiresAt = fromMS(expires)
		t.UsedAt = fromNullMS(used)
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteJoinToken deletes a join token by ID.
func (r *ResultDB) DeleteJoinToken(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM agent_join_tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("join_token", id)
	}
	return nil
}

// ConsumeJoinToken atomically verifies expiration and single-use constraints for tokenHash.
func (r *ResultDB) ConsumeJoinToken(ctx context.Context, tokenHash string, now time.Time) (*AgentJoinToken, error) {
	if now.IsZero() {
		now = time.Now()
	}
	nowMS := ms(now)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var t AgentJoinToken
	var single int
	var created, expires int64
	var used sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id, name, single_use, used_count, created_by, created_at, expires_at, used_at
		FROM agent_join_tokens WHERE token_hash = ?`, tokenHash).
		Scan(&t.ID, &t.Name, &single, &t.UsedCount, &t.CreatedBy, &created, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("join_token", "hash")
	}
	if err != nil {
		return nil, err
	}
	t.SingleUse = single != 0
	t.CreatedAt = fromMS(created)
	t.ExpiresAt = fromMS(expires)
	t.UsedAt = fromNullMS(used)

	if nowMS > expires {
		return nil, invalid("token", "节点加入令牌已过期")
	}
	res, err := tx.ExecContext(ctx, `UPDATE agent_join_tokens
		SET used_count = used_count + 1, used_at = ?
		WHERE id = ? AND expires_at >= ? AND (single_use = 0 OR used_count = 0)`,
		nowMS, t.ID, nowMS)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, invalid("token", "一次性节点加入令牌已被使用或已过期")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	t.UsedCount++
	t.UsedAt = fromMS(nowMS)
	return &t, nil
}

// SaveAgentCert records a newly signed client certificate serial number.
func (r *ResultDB) SaveAgentCert(ctx context.Context, serialHex, nodeName string, expiresAt time.Time) error {
	now := time.Now()
	_, err := r.db.ExecContext(ctx, `INSERT INTO agent_certs (serial_hex, node_id, node_name, created_at, expires_at)
		VALUES (?, '', ?, ?, ?)
		ON CONFLICT (serial_hex) DO UPDATE SET node_name = excluded.node_name, expires_at = excluded.expires_at`,
		strings.ToLower(strings.TrimSpace(serialHex)), strings.TrimSpace(nodeName), ms(now), ms(expiresAt))
	return err
}

// BindAgentCertNode links an enrolled certificate serial to the node ID assigned on Connect.
func (r *ResultDB) BindAgentCertNode(ctx context.Context, serialHex, nodeID, nodeName string) error {
	if strings.TrimSpace(serialHex) == "" || strings.TrimSpace(nodeID) == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `UPDATE agent_certs SET node_id = ?, node_name = ? WHERE serial_hex = ?`,
		nodeID, strings.TrimSpace(nodeName), strings.ToLower(strings.TrimSpace(serialHex)))
	return err
}

// RevokeAgent marks the node ID and any associated certificate serials as revoked.
func (r *ResultDB) RevokeAgent(ctx context.Context, nodeID, nodeName, certSerial, reason string) error {
	nowMS := ms(time.Now())
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if nodeID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO revoked_nodes (node_id, reason, revoked_at) VALUES (?, ?, ?)
			ON CONFLICT (node_id) DO UPDATE SET reason = excluded.reason, revoked_at = excluded.revoked_at`,
			nodeID, reason, nowMS); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_certs SET revoked_at = COALESCE(revoked_at, ?) WHERE node_id = ?`,
			nowMS, nodeID); err != nil {
			return err
		}
	}
	if certSerial != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_certs SET revoked_at = COALESCE(revoked_at, ?) WHERE serial_hex = ?`,
			nowMS, strings.ToLower(strings.TrimSpace(certSerial))); err != nil {
			return err
		}
	}
	if nodeName != "" && nodeID == "" && certSerial == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_certs SET revoked_at = COALESCE(revoked_at, ?) WHERE node_name = ?`,
			nowMS, nodeName); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// IsAgentRevoked reports whether either certSerial or nodeID has been revoked.
func (r *ResultDB) IsAgentRevoked(ctx context.Context, certSerial, nodeID string) (bool, error) {
	if serial := strings.ToLower(strings.TrimSpace(certSerial)); serial != "" {
		var rev sql.NullInt64
		err := r.db.QueryRowContext(ctx, `SELECT revoked_at FROM agent_certs WHERE serial_hex = ?`, serial).Scan(&rev)
		if err == nil && rev.Valid {
			return true, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	if nodeID != "" {
		var count int
		if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM revoked_nodes WHERE node_id = ?`, nodeID).Scan(&count); err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}
