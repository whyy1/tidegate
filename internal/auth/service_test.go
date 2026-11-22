package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestMigrateIdempotentAndVersioned(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT value FROM auth_meta WHERE key = 'schema_version'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "3" {
		t.Fatalf("schema version = %q, want 3", v)
	}
	if _, err := db.Exec(`UPDATE auth_meta SET value = '99' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil {
		t.Fatal("migrate must refuse a newer schema")
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	db := openTestDB(t)
	if _, err := New(db, Options{Argon2: testParams, MinPasswordLength: 4}); err == nil {
		t.Fatal("too small minimum password length accepted")
	}
	if _, err := New(db, Options{Argon2: Argon2Params{Memory: 1, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}}); err == nil {
		t.Fatal("invalid argon2 params accepted")
	}
	if _, err := New(nil, Options{}); err == nil {
		t.Fatal("nil db accepted")
	}
}

func TestSetupFlow(t *testing.T) {
	ctx := context.Background()
	clk := newFakeClock()
	s := newTestService(t, openTestDB(t), clk, nil)

	pending, err := s.SetupPending(ctx)
	if err != nil || !pending {
		t.Fatalf("SetupPending = %v, %v; want true", pending, err)
	}
	if _, err := login(s, testAdmin, testPassword, testAddr); !errors.Is(err, ErrSetupRequired) {
		t.Fatalf("login before setup: %v, want ErrSetupRequired", err)
	}

	code, err := s.IssueSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){5}$`).MatchString(code) {
		t.Fatalf("unexpected setup code format %q", code)
	}
	// The code may be typed in lower case without separators.
	typed := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	res, err := s.Setup(ctx, SetupRequest{Code: typed, Username: " Admin ", Password: testPassword, RemoteAddr: testAddr, UserAgent: "ua"})
	if err != nil {
		t.Fatal(err)
	}
	if res.User.Username != "Admin" || res.User.Role != RoleAdmin || res.Token == "" || res.Session.UserID != res.User.ID {
		t.Fatalf("unexpected setup result %+v", res)
	}
	if u, _, err := s.Authenticate(ctx, res.Token); err != nil || u.ID != res.User.ID {
		t.Fatalf("setup session not usable: %v", err)
	}
	if pending, _ := s.SetupPending(ctx); pending {
		t.Fatal("still pending after setup")
	}

	// The code is single use and setup cannot run twice.
	_, err = s.Setup(ctx, SetupRequest{Code: code, Username: "other", Password: testPassword, RemoteAddr: testAddr})
	wantErr(t, err, ErrSetupDone)
	_, err = s.IssueSetupCode(ctx)
	wantErr(t, err, ErrSetupDone)

	// Usernames are case-insensitive at login.
	if _, err := login(s, "ADMIN", testPassword, testAddr); err != nil {
		t.Fatalf("login after setup: %v", err)
	}
}

func TestSetupWrongCode(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, openTestDB(t), newFakeClock(), nil)

	// No code issued yet.
	_, err := s.Setup(ctx, SetupRequest{Code: "AAAA-AAAA", Username: testAdmin, Password: testPassword, RemoteAddr: "192.0.2.10:1"})
	wantErr(t, err, ErrInvalidSetupCode)

	code, _ := s.IssueSetupCode(ctx)
	wrong := "0000-0000-0000-0000-0000-0000"
	if wrong == code {
		wrong = "1111-1111-1111-1111-1111-1111"
	}
	_, err = s.Setup(ctx, SetupRequest{Code: wrong, Username: testAdmin, Password: testPassword, RemoteAddr: "192.0.2.11:1"})
	wantErr(t, err, ErrInvalidSetupCode)
	if pending, _ := s.SetupPending(ctx); !pending {
		t.Fatal("wrong code created a user")
	}

	// Issuing a new code replaces the old one.
	newCode, _ := s.IssueSetupCode(ctx)
	_, err = s.Setup(ctx, SetupRequest{Code: code, Username: testAdmin, Password: testPassword, RemoteAddr: "192.0.2.12:1"})
	wantErr(t, err, ErrInvalidSetupCode)
	if _, err := s.Setup(ctx, SetupRequest{Code: newCode, Username: testAdmin, Password: testPassword, RemoteAddr: "192.0.2.12:1"}); err != nil {
		t.Fatal(err)
	}
}

