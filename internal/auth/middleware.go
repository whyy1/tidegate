package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
)

// RequestHeader must be sent with value "1" on every state-changing request.
// Browsers cannot add it to cross-site requests without a CORS preflight,
// which the server never approves.
const RequestHeader = "X-Tidegate-Request"

// GuardConfig configures the HTTP guards.
type GuardConfig struct {
	// TLS is true when the server terminates TLS itself. Requests with a
	// non-nil r.TLS are treated as secure in any case.
	TLS bool
	// AllowInsecureRemote permits login and setup over plain HTTP from
	// non-loopback clients.
	AllowInsecureRemote bool
	// AllowedOrigins are extra trusted origins such as
	// "https://tidegate.example.com", for deployments where the browser-facing
	// host differs from the Host header the server receives.
	AllowedOrigins []string
	// TrustedProxies lists reverse proxy IPs or CIDRs whose X-Forwarded-Proto,
	// X-Forwarded-Host and X-Forwarded-For headers are trusted.
	TrustedProxies []string
	// ErrorHandler writes guard failures. Defaults to WriteError.
	ErrorHandler func(w http.ResponseWriter, r *http.Request, err error)
}

// Guard provides net/http middleware around a Service.
type Guard struct {
	svc                 *Service
	tls                 bool
	allowInsecureRemote bool
	origins             map[string]struct{}
	trustedNets         []netip.Prefix
	onError             func(w http.ResponseWriter, r *http.Request, err error)
}

// NewGuard validates cfg and returns a Guard. svc may be nil if
// RequireSession is not used.
func NewGuard(svc *Service, cfg GuardConfig) (*Guard, error) {
	g := &Guard{
		svc:                 svc,
		tls:                 cfg.TLS,
		allowInsecureRemote: cfg.AllowInsecureRemote,
		origins:             make(map[string]struct{}, len(cfg.AllowedOrigins)),
		onError:             cfg.ErrorHandler,
	}
	for _, o := range cfg.AllowedOrigins {
		norm, _, ok := parseOrigin(strings.TrimSuffix(strings.TrimSpace(o), "/"))
		if !ok {
			return nil, fmt.Errorf("auth: invalid allowed origin %q: want scheme://host[:port]", o)
		}
		g.origins[norm] = struct{}{}
	}
	for _, raw := range cfg.TrustedProxies {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if strings.EqualFold(p, "loopback") {
			g.trustedNets = append(g.trustedNets,
				netip.MustParsePrefix("127.0.0.0/8"),
				netip.MustParsePrefix("::1/128"),
			)
			continue
		}
		if pref, err := netip.ParsePrefix(p); err == nil {
			g.trustedNets = append(g.trustedNets, pref.Masked())
			continue
		}
		if addr, err := netip.ParseAddr(p); err == nil {
			u := addr.Unmap()
			bits := 32
			if u.Is6() {
				bits = 128
			}
			g.trustedNets = append(g.trustedNets, netip.PrefixFrom(u, bits))
			continue
		}
		return nil, fmt.Errorf("auth: invalid trusted proxy %q: want IP, CIDR or \"loopback\"", raw)
	}
	if g.onError == nil {
		g.onError = func(w http.ResponseWriter, _ *http.Request, err error) { WriteError(w, err) }
	}
	return g, nil
}

