package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// testParams keeps Argon2 cheap so the tests stay fast.
var testParams = Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

const (
	testAdmin    = "admin"
	testPassword = "correct horse battery"
	testAddr     = "127.0.0.1:50000"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	// A single connection makes any query issued while another result set
	// is still open deadlock, which surfaces such bugs in tests.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func newTestService(t *testing.T, db *sql.DB, clk *fakeClock, mod func(*Options)) *Service {
	t.Helper()
	opts := Options{Argon2: testParams, Now: clk.Now}
	if mod != nil {
		mod(&opts)
	}
	s, err := New(db, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// newSetupService returns a service whose first admin is testAdmin with
// testPassword.
func newSetupService(t *testing.T, mod func(*Options)) (*Service, *fakeClock, *LoginResult) {
	t.Helper()
	clk := newFakeClock()
	s := newTestService(t, openTestDB(t), clk, mod)
	res := mustSetup(t, s)
	return s, clk, res
}

func mustSetup(t *testing.T, s *Service) *LoginResult {
	t.Helper()
	ctx := context.Background()
	code, err := s.IssueSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Setup(ctx, SetupRequest{Code: code, Username: testAdmin, Password: testPassword, RemoteAddr: testAddr})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func login(s *Service, user, pass, addr string) (*LoginResult, error) {
	return s.Login(context.Background(), LoginRequest{Username: user, Password: pass, RemoteAddr: addr, UserAgent: "test"})
}

func mustLogin(t *testing.T, s *Service) *LoginResult {
	t.Helper()
	res, err := login(s, testAdmin, testPassword, testAddr)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}