func TestSetupExpiredCode(t *testing.T) {
	ctx := context.Background()
	clk := newFakeClock()
	s := newTestService(t, openTestDB(t), clk, func(o *Options) { o.SetupCodeTTL = time.Hour })
	code, _ := s.IssueSetupCode(ctx)
	clk.Advance(time.Hour)
	_, err := s.Setup(ctx, SetupRequest{Code: code, Username: testAdmin, Password: testPassword, RemoteAddr: testAddr})
	wantErr(t, err, ErrSetupCodeExpired)

	code, _ = s.IssueSetupCode(ctx)
	clk.Advance(59 * time.Minute)
	if _, err := s.Setup(ctx, SetupRequest{Code: code, Username: testAdmin, Password: testPassword, RemoteAddr: testAddr}); err != nil {
		t.Fatalf("fresh code rejected: %v", err)
	}
}

func TestSetupValidationKeepsCode(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, openTestDB(t), newFakeClock(), nil)
	code, _ := s.IssueSetupCode(ctx)

	_, err := s.Setup(ctx, SetupRequest{Code: code, Username: testAdmin, Password: "short", RemoteAddr: testAddr})
	wantErr(t, err, ErrWeakPassword)
	_, err = s.Setup(ctx, SetupRequest{Code: code, Username: "bad name", Password: testPassword, RemoteAddr: testAddr})
	wantErr(t, err, ErrInvalidUsername)
	if _, err := s.Setup(ctx, SetupRequest{Code: code, Username: testAdmin, Password: testPassword, RemoteAddr: testAddr}); err != nil {
		t.Fatalf("code consumed by a rejected attempt: %v", err)
	}
}

func TestSetupRateLimited(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, openTestDB(t), newFakeClock(), nil)
	code, _ := s.IssueSetupCode(ctx)
	attacker := "198.51.100.7:4000"
	for range DefaultFreeAttempts + 1 {
		_, err := s.Setup(ctx, SetupRequest{Code: "WRONG", Username: testAdmin, Password: testPassword, RemoteAddr: attacker})
		wantErr(t, err, ErrInvalidSetupCode)
	}
	// Even the right code is refused while the address is blocked.
	_, err := s.Setup(ctx, SetupRequest{Code: code, Username: testAdmin, Password: testPassword, RemoteAddr: attacker})
	wantErr(t, err, ErrRateLimited)
	if d, ok := RetryAfter(err); !ok || d != time.Second {
		t.Fatalf("RetryAfter = %v, %v; want 1s", d, ok)
	}
	if _, err := s.Setup(ctx, SetupRequest{Code: code, Username: testAdmin, Password: testPassword, RemoteAddr: testAddr}); err != nil {
		t.Fatalf("other address blocked: %v", err)
	}
}

func TestSetupFromPasswordFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := newTestService(t, openTestDB(t), newFakeClock(), nil)
	code, _ := s.IssueSetupCode(ctx)

	missing := filepath.Join(dir, "missing")
	if _, err := s.SetupFromPasswordFile(ctx, missing, ""); err == nil {
		t.Fatal("missing file accepted")
	}
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, []byte("\r\n"), 0o600)
	if _, err := s.SetupFromPasswordFile(ctx, empty, ""); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("empty file: %v", err)
	}
	weak := filepath.Join(dir, "weak")
	os.WriteFile(weak, []byte("12345\n"), 0o600)
	if _, err := s.SetupFromPasswordFile(ctx, weak, ""); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password file: %v", err)
	}

	good := filepath.Join(dir, "good")
	os.WriteFile(good, []byte("\xef\xbb\xbf"+testPassword+"\r\n"), 0o600)
	created, err := s.SetupFromPasswordFile(ctx, good, "")
	if err != nil || !created {
		t.Fatalf("SetupFromPasswordFile = %v, %v", created, err)
	}
	if _, err := login(s, DefaultAdminUsername, testPassword, testAddr); err != nil {
		t.Fatalf("login with preset password: %v", err)
	}
	// Idempotent on later starts, and the printed code no longer works.
	created, err = s.SetupFromPasswordFile(ctx, good, "")
	if err != nil || created {
		t.Fatalf("second call = %v, %v; want false, nil", created, err)
	}
	// Once set up, the file is not read at all.
	created, err = s.SetupFromPasswordFile(ctx, missing, "")
	if err != nil || created {
		t.Fatalf("call with removed file = %v, %v; want false, nil", created, err)
	}
	_, err = s.Setup(ctx, SetupRequest{Code: code, Username: "x", Password: testPassword, RemoteAddr: testAddr})
	wantErr(t, err, ErrSetupDone)
}

