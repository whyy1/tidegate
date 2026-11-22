package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Session is a server-side login session. ID is a public handle used to
// list and revoke sessions; it is not the secret token.
type Session struct {
	ID         string
	UserID     int64
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time // absolute expiry; idle expiry comes earlier if unused
	RemoteAddr string
	UserAgent  string
}

const (
	tokenBytes     = 32 // 256-bit session secret
	sessionIDBytes = 16
	maxUserAgent   = 256
)

// newToken returns the encoded token for the client and the hash to store.
func newToken() (string, []byte, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("auth: read random: %w", err)
	}
	h := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), h[:], nil
}

// tokenHash decodes a client token and returns its stored form. Tokens of
// the wrong shape are rejected before touching the database.
func tokenHash(token string) ([]byte, bool) {
	if len(token) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != tokenBytes {
		return nil, false
	}
	h := sha256.Sum256(raw)
	return h[:], true
}

func newSessionID() (string, error) {
	b := make([]byte, sessionIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: read random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// createSession stores a new session and trims the user's oldest sessions
// beyond MaxSessionsPerUser.
func (s *Service) createSession(ctx context.Context, userID int64, remoteAddr, userAgent string) (*Session, string, error) {
	token, hash, err := newToken()
	if err != nil {
		return nil, "", err
	}
	id, err := newSessionID()
	if err != nil {
		return nil, "", err
	}
	now := s.clock()
	sess := &Session{
		ID:         id,
		UserID:     userID,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.absolute),
		RemoteAddr: clientIP(remoteAddr),
		UserAgent:  clip(userAgent, maxUserAgent),
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", fmt.Errorf("auth: create session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO auth_sessions (id, token_hash, user_id, created_at, last_seen_at, expires_at, remote_addr, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, hash, userID, now.UnixMilli(), now.UnixMilli(), sess.ExpiresAt.UnixMilli(),
		sess.RemoteAddr, sess.UserAgent); err != nil {
		return nil, "", fmt.Errorf("auth: create session: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE user_id = ? AND id NOT IN (
			SELECT id FROM auth_sessions WHERE user_id = ?
			ORDER BY last_seen_at DESC, created_at DESC LIMIT ?)`,
		userID, userID, s.maxSessions); err != nil {
		return nil, "", fmt.Errorf("auth: trim sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("auth: create session: %w", err)
	}
	return sess, token, nil
}

// expired reports whether a session is past its absolute lifetime or has
// been idle too long. The current settings apply to existing sessions too.
func (s *Service) expired(sess *Session, now time.Time) bool {
	return !now.Before(sess.ExpiresAt) ||
		!now.Before(sess.CreatedAt.Add(s.absolute)) ||
		!now.Before(sess.LastSeenAt.Add(s.idle))
}

// Authenticate resolves a session token. It returns ErrUnauthenticated for
// unknown or malformed tokens and ErrSessionExpired (after deleting the
// session) for expired ones. A valid session's last-use time is refreshed.
func (s *Service) Authenticate(ctx context.Context, token string) (*User, *Session, error) {
	hash, ok := tokenHash(token)
	if !ok {
		return nil, nil, ErrUnauthenticated
	}
	var (
		sess                         Session
		u                            User
		role                         string
		created, seen, expires       int64
		userCreated, userPwChangedAt int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT s.id, s.user_id, s.created_at, s.last_seen_at, s.expires_at, s.remote_addr, s.user_agent,
		        u.username, u.role, u.created_at, u.password_changed_at
		   FROM auth_sessions s JOIN auth_users u ON u.id = s.user_id
		  WHERE s.token_hash = ?`, hash).
		Scan(&sess.ID, &sess.UserID, &created, &seen, &expires, &sess.RemoteAddr, &sess.UserAgent,
			&u.Username, &role, &userCreated, &userPwChangedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, nil, fmt.Errorf("auth: load session: %w", err)
	}
	sess.CreatedAt = time.UnixMilli(created)
	sess.LastSeenAt = time.UnixMilli(seen)
	sess.ExpiresAt = time.UnixMilli(expires)
	u.ID = sess.UserID
	u.Role = Role(role)
	u.CreatedAt = time.UnixMilli(userCreated)
	u.PasswordChangedAt = time.UnixMilli(userPwChangedAt)

	now := s.clock()
	if s.expired(&sess, now) {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE id = ?`, sess.ID); err != nil {
			s.log.Warn("auth: delete expired session failed", "err", err)
		}
		return nil, nil, ErrSessionExpired
	}
	if now.Sub(sess.LastSeenAt) >= s.touchInterval {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE auth_sessions SET last_seen_at = ? WHERE id = ? AND last_seen_at < ?`,
			now.UnixMilli(), sess.ID, now.UnixMilli()); err != nil {
			return nil, nil, fmt.Errorf("auth: touch session: %w", err)
		}
		sess.LastSeenAt = now
	}
	return &u, &sess, nil
}

// Logout revokes the session behind token. Unknown tokens are ignored.
func (s *Service) Logout(ctx context.Context, token string) error {
	hash, ok := tokenHash(token)
	if !ok {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_hash = ?`, hash); err != nil {
		return fmt.Errorf("auth: logout: %w", err)
	}
	return nil
}

// Sessions lists the user's active sessions, most recently used first.
func (s *Service) Sessions(ctx context.Context, userID int64) ([]Session, error) {
	now := s.clock()
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, created_at, last_seen_at, expires_at, remote_addr, user_agent
		   FROM auth_sessions
		  WHERE user_id = ? AND expires_at > ? AND created_at > ? AND last_seen_at > ?
		  ORDER BY last_seen_at DESC, created_at DESC`,
		userID, now.UnixMilli(), now.Add(-s.absolute).UnixMilli(), now.Add(-s.idle).UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("auth: list sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var (
			sess                   Session
			created, seen, expires int64
		)
		if err := rows.Scan(&sess.ID, &sess.UserID, &created, &seen, &expires, &sess.RemoteAddr, &sess.UserAgent); err != nil {
			return nil, fmt.Errorf("auth: list sessions: %w", err)
		}
		sess.CreatedAt = time.UnixMilli(created)
		sess.LastSeenAt = time.UnixMilli(seen)
		sess.ExpiresAt = time.UnixMilli(expires)
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list sessions: %w", err)
	}
	return out, nil
}

// RevokeSession deletes one of the user's sessions by its public ID. It
// returns ErrSessionNotFound if the session does not exist or belongs to
// another user.
func (s *Service) RevokeSession(ctx context.Context, userID int64, sessionID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE id = ? AND user_id = ?`, sessionID, userID)
	if err != nil {
		return fmt.Errorf("auth: revoke session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: revoke session: %w", err)
	}
	if n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// RevokeOtherSessions deletes all of the user's sessions except keepID and
// returns how many were removed.
func (s *Service) RevokeOtherSessions(ctx context.Context, userID int64, keepID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = ? AND id <> ?`, userID, keepID)
	if err != nil {
		return 0, fmt.Errorf("auth: revoke sessions: %w", err)
	}
	return res.RowsAffected()
}

// CleanupExpired deletes expired sessions and forgets stale rate limit
// entries. It returns the number of sessions deleted.
func (s *Service) CleanupExpired(ctx context.Context) (int64, error) {
	now := s.clock()
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE expires_at <= ? OR created_at <= ? OR last_seen_at <= ?`,
		now.UnixMilli(), now.Add(-s.absolute).UnixMilli(), now.Add(-s.idle).UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("auth: cleanup sessions: %w", err)
	}
	s.loginByUser.prune()
	s.loginByAddr.prune()
	s.setupLimiter.prune()
	return res.RowsAffected()
}

// RunCleanup calls CleanupExpired every interval until ctx is done. Run it
// in its own goroutine.
func (s *Service) RunCleanup(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.CleanupExpired(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("auth: session cleanup failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