func (g *Guard) isTrustedPeer(remoteAddr string) bool {
	if len(g.trustedNets) == 0 {
		return false
	}
	addr, err := netip.ParseAddr(hostOnly(remoteAddr))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range g.trustedNets {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// ClientAddr returns the client's effective address, honoring X-Forwarded-For /
// X-Real-IP only when the direct TCP peer matches TrustedProxies.
func (g *Guard) ClientAddr(r *http.Request) string {
	if r == nil {
		return ""
	}
	if !g.isTrustedPeer(r.RemoteAddr) {
		return r.RemoteAddr
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ipStr := strings.TrimSpace(parts[i])
			addr, err := netip.ParseAddr(hostOnly(ipStr))
			if err != nil {
				continue
			}
			u := addr.Unmap()
			trusted := false
			for _, p := range g.trustedNets {
				if p.Contains(u) {
					trusted = true
					break
				}
			}
			if !trusted {
				return u.String()
			}
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if addr, err := netip.ParseAddr(hostOnly(xri)); err == nil {
			return addr.Unmap().String()
		}
	}
	return r.RemoteAddr
}

// IsSecureRequest reports whether the request arrived directly over TLS or
// over HTTPS terminated at a configured trusted reverse proxy.
func (g *Guard) IsSecureRequest(r *http.Request) bool {
	if g.tls || (r != nil && r.TLS != nil) {
		return true
	}
	if r == nil || !g.isTrustedPeer(r.RemoteAddr) {
		return false
	}
	if xfp := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xfp != "" {
		first, _, _ := strings.Cut(xfp, ",")
		if strings.EqualFold(strings.TrimSpace(first), "https") {
			return true
		}
	}
	if fwd := r.Header.Get("Forwarded"); fwd != "" {
		for _, elem := range strings.Split(fwd, ",") {
			for _, param := range strings.Split(elem, ";") {
				k, v, ok := strings.Cut(strings.TrimSpace(param), "=")
				if ok && strings.EqualFold(strings.TrimSpace(k), "proto") {
					if strings.EqualFold(strings.Trim(strings.TrimSpace(v), `"`), "https") {
						return true
					}
				}
			}
		}
	}
	return false
}

// SecureCookies reports whether cookies for r should carry the Secure flag.
func (g *Guard) SecureCookies(r *http.Request) bool {
	return g.IsSecureRequest(r)
}

// ---------------------------------------------------------------------------
// Session

type ctxKey struct{}

type principal struct {
	user    *User
	session *Session
	token   *APIToken
}

// WithSession returns a context carrying the authenticated user and session.
func WithSession(ctx context.Context, u *User, s *Session) context.Context {
	return context.WithValue(ctx, ctxKey{}, principal{user: u, session: s})
}

// WithAPIToken returns a context carrying the user authenticated by an API token.
func WithAPIToken(ctx context.Context, u *User, t *APIToken) context.Context {
	return context.WithValue(ctx, ctxKey{}, principal{user: u, token: t})
}

// UserFromContext returns the user stored by RequireSession.
func UserFromContext(ctx context.Context) (*User, bool) {
	p, ok := ctx.Value(ctxKey{}).(principal)
	return p.user, ok && p.user != nil
}

// SessionFromContext returns the session stored by RequireSession.
func SessionFromContext(ctx context.Context) (*Session, bool) {
	p, ok := ctx.Value(ctxKey{}).(principal)
	return p.session, ok && p.session != nil
}

// TokenFromContext returns the API token stored by RequireSession when
// authenticated via Bearer token.
func TokenFromContext(ctx context.Context) (*APIToken, bool) {
	p, ok := ctx.Value(ctxKey{}).(principal)
	return p.token, ok && p.token != nil
}

// WithEffectiveRole returns a copy of ctx with the user's effective role narrowed
// to role (used after per-project ACL resolution).
func WithEffectiveRole(ctx context.Context, role Role) context.Context {
	p, ok := ctx.Value(ctxKey{}).(principal)
	if !ok || p.user == nil {
		return ctx
	}
	cp := *p.user
	cp.Role = role
	p.user = &cp
	return context.WithValue(ctx, ctxKey{}, p)
}

// CheckScope verifies that an API token (when present) includes scope. Cookie
// sessions and legacy unscoped tokens (len(tok.Scopes) == 0) are always allowed.
func CheckScope(ctx context.Context, scope string) error {
	tok, ok := TokenFromContext(ctx)
	if !ok || len(tok.Scopes) == 0 {
		return nil
	}
	if slices.Contains(tok.Scopes, scope) {
		return nil
	}
	return ErrScopeForbidden
}

// CheckMCPTool verifies that an API token restricted via MCPTools permits toolName.
// Cookie sessions and tokens with empty MCPTools are allowed based on scopes/RBAC.
func CheckMCPTool(ctx context.Context, toolName string) error {
	tok, ok := TokenFromContext(ctx)
	if !ok || len(tok.MCPTools) == 0 {
		return nil
	}
	want := strings.ToLower(strings.TrimSpace(toolName))
	if want == "" || slices.Contains(tok.MCPTools, want) {
		return nil
	}
	return &Error{
		Code:    CodeScopeForbidden,
		Status:  http.StatusForbidden,
		Message: fmt.Sprintf("当前令牌未授权调用 MCP 工具 %q", toolName),
	}
}

// BearerToken extracts the token from an "Authorization: Bearer <token>" header.
func BearerToken(r *http.Request) string {
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(authz) <= 7 || !strings.EqualFold(authz[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(authz[7:])
}

// RequireSession rejects requests without a valid session cookie or Bearer API
// token and stores the authenticated principal in the request context. A stale
// cookie is cleared.
func (g *Guard) RequireSession(next http.Handler) http.Handler {
	if g.svc == nil {
		panic("auth: RequireSession needs a Guard created with a Service")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bt := BearerToken(r); bt != "" {
			u, tok, err := g.svc.AuthenticateBearerFrom(r.Context(), bt, g.ClientAddr(r))
			if err != nil {
				if _, ok := AsError(err); !ok && !errors.Is(err, context.Canceled) {
					g.svc.log.Error("auth: authenticate bearer request", "err", err)
				}
				g.onError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithAPIToken(r.Context(), u, tok)))
			return
		}
		token := SessionToken(r)
		if token == "" {
			g.onError(w, r, ErrUnauthenticated)
			return
		}
		u, sess, err := g.svc.Authenticate(r.Context(), token)
		if err != nil {
			if _, ok := AsError(err); ok {
				ClearSessionCookie(w, g.SecureCookies(r))
			} else if !errors.Is(err, context.Canceled) {
				g.svc.log.Error("auth: authenticate request", "err", err)
			}
			g.onError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithSession(r.Context(), u, sess)))
	})
}

// ---------------------------------------------------------------------------
// CSRF and Origin

func isSafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

// CheckCSRF validates a state-changing request: Bearer-authenticated CLI/CI
// requests bypass browser CSRF checks, whereas cookie-based requests require
// RequestHeader=="1" and a trusted origin.
func (g *Guard) CheckCSRF(r *http.Request) error {
	if isSafeMethod(r.Method) {
		return nil
	}
	if BearerToken(r) != "" {
		return nil
	}
	if r.Header.Get(RequestHeader) != "1" {
		return ErrCSRFHeaderMissing
	}
	return g.CheckOrigin(r)
}

// CSRF rejects state-changing requests that fail CheckCSRF.
func (g *Guard) CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := g.CheckCSRF(r); err != nil {
			g.onError(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CheckOrigin accepts a request whose Origin header names the host the
// request was sent to, or one of the allowed origins. Without an Origin
// header (non-browser clients) the request passes unless Sec-Fetch-Site
// shows a browser sent it from another site.
func (g *Guard) CheckOrigin(r *http.Request) error {
	origin := r.Header.Get("Origin")
	if origin == "" {
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
			return nil
		}
		return ErrOriginNotAllowed
	}
	if g.originAllowed(r, origin) {
		return nil
	}
	return ErrOriginNotAllowed
}

// CheckWebSocketOrigin validates the Origin of a WebSocket handshake.
// Browsers always send Origin on WebSocket handshakes, so a missing header
// means a non-browser client, which cannot ride on a victim's cookies.
func (g *Guard) CheckWebSocketOrigin(r *http.Request) error {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	if g.originAllowed(r, origin) {
		return nil
	}
	return ErrOriginNotAllowed
}

func (g *Guard) originAllowed(r *http.Request, origin string) bool {
	norm, hostPort, ok := parseOrigin(origin)
	if !ok {
		return false
	}
	if _, ok := g.origins[norm]; ok {
		return true
	}
	scheme, _, _ := strings.Cut(norm, "://")
	if hostPort == requestHostPort(r.Host, scheme) {
		return true
	}
	if g.isTrustedPeer(r.RemoteAddr) {
		if fHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); fHost != "" {
			first, _, _ := strings.Cut(fHost, ",")
			if hostPort == requestHostPort(strings.TrimSpace(first), scheme) {
				return true
			}
		}
	}
	return false
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

// parseOrigin normalizes an origin to lower-case scheme://host[:port] with
// the default port removed. It also returns the host[:port] part.
func parseOrigin(s string) (norm, hostPort string, ok bool) {
	u, err := url.Parse(s)
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return "", "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", "", false
	}
	port := u.Port()
	if port == defaultPort(scheme) {
		port = ""
	}
	hostPort = joinHostPort(host, port)
	return scheme + "://" + hostPort, hostPort, true
}

// requestHostPort normalizes a Host header the same way, dropping the
// default port of the origin's scheme.
func requestHostPort(host, scheme string) string {
	host = strings.ToLower(host)
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		name, port = strings.Trim(host, "[]"), ""
	}
	if port == defaultPort(scheme) {
		port = ""
	}
	return joinHostPort(name, port)
}

func joinHostPort(host, port string) string {
	if port != "" {
		return net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// ---------------------------------------------------------------------------
// Transport

// IsLoopback reports whether remoteAddr (host:port or a bare IP) is a
// loopback address.
func IsLoopback(remoteAddr string) bool {
	a, err := netip.ParseAddr(hostOnly(remoteAddr))
	if err != nil {
		return false
	}
	return a.Unmap().IsLoopback()
}

// CheckTransport refuses requests that would send a password in clear text
// over the network: plain HTTP from a non-loopback client, unless the server
// runs TLS or was started with AllowInsecureRemote. The decision uses the
// TCP peer address only, never forwarding headers.
func (g *Guard) CheckTransport(r *http.Request) error {
	if g.IsSecureRequest(r) || g.allowInsecureRemote || IsLoopback(r.RemoteAddr) {
		return nil
	}
	return ErrInsecureTransport
}

// RequireSecureTransport applies CheckTransport. Wrap the login and setup
// handlers with it.
func (g *Guard) RequireSecureTransport(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := g.CheckTransport(r); err != nil {
			g.onError(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}