func TestLoginSuccessAndFailure(t *testing.T) {
	s, _, _ := newSetupService(t, nil)

	res, err := login(s, testAdmin, testPassword, testAddr)
	if err != nil {
		t.Fatal(err)
	}
	if res.User.Username != testAdmin || len(res.Token) != 43 || res.Session.RemoteAddr != "127.0.0.1" || res.Session.UserAgent != "test" {
		t.Fatalf("unexpected login result %+v %+v", res.User, res.Session)
	}

	_, errWrong := login(s, testAdmin, "wrong password!", "192.0.2.1:1")
	_, errMissing := login(s, "nobody", "wrong password!", "192.0.2.2:1")
	wantErr(t, errWrong, ErrInvalidCredentials)
	wantErr(t, errMissing, ErrInvalidCredentials)
	if errWrong.Error() != errMissing.Error() {
		t.Fatalf("unknown user and wrong password must look the same: %q vs %q", errWrong, errMissing)
	}
	_, err = login(s, "", "", "192.0.2.3:1")
	wantErr(t, err, ErrInvalidCredentials)

	if p, _, _, err := parsePHC(s.dummyHash); err != nil || p != testParams {
		t.Fatalf("dummy hash must use the current parameters: %+v %v", p, err)
	}
}

func TestLoginRateLimitGrowthAndReset(t *testing.T) {
	s, clk, _ := newSetupService(t, nil)

	fail := func() error {
		_, err := login(s, testAdmin, "wrong password!", testAddr)
		return err
	}
	for i := range DefaultFreeAttempts {
		if err := fail(); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("free attempt %d: %v", i, err)
		}
	}
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		wantErr(t, fail(), ErrInvalidCredentials)
		// Blocked, even for the right password, so the limit is no oracle.
		_, err := login(s, testAdmin, testPassword, testAddr)
		wantErr(t, err, ErrRateLimited)
		if d, _ := RetryAfter(err); d != wait {
			t.Fatalf("RetryAfter = %v, want %v", d, wait)
		}
		if e, _ := AsError(err); e.Status != 429 || !strings.Contains(e.Message, "秒") {
			t.Fatalf("unexpected rate limit error %+v", e)
		}
		clk.Advance(wait)
	}

	// The address is limited for any username...
	_, err := login(s, "someone-else", "whatever-pass", testAddr)
	wantErr(t, err, ErrInvalidCredentials)
	_, err = login(s, "another-one", "whatever-pass", testAddr)
	wantErr(t, err, ErrRateLimited)
	// ...and the username from any address.
	_, err = login(s, testAdmin, "wrong password!", "203.0.113.5:1")
	wantErr(t, err, ErrInvalidCredentials)
	_, err = login(s, testAdmin, testPassword, "203.0.113.6:1")
	wantErr(t, err, ErrRateLimited)

	// After waiting, a success clears both counters.
	clk.Advance(8 * time.Second)
	if _, err := login(s, testAdmin, testPassword, testAddr); err != nil {
		t.Fatalf("login after wait: %v", err)
	}
	if err := fail(); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("first failure after reset: %v", err)
	}
	if _, err := login(s, testAdmin, testPassword, testAddr); err != nil {
		t.Fatalf("counter not reset by success: %v", err)
	}
}

func TestLoginUnknownUserIsRateLimitedToo(t *testing.T) {
	s, _, _ := newSetupService(t, nil)
	for range DefaultFreeAttempts + 1 {
		login(s, "ghost", "wrong password!", "192.0.2.50:1")
	}
	_, err := login(s, "ghost", "wrong password!", "192.0.2.51:1")
	wantErr(t, err, ErrRateLimited)
}

func TestRehashOnLogin(t *testing.T) {
	ctx := context.Background()
	clk := newFakeClock()
	db := openTestDB(t)
	old := newTestService(t, db, clk, nil)
	mustSetup(t, old)

	stronger := testParams
	stronger.Time = 2
	s := newTestService(t, db, clk, func(o *Options) { o.Argon2 = stronger })
	if _, err := login(s, testAdmin, testPassword, testAddr); err != nil {
		t.Fatal(err)
	}
	var h string
	if err := db.QueryRowContext(ctx, `SELECT password_hash FROM auth_users`).Scan(&h); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h, "t=2") || needsRehash(h, stronger) {
		t.Fatalf("hash not upgraded: %s", h)
	}
	if _, err := login(s, testAdmin, testPassword, testAddr); err != nil {
		t.Fatalf("login with upgraded hash: %v", err)
	}
}

func TestSessionIdleExpiry(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSetupService(t, nil)
	res := mustLogin(t, s)

	// Activity keeps the session alive past one idle period.
	for range 3 {
		clk.Advance(11 * time.Hour)
		if _, _, err := s.Authenticate(ctx, res.Token); err != nil {
			t.Fatalf("active session rejected: %v", err)
		}
	}
	clk.Advance(DefaultIdleTimeout)
	_, _, err := s.Authenticate(ctx, res.Token)
	wantErr(t, err, ErrSessionExpired)
	// The expired session is gone.
	_, _, err = s.Authenticate(ctx, res.Token)
	wantErr(t, err, ErrUnauthenticated)
}

