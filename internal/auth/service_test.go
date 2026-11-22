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
	if v != "1" {
		t.Fatalf("schema version = %q, want 1", v)
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
