// Package auth implements local accounts for the Tidegate web UI: first-run
// setup, Argon2id password storage, server-side sessions, login rate
// limiting and the HTTP guards that protect the API (session check, CSRF
// and Origin checks, plain-HTTP remote login refusal).
//
// The package owns its tables in a SQLite database opened by the caller;
// call Migrate before New.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// Role is a user's role. Only RoleAdmin exists for now.
type Role string

// RoleAdmin can do everything.
const RoleAdmin Role = "admin"

// User is an account.
type User struct {
	ID                int64
	Username          string
	Role              Role
	CreatedAt         time.Time
	PasswordChangedAt time.Time
}

// Defaults for Options.
const (
	DefaultIdleTimeout         = 12 * time.Hour
	DefaultAbsoluteLifetime    = 7 * 24 * time.Hour
	DefaultMinPasswordLength   = 10
	DefaultSetupCodeTTL        = 24 * time.Hour
	DefaultMaxConcurrentHashes = 2
	DefaultMaxSessionsPerUser  = 32
	DefaultAdminUsername       = "admin"

	// minAllowedPasswordLength is the lowest MinPasswordLength accepted.
	minAllowedPasswordLength = 8
)

// AdminPasswordFileEnv names the environment variable that points at a file
// holding the initial admin password. The caller reads the variable and
// passes the path to SetupFromPasswordFile.
const AdminPasswordFileEnv = "TIDEGATE_ADMIN_PASSWORD_FILE"

// Options configures a Service. Zero values select the defaults.
type Options struct {
	// IdleTimeout ends a session that has not been used for this long.
	IdleTimeout time.Duration
	// AbsoluteLifetime ends a session this long after login, even if active.
	AbsoluteLifetime time.Duration
	// MinPasswordLength is the minimum number of characters; at least 8.
	MinPasswordLength int
	// Argon2 sets the hashing cost for new hashes. Hashes with different
	// parameters are upgraded on the next successful login.
	Argon2 Argon2Params
	// SetupCodeTTL is how long an issued setup code stays valid.
	SetupCodeTTL time.Duration
	// RateLimit configures login and setup throttling.
	RateLimit RateLimitConfig
	// MaxConcurrentHashes bounds concurrent Argon2 computations, which
	// bounds the memory an attacker can make the server allocate.
	MaxConcurrentHashes int
	// MaxSessionsPerUser keeps only the most recently used sessions.
	MaxSessionsPerUser int
	// Now overrides the clock, for tests.
	Now func() time.Time
	// Logger receives operational warnings. It never receives secrets.
	Logger *slog.Logger
}

// Service manages users and sessions.
type Service struct {
	db            *sql.DB
	idle          time.Duration
	absolute      time.Duration
	touchInterval time.Duration
	minPassLen    int
	params        Argon2Params
	setupTTL      time.Duration
	maxSessions   int
	now           func() time.Time
	log           *slog.Logger

	hashSem   chan struct{}
	dummyHash string

	loginByUser  *limiter
	loginByAddr  *limiter
	setupLimiter *limiter

	// setupMu serializes first-admin creation and guards setup.
	setupMu sync.Mutex
	setup   setupCode
}

// setupCode keeps only the SHA-256 of the issued code, in memory. It is
// valid until used, until it expires, or until the process exits.
type setupCode struct {
	hash    [sha256.Size]byte
	expires time.Time
	issued  bool
}

