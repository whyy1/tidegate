package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tidegate/internal/auth"
	"tidegate/internal/controller"
	"tidegate/internal/importer"
	"tidegate/internal/store"
	"tidegate/internal/testserver"
)

type apiTestEnv struct {
	srv      *httptest.Server
	baseURL  string
	authSvc  *auth.Service
	setupCod string
	ctrl     *controller.Controller
	st       *store.Store
	rdb      *store.ResultDB
}

func newAPITestEnv(t *testing.T, allowNonLoopback bool) *apiTestEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenDB(filepath.Join(dir, "tidegate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := auth.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	rdb := store.NewResultDB(db)
	authSvc, err := auth.New(db, auth.Options{
		Argon2: auth.Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 8, KeyLen: 16},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := authSvc.IssueSetupCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	guard, err := auth.NewGuard(authSvc, auth.GuardConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctrl, err := controller.New(context.Background(), controller.Options{
		Store:               st,
		Results:             rdb,
		Version:             "1.0-api-test",
		ClusterListen:       "127.0.0.1:0",
		InProcessLocalAgent: true,
		StartDelay:          40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctrl.Close() })

	apiSrv, err := NewServer(Options{
		Controller:     ctrl,
		Store:          st,
		Results:        rdb,
		Auth:           authSvc,
		Guard:          guard,
		Version:        "1.0-api-test",
		ListenLoopback: !allowNonLoopback,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { apiSrv.Close() })

	hs := httptest.NewServer(apiSrv)
	t.Cleanup(hs.Close)
	return &apiTestEnv{
		srv:      hs,
		baseURL:  hs.URL,
		authSvc:  authSvc,
		setupCod: code,
		ctrl:     ctrl,
		st:       st,
		rdb:      rdb,
	}
}

func newCookieClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

func doJSONReq(t *testing.T, cl *http.Client, method, url string, body any, csrf bool) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf {
		req.Header.Set(auth.RequestHeader, "1")
	}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer resp.Body.Close()
	if dst == nil {
		return
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
}

// testWSClient implements a minimal masked RFC 6455 client for testing /api/v1/ws.
type testWSClient struct {
	conn net.Conn
	br   *bufio.Reader
}

func dialTestWS(t *testing.T, baseURL string, cookies []*http.Cookie, origin string) *testWSClient {
	t.Helper()
	addr := strings.TrimPrefix(baseURL, "http://")
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	var kb [16]byte
	_, _ = rand.Read(kb[:])
	secKey := base64.StdEncoding.EncodeToString(kb[:])

	var req strings.Builder
	req.WriteString("GET /api/v1/ws HTTP/1.1\r\n")
	req.WriteString("Host: " + addr + "\r\n")
	req.WriteString("Upgrade: websocket\r\n")
	req.WriteString("Connection: Upgrade\r\n")
	req.WriteString("Sec-WebSocket-Version: 13\r\n")
	req.WriteString("Sec-WebSocket-Key: " + secKey + "\r\n")
	if origin != "" {
		req.WriteString("Origin: " + origin + "\r\n")
	}
	if len(cookies) > 0 {
		var cs []string
		for _, c := range cookies {
			cs = append(cs, c.Name+"="+c.Value)
		}
		req.WriteString("Cookie: " + strings.Join(cs, "; ") + "\r\n")
	}
	req.WriteString("\r\n")
	if _, err := conn.Write([]byte(req.String())); err != nil {
		conn.Close()
		t.Fatal(err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		t.Fatalf("ws status = %d", resp.StatusCode)
	}
	sum := sha1.Sum([]byte(secKey + wsMagicGUID))
	wantAccept := base64.StdEncoding.EncodeToString(sum[:])
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != wantAccept {
		conn.Close()
		t.Fatalf("ws accept = %q, want %q", got, wantAccept)
	}
	return &testWSClient{conn: conn, br: br}
}

func (c *testWSClient) Close() { _ = c.conn.Close() }

func (c *testWSClient) SendJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var hdr [6]byte
	hdr[0] = 0x81 // FIN + text
	hdr[1] = 0x80 | byte(len(payload))
	_, _ = rand.Read(hdr[2:6])
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ hdr[2+i%4]
	}
	if _, err := c.conn.Write(hdr[:]); err != nil {
		return err
	}
	_, err = c.conn.Write(masked)
	return err
}

func (c *testWSClient) ReadText(timeout time.Duration) ([]byte, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		var hdr [2]byte
		if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
			return nil, err
		}
		op := hdr[0] & 0x0F
		plen := uint64(hdr[1] & 0x7F)
		switch plen {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(c.br, ext[:]); err != nil {
				return nil, err
			}
			plen = uint64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(c.br, ext[:]); err != nil {
				return nil, err
			}
			plen = binary.BigEndian.Uint64(ext[:])
		}
		payload := make([]byte, int(plen))
		if plen > 0 {
			if _, err := io.ReadFull(c.br, payload); err != nil {
				return nil, err
			}
		}
		if op == wsOpPing {
			continue
		}
		if op == wsOpText {
			return payload, nil
		}
		return nil, io.EOF
	}
}

func TestAPIAuthSecurityProjectsAndTools(t *testing.T) {
	env := newAPITestEnv(t, false)
	cl := newCookieClient(t)

	// Wait briefly until in-process local agent registers.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ag, _ := env.ctrl.Agents(context.Background()); len(ag) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 1. Setup status before init.
	resp := doJSONReq(t, cl, "GET", env.baseURL+"/api/v1/setup/status", nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("setup/status = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing security headers")
	}
	var stBody struct {
		SetupRequired bool   `json:"setup_required"`
		Version       string `json:"version"`
		TLS           bool   `json:"tls"`
	}
	decodeBody(t, resp, &stBody)
	if !stBody.SetupRequired || stBody.TLS {
		t.Fatalf("unexpected setup status: %+v", stBody)
	}

	// 2. Host check prevents DNS rebinding.
	badHostReq, _ := http.NewRequest("GET", env.baseURL+"/api/v1/setup/status", nil)
	badHostReq.Host = "evil.example.com"
	badResp, err := http.DefaultClient.Do(badHostReq)
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != 403 {
		t.Fatalf("expected 403 for untrusted Host, got %d", badResp.StatusCode)
	}

	// 3. CSRF header required on POST /setup.
	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/setup", map[string]string{
		"code": env.setupCod, "username": "admin", "password": "strong password 123",
	}, false)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("expected 403 without CSRF header, got %d", resp.StatusCode)
	}

	// 4. Perform setup -> creates user, logs in, auto-creates default project.
	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/setup", map[string]string{
		"code": env.setupCod, "username": "admin", "password": "strong password 123",
	}, true)
	if resp.StatusCode != 201 {
		t.Fatalf("setup = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify /auth/me & logout -> login.
	resp = doJSONReq(t, cl, "GET", env.baseURL+"/api/v1/auth/me", nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("auth/me = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/auth/logout", nil, true)
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	resp = doJSONReq(t, cl, "GET", env.baseURL+"/api/v1/projects", nil, false)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 after logout, got %d", resp.StatusCode)
	}
	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "strong password 123",
	}, true)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login = %d", resp.StatusCode)
	}

	// 5. Default project listed + partial PATCH project.
	resp = doJSONReq(t, cl, "GET", env.baseURL+"/api/v1/projects", nil, false)
	var projects []store.Project
	decodeBody(t, resp, &projects)
	if len(projects) != 1 || projects[0].Name != "默认项目" {
		t.Fatalf("expected default project, got %+v", projects)
	}
	pid := projects[0].ID

	resp = doJSONReq(t, cl, "PATCH", env.baseURL+"/api/v1/projects/"+pid, map[string]any{
		"description": "updated desc",
		"version":     projects[0].Version,
	}, true)
	if resp.StatusCode != 200 {
		t.Fatalf("patch project = %d", resp.StatusCode)
	}
	var updatedProj store.Project
	decodeBody(t, resp, &updatedProj)
	if updatedProj.Name != "默认项目" || updatedProj.Description != "updated desc" {
		t.Fatalf("patch project mismatch: %+v", updatedProj)
	}

	// 6. Folder + API CRUD + copy + move + resolve-preview + conflict 409.
	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/projects/"+pid+"/folders", map[string]any{
		"name":    "认证模块",
		"headers": []map[string]any{{"name": "X-Folder", "value": "1"}},
		"auth":    map[string]any{"type": "bearer", "token": "{{tok}}"},
	}, true)
	if resp.StatusCode != 201 {
		t.Fatalf("create folder = %d", resp.StatusCode)
	}
	var folder store.Folder
	decodeBody(t, resp, &folder)

	resp = doJSONReq(t, cl, "PATCH", env.baseURL+"/api/v1/projects/"+pid+"/folders/"+folder.ID, map[string]any{
		"name":    "用户认证",
		"version": folder.Version,
	}, true)
	if resp.StatusCode != 200 {
		t.Fatalf("patch folder = %d", resp.StatusCode)
	}
	decodeBody(t, resp, &folder)
	if folder.Auth == nil || len(folder.Headers) != 1 {
		t.Fatalf("patch folder unexpectedly wiped headers/auth: %+v", folder)
	}

	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/projects/"+pid+"/apis", map[string]any{
		"name":      "获取用户",
		"folder_id": folder.ID,
		"request": map[string]any{
			"method": "GET",
			"url":    "/users/:id",
			"path_params": []map[string]any{
				{"name": "id", "value": "99"},
			},
		},
	}, true)
	if resp.StatusCode != 201 {
		t.Fatalf("create api = %d", resp.StatusCode)
	}
	var apiObj store.API
	decodeBody(t, resp, &apiObj)

	// Stale version -> 409 with current_version.
	resp = doJSONReq(t, cl, "PUT", env.baseURL+"/api/v1/projects/"+pid+"/apis/"+apiObj.ID, map[string]any{
		"name":    "x",
		"version": "stale-ver",
		"request": apiObj.Request,
	}, true)
	var errBody apiErrorBody
	decodeBody(t, resp, &errBody)
	if resp.StatusCode != 409 || errBody.Error.Code != "conflict" || errBody.Error.CurrentVersion != apiObj.Version {
		t.Fatalf("expected 409 conflict with current version, got status=%d body=%+v", resp.StatusCode, errBody)
	}

	// Copy + resolve-preview + cURL parse & to-curl.
	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/projects/"+pid+"/apis/"+apiObj.ID+"/copy", nil, true)
	if resp.StatusCode != 201 {
		t.Fatalf("copy api = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/projects/"+pid+"/apis/resolve-preview", map[string]any{
		"api_id": apiObj.ID,
	}, true)
	if resp.StatusCode != 200 {
		t.Fatalf("resolve-preview = %d", resp.StatusCode)
	}
	var preview store.EffectivePreview
	decodeBody(t, resp, &preview)
	if preview.URL != "/users/99" || preview.EffectiveAuth.Source != "folder" {
		t.Fatalf("preview = %+v", preview)
	}

	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/tools/curl/parse", map[string]string{
		"command": `curl -X POST https://example.com/hook -H 'Content-Type: application/json' -d '{"a":1}'`,
	}, true)
	if resp.StatusCode != 200 {
		t.Fatalf("curl/parse = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/projects/"+pid+"/apis/to-curl", map[string]any{
		"api_id":         apiObj.ID,
		"mode":           "expanded",
		"shell":          "bash",
		"redact_secrets": true,
	}, true)
	if resp.StatusCode != 200 {
		t.Fatalf("to-curl = %d", resp.StatusCode)
	}
	var toCurlResp struct {
		Curl string `json:"curl"`
	}
	decodeBody(t, resp, &toCurlResp)
	if !strings.Contains(toCurlResp.Curl, "/users/99") {
		t.Fatalf("to-curl output = %q", toCurlResp.Curl)
	}

	// 7. Multipart CSV upload & preview.
	var mbuf bytes.Buffer
	mw := multipart.NewWriter(&mbuf)
	part, err := mw.CreateFormFile("file", "users.csv")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("name,role\nalice,admin\nbob,user\n"))
	_ = mw.Close()

	upReq, _ := http.NewRequest("POST", env.baseURL+"/api/v1/projects/"+pid+"/files", &mbuf)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upReq.Header.Set(auth.RequestHeader, "1")
	resp, err = cl.Do(upReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("upload file = %d", resp.StatusCode)
	}

	resp = doJSONReq(t, cl, "GET", env.baseURL+"/api/v1/projects/"+pid+"/files/users.csv/preview?rows=5", nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("preview file = %d", resp.StatusCode)
	}
	var fp store.FilePreview
	decodeBody(t, resp, &fp)
	if !fp.IsCSV || len(fp.Columns) != 2 || fp.TotalRows != 2 {
		t.Fatalf("file preview = %+v", fp)
	}
}

