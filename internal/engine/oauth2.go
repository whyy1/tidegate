package engine

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"tidegate/internal/scenario"
)

type oauth2Cache struct {
	client  *http.Client
	timeout time.Duration
	entries sync.Map // cacheKey -> *oauth2Entry
}

type oauth2Entry struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func newOAuth2Cache(cfg *scenario.HTTPConfig, d *dialer) *oauth2Cache {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.InsecureSkipVerify,
		},
		MaxIdleConns:    32,
		IdleConnTimeout: 30 * time.Second,
	}
	if d != nil {
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			o, err := scenario.NewOrigin("http", addr)
			if err != nil {
				return net.Dial(network, addr)
			}
			var dt dialTiming
			return d.dialTCP(ctx, o, &dt)
		}
	}
	if cfg.ProxyURL != nil {
		tr.Proxy = http.ProxyURL(cfg.ProxyURL)
	}
	to := cfg.TotalTimeout
	if to <= 0 || to > 15*time.Second {
		to = 15 * time.Second
	}
	return &oauth2Cache{
		timeout: to,
		client:  &http.Client{Transport: tr},
	}
}

func (c *oauth2Cache) close() {
	if c != nil && c.client != nil {
		c.client.CloseIdleConnections()
	}
}

func (c *oauth2Cache) token(ctx context.Context, plan *scenario.OAuth2Plan, env *scenario.Env) (string, error) {
	if plan == nil {
		return "", nil
	}
	var buf []byte
	render := func(t *scenario.Template) string {
		if t == nil {
			return ""
		}
		if t.Static() {
			return t.Literal()
		}
		buf = t.Append(buf[:0], env)
		return string(buf)
	}
	tokenURL := strings.TrimSpace(render(plan.TokenURL))
	clientID := render(plan.ClientID)
	clientSecret := render(plan.ClientSecret)
	scope := strings.TrimSpace(render(plan.Scope))
	clientAuth := plan.ClientAuth
	if clientAuth == "" {
		clientAuth = "basic"
	}

	key := clientAuth + "\x00" + tokenURL + "\x00" + clientID + "\x00" + clientSecret + "\x00" + scope
	raw, _ := c.entries.LoadOrStore(key, &oauth2Entry{})
	entry := raw.(*oauth2Entry)

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.token != "" && (entry.expiresAt.IsZero() || time.Until(entry.expiresAt) > 10*time.Second) {
		return entry.token, nil
	}

	tok, ttl, err := c.fetchToken(ctx, tokenURL, clientID, clientSecret, scope, clientAuth)
	if err != nil {
		return "", err
	}
	entry.token = tok
	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	} else {
		entry.expiresAt = time.Now().Add(5 * time.Minute)
	}
	return tok, nil
}

func (c *oauth2Cache) fetchToken(ctx context.Context, tokenURL, clientID, clientSecret, scope, clientAuth string) (string, time.Duration, error) {
	if tokenURL == "" || clientID == "" {
		return "", 0, fmt.Errorf("OAuth 2.0 缺少 token_url 或 client_id")
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	if scope != "" {
		form.Set("scope", scope)
	}
	if clientAuth == "body" {
		form.Set("client_id", clientID)
		if clientSecret != "" {
			form.Set("client_secret", clientSecret)
		}
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("构造 OAuth 2.0 请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if clientAuth != "body" {
		req.SetBasicAuth(clientID, clientSecret)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("请求令牌端点失败：%w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, fmt.Errorf("读取令牌响应失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("令牌端点返回 HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", 0, fmt.Errorf("解析令牌 JSON 失败：%w", err)
	}
	if parsed.Error != "" {
		return "", 0, fmt.Errorf("令牌错误 %s：%s", parsed.Error, parsed.ErrorDesc)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return "", 0, fmt.Errorf("令牌响应缺少 access_token")
	}
	return parsed.AccessToken, time.Duration(parsed.ExpiresIn) * time.Second, nil
}
