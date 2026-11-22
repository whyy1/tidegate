package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	apiTokenPrefix     = "tg_"
	apiTokenBytes      = 25
	apiTokenShownChars = 11
	maxTokenNameRunes  = 100
)

// APIToken describes a long-lived bearer credential for CLI/CI automation.
// Only its SHA-256 digest is persisted in SQLite.
type APIToken struct {
	ID         string     `json:"id"`
	UserID     int64      `json:"user_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Role       Role       `json:"role"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// CreateTokenRequest configures a new API access token.
type CreateTokenRequest struct {
	UserID int64
	Name   string
	Role   Role
	TTL    time.Duration
}

// CreateAPIToken mints a new token for req.UserID, caps its role to not exceed
// the owner's role, and returns the metadata alongside the one-time secret.
func (s *Service) CreateAPIToken(ctx context.Context, req CreateTokenRequest) (*APIToken, string, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > maxTokenNameRunes {
		return nil, "", ErrInvalidTokenInput
	}
	owner, _, err := s.userByID(ctx, req.UserID)
	if err != nil {
		return nil, "", err
	}
	role := req.Role
	if role == "" {
		role = owner.Role
	}
	if !role.Valid() {
		return nil, "", ErrInvalidRole
	}
	role = EffectiveRole(owner.Role, role)

	id, err := newSessionID()
	if err != nil {
		return nil, "", err
	}
	raw := make([]byte, apiTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("auth: read random: %w", err)
	}
	plaintext := apiTokenPrefix + strings.ToLower(setupEncoding.EncodeToString(raw))
	sum := sha256.Sum256([]byte(plaintext))
	prefix := plaintext[:min(apiTokenShownChars, len(plaintext))]

	now := s.clock()
	var (
		expiresAt *time.Time
		expMS     sql.NullInt64
	)
	if req.TTL > 0 {
		exp := now.Add(req.TTL)
		expiresAt = &exp
		expMS = sql.NullInt64{Int64: exp.UnixMilli(), Valid: true}
	}

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO auth_tokens (id, user_id, name, token_prefix, token_hash, role, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, owner.ID, name, prefix, sum[:], string(role), now.UnixMilli(), expMS); err != nil {
		return nil, "", fmt.Errorf("auth: create api token: %w", err)
	}
	return &APIToken{
		ID:        id,
		UserID:    owner.ID,
		Name:      name,
		Prefix:    prefix,
		Role:      role,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}, plaintext, nil
}

// ListAPITokens returns API tokens owned by userID ordered newest first.
func (s *Service) ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, name, token_prefix, role, created_at, last_used_at, expires_at
		   FROM auth_tokens
		  WHERE user_id = ?
		  ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: list api tokens: %w", err)
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var (
			tok            APIToken
			role           string
			created        int64
			lastUsed, exp  sql.NullInt64
		)
		if err := rows.Scan(&tok.ID, &tok.UserID, &tok.Name, &tok.Prefix, &role, &created, &lastUsed, &exp); err != nil {
			return nil, fmt.Errorf("auth: scan api token: %w", err)
		}
		tok.Role = Role(role)
		tok.CreatedAt = time.UnixMilli(created)
		if lastUsed.Valid {
			t := time.UnixMilli(lastUsed.Int64)
			tok.LastUsedAt = &t
		}
		if exp.Valid {
			t := time.UnixMilli(exp.Int64)
			tok.ExpiresAt = &t
		}
		out = append(out, tok)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list api tokens: %w", err)
	}
	return out, nil
}

// RevokeAPIToken removes tokenID if owned by actorUserID or when isAdmin is true.
func (s *Service) RevokeAPIToken(ctx context.Context, actorUserID int64, tokenID string, isAdmin bool) error {
	var (
		res sql.Result
		err error
	)
	if isAdmin {
		res, err = s.db.ExecContext(ctx, `DELETE FROM auth_tokens WHERE id = ?`, tokenID)
	} else {
		res, err = s.db.ExecContext(ctx, `DELETE FROM auth_tokens WHERE id = ? AND user_id = ?`, tokenID, actorUserID)
	}
	if err != nil {
		return fmt.Errorf("auth: revoke api token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: revoke api token: %w", err)
	}
	if n == 0 {
		return ErrTokenNotFound
	}
	return nil
}

// AuthenticateBearer validates a raw bearer token and returns the owning user
// with u.Role capped to EffectiveRole(userRole, tokenRole).
func (s *Service) AuthenticateBearer(ctx context.Context, rawToken string) (*User, *APIToken, error) {
	if !strings.HasPrefix(rawToken, apiTokenPrefix) || len(rawToken) < len(apiTokenPrefix)+16 {
		return nil, nil, ErrUnauthenticated
	}
	sum := sha256.Sum256([]byte(rawToken))
	var (
		tok                          APIToken
		u                            User
		tokRole, userRole            string
		tokCreated                   int64
		lastUsed, exp                sql.NullInt64
		userCreated, userPwChangedAt int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT t.id, t.user_id, t.name, t.token_prefix, t.role, t.created_at, t.last_used_at, t.expires_at,
		        u.username, u.role, u.created_at, u.password_changed_at
		   FROM auth_tokens t JOIN auth_users u ON u.id = t.user_id
		  WHERE t.token_hash = ?`, sum[:]).
		Scan(&tok.ID, &tok.UserID, &tok.Name, &tok.Prefix, &tokRole, &tokCreated, &lastUsed, &exp,
			&u.Username, &userRole, &userCreated, &userPwChangedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, nil, fmt.Errorf("auth: load api token: %w", err)
	}
	tok.Role = Role(tokRole)
	tok.CreatedAt = time.UnixMilli(tokCreated)
	if lastUsed.Valid {
		t := time.UnixMilli(lastUsed.Int64)
		tok.LastUsedAt = &t
	}
	if exp.Valid {
		t := time.UnixMilli(exp.Int64)
		tok.ExpiresAt = &t
	}

	now := s.clock()
	if tok.ExpiresAt != nil && !now.Before(*tok.ExpiresAt) {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_tokens WHERE id = ?`, tok.ID); err != nil {
			s.log.Warn("auth: delete expired api token failed", "err", err)
		}
		return nil, nil, ErrTokenExpired
	}

	u.ID = tok.UserID
	u.Role = EffectiveRole(Role(userRole), tok.Role)
	u.CreatedAt = time.UnixMilli(userCreated)
	u.PasswordChangedAt = time.UnixMilli(userPwChangedAt)

	if tok.LastUsedAt == nil || now.Sub(*tok.LastUsedAt) >= s.touchInterval {
		if _, err := s.db.ExecContext(ctx, `UPDATE auth_tokens SET last_used_at = ? WHERE id = ?`, now.UnixMilli(), tok.ID); err != nil {
			return nil, nil, fmt.Errorf("auth: touch api token: %w", err)
		}
		tok.LastUsedAt = &now
	}
	return &u, &tok, nil
}