func TestSessionAbsoluteExpiry(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSetupService(t, nil)
	res := mustLogin(t, s)
	if !res.Session.ExpiresAt.Equal(clk.Now().Add(DefaultAbsoluteLifetime)) {
		t.Fatalf("ExpiresAt = %v", res.Session.ExpiresAt)
	}
	var elapsed time.Duration
	for elapsed+11*time.Hour < DefaultAbsoluteLifetime {
		clk.Advance(11 * time.Hour)
		elapsed += 11 * time.Hour
		if _, _, err := s.Authenticate(ctx, res.Token); err != nil {
			t.Fatalf("session rejected after %v: %v", elapsed, err)
		}
	}
	clk.Advance(DefaultAbsoluteLifetime - elapsed)
	_, _, err := s.Authenticate(ctx, res.Token)
	wantErr(t, err, ErrSessionExpired)
}

func TestSessionTimeoutsConfigurable(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSetupService(t, func(o *Options) {
		o.IdleTimeout = 30 * time.Minute
		o.AbsoluteLifetime = 2 * time.Hour
	})
	res := mustLogin(t, s)
	clk.Advance(29 * time.Minute)
	if _, _, err := s.Authenticate(ctx, res.Token); err != nil {
		t.Fatal(err)
	}
	clk.Advance(30 * time.Minute)
	_, _, err := s.Authenticate(ctx, res.Token)
	wantErr(t, err, ErrSessionExpired)

	res = mustLogin(t, s)
	for range 4 {
		clk.Advance(29 * time.Minute)
		if _, _, err := s.Authenticate(ctx, res.Token); err != nil {
			t.Fatal(err)
		}
	}
	clk.Advance(5 * time.Minute)
	_, _, err = s.Authenticate(ctx, res.Token)
	wantErr(t, err, ErrSessionExpired)
}

func TestAuthenticateRejectsBadTokens(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSetupService(t, nil)
	for _, tok := range []string{"", "x", strings.Repeat("A", 43), strings.Repeat("A", 44), strings.Repeat("!", 43)} {
		_, _, err := s.Authenticate(ctx, tok)
		wantErr(t, err, ErrUnauthenticated)
	}
}