func TestAPIDebugDryRunWebSocketRunAndExport(t *testing.T) {
	tsrv, err := testserver.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tsrv.Start()
	defer tsrv.Close()

	env := newAPITestEnv(t, false)
	cl := newCookieClient(t)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ag, _ := env.ctrl.Agents(context.Background()); len(ag) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Setup and authenticate.
	resp := doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/setup", map[string]string{
		"code": env.setupCod, "username": "admin", "password": "strong password 123",
	}, true)
	if resp.StatusCode != 201 {
		t.Fatalf("setup = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doJSONReq(t, cl, "GET", env.baseURL+"/api/v1/projects", nil, false)
	var projects []store.Project
	decodeBody(t, resp, &projects)
	pid := projects[0].ID

	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/projects/"+pid+"/environments", map[string]any{
		"name":     "local",
		"base_url": tsrv.URL(),
	}, true)
	var envObj store.Environment
	decodeBody(t, resp, &envObj)

	// Single API draft debug + debug-history retrieval.
	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/projects/"+pid+"/debug", map[string]any{
		"environment_id": envObj.ID,
		"api": map[string]any{
			"name": "ping",
			"request": map[string]any{
				"method": "GET",
				"url":    "/ping",
			},
		},
	}, true)
	if resp.StatusCode != 200 {
		t.Fatalf("debug = %d", resp.StatusCode)
	}
	var dbgResp controller.DebugAPIResponse
	decodeBody(t, resp, &dbgResp)
	if !dbgResp.Result.Passed || dbgResp.RecordID == "" {
		t.Fatalf("debug resp = %+v", dbgResp)
	}

	resp = doJSONReq(t, cl, "GET", env.baseURL+"/api/v1/projects/"+pid+"/debug-history", nil, false)
	var hist []store.DebugRecord
	decodeBody(t, resp, &hist)
	if len(hist) != 1 || hist[0].ID != dbgResp.RecordID {
		t.Fatalf("debug history = %+v", hist)
	}

	// Connect WebSocket with authenticated cookies and matching Origin.
	cookies := cl.Jar.Cookies(mustParseURL(t, env.baseURL))
	ws := dialTestWS(t, env.baseURL, cookies, env.baseURL)
	defer ws.Close()

	// Initial frame should be "agents".
	rawInit, err := ws.ReadText(3 * time.Second)
	if err != nil {
		t.Fatalf("read initial ws frame: %v", err)
	}
	var initEv controller.Event
	if err := json.Unmarshal(rawInit, &initEv); err != nil || initEv.Type != controller.EventAgents || len(initEv.Agents) == 0 {
		t.Fatalf("unexpected initial ws event: %s err=%v", rawInit, err)
	}
	if err := ws.SendJSON(map[string]string{"type": "subscribe", "run_id": "*"}); err != nil {
		t.Fatal(err)
	}

	// Start a short API load test run via POST /projects/{pid}/runs.
	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/projects/"+pid+"/runs", map[string]any{
		"environment_id": envObj.ID,
		"api": map[string]any{
			"name": "load-ping",
			"request": map[string]any{
				"method": "GET",
				"url":    "/ping",
			},
		},
		"load": map[string]any{
			"mode":   "vus",
			"stages": []map[string]any{{"duration": "1200ms", "target": 2}},
		},
	}, true)
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("start run = %d: %s", resp.StatusCode, b)
	}
	var run store.Run
	decodeBody(t, resp, &run)

	gotSnap := false
	gotFinished := false
	wsDeadline := time.Now().Add(10 * time.Second)
	for !gotFinished && time.Now().Before(wsDeadline) {
		msg, err := ws.ReadText(5 * time.Second)
		if err != nil {
			t.Fatalf("ws read error: %v", err)
		}
		var ev controller.Event
		if err := json.Unmarshal(msg, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.RunID == run.ID && ev.Type == controller.EventSnapshot {
			gotSnap = true
		}
		if ev.RunID == run.ID && ev.Type == controller.EventRunStatus && ev.Status == store.RunFinished {
			gotFinished = true
		}
		if ev.RunID == run.ID && ev.Type == controller.EventRunStatus && ev.Status == store.RunFailed {
			t.Fatalf("run failed via ws: %s", ev.Error)
		}
	}
	if !gotFinished || !gotSnap {
		t.Fatalf("missing ws events: gotFinished=%v gotSnap=%v", gotFinished, gotSnap)
	}

	// Check GET /runs/{id}, /runs/{id}/snapshots, and /export?format=csv|json.
	resp = doJSONReq(t, cl, "GET", env.baseURL+"/api/v1/runs/"+run.ID, nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("get run = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doJSONReq(t, cl, "GET", env.baseURL+"/api/v1/runs/"+run.ID+"/export?format=csv", nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("export csv = %d", resp.StatusCode)
	}
	csvRows, err := csv.NewReader(resp.Body).ReadAll()
	resp.Body.Close()
	if err != nil || len(csvRows) < 2 || csvRows[0][0] != "seq" || csvRows[0][9] != "requests" {
		t.Fatalf("unexpected csv export: rows=%d err=%v header=%v", len(csvRows), err, csvRows)
	}

	resp = doJSONReq(t, cl, "GET", env.baseURL+"/api/v1/runs/"+run.ID+"/export?format=json", nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("export json = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Scenario draft dry-run via POST /projects/{pid}/scenarios/debug.
	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/projects/"+pid+"/apis", map[string]any{
		"name": "step-login",
		"request": map[string]any{
			"method": "POST",
			"url":    "/login",
			"body":   map[string]any{"type": "json", "content": `{"user":"a","pwd":"b"}`},
		},
		"extract": []map[string]any{{"name": "tk", "from": "jsonpath", "expr": "$.data.token"}},
	}, true)
	var loginAPI store.API
	decodeBody(t, resp, &loginAPI)

	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/projects/"+pid+"/apis", map[string]any{
		"name":    "step-items",
		"request": map[string]any{"method": "GET", "url": "/items"},
		"auth":    map[string]any{"type": "bearer", "token": "{{tk}}"},
	}, true)
	var itemsAPI store.API
	decodeBody(t, resp, &itemsAPI)

	resp = doJSONReq(t, cl, "POST", env.baseURL+"/api/v1/projects/"+pid+"/scenarios/debug", map[string]any{
		"environment_id": envObj.ID,
		"scenario": map[string]any{
			"name": "dry",
			"steps": []map[string]any{
				{"id": "s1", "api_id": loginAPI.ID},
				{"id": "s2", "api_id": itemsAPI.ID},
			},
		},
	}, true)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("scenarios/debug = %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
}

