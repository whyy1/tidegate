package auth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionCookieFlags(t *testing.T) {
	exp := time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC)
	for _, secure := range []bool{false, true} {
		rec := httptest.NewRecorder()
		http.SetCookie(rec, NewSessionCookie("tok", exp, secure))
		got := rec.Header().Get("Set-Cookie")
		for _, part := range []string{"tidegate_session=tok", "Path=/", "HttpOnly", "SameSite=Strict", "Expires=Sun, 08 Mar 2026 09:00:00 GMT"} {
			if !strings.Contains(got, part) {
				t.Errorf("secure=%v: %q lacks %q", secure, got, part)
			}
		}
		if strings.Contains(got, "Secure") != secure {
			t.Errorf("secure=%v: %q has the wrong Secure flag", secure, got)
		}
		if strings.Contains(got, "Domain") {
			t.Errorf("cookie must be host-only: %q", got)
		}
	}

	rec := httptest.NewRecorder()
	ClearSessionCookie(rec, true)
	got := rec.Header().Get("Set-Cookie")
	for _, part := range []string{"tidegate_session=", "Max-Age=0", "HttpOnly", "SameSite=Strict", "Secure", "Path=/"} {
		if !strings.Contains(got, part) {
			t.Errorf("clear cookie %q lacks %q", got, part)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if SessionToken(r) != "" {
		t.Fatal("token from a request without cookie")
	}
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "abc"})
	if SessionToken(r) != "abc" {
		t.Fatal("token not read from cookie")
	}
}

func TestSecureCookies(t *testing.T) {
	g, _ := NewGuard(nil, GuardConfig{})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if g.SecureCookies(r) {
		t.Fatal("plain HTTP request marked secure")
	}
	r.TLS = &tls.ConnectionState{}
	if !g.SecureCookies(r) {
		t.Fatal("TLS request not marked secure")
	}
	g, _ = NewGuard(nil, GuardConfig{TLS: true})
	if !g.SecureCookies(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("TLS server not marked secure")
	}
}