func TestTokenStoredOnlyAsHash(t *testing.T) {
	s, _, res := newSetupService(t, nil)
	raw, err := base64.RawURLEncoding.DecodeString(res.Token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token is not 256 random bits: %v", err)
	}
	var stored []byte
	if err := s.db.QueryRow(`SELECT token_hash FROM auth_sessions WHERE id = ?`, res.Session.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(raw)
	if string(stored) != string(want[:]) {
		t.Fatal("stored value is not the SHA-256 of the token")
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM auth_sessions WHERE token_hash = ? OR id = ?`, []byte(res.Token), res.Token).Scan(&n)
	if n != 0 {
		t.Fatal("plain token found in the database")
	}
}

func TestLogout(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSetupService(t, nil)
	res := mustLogin(t, s)
	if err := s.Logout(ctx, res.Token); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Authenticate(ctx, res.Token)
	wantErr(t, err, ErrUnauthenticated)
	if err := s.Logout(ctx, res.Token); err != nil {
		t.Fatalf("second logout: %v", err)
	}
	if err := s.Logout(ctx, "garbage"); err != nil {
		t.Fatalf("logout with garbage: %v", err)
	}
}

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	ctx := context.Background()
	s, _, setup := newSetupService(t, nil)
	a := mustLogin(t, s)
	b := mustLogin(t, s)
	const newPassword = "a much better passphrase"

	err := s.ChangePassword(ctx, ChangePasswordRequest{UserID: a.User.ID, CurrentPassword: "wrong password!", NewPassword: newPassword, KeepSessionID: a.Session.ID, RemoteAddr: testAddr})
	wantErr(t, err, ErrIncorrectPassword)
	err = s.ChangePassword(ctx, ChangePasswordRequest{UserID: a.User.ID, CurrentPassword: testPassword, NewPassword: testPassword, KeepSessionID: a.Session.ID, RemoteAddr: testAddr})
	wantErr(t, err, ErrWeakPassword)
	err = s.ChangePassword(ctx, ChangePasswordRequest{UserID: a.User.ID, CurrentPassword: testPassword, NewPassword: "0123456789", KeepSessionID: a.Session.ID, RemoteAddr: testAddr})
	wantErr(t, err, ErrWeakPassword)
	// Rejected attempts leave every session alone.
	for _, tok := range []string{setup.Token, a.Token, b.Token} {
		if _, _, err := s.Authenticate(ctx, tok); err != nil {
			t.Fatalf("session revoked by a rejected change: %v", err)
		}
	}

	if err := s.ChangePassword(ctx, ChangePasswordRequest{UserID: a.User.ID, CurrentPassword: testPassword, NewPassword: newPassword, KeepSessionID: a.Session.ID, RemoteAddr: testAddr}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, a.Token); err != nil {
		t.Fatalf("current session revoked: %v", err)
	}
	for _, tok := range []string{setup.Token, b.Token} {
		_, _, err := s.Authenticate(ctx, tok)
		wantErr(t, err, ErrUnauthenticated)
	}
	_, err = login(s, testAdmin, testPassword, testAddr)
	wantErr(t, err, ErrInvalidCredentials)
	if _, err := login(s, testAdmin, newPassword, testAddr); err != nil {
		t.Fatalf("login with new password: %v", err)
	}

	wantErr(t, s.ChangePassword(ctx, ChangePasswordRequest{UserID: 999}), ErrUserNotFound)
}

func TestSessionsListRevokeAndCleanup(t *testing.T) {
	ctx := context.Background()
	s, clk, setup := newSetupService(t, nil)
	clk.Advance(time.Minute)
	a := mustLogin(t, s)
	clk.Advance(time.Minute)
	b := mustLogin(t, s)

	list, err := s.Sessions(ctx, setup.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != b.Session.ID || list[2].ID != setup.Session.ID {
		t.Fatalf("unexpected session list %+v", list)
	}

	wantErr(t, s.RevokeSession(ctx, setup.User.ID+1, a.Session.ID), ErrSessionNotFound)
	wantErr(t, s.RevokeSession(ctx, setup.User.ID, "nope"), ErrSessionNotFound)
	if err := s.RevokeSession(ctx, setup.User.ID, a.Session.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Authenticate(ctx, a.Token)
	wantErr(t, err, ErrUnauthenticated)

	n, err := s.RevokeOtherSessions(ctx, setup.User.ID, b.Session.ID)
	if err != nil || n != 1 {
		t.Fatalf("RevokeOtherSessions = %d, %v; want 1", n, err)
	}

	// b idles out; cleanup removes it without anyone touching it.
	clk.Advance(DefaultIdleTimeout)
	n, err = s.CleanupExpired(ctx)
	if err != nil || n != 1 {
		t.Fatalf("CleanupExpired = %d, %v; want 1", n, err)
	}
	if list, _ := s.Sessions(ctx, setup.User.ID); len(list) != 0 {
		t.Fatalf("sessions left after cleanup: %+v", list)
	}
}

func TestMaxSessionsPerUser(t *testing.T) {
	ctx := context.Background()
	s, clk, setup := newSetupService(t, func(o *Options) { o.MaxSessionsPerUser = 2 })
	clk.Advance(time.Second)
	mustLogin(t, s)
	clk.Advance(time.Second)
	last := mustLogin(t, s)
	list, _ := s.Sessions(ctx, setup.User.ID)
	if len(list) != 2 || list[0].ID != last.Session.ID {
		t.Fatalf("unexpected sessions %+v", list)
	}
	_, _, err := s.Authenticate(ctx, setup.Token)
	wantErr(t, err, ErrUnauthenticated)
}

func TestRunCleanupStops(t *testing.T) {
	s, _, _ := newSetupService(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.RunCleanup(ctx, time.Millisecond)
		close(done)
	}()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunCleanup did not stop")
	}
}

func TestUserLookup(t *testing.T) {
	ctx := context.Background()
	s, _, setup := newSetupService(t, nil)
	u, err := s.User(ctx, setup.User.ID)
	if err != nil || u.Username != testAdmin || u.Role != RoleAdmin {
		t.Fatalf("User = %+v, %v", u, err)
	}
	_, err = s.User(ctx, 12345)
	wantErr(t, err, ErrUserNotFound)
}

func TestResetPassword(t *testing.T) {
	ctx := context.Background()
	s, _, setup := newSetupService(t, nil)
	a := mustLogin(t, s)

	wantErr(t, s.ResetPassword(ctx, "nonexistent", "a valid passphrase"), ErrUserNotFound)
	wantErr(t, s.ResetPassword(ctx, testAdmin, "short"), ErrWeakPassword)

	const nextPass = "reset passphrase 2026!"
	if err := s.ResetPassword(ctx, testAdmin, nextPass); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{setup.Token, a.Token} {
		_, _, err := s.Authenticate(ctx, tok)
		wantErr(t, err, ErrUnauthenticated)
	}
	_, err := login(s, testAdmin, testPassword, testAddr)
	wantErr(t, err, ErrInvalidCredentials)
	if _, err := login(s, testAdmin, nextPass, testAddr); err != nil {
		t.Fatalf("login with reset password: %v", err)
	}
}

func TestRolesUsersAndAPITokens(t *testing.T) {
	ctx := context.Background()
	s, clk, setup := newSetupService(t, nil)

	// Last admin cannot be demoted or deleted.
	wantErr(t, func() error { _, err := s.UpdateUserRole(ctx, setup.User.ID, RoleMember); return err }(), ErrLastAdmin)
	wantErr(t, s.DeleteUser(ctx, 0, setup.User.ID), ErrLastAdmin)
	wantErr(t, s.DeleteUser(ctx, setup.User.ID, setup.User.ID), ErrForbidden)

	member, err := s.CreateUser(ctx, CreateUserRequest{
		Username: "bob_member",
		Password: "bob-strong-password-1",
		Role:     RoleMember,
	})
	if err != nil {
		t.Fatalf("CreateUser(member): %v", err)
	}
	viewer, err := s.CreateUser(ctx, CreateUserRequest{
		Username: "carol_viewer",
		Password: "carol-strong-password-1",
		Role:     RoleViewer,
	})
	if err != nil {
		t.Fatalf("CreateUser(viewer): %v", err)
	}
	// Duplicate username rejected.
	if _, err := s.CreateUser(ctx, CreateUserRequest{Username: "BOB_MEMBER", Password: "bob-strong-password-1"}); !errors.Is(err, ErrUserExists) {
		t.Fatalf("expected ErrUserExists, got %v", err)
	}

	users, err := s.ListUsers(ctx)
	if err != nil || len(users) != 3 {
		t.Fatalf("ListUsers = %+v, err=%v", users, err)
	}

	// Token role is capped to owner's role.
	tokMeta, rawSecret, err := s.CreateAPIToken(ctx, CreateTokenRequest{
		UserID: member.ID,
		Name:   "ci-runner",
		Role:   RoleAdmin, // capped to RoleMember
		TTL:    2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if !strings.HasPrefix(rawSecret, "tg_") || tokMeta.Role != RoleMember || tokMeta.Prefix != rawSecret[:11] {
		t.Fatalf("unexpected token metadata=%+v secret=%q", tokMeta, rawSecret)
	}

	// Verify only hash is persisted, never plaintext.
	var storedHash []byte
	if err := s.db.QueryRow(`SELECT token_hash FROM auth_tokens WHERE id = ?`, tokMeta.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256([]byte(rawSecret))
	if string(storedHash) != string(wantHash[:]) {
		t.Fatal("stored token_hash does not match SHA-256 of raw token")
	}

	// Authenticating bearer works and respects EffectiveRole.
	authUser, authTok, err := s.AuthenticateBearer(ctx, rawSecret)
	if err != nil || authUser.ID != member.ID || authUser.Role != RoleMember || authTok.ID != tokMeta.ID {
		t.Fatalf("AuthenticateBearer = (%+v, %+v, %v)", authUser, authTok, err)
	}

	// Demoting owner dynamically restricts token's EffectiveRole.
	if _, err := s.UpdateUserRole(ctx, member.ID, RoleViewer); err != nil {
		t.Fatal(err)
	}
	authUser, _, err = s.AuthenticateBearer(ctx, rawSecret)
	if err != nil || authUser.Role != RoleViewer {
		t.Fatalf("expected EffectiveRole downgraded to RoleViewer, got %+v err=%v", authUser, err)
	}

	// Expired token is deleted and rejected with ErrTokenExpired.
	clk.Advance(3 * time.Hour)
	_, _, err = s.AuthenticateBearer(ctx, rawSecret)
	wantErr(t, err, ErrTokenExpired)

	// Admin can delete viewer and member, and revoke tokens.
	_, adminRaw, err := s.CreateAPIToken(ctx, CreateTokenRequest{UserID: setup.User.ID, Name: "admin-cli"})
	if err != nil {
		t.Fatal(err)
	}
	adminTokList, err := s.ListAPITokens(ctx, setup.User.ID)
	if err != nil || len(adminTokList) != 1 {
		t.Fatalf("ListAPITokens = %+v, err=%v", adminTokList, err)
	}
	if err := s.RevokeAPIToken(ctx, viewer.ID, adminTokList[0].ID, false); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("non-owner revoke should fail with ErrTokenNotFound, got %v", err)
	}
	if err := s.RevokeAPIToken(ctx, setup.User.ID, adminTokList[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthenticateBearer(ctx, adminRaw); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked bearer token should be unauthenticated, got %v", err)
	}
	for i := 0; i < DefaultFreeAttempts+1; i++ {
		if _, _, err := s.AuthenticateBearerFrom(ctx, "tg_invalidtokenvalue123456", "203.0.113.10:4444"); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("attempt %d: expected ErrUnauthenticated, got %v", i, err)
		}
	}
	if _, _, err := s.AuthenticateBearerFrom(ctx, "tg_invalidtokenvalue123456", "203.0.113.10:5555"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited after repeated invalid bearer tokens, got %v", err)
	}
	if err := s.DeleteUser(ctx, setup.User.ID, viewer.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, setup.User.ID, member.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTOTPEnrollConfirmLoginReplayRecoveryAndReset(t *testing.T) {
	ctx := context.Background()
	s, clk, setup := newSetupService(t, nil)

	// Align fake clock cleanly to step start.
	clk.Advance(30 * time.Second)

	enroll, err := s.BeginTOTPEnrollment(ctx, setup.User.ID)
	if err != nil {
		t.Fatalf("BeginTOTPEnrollment: %v", err)
	}
	if enroll.Secret == "" || !strings.HasPrefix(enroll.OTPAuthURL, "otpauth://totp/") {
		t.Fatalf("unexpected enrollment: %+v", enroll)
	}

	// Confirming with an invalid code must fail.
	_, err = s.ConfirmTOTPEnrollment(ctx, setup.User.ID, "000000")
	wantErr(t, err, ErrInvalidTOTP)

	// Confirm with valid RFC 6238 code succeeds and issues 8 recovery codes.
	code1, err := GenerateTOTPCode(enroll.Secret, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := s.ConfirmTOTPEnrollment(ctx, setup.User.ID, code1)
	if err != nil {
		t.Fatalf("ConfirmTOTPEnrollment: %v", err)
	}
	if len(recovery) != 8 {
		t.Fatalf("recovery codes len = %d, want 8", len(recovery))
	}
	if _, err := s.BeginTOTPEnrollment(ctx, setup.User.ID); !errors.Is(err, ErrTOTPAlreadyEnabled) {
		t.Fatalf("re-enroll while enabled = %v, want ErrTOTPAlreadyEnabled", err)
	}

	// Password-only login now requires TOTP.
	_, err = login(s, testAdmin, testPassword, testAddr)
	wantErr(t, err, ErrTOTPRequired)

	// Wrong TOTP fails.
	_, err = s.Login(ctx, LoginRequest{
		Username:   testAdmin,
		Password:   testPassword,
		TOTPCode:   "999999",
		RemoteAddr: testAddr,
		UserAgent:  "test",
	})
	wantErr(t, err, ErrInvalidTOTP)

	// Same step used for enrollment cannot be replayed.
	_, err = s.Login(ctx, LoginRequest{
		Username:   testAdmin,
		Password:   testPassword,
		TOTPCode:   code1,
		RemoteAddr: testAddr,
		UserAgent:  "test",
	})
	wantErr(t, err, ErrInvalidTOTP)

	// Advancing one step (+30s) allows next valid TOTP login, then blocks replay on that step.
	clk.Advance(30 * time.Second)
	code2, err := GenerateTOTPCode(enroll.Secret, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, LoginRequest{
		Username:   testAdmin,
		Password:   testPassword,
		TOTPCode:   code2,
		RemoteAddr: testAddr,
		UserAgent:  "test",
	}); err != nil {
		t.Fatalf("login with step2 TOTP: %v", err)
	}
	_, err = s.Login(ctx, LoginRequest{
		Username:   testAdmin,
		Password:   testPassword,
		TOTPCode:   code2,
		RemoteAddr: testAddr,
		UserAgent:  "test",
	})
	wantErr(t, err, ErrInvalidTOTP)

	// Recovery code works once and is burned afterwards.
	if _, err := s.Login(ctx, LoginRequest{
		Username:   testAdmin,
		Password:   testPassword,
		TOTPCode:   recovery[0],
		RemoteAddr: testAddr,
		UserAgent:  "test",
	}); err != nil {
		t.Fatalf("login with recovery code: %v", err)
	}
	_, err = s.Login(ctx, LoginRequest{
		Username:   testAdmin,
		Password:   testPassword,
		TOTPCode:   recovery[0],
		RemoteAddr: testAddr,
		UserAgent:  "test",
	})
	wantErr(t, err, ErrInvalidTOTP)

	// Wrong password cannot disable TOTP.
	err = s.DisableTOTP(ctx, setup.User.ID, "wrong password!", recovery[1], testAddr)
	wantErr(t, err, ErrIncorrectPassword)

	// AdminResetTOTP clears 2FA so password-only login works again.
	if err := s.AdminResetTOTP(ctx, setup.User.ID); err != nil {
		t.Fatalf("AdminResetTOTP: %v", err)
	}
	uAfter, err := s.User(ctx, setup.User.ID)
	if err != nil || uAfter.TOTPEnabled {
		t.Fatalf("expected TOTPEnabled=false after reset, got %+v err=%v", uAfter, err)
	}
	if _, err := login(s, testAdmin, testPassword, testAddr); err != nil {
		t.Fatalf("password login after TOTP reset: %v", err)
	}
}

func TestTokenScopesAndProjectACL(t *testing.T) {
	ctx := context.Background()
	s, _, setup := newSetupService(t, nil)

	member, err := s.CreateUser(ctx, CreateUserRequest{
		Username: "acl_member",
		Password: "acl-member-password-1",
		Role:     RoleMember,
	})
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := s.CreateUser(ctx, CreateUserRequest{
		Username: "acl_viewer",
		Password: "acl-viewer-password-1",
		Role:     RoleViewer,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Unknown scope or non-admin requesting "admin" scope is rejected.
	if _, _, err := s.CreateAPIToken(ctx, CreateTokenRequest{
		UserID: member.ID,
		Name:   "bad-scope",
		Scopes: []string{"bogus"},
	}); !errors.Is(err, ErrInvalidTokenInput) {
		t.Fatalf("unknown scope err = %v, want ErrInvalidTokenInput", err)
	}
	if _, _, err := s.CreateAPIToken(ctx, CreateTokenRequest{
		UserID: member.ID,
		Name:   "non-admin-escalate",
		Scopes: []string{"admin"},
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin admin scope err = %v, want ErrForbidden", err)
	}

	// Valid scoped & project-bound token persists and enforces CheckScope.
	tokMeta, rawSecret, err := s.CreateAPIToken(ctx, CreateTokenRequest{
		UserID:     member.ID,
		Name:       "mcp-reader",
		Scopes:     []string{"mcp", "read", "mcp"},
		ProjectIDs: []string{"proj_a"},
	})
	if err != nil {
		t.Fatalf("CreateAPIToken(scoped): %v", err)
	}
	if len(tokMeta.Scopes) != 2 || tokMeta.Scopes[0] != "mcp" || tokMeta.Scopes[1] != "read" {
		t.Fatalf("unexpected normalized Scopes: %+v", tokMeta.Scopes)
	}
	authU, authTok, err := s.AuthenticateBearerFrom(ctx, rawSecret, testAddr)
	if err != nil {
		t.Fatalf("AuthenticateBearerFrom: %v", err)
	}
	tokCtx := WithAPIToken(ctx, authU, authTok)
	if err := CheckScope(tokCtx, "read"); err != nil {
		t.Fatalf("CheckScope(read) = %v", err)
	}
	if err := CheckScope(tokCtx, "write"); !errors.Is(err, ErrScopeForbidden) {
		t.Fatalf("CheckScope(write) = %v, want ErrScopeForbidden", err)
	}

	// Project ACL resolution: restricted project isolates unlisted member/viewer,
	// grants explicit viewer->member elevation, respects admin RawRole under narrowed token,
	// and respects token project_ids restriction.
	acl, err := s.SetProjectACL(ctx, "proj_secret", true, []ProjectMember{
		{UserID: viewer.ID, Role: RoleMember},
	})
	if err != nil {
		t.Fatalf("SetProjectACL: %v", err)
	}
	if !acl.Restricted || len(acl.Members) != 1 || acl.Members[0].Role != RoleMember {
		t.Fatalf("unexpected ACL: %+v", acl)
	}

	if role, ok, err := s.ResolveProjectRole(ctx, member, nil, "proj_secret"); err != nil || ok || role != "" {
		t.Fatalf("unlisted member on restricted project = (%q, %v, %v), want denied", role, ok, err)
	}
	if role, ok, err := s.ResolveProjectRole(ctx, viewer, nil, "proj_secret"); err != nil || !ok || role != RoleMember {
		t.Fatalf("elevated viewer on restricted project = (%q, %v, %v), want member/true", role, ok, err)
	}

	// Admin with a viewer-capped bearer token resolves RawRole=admin capped to viewer.
	_, adminViewerSecret, err := s.CreateAPIToken(ctx, CreateTokenRequest{
		UserID:     setup.User.ID,
		Name:       "admin-ro",
		Role:       RoleViewer,
		ProjectIDs: []string{"proj_secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	adminU, adminTok, err := s.AuthenticateBearerFrom(ctx, adminViewerSecret, testAddr)
	if err != nil {
		t.Fatal(err)
	}
	if role, ok, err := s.ResolveProjectRole(ctx, adminU, adminTok, "proj_secret"); err != nil || !ok || role != RoleViewer {
		t.Fatalf("admin viewer token on restricted project = (%q, %v, %v), want viewer/true", role, ok, err)
	}
	if role, ok, err := s.ResolveProjectRole(ctx, adminU, adminTok, "proj_other"); err != nil || ok || role != "" {
		t.Fatalf("token excluding proj_other = (%q, %v, %v), want denied", role, ok, err)
	}
}