func TestSessionsImportExportAndHTMLReport(t *testing.T) {
	tsrv, err := testserver.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tsrv.Start()
	defer tsrv.Close()

	env := newAPITestEnv(t, false)
	cl1 := newCookieClient(t)
	cl2 := newCookieClient(t)

	agentDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(agentDeadline) {
		if ag, _ := env.ctrl.Agents(context.Background()); len(ag) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Setup first session on cl1.
	resp := doJSONReq(t, cl1, "POST", env.baseURL+"/api/v1/setup", map[string]string{
		"code": env.setupCod, "username": "admin", "password": "strong password 123",
	}, true)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("setup = %d", resp.StatusCode)
	}

	// Second login on cl2.
	resp = doJSONReq(t, cl2, "POST", env.baseURL+"/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "strong password 123",
	}, true)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login cl2 = %d", resp.StatusCode)
	}

	// 1. Session list & targeted revocation from cl1.
	resp = doJSONReq(t, cl1, "GET", env.baseURL+"/api/v1/auth/sessions", nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("list sessions = %d", resp.StatusCode)
	}
	var sessList struct {
		Sessions []sessionDTO `json:"sessions"`
	}
	decodeBody(t, resp, &sessList)
	if len(sessList.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessList.Sessions))
	}
	var currentCount int
	var otherID string
	for _, s := range sessList.Sessions {
		if s.Current {
			currentCount++
		} else {
			otherID = s.ID
		}
	}
	if currentCount != 1 || otherID == "" {
		t.Fatalf("unexpected session markers: %+v", sessList.Sessions)
	}

	resp = doJSONReq(t, cl1, "DELETE", env.baseURL+"/api/v1/auth/sessions/"+otherID, nil, true)
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("revoke other session = %d", resp.StatusCode)
	}

	// cl2 is now 401 while cl1 stays 200.
	resp = doJSONReq(t, cl2, "GET", env.baseURL+"/api/v1/projects", nil, false)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("expected revoked cl2 to get 401, got %d", resp.StatusCode)
	}
	resp = doJSONReq(t, cl1, "GET", env.baseURL+"/api/v1/projects", nil, false)
	var projects []store.Project
	decodeBody(t, resp, &projects)
	if len(projects) == 0 {
		t.Fatal("expected default project for cl1")
	}
	pid := projects[0].ID

	// 2. Two-step OpenAPI import: JSON preview (non-mutating) -> multipart apply.
	openAPISpec := `
openapi: 3.0.3
info:
  title: Catalog
servers:
  - url: ` + tsrv.URL() + `
tags:
  - name: Items
paths:
  /ping:
    get:
      summary: Fetch Ping
      tags: [Items]
`
	resp = doJSONReq(t, cl1, "POST", env.baseURL+"/api/v1/projects/"+pid+"/import/preview", map[string]any{
		"filename": "catalog.yaml",
		"content":  openAPISpec,
	}, true)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("import preview = %d: %s", resp.StatusCode, b)
	}
	var previewRes importer.PreviewResult
	decodeBody(t, resp, &previewRes)
	if previewRes.Report.APIsFound != 1 || len(previewRes.Diffs) != 1 || previewRes.Diffs[0].Action != importer.ActionCreate {
		t.Fatalf("unexpected import preview: %+v", previewRes)
	}

	// Ensure preview did not mutate project tree.
	resp = doJSONReq(t, cl1, "GET", env.baseURL+"/api/v1/projects/"+pid+"/tree", nil, false)
	var treeBefore store.Tree
	decodeBody(t, resp, &treeBefore)
	if len(treeBefore.Children) != 0 {
		t.Fatalf("preview unexpectedly mutated tree: %+v", treeBefore.Children)
	}

	// Apply via multipart/form-data upload.
	var mpBody bytes.Buffer
	mw := multipart.NewWriter(&mpBody)
	fw, err := mw.CreateFormFile("file", "catalog.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(openAPISpec)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	applyReq, _ := http.NewRequest("POST", env.baseURL+"/api/v1/projects/"+pid+"/import/apply", &mpBody)
	applyReq.Header.Set("Content-Type", mw.FormDataContentType())
	applyReq.Header.Set(auth.RequestHeader, "1")
	resp, err = cl1.Do(applyReq)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("import apply = %d: %s", resp.StatusCode, b)
	}
	var applyRes importer.ApplyResult
	decodeBody(t, resp, &applyRes)
	if applyRes.Report.APIsCreated != 1 || applyRes.Report.FoldersCreated != 1 {
		t.Fatalf("unexpected import apply report: %+v", applyRes.Report)
	}

	// 3. Export OpenAPI YAML & .tidegate archive, then re-import as ClonedProject.
	resp = doJSONReq(t, cl1, "GET", env.baseURL+"/api/v1/projects/"+pid+"/export?format=openapi-yaml", nil, false)
	yamlData, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(yamlData), "openapi: 3.0.3") || !strings.Contains(string(yamlData), "/ping:") {
		t.Fatalf("export openapi-yaml = %d: %s", resp.StatusCode, yamlData)
	}

	resp = doJSONReq(t, cl1, "GET", env.baseURL+"/api/v1/projects/"+pid+"/export?format=postman", nil, false)
	pmData, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(pmData), "collection/v2.1.0") {
		t.Fatalf("export postman = %d: %s", resp.StatusCode, pmData)
	}

	resp = doJSONReq(t, cl1, "GET", env.baseURL+"/api/v1/projects/"+pid+"/export?format=tidegate", nil, false)
	zipData, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(zipData) < 4 || zipData[0] != 'P' || zipData[1] != 'K' {
		t.Fatalf("export tidegate = %d len=%d", resp.StatusCode, len(zipData))
	}

	var impZipBody bytes.Buffer
	zmw := multipart.NewWriter(&impZipBody)
	_ = zmw.WriteField("name", "ClonedProject")
	zfw, err := zmw.CreateFormFile("file", "backup.tidegate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zfw.Write(zipData); err != nil {
		t.Fatal(err)
	}
	if err := zmw.Close(); err != nil {
		t.Fatal(err)
	}
	impReq, _ := http.NewRequest("POST", env.baseURL+"/api/v1/projects/import", &impZipBody)
	impReq.Header.Set("Content-Type", zmw.FormDataContentType())
	impReq.Header.Set(auth.RequestHeader, "1")
	resp, err = cl1.Do(impReq)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("import project archive = %d: %s", resp.StatusCode, b)
	}
	var importedOut struct {
		Project store.Project             `json:"project"`
		Report  importer.ConversionReport `json:"report"`
	}
	decodeBody(t, resp, &importedOut)
	if importedOut.Project.Name != "ClonedProject" || importedOut.Report.APIsCreated != 1 {
		t.Fatalf("unexpected project archive import result: %+v", importedOut)
	}

	resp = doJSONReq(t, cl1, "GET", env.baseURL+"/api/v1/projects/"+importedOut.Project.ID+"/tree", nil, false)
	var clonedTree store.Tree
	decodeBody(t, resp, &clonedTree)
	if len(clonedTree.Children) != 1 || clonedTree.Children[0].Name != "Items" {
		t.Fatalf("expected cloned folder tree, got %+v", clonedTree.Children)
	}

	// 4. Start & finish a real short run against testserver, then export standalone HTML report.
	resp = doJSONReq(t, cl1, "GET", env.baseURL+"/api/v1/projects/"+pid+"/environments", nil, false)
	var envs []store.Environment
	decodeBody(t, resp, &envs)
	if len(envs) == 0 {
		t.Fatal("expected imported environment")
	}

	apiID := ""
	resp = doJSONReq(t, cl1, "GET", env.baseURL+"/api/v1/projects/"+pid+"/tree", nil, false)
	var treeAfter store.Tree
	decodeBody(t, resp, &treeAfter)
	if len(treeAfter.Children) > 0 && len(treeAfter.Children[0].Children) > 0 {
		apiID = treeAfter.Children[0].Children[0].ID
	}
	if apiID == "" {
		t.Fatal("expected imported API in tree")
	}

	resp = doJSONReq(t, cl1, "POST", env.baseURL+"/api/v1/projects/"+pid+"/runs", map[string]any{
		"api_id":         apiID,
		"environment_id": envs[0].ID,
		"load": map[string]any{
			"mode":   "vus",
			"stages": []map[string]any{{"duration": "400ms", "target": 1}},
		},
	}, true)
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("start run = %d: %s", resp.StatusCode, b)
	}
	var run store.Run
	decodeBody(t, resp, &run)

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		rresp := doJSONReq(t, cl1, "GET", env.baseURL+"/api/v1/runs/"+run.ID, nil, false)
		var cur store.Run
		decodeBody(t, rresp, &cur)
		if !cur.Status.Active() {
			run = cur
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if run.Status != store.RunFinished {
		t.Fatalf("run ended with status=%s error=%q", run.Status, run.Error)
	}

	resp = doJSONReq(t, cl1, "GET", env.baseURL+"/api/v1/runs/"+run.ID+"/export?format=html", nil, false)
	htmlBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("export html = %d: %s", resp.StatusCode, htmlBytes)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("unexpected html Content-Type: %q", ct)
	}
	htmlStr := string(htmlBytes)
	if !strings.Contains(htmlStr, "<!DOCTYPE html>") || !strings.Contains(htmlStr, `id="tidegate-report-data"`) {
		t.Fatalf("HTML report missing expected structure")
	}

	resp = doJSONReq(t, cl1, "GET", env.baseURL+"/api/v1/runs/"+run.ID+"/export?format=har", nil, false)
	harBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(harBytes), `"version": "1.2"`) {
		t.Fatalf("export har = %d: %s", resp.StatusCode, harBytes)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