// errorCode decodes the code of a JSON error response.
func errorCode(t *testing.T, body io.Reader) Code {
	t.Helper()
	var b errorBody
	if err := json.NewDecoder(body).Decode(&b); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if b.Error.Message == "" {
		t.Fatal("error body without message")
	}
	return b.Error.Code
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

func TestCSRFGuard(t *testing.T) {
	g, err := NewGuard(nil, GuardConfig{AllowedOrigins: []string{"https://tidegate.example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	h := g.CSRF(okHandler)

	cases := []struct {
		name    string
		method  string
		host    string
		headers map[string]string
		want    int
		code    Code
	}{
		{"get needs nothing", "GET", "127.0.0.1:8080", nil, 204, ""},
		{"head needs nothing", "HEAD", "127.0.0.1:8080", map[string]string{"Origin": "https://evil.example"}, 204, ""},
		{"missing header", "POST", "127.0.0.1:8080", map[string]string{"Origin": "http://127.0.0.1:8080"}, 403, CodeCSRFHeaderMissing},
		{"wrong header value", "POST", "127.0.0.1:8080", map[string]string{"Origin": "http://127.0.0.1:8080", RequestHeader: "true"}, 403, CodeCSRFHeaderMissing},
		{"bad origin", "POST", "127.0.0.1:8080", map[string]string{"Origin": "https://evil.example", RequestHeader: "1"}, 403, CodeOriginNotAllowed},
		{"other port", "DELETE", "127.0.0.1:8080", map[string]string{"Origin": "http://127.0.0.1:9090", RequestHeader: "1"}, 403, CodeOriginNotAllowed},
		{"null origin", "PUT", "127.0.0.1:8080", map[string]string{"Origin": "null", RequestHeader: "1"}, 403, CodeOriginNotAllowed},
		{"origin with path", "POST", "127.0.0.1:8080", map[string]string{"Origin": "http://127.0.0.1:8080/x", RequestHeader: "1"}, 403, CodeOriginNotAllowed},
		{"cross-site fetch without origin", "POST", "127.0.0.1:8080", map[string]string{"Sec-Fetch-Site": "cross-site", RequestHeader: "1"}, 403, CodeOriginNotAllowed},
		{"good request", "POST", "127.0.0.1:8080", map[string]string{"Origin": "http://127.0.0.1:8080", RequestHeader: "1"}, 204, ""},
		{"case-insensitive host", "PATCH", "LocalHost:8080", map[string]string{"Origin": "http://localhost:8080", RequestHeader: "1"}, 204, ""},
		{"default port", "POST", "tidegate.lan:443", map[string]string{"Origin": "https://tidegate.lan", RequestHeader: "1"}, 204, ""},
		{"ipv6 host", "POST", "[::1]:8080", map[string]string{"Origin": "http://[::1]:8080", RequestHeader: "1"}, 204, ""},
		{"allow-listed origin", "POST", "10.0.0.5:8080", map[string]string{"Origin": "https://tidegate.example.com", RequestHeader: "1"}, 204, ""},
		{"non-browser client", "POST", "127.0.0.1:8080", map[string]string{RequestHeader: "1"}, 204, ""},
		{"same-origin fetch without origin", "POST", "127.0.0.1:8080", map[string]string{"Sec-Fetch-Site": "same-origin", RequestHeader: "1"}, 204, ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "http://"+c.host+"/api/v1/x", nil)
		r.Host = c.host
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.want)
			continue
		}
		if c.code != "" {
			if got := errorCode(t, rec.Body); got != c.code {
				t.Errorf("%s: code %q, want %q", c.name, got, c.code)
			}
		}
	}

	if _, err := NewGuard(nil, GuardConfig{AllowedOrigins: []string{"tidegate.example.com"}}); err == nil {
		t.Fatal("origin without scheme accepted")
	}
	if _, err := NewGuard(nil, GuardConfig{AllowedOrigins: []string{"https://x.example/path"}}); err == nil {
		t.Fatal("origin with path accepted")
	}
}

func TestWebSocketOrigin(t *testing.T) {
	g, _ := NewGuard(nil, GuardConfig{AllowedOrigins: []string{"https://ui.example.com"}})
	req := func(origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/ws", nil)
		r.Host = "127.0.0.1:8080"
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	for origin, ok := range map[string]bool{
		"http://127.0.0.1:8080":  true,
		"https://ui.example.com": true,
		"":                       true,
		"http://evil.example":    false,
		"http://127.0.0.1:8081":  false,
		"null":                   false,
	} {
		err := g.CheckWebSocketOrigin(req(origin))
		if (err == nil) != ok {
			t.Errorf("origin %q: err = %v, want ok=%v", origin, err, ok)
		}
		if err != nil && !errors.Is(err, ErrOriginNotAllowed) {
			t.Errorf("origin %q: wrong error %v", origin, err)
		}
	}
}

func TestLoopbackGuard(t *testing.T) {
	plain, _ := NewGuard(nil, GuardConfig{})
	withTLS, _ := NewGuard(nil, GuardConfig{TLS: true})
	allowed, _ := NewGuard(nil, GuardConfig{AllowInsecureRemote: true})

	req := func(remote string, tlsConn bool) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		r.RemoteAddr = remote
		if tlsConn {
			r.TLS = &tls.ConnectionState{}
		}
		return r
	}
	cases := []struct {
		name   string
		g      *Guard
		remote string
		tls    bool
		ok     bool
	}{
		{"ipv4 loopback", plain, "127.0.0.1:5000", false, true},
		{"other loopback ip", plain, "127.8.9.10:5000", false, true},
		{"ipv6 loopback", plain, "[::1]:5000", false, true},
		{"mapped loopback", plain, "[::ffff:127.0.0.1]:5000", false, true},
		{"lan client", plain, "192.168.1.20:5000", false, false},
		{"ipv6 client", plain, "[2001:db8::1]:5000", false, false},
		{"garbage address", plain, "somewhere", false, false},
		{"lan client over tls conn", plain, "192.168.1.20:5000", true, true},
		{"lan client, tls server", withTLS, "192.168.1.20:5000", false, true},
		{"lan client, insecure allowed", allowed, "192.168.1.20:5000", false, true},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		c.g.RequireSecureTransport(okHandler).ServeHTTP(rec, req(c.remote, c.tls))
		if ok := rec.Code == http.StatusNoContent; ok != c.ok {
			t.Errorf("%s: status %d, want ok=%v", c.name, rec.Code, c.ok)
			continue
		}
		if !c.ok {
			if rec.Code != http.StatusForbidden || errorCode(t, rec.Body) != CodeInsecureTransport {
				t.Errorf("%s: wrong rejection", c.name)
			}
		}
	}
	// A spoofed forwarding header does not make a remote client local.
	r := req("192.168.1.20:5000", false)
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	if plain.CheckTransport(r) == nil {
		t.Fatal("X-Forwarded-For trusted")
	}
}

func TestRequireSession(t *testing.T) {
	s, clk, res := newSetupService(t, nil)
	g, _ := NewGuard(s, GuardConfig{})
	h := g.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		sess, ok2 := SessionFromContext(r.Context())
		if !ok || !ok2 || u.Username != testAdmin || sess.ID != res.Session.ID {
			t.Errorf("context lacks user or session: %+v %+v", u, sess)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	serve := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	rec := serve("")
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec.Body) != CodeUnauthenticated {
		t.Fatalf("no cookie: %d", rec.Code)
	}
	if rec := serve(res.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("valid session: %d %s", rec.Code, rec.Body)
	}
	clk.Advance(DefaultIdleTimeout)
	rec = serve(res.Token)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec.Body) != CodeSessionExpired {
		t.Fatalf("expired session: %d", rec.Code)
	}
	if c := rec.Header().Get("Set-Cookie"); !strings.Contains(c, "Max-Age=0") {
		t.Fatalf("stale cookie not cleared: %q", c)
	}

	if _, ok := UserFromContext(context.Background()); ok {
		t.Fatal("user found in an empty context")
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, rateLimited(1500*time.Millisecond))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("status %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatal("not JSON")
	}
	var b errorBody
	json.NewDecoder(rec.Body).Decode(&b)
	if b.Error.Code != CodeRateLimited || b.Error.Message != "尝试次数过多，请 2 秒后再试" {
		t.Fatalf("body %+v", b)
	}

	rec = httptest.NewRecorder()
	WriteError(rec, errors.New("disk I/O error at /secret/path"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("internal error leaked: %d %s", rec.Code, rec.Body)
	}
	if HTTPStatus(ErrSetupDone) != http.StatusConflict || HTTPStatus(errors.New("x")) != 500 {
		t.Fatal("HTTPStatus mapping")
	}
	if got := formatWait(10 * time.Minute); got != " 10 分钟" {
		t.Fatalf("formatWait = %q", got)
	}
}

// TestHTTPFlow wires the guards the way the API layer is expected to and
// drives them through a real HTTP client.
func TestHTTPFlow(t *testing.T) {
	// The cookie jar checks Expires against the wall clock.
	s, _, _ := newSetupService(t, func(o *Options) { o.Now = time.Now })
	g, _ := NewGuard(s, GuardConfig{})
	mux := http.NewServeMux()
	mux.Handle("POST /login", g.CSRF(g.RequireSecureTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		res, err := s.Login(r.Context(), LoginRequest{Username: in.Username, Password: in.Password, RemoteAddr: r.RemoteAddr, UserAgent: r.UserAgent()})
		if err != nil {
			WriteError(w, err)
			return
		}
		SetSessionCookie(w, res, g.SecureCookies(r))
		w.WriteHeader(http.StatusNoContent)
	}))))
	mux.Handle("GET /me", g.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := UserFromContext(r.Context())
		io.WriteString(w, u.Username)
	})))
	mux.Handle("POST /logout", g.CSRF(g.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = s.Logout(r.Context(), SessionToken(r))
		ClearSessionCookie(w, g.SecureCookies(r))
		w.WriteHeader(http.StatusNoContent)
	}))))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	post := func(path, body string, csrf bool) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Origin", srv.URL)
		if csrf {
			req.Header.Set(RequestHeader, "1")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	get := func(path string) (int, string) {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	body := `{"Username":"admin","Password":"` + testPassword + `"}`
	if resp := post("/login", body, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("login without CSRF header: %d", resp.StatusCode)
	}
	if code, _ := get("/me"); code != http.StatusUnauthorized {
		t.Fatalf("/me before login: %d", code)
	}
	if resp := post("/login", body, true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	if code, name := get("/me"); code != http.StatusOK || name != testAdmin {
		t.Fatalf("/me after login: %d %q", code, name)
	}
	if resp := post("/logout", "", true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if code, _ := get("/me"); code != http.StatusUnauthorized {
		t.Fatalf("/me after logout: %d", code)
	}
}

func TestBearerTokenAndCSRFBypass(t *testing.T) {
	ctx := context.Background()
	s, _, setup := newSetupService(t, nil)
	g, err := NewGuard(s, GuardConfig{})
	if err != nil {
		t.Fatal(err)
	}
	_, bearer, err := s.CreateAPIToken(ctx, CreateTokenRequest{
		UserID: setup.User.ID,
		Name:   "ci",
		Role:   RoleMember,
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := g.CSRF(g.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		tok, hasTok := TokenFromContext(r.Context())
		if !ok || !hasTok || u.Role != RoleMember || tok.Name != "ci" {
			t.Errorf("unexpected context principal u=%+v tok=%+v", u, tok)
		}
		w.WriteHeader(http.StatusNoContent)
	})))

	// Cookie POST without X-Tidegate-Request still fails CSRF check.
	cookieReq := httptest.NewRequest(http.MethodPost, "/api/v1/projects", nil)
	cookieReq.AddCookie(&http.Cookie{Name: CookieName, Value: setup.Token})
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, cookieReq)
	if rec1.Code != http.StatusForbidden || errorCode(t, rec1.Body) != CodeCSRFHeaderMissing {
		t.Fatalf("cookie POST without CSRF header got %d (%s)", rec1.Code, rec1.Body.String())
	}

	// Bearer POST succeeds without X-Tidegate-Request or Origin header.
	bearerReq := httptest.NewRequest(http.MethodPost, "/api/v1/projects", nil)
	bearerReq.Header.Set("Authorization", "Bearer "+bearer)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, bearerReq)
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("bearer POST got %d (%s)", rec2.Code, rec2.Body.String())
	}

	// Invalid bearer token returns 401 and does not set clearing cookie.
	badReq := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	badReq.Header.Set("Authorization", "Bearer tg_invalidtokenvaluehere0000")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, badReq)
	if rec3.Code != http.StatusUnauthorized || rec3.Header().Get("Set-Cookie") != "" {
		t.Fatalf("bad bearer got %d cookie=%q", rec3.Code, rec3.Header().Get("Set-Cookie"))
	}
}

