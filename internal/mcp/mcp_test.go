package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tidegate/internal/api"
	"tidegate/internal/auth"
	"tidegate/internal/controller"
	"tidegate/internal/mcp"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

type mcpTestEnv struct {
	srv         *httptest.Server
	ctrl        *controller.Controller
	st          *store.Store
	rdb         *store.ResultDB
	adminToken  string
	memberToken string
	viewerToken string
	project     *store.Project
	apiAllowed  *store.API
	apiBlocked  *store.API
	targetSrv   *httptest.Server
}

func setupMCPTestEnv(t *testing.T) *mcpTestEnv {
	t.Helper()
	ctx := context.Background()
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
	if err := auth.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	rdb := store.NewResultDB(db)
	box, err := store.LoadOrCreateMasterKey(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSecretVault(store.NewSecretVault(rdb, box))

	authSvc, err := auth.New(db, auth.Options{
		Argon2: auth.Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 8, KeyLen: 16},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := authSvc.IssueSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	setupRes, err := authSvc.Setup(ctx, auth.SetupRequest{
		Code:       code,
		Username:   "admin",
		Password:   "admin-strong-password-1",
		RemoteAddr: "127.0.0.1:1000",
	})
	if err != nil {
		t.Fatal(err)
	}

	memberUser, err := authSvc.CreateUser(ctx, auth.CreateUserRequest{
		Username: "bob_member",
		Password: "member-strong-password-1",
		Role:     auth.RoleMember,
	})
	if err != nil {
		t.Fatal(err)
	}
	viewerUser, err := authSvc.CreateUser(ctx, auth.CreateUserRequest{
		Username: "carol_viewer",
		Password: "viewer-strong-password-1",
		Role:     auth.RoleViewer,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, adminTok, err := authSvc.CreateAPIToken(ctx, auth.CreateTokenRequest{UserID: setupRes.User.ID, Name: "mcp-admin"})
	if err != nil {
		t.Fatal(err)
	}
	_, memberTok, err := authSvc.CreateAPIToken(ctx, auth.CreateTokenRequest{UserID: memberUser.ID, Name: "mcp-member"})
	if err != nil {
		t.Fatal(err)
	}
	_, viewerTok, err := authSvc.CreateAPIToken(ctx, auth.CreateTokenRequest{UserID: viewerUser.ID, Name: "mcp-viewer"})
	if err != nil {
		t.Fatal(err)
	}

	guard, err := auth.NewGuard(authSvc, auth.GuardConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctrl, err := controller.New(ctx, controller.Options{
		Store:               st,
		Results:             rdb,
		Version:             "1.0-mcp-test",
		ClusterListen:       "127.0.0.1:0",
		InProcessLocalAgent: true,
		StartDelay:          30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctrl.Close() })

	apiSrv, err := api.NewServer(api.Options{
		Controller: ctrl,
		Store:      st,
		Results:    rdb,
		Auth:       authSvc,
		Guard:      guard,
		Version:    "1.0-mcp-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(apiSrv.Close)

	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"auth_hdr":%q,"secret_q":%q}`, r.Header.Get("Authorization"), r.URL.Query().Get("tok"))
	}))
	t.Cleanup(targetSrv.Close)

	pr, err := st.CreateProject(&store.Project{
		Name:        "MCPDemo",
		Description: "MCP 安全测试项目",
		Variables: store.KeyValueList{
			{Name: "vault_key", Value: scenario.Scalar("vault-secret-plaintext-777"), Secret: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	apiOK, err := st.CreateAPI(pr.ID, &store.API{
		Name: "GetStatus",
		Request: store.APIRequest{
			Method:  "GET",
			URL:     targetSrv.URL + "/ok?tok={{vault_key}}",
			Headers: store.KeyValueList{{Name: "Authorization", Value: "Bearer {{vault_key}}"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	apiSSRF, err := st.CreateAPI(pr.ID, &store.API{
		Name: "ProbeMetadata",
		Request: store.APIRequest{
			Method: "GET",
			URL:    "http://169.254.169.254/latest/meta-data/",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(targetSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.SetSecurityPolicy(ctx, controller.SecurityPolicy{
		AllowlistEnabled: true,
		AllowedTargets:   []string{u.Hostname()},
	}); err != nil {
		t.Fatal(err)
	}

	hs := httptest.NewServer(apiSrv)
	t.Cleanup(hs.Close)

	return &mcpTestEnv{
		srv:         hs,
		ctrl:        ctrl,
		st:          st,
		rdb:         rdb,
		adminToken:  adminTok,
		memberToken: memberTok,
		viewerToken: viewerTok,
		project:     pr,
		apiAllowed:  apiOK,
		apiBlocked:  apiSSRF,
		targetSrv:   targetSrv,
	}
}

func callMCPRPC(t *testing.T, baseURL, token string, payload any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/mcp", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		return resp.StatusCode, nil
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestMCPHandshakeToolsRBACSecretsSSRFAndAudit(t *testing.T) {
	env := setupMCPTestEnv(t)

	// 1. Unauthenticated request is rejected with 401.
	st, _ := callMCPRPC(t, env.srv.URL, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	if st != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST /api/v1/mcp = %d, want 401", st)
	}

	// 2. Initialize + notification + tools/list -> verify all 16 tools + annotations.
	st, initOut := callMCPRPC(t, env.srv.URL, env.viewerToken, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
		},
	})
	if st != 200 {
		t.Fatalf("initialize = %d: %+v", st, initOut)
	}
	resMap, _ := initOut["result"].(map[string]any)
	if resMap["protocolVersion"] != "2025-03-26" {
		t.Fatalf("unexpected protocolVersion: %+v", resMap)
	}

	st, _ = callMCPRPC(t, env.srv.URL, env.viewerToken, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	if st != http.StatusAccepted {
		t.Fatalf("notification status = %d, want 202", st)
	}

	st, listOut := callMCPRPC(t, env.srv.URL, env.viewerToken, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})
	if st != 200 {
		t.Fatalf("tools/list = %d", st)
	}
	toolsList := listOut["result"].(map[string]any)["tools"].([]any)
	if len(toolsList) != 16 {
		t.Fatalf("expected 16 MCP tools, got %d", len(toolsList))
	}
	for _, rawTool := range toolsList {
		toolObj := rawTool.(map[string]any)
		if toolObj["annotations"] == nil {
			t.Fatalf("tool %v missing annotations", toolObj["name"])
		}
	}

	// 3. Viewer can call read-only tools & never sees Vault plaintext secret.
	st, detOut := callMCPRPC(t, env.srv.URL, env.viewerToken, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "get_api_detail",
			"arguments": map[string]any{
				"project_id": env.project.ID,
				"api_id":     env.apiAllowed.ID,
			},
		},
	})
	if st != 200 {
		t.Fatalf("get_api_detail = %d", st)
	}
	rawDet, _ := json.Marshal(detOut)
	if strings.Contains(string(rawDet), "vault-secret-plaintext-777") {
		t.Fatalf("viewer get_api_detail leaked Vault secret: %s", rawDet)
	}

	// 4. Viewer is denied write / debug / load tools (tool isError=true).
	for _, blockedTool := range []string{"save_api", "debug_api", "manage_load_run"} {
		st, callOut := callMCPRPC(t, env.srv.URL, env.viewerToken, map[string]any{
			"jsonrpc": "2.0",
			"id":      4,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      blockedTool,
				"arguments": map[string]any{},
			},
		})
		if st != 200 {
			t.Fatalf("viewer call %s HTTP status = %d", blockedTool, st)
		}
		res := callOut["result"].(map[string]any)
		if isErr, _ := res["isError"].(bool); !isErr {
			t.Fatalf("expected viewer call to %s blocked with isError=true, got %+v", blockedTool, res)
		}
	}

	// 5. Member debug_api succeeds against allowed target & redacts echoed secret value!
	st, dbgOut := callMCPRPC(t, env.srv.URL, env.memberToken, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "debug_api",
			"arguments": map[string]any{
				"project_id": env.project.ID,
				"api_id":     env.apiAllowed.ID,
			},
		},
	})
	if st != 200 {
		t.Fatalf("debug_api = %d", st)
	}
	dbgBytes, _ := json.Marshal(dbgOut)
	if strings.Contains(string(dbgBytes), "vault-secret-plaintext-777") {
		t.Fatalf("debug_api over MCP leaked Vault plaintext secret: %s", dbgBytes)
	}
	if dbgOut["result"].(map[string]any)["isError"] == true {
		t.Fatalf("expected allowed debug_api to succeed, got %+v", dbgOut)
	}

	// 6. SSRF allowlist blocks disallowed metadata IP via debug_api.
	_, ssrfOut := callMCPRPC(t, env.srv.URL, env.memberToken, map[string]any{
		"jsonrpc": "2.0",
		"id":      6,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "debug_api",
			"arguments": map[string]any{
				"project_id": env.project.ID,
				"api_id":     env.apiBlocked.ID,
			},
		},
	})
	if ssrfOut["result"].(map[string]any)["isError"] != true {
		t.Fatalf("expected SSRF metadata debug_api blocked, got %+v", ssrfOut)
	}

	// 7. MCP load guardrail enforces MCPMaxVUs ceiling & MCPDisabled kill-switch.
	_, startOverOut := callMCPRPC(t, env.srv.URL, env.adminToken, map[string]any{
		"jsonrpc": "2.0",
		"id":      7,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "manage_load_run",
			"arguments": map[string]any{
				"action":     "start",
				"project_id": env.project.ID,
				"api_id":     env.apiAllowed.ID,
				"load": map[string]any{
					"mode": "vus",
					"stages": []map[string]any{
						{"duration": "2s", "target": controller.DefaultMCPMaxVUs + 25},
					},
				},
			},
		},
	})
	if startOverOut["result"].(map[string]any)["isError"] != true {
		t.Fatalf("expected manage_load_run > MCPMaxVUs rejected, got %+v", startOverOut)
	}

	// Verify audit logs captured mcp.tool.* entries.
	audits, err := env.rdb.ListAuditLogs(context.Background(), store.AuditQuery{Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	seenAudit := false
	for _, a := range audits {
		if a.Action == "mcp.tool.debug_api" {
			seenAudit = true
			break
		}
	}
	if !seenAudit {
		t.Fatalf("expected mcp.tool.debug_api audit log, got %+v", audits)
	}

	// Verify global MCPDisabled switch blocks endpoint with HTTP 403.
	if _, err := env.ctrl.SetSecurityPolicy(context.Background(), controller.SecurityPolicy{
		MCPDisabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	st, disOut := callMCPRPC(t, env.srv.URL, env.adminToken, map[string]any{"jsonrpc": "2.0", "id": 8, "method": "ping"})
	if st != http.StatusForbidden {
		t.Fatalf("expected 403 when MCPDisabled=true, got %d: %+v", st, disOut)
	}
}

func TestRunStdioProxyRoundTrip(t *testing.T) {
	env := setupMCPTestEnv(t)

	inBuf := bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_projects","arguments":{}}}` + "\n",
	)
	var outBuf, errBuf bytes.Buffer
	err := mcp.RunStdioProxy(context.Background(), mcp.StdioOptions{
		Endpoint: env.srv.URL,
		Token:    env.viewerToken,
	}, inBuf, &outBuf, &errBuf)
	if err != nil {
		t.Fatalf("RunStdioProxy error: %v (stderr=%s)", err, errBuf.String())
	}
	lines := strings.Split(strings.TrimSpace(outBuf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSON-RPC responses on stdout (notification skipped), got %d:\n%s", len(lines), outBuf.String())
	}
	if !strings.Contains(lines[1], `"MCPDemo"`) {
		t.Fatalf("expected list_projects output to contain MCPDemo, got: %s", lines[1])
	}
}