// New creates a Service. The schema must already exist (see Migrate).
func New(db *sql.DB, opts Options) (*Service, error) {
	if db == nil {
		return nil, errors.New("auth: nil database")
	}
	s := &Service{
		db:          db,
		idle:        opts.IdleTimeout,
		absolute:    opts.AbsoluteLifetime,
		minPassLen:  opts.MinPasswordLength,
		params:      opts.Argon2,
		setupTTL:    opts.SetupCodeTTL,
		maxSessions: opts.MaxSessionsPerUser,
		now:         opts.Now,
		log:         opts.Logger,
	}
	if s.idle <= 0 {
		s.idle = DefaultIdleTimeout
	}
	if s.absolute <= 0 {
		s.absolute = DefaultAbsoluteLifetime
	}
	if s.minPassLen == 0 {
		s.minPassLen = DefaultMinPasswordLength
	}
	if s.minPassLen < minAllowedPasswordLength {
		return nil, fmt.Errorf("auth: minimum password length must be at least %d", minAllowedPasswordLength)
	}
	if s.params == (Argon2Params{}) {
		s.params = DefaultArgon2Params()
	}
	if err := s.params.validate(); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	if s.setupTTL <= 0 {
		s.setupTTL = DefaultSetupCodeTTL
	}
	if s.maxSessions <= 0 {
		s.maxSessions = DefaultMaxSessionsPerUser
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	n := opts.MaxConcurrentHashes
	if n <= 0 {
		n = DefaultMaxConcurrentHashes
	}
	s.hashSem = make(chan struct{}, n)

	// Session last-use times are written at most this often, which keeps
	// authenticated reads from turning into a write per request.
	s.touchInterval = min(time.Minute, s.idle/10)

	s.loginByUser = newLimiter(opts.RateLimit, s.now)
	s.loginByAddr = newLimiter(opts.RateLimit, s.now)
	s.setupLimiter = newLimiter(opts.RateLimit, s.now)

	// Unknown usernames are checked against this hash so that a missing
	// account costs the same time as a wrong password.
	dummy, err := hashPassword(randomText(), s.params)
	if err != nil {
		return nil, err
	}
	s.dummyHash = dummy
	return s, nil
}

func (s *Service) clock() time.Time { return time.UnixMilli(s.now().UnixMilli()) }

func (s *Service) acquireHash(ctx context.Context) error {
	select {
	case s.hashSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releaseHash() { <-s.hashSem }

// hash computes a new password hash under the concurrency bound.
func (s *Service) hash(ctx context.Context, password string) (string, error) {
	if err := s.acquireHash(ctx); err != nil {
		return "", err
	}
	defer s.releaseHash()
	return hashPassword(password, s.params)
}

// ---------------------------------------------------------------------------
// Setup

// SetupPending reports whether no user exists yet.
func (s *Service) SetupPending(ctx context.Context) (bool, error) {
	n, err := s.userCount(ctx, s.db)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// setupEncoding uses Crockford's base32 alphabet: no I, L, O or U.
var setupEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// setupCodeBytes gives 120 bits of entropy, 24 characters.
const setupCodeBytes = 15

// IssueSetupCode generates a new one-time setup code and returns it for the
// caller to print. Only its hash is kept, in memory, so the code is valid
// until it is used, until SetupCodeTTL passes, or until the process exits;
// issuing a new code replaces the previous one. It fails with ErrSetupDone
// once a user exists.
func (s *Service) IssueSetupCode(ctx context.Context) (string, error) {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	pending, err := s.SetupPending(ctx)
	if err != nil {
		return "", err
	}
	if !pending {
		return "", ErrSetupDone
	}
	raw := make([]byte, setupCodeBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: read random: %w", err)
	}
	plain := setupEncoding.EncodeToString(raw)
	s.setup = setupCode{
		hash:    sha256.Sum256([]byte(plain)),
		expires: s.clock().Add(s.setupTTL),
		issued:  true,
	}
	var b strings.Builder
	for i := 0; i < len(plain); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(plain[i:min(i+4, len(plain))])
	}
	return b.String(), nil
}

// normalizeSetupCode accepts the code with or without separators, in any
// case, and with the usual Crockford look-alike substitutions.
func normalizeSetupCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		switch r {
		case '-', ' ', '\t':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SetupRequest creates the first admin.
type SetupRequest struct {
	Code       string
	Username   string
	Password   string
	RemoteAddr string // used for rate limiting and stored with the session
	UserAgent  string
}

// Setup verifies the setup code, creates the first admin, invalidates the
// code and logs the new admin in.
func (s *Service) Setup(ctx context.Context, req SetupRequest) (*LoginResult, error) {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()

	pending, err := s.SetupPending(ctx)
	if err != nil {
		return nil, err
	}
	if !pending {
		return nil, ErrSetupDone
	}
	addrKey := addressKey(req.RemoteAddr)
	if d := s.setupLimiter.retryAfter(addrKey); d > 0 {
		return nil, rateLimited(d)
	}
	if !s.setup.issued {
		s.setupLimiter.fail(addrKey)
		return nil, ErrInvalidSetupCode
	}
	if !s.clock().Before(s.setup.expires) {
		return nil, ErrSetupCodeExpired
	}
	got := sha256.Sum256([]byte(normalizeSetupCode(req.Code)))
	if subtle.ConstantTimeCompare(got[:], s.setup.hash[:]) != 1 {
		s.setupLimiter.fail(addrKey)
		return nil, ErrInvalidSetupCode
	}

	username, err := normalizeUsername(req.Username)
	if err != nil {
		return nil, err
	}
	if err := checkPasswordPolicy(req.Password, username, s.minPassLen); err != nil {
		return nil, err
	}
	u, err := s.createFirstAdmin(ctx, username, req.Password)
	if err != nil {
		return nil, err
	}
	s.setup = setupCode{}
	s.setupLimiter.reset(addrKey)

	sess, token, err := s.createSession(ctx, u.ID, req.RemoteAddr, req.UserAgent)
	if err != nil {
		return nil, err
	}
	return &LoginResult{User: u, Session: sess, Token: token}, nil
}

// SetupFromPasswordFile creates the first admin with the password stored in
// path (see AdminPasswordFileEnv). A UTF-8 byte order mark and trailing line
// breaks are removed. If a user already exists it does nothing and returns
// false, so it is safe to call on every start. An empty username selects
// DefaultAdminUsername. The password must satisfy the password policy.
func (s *Service) SetupFromPasswordFile(ctx context.Context, path, username string) (created bool, err error) {
	if path == "" {
		return false, errors.New("auth: empty admin password file path")
	}
	if username == "" {
		username = DefaultAdminUsername
	}
	username, err = normalizeUsername(username)
	if err != nil {
		return false, err
	}

	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	pending, err := s.SetupPending(ctx)
	if err != nil || !pending {
		return false, err
	}
	password, err := readPasswordFile(path)
	if err != nil {
		return false, err
	}
	if err := checkPasswordPolicy(password, username, s.minPassLen); err != nil {
		return false, err
	}
	if _, err := s.createFirstAdmin(ctx, username, password); err != nil {
		if errors.Is(err, ErrSetupDone) {
			return false, nil
		}
		return false, err
	}
	s.setup = setupCode{}
	return true, nil
}

func readPasswordFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("auth: open admin password file: %w", err)
	}
	defer f.Close()
	// Allow room for a BOM and a trailing CRLF on top of the longest
	// acceptable password.
	buf, err := io.ReadAll(io.LimitReader(f, MaxPasswordBytes+8))
	if err != nil {
		return "", fmt.Errorf("auth: read admin password file: %w", err)
	}
	defer clear(buf)
	text := strings.TrimPrefix(string(buf), string(rune(0xFEFF)))
	text = strings.TrimRight(text, "\r\n")
	if text == "" {
		return "", weakPassword("管理员密码文件是空的")
	}
	if len(text) > MaxPasswordBytes {
		return "", weakPassword(fmt.Sprintf("密码不能超过 %d 个字节", MaxPasswordBytes))
	}
	return text, nil
}

// createFirstAdmin inserts an admin only if the users table is empty, so two
// processes sharing a database cannot both create one.
func (s *Service) createFirstAdmin(ctx context.Context, username, password string) (*User, error) {
	hash, err := s.hash(ctx, password)
	if err != nil {
		return nil, err
	}
	now := s.clock()
	ms := now.UnixMilli()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO auth_users (username, password_hash, role, created_at, updated_at, password_changed_at)
		 SELECT ?, ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM auth_users)`,
		username, hash, string(RoleAdmin), ms, ms, ms)
	if err != nil {
		return nil, fmt.Errorf("auth: create admin: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("auth: create admin: %w", err)
	} else if n == 0 {
		return nil, ErrSetupDone
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("auth: create admin: %w", err)
	}
	return &User{ID: id, Username: username, Role: RoleAdmin, CreatedAt: now, PasswordChangedAt: now}, nil
}

// ---------------------------------------------------------------------------
// Login

// LoginRequest carries login input. RemoteAddr should be the peer address
// (http.Request.RemoteAddr) unless the caller trusts a proxy header.
type LoginRequest struct {
	Username   string
	Password   string
	RemoteAddr string
	UserAgent  string
}

// LoginResult is a new session. Token is the only copy of the session
// secret; send it to the client (see NewSessionCookie) and do not log it.
type LoginResult struct {
	User    *User
	Session *Session
	Token   string
}

// Login checks credentials and opens a session. Unknown usernames and wrong
// passwords produce the same error after the same amount of hashing work.
// Repeated failures for a username or a client address are throttled with
// ErrRateLimited.
func (s *Service) Login(ctx context.Context, req LoginRequest) (*LoginResult, error) {
	username := strings.TrimSpace(req.Username)
	userKey := usernameKey(username)
	addrKey := addressKey(req.RemoteAddr)
	if d := s.loginRetryAfter(userKey, addrKey); d > 0 {
		return nil, rateLimited(d)
	}
	// Whether setup is pending is public, so answering early leaks nothing.
	if pending, err := s.SetupPending(ctx); err != nil {
		return nil, err
	} else if pending {
		return nil, ErrSetupRequired
	}
	if err := s.acquireHash(ctx); err != nil {
		return nil, err
	}
	held := true
	release := func() {
		if held {
			held = false
			s.releaseHash()
		}
	}
	defer release()
	// Requests may have queued for a hash slot while others failed.
	if d := s.loginRetryAfter(userKey, addrKey); d > 0 {
		return nil, rateLimited(d)
	}

	u, stored, err := s.userByName(ctx, username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		_, _ = verifyPassword(req.Password, s.dummyHash)
		s.loginFailed(userKey, addrKey)
		return nil, ErrInvalidCredentials
	}
	ok, err := verifyPassword(req.Password, stored)
	if err != nil {
		return nil, fmt.Errorf("auth: stored password hash for user %d: %w", u.ID, err)
	}
	if !ok {
		s.loginFailed(userKey, addrKey)
		return nil, ErrInvalidCredentials
	}
	s.loginByUser.reset(userKey)
	s.loginByAddr.reset(addrKey)

	if needsRehash(stored, s.params) {
		s.rehash(ctx, u.ID, stored, req.Password)
	}
	release()

	sess, token, err := s.createSession(ctx, u.ID, req.RemoteAddr, req.UserAgent)
	if err != nil {
		return nil, err
	}
	return &LoginResult{User: u, Session: sess, Token: token}, nil
}

func (s *Service) loginRetryAfter(userKey, addrKey string) time.Duration {
	return max(s.loginByUser.retryAfter(userKey), s.loginByAddr.retryAfter(addrKey))
}

func (s *Service) loginFailed(userKey, addrKey string) {
	s.loginByUser.fail(userKey)
	s.loginByAddr.fail(addrKey)
}

// rehash upgrades a stored hash to the current parameters. It must be called
// with a hash slot held. Failures are logged and otherwise ignored: the old
// hash still works.
func (s *Service) rehash(ctx context.Context, userID int64, old, password string) {
	h, err := hashPassword(password, s.params)
	if err == nil {
		_, err = s.db.ExecContext(ctx,
			`UPDATE auth_users SET password_hash = ?, updated_at = ? WHERE id = ? AND password_hash = ?`,
			h, s.clock().UnixMilli(), userID, old)
	}
	if err != nil {
		s.log.Warn("auth: password rehash failed", "user_id", userID, "err", err)
	}
}

// ---------------------------------------------------------------------------
// Password change

// ChangePasswordRequest changes a user's password. Every session of the user
// except KeepSessionID is revoked; leave KeepSessionID empty to revoke all.
type ChangePasswordRequest struct {
	UserID          int64
	CurrentPassword string
	NewPassword     string
	KeepSessionID   string
	RemoteAddr      string
}

// ChangePassword verifies the current password, stores the new one and
// revokes the user's other sessions. Wrong current passwords count towards
// the login rate limit.
func (s *Service) ChangePassword(ctx context.Context, req ChangePasswordRequest) error {
	u, stored, err := s.userByID(ctx, req.UserID)
	if err != nil {
		return err
	}
	userKey := usernameKey(u.Username)
	addrKey := addressKey(req.RemoteAddr)
	if d := s.loginRetryAfter(userKey, addrKey); d > 0 {
		return rateLimited(d)
	}
	if err := s.acquireHash(ctx); err != nil {
		return err
	}
	defer s.releaseHash()
	if d := s.loginRetryAfter(userKey, addrKey); d > 0 {
		return rateLimited(d)
	}
	ok, err := verifyPassword(req.CurrentPassword, stored)
	if err != nil {
		return fmt.Errorf("auth: stored password hash for user %d: %w", u.ID, err)
	}
	if !ok {
		s.loginFailed(userKey, addrKey)
		return ErrIncorrectPassword
	}
	if req.NewPassword == req.CurrentPassword {
		return weakPassword("新密码不能与当前密码相同")
	}
	if err := checkPasswordPolicy(req.NewPassword, u.Username, s.minPassLen); err != nil {
		return err
	}
	h, err := hashPassword(req.NewPassword, s.params)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("auth: change password: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ms := s.clock().UnixMilli()
	if _, err := tx.ExecContext(ctx,
		`UPDATE auth_users SET password_hash = ?, updated_at = ?, password_changed_at = ? WHERE id = ?`,
		h, ms, ms, u.ID); err != nil {
		return fmt.Errorf("auth: change password: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE user_id = ? AND id <> ?`, u.ID, req.KeepSessionID); err != nil {
		return fmt.Errorf("auth: change password: revoke sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("auth: change password: %w", err)
	}
	s.loginByUser.reset(userKey)
	s.loginByAddr.reset(addrKey)
	return nil
}

// ---------------------------------------------------------------------------
// Users

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Service) userCount(ctx context.Context, q queryer) (int, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("auth: count users: %w", err)
	}
	return n, nil
}