func TestTrustedProxiesXFFAndForwardedProto(t *testing.T) {
	g, err := NewGuard(nil, GuardConfig{
		TrustedProxies: []string{"172.16.0.0/12", "loopback"},
		AllowedOrigins: []string{"https://tg.example.com"},
	})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	// 1. Untrusted peer spoofing forwarding headers must be ignored.
	spoof := httptest.NewRequest(http.MethodPost, "http://tg.example.com/api/v1/auth/login", nil)
	spoof.RemoteAddr = "198.51.100.44:9999"
	spoof.Header.Set("X-Forwarded-Proto", "https")
	spoof.Header.Set("X-Forwarded-For", "127.0.0.1")
	if g.IsSecureRequest(spoof) {
		t.Fatal("untrusted peer must not make request secure")
	}
	if err := g.CheckTransport(spoof); !errors.Is(err, ErrInsecureTransport) {
		t.Fatalf("untrusted plain HTTP spoof = %v, want ErrInsecureTransport", err)
	}
	if got := g.ClientAddr(spoof); got != "198.51.100.44:9999" {
		t.Fatalf("untrusted ClientAddr = %q, want peer addr", got)
	}

	// 2. Trusted Docker bridge proxy terminating TLS: honors X-Forwarded-Proto,
	// walks XFF right-to-left skipping trusted hops, and accepts X-Forwarded-Host origin.
	proxied := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/v1/auth/login", nil)
	proxied.Host = "127.0.0.1:8080"
	proxied.RemoteAddr = "172.18.0.2:41234"
	proxied.Header.Set("X-Forwarded-Proto", "https")
	proxied.Header.Set("X-Forwarded-Host", "proxy.internal.local")
	proxied.Header.Set("Origin", "https://proxy.internal.local")
	proxied.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.88, 172.17.0.9")

	if !g.IsSecureRequest(proxied) || !g.SecureCookies(proxied) {
		t.Fatal("trusted proxy with X-Forwarded-Proto=https must be secure")
	}
	if err := g.CheckTransport(proxied); err != nil {
		t.Fatalf("trusted proxy CheckTransport = %v, want nil", err)
	}
	if got := g.ClientAddr(proxied); got != "203.0.113.88" {
		t.Fatalf("trusted ClientAddr = %q, want 203.0.113.88", got)
	}
	if err := g.CheckOrigin(proxied); err != nil {
		t.Fatalf("trusted X-Forwarded-Host origin CheckOrigin = %v, want nil", err)
	}

	if _, err := NewGuard(nil, GuardConfig{TrustedProxies: []string{"not-an-ip"}}); err == nil {
		t.Fatal("expected error for invalid trusted proxy entry")
	}
}