const userColumns = `id, username, role, created_at, password_changed_at, password_hash`

func scanUser(row *sql.Row) (*User, string, error) {
	var (
		u                  User
		role, hash         string
		created, pwChanged int64
	)
	if err := row.Scan(&u.ID, &u.Username, &role, &created, &pwChanged, &hash); err != nil {
		return nil, "", err
	}
	u.Role = Role(role)
	u.CreatedAt = time.UnixMilli(created)
	u.PasswordChangedAt = time.UnixMilli(pwChanged)
	return &u, hash, nil
}

// userByName returns (nil, "", nil) when no such user exists.
func (s *Service) userByName(ctx context.Context, username string) (*User, string, error) {
	if username == "" || len(username) > 4*maxUsernameRunes {
		return nil, "", nil
	}
	u, hash, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM auth_users WHERE username = ?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("auth: load user: %w", err)
	}
	return u, hash, nil
}

func (s *Service) userByID(ctx context.Context, id int64) (*User, string, error) {
	u, hash, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM auth_users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrUserNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("auth: load user: %w", err)
	}
	return u, hash, nil
}

// User returns the user with the given ID, or ErrUserNotFound.
func (s *Service) User(ctx context.Context, id int64) (*User, error) {
	u, _, err := s.userByID(ctx, id)
	return u, err
}

// randomText returns 32 random bytes as text, for throwaway passwords.
func randomText() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base32.StdEncoding.EncodeToString(b)
}
