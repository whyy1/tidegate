package curl

import (
	"strings"
	"testing"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

func TestParseBashChromeDevToolsAndAuth(t *testing.T) {
	input := `$ curl 'https://api.example.com/v1/orders?page=2&token={{tok}}' \
  -H 'Accept: application/json' \
  -H 'Authorization: Bearer secret-jwt-123' \
  -H 'X-Custom: a' \
  -H 'X-Custom: b' \
  --data-raw $'{"note":"line1\nline2","user":"{{user}}"}' \
  --compressed`

	api, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if api.Request.Method != "POST" {
		t.Fatalf("method = %q, want POST", api.Request.Method)
	}
	if api.Request.URL != "https://api.example.com/v1/orders" {
		t.Fatalf("url = %q", api.Request.URL)
	}
	if api.Name != "POST /v1/orders" {
		t.Fatalf("name = %q", api.Name)
	}
	if api.Auth == nil || api.Auth.Type != store.AuthBearer || api.Auth.Token != "secret-jwt-123" {
		t.Fatalf("auth = %+v", api.Auth)
	}
	if len(api.Request.Query) != 2 || api.Request.Query[0].Name != "page" || api.Request.Query[0].Value != "2" ||
		api.Request.Query[1].Name != "token" || api.Request.Query[1].Value != "{{tok}}" {
		t.Fatalf("query = %+v", api.Request.Query)
	}
	if len(api.Request.Headers) != 3 {
		t.Fatalf("expected 3 non-auth headers (Authorization extracted), got %+v", api.Request.Headers)
	}
	if api.Request.Body == nil || api.Request.Body.Type != "json" {
		t.Fatalf("body = %+v", api.Request.Body)
	}
	if !strings.Contains(api.Request.Body.Content, "{{user}}") || !strings.Contains(api.Request.Body.Content, "\n") {
		t.Fatalf("body content = %q", api.Request.Body.Content)
	}
}

func TestParseWindowsCMDAndPowerShell(t *testing.T) {
	cmdInput := "curl \"https://api.example.com/login\" ^\r\n" +
		"  -H \"Content-Type: application/json\" ^\r\n" +
		"  --data-raw ^\"^{^\\\"u^\\\":^\\\"bob^\\\"^}^\""

	api, err := Parse(cmdInput)
	if err != nil {
		t.Fatalf("CMD parse failed: %v", err)
	}
	if api.Request.Method != "POST" || api.Request.Body == nil || api.Request.Body.Type != "json" {
		t.Fatalf("cmd api = %+v", api)
	}
	if api.Request.Body.Content != `{"u":"bob"}` {
		t.Fatalf("cmd body = %q", api.Request.Body.Content)
	}

	psInput := "PS> curl.exe -X PUT `\n" +
		"  https://api.example.com/items/1 `\n" +
		"  -H 'Content-Type: application/x-www-form-urlencoded' `\n" +
		"  -d 'name=alice+smith&role=admin'"

	psAPI, err := Parse(psInput)
	if err != nil {
		t.Fatalf("PowerShell parse failed: %v", err)
	}
	if psAPI.Request.Method != "PUT" || psAPI.Request.Body == nil || psAPI.Request.Body.Type != "form" {
		t.Fatalf("ps api = %+v", psAPI)
	}
	if len(psAPI.Request.Body.Fields) != 2 || psAPI.Request.Body.Fields[0].Value != "alice smith" {
		t.Fatalf("ps fields = %+v", psAPI.Request.Body.Fields)
	}
}

func TestParseJSONFlagMultipartGetAndBasicUser(t *testing.T) {
	jsonAPI, err := Parse(`curl --json '{"ok":true}' https://example.com/hook`)
	if err != nil {
		t.Fatal(err)
	}
	if jsonAPI.Request.Method != "POST" || jsonAPI.Request.Body.Type != "json" || !hasHeader(jsonAPI.Request.Headers, "Content-Type") {
		t.Fatalf("json flag api = %+v", jsonAPI)
	}

	getAPI, err := Parse(`curl -sG -u alice:s3cr3t -d 'q=hello world' --data-urlencode 'tag={{t}}' https://example.com/search`)
	if err != nil {
		t.Fatal(err)
	}
	if getAPI.Request.Method != "GET" || getAPI.Request.Body != nil {
		t.Fatalf("get api method=%s body=%+v", getAPI.Request.Method, getAPI.Request.Body)
	}
	if getAPI.Auth == nil || getAPI.Auth.Type != store.AuthBasic || getAPI.Auth.Username != "alice" || getAPI.Auth.Password != "s3cr3t" {
		t.Fatalf("basic auth = %+v", getAPI.Auth)
	}
	if len(getAPI.Request.Query) != 2 || getAPI.Request.Query[1].Value != "{{t}}" {
		t.Fatalf("get query = %+v", getAPI.Request.Query)
	}

	multiAPI, err := Parse(`curl -XPOST -F "title=report" -F "file=@C:\tmp\users.csv" https://example.com/upload`)
	if err != nil {
		t.Fatal(err)
	}
	if multiAPI.Request.Body == nil || multiAPI.Request.Body.Type != "multipart" {
		t.Fatalf("multipart body = %+v", multiAPI.Request.Body)
	}
	if len(multiAPI.Request.Body.Files) != 1 || multiAPI.Request.Body.Files[0].Path != "users.csv" {
		t.Fatalf("multipart files = %+v", multiAPI.Request.Body.Files)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse("   "); err == nil {
		t.Fatal("expected error for empty input")
	}
	if _, err := Parse("curl 'unterminated"); err == nil {
		t.Fatal("expected error for unterminated quote")
	}
	if _, err := Parse("curl -X POST"); err == nil {
		t.Fatal("expected error when URL is missing")
	}
}

func TestFormatAndFormatAPI(t *testing.T) {
	cmd, err := Format(FormatOptions{
		Method:        "POST",
		URL:           "https://api.example.com/v1/items?a=1",
		Headers:       []metrics.Header{{Name: "Authorization", Value: "Bearer secret-tok"}, {Name: "X-Req", Value: "ok"}},
		Body:          `{"k":"it's"}`,
		Shell:         ShellBash,
		RedactSecrets: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "Bearer <REDACTED>") || strings.Contains(cmd, "secret-tok") {
		t.Fatalf("secret leaked in redacted cURL:\n%s", cmd)
	}
	if !strings.Contains(cmd, `'{"k":"it'\''s"}'`) {
		t.Fatalf("bash quote escaping mismatch:\n%s", cmd)
	}

	api := &store.API{
		Name: "create",
		Request: store.APIRequest{
			Method: "POST",
			URL:    "/users/:id",
			PathParams: store.KeyValueList{
				{Name: "id", Value: "42"},
			},
			Query: store.KeyValueList{
				{Name: "env", Value: "{{envName}}"},
			},
			Body: &store.APIBody{
				Type:    "json",
				Content: `{"key":"{{apiSecret}}"}`,
			},
		},
	}
	preview := &store.EffectivePreview{
		BaseURL: "https://api.example.com",
		InheritedHeaders: []store.InheritedHeader{
			{Name: "X-Tenant", Value: "acme"},
			{Name: "Authorization", Value: "old", Overridden: true},
		},
		EffectiveAuth: store.EffectiveAuth{
			Auth: &store.Auth{Type: store.AuthBearer, Token: "{{authToken}}"},
		},
		Variables: []store.ResolvedVariable{
			{Name: "envName", Value: "staging"},
			{Name: "apiSecret", Value: "top-secret-xyz", Secret: true},
			{Name: "authToken", Value: "tok-999", Secret: true},
		},
	}

	expanded, err := FormatAPI(api, preview, ModeExpanded, ShellBash, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expanded, "https://api.example.com/users/42?env=staging") {
		t.Fatalf("expanded URL mismatch:\n%s", expanded)
	}
	if !strings.Contains(expanded, "Bearer <REDACTED>") || !strings.Contains(expanded, `{"key":"<REDACTED>"}`) {
		t.Fatalf("expanded redaction mismatch:\n%s", expanded)
	}
	if strings.Contains(expanded, "top-secret-xyz") || strings.Contains(expanded, "tok-999") {
		t.Fatalf("expanded leaked secret:\n%s", expanded)
	}
	if !strings.Contains(expanded, "X-Tenant: acme") || strings.Contains(expanded, "Authorization: old") {
		t.Fatalf("inherited header mismatch:\n%s", expanded)
	}

	tmplPS, err := FormatAPI(api, preview, ModeTemplate, ShellPowerShell, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tmplPS, "curl.exe -X POST") || !strings.Contains(tmplPS, "{{envName}}") || !strings.Contains(tmplPS, "Bearer {{authToken}}") {
		t.Fatalf("template powershell mismatch:\n%s", tmplPS)
	}
	// Round-trip the generated non-redacted expanded Bash command back through Parse!
	expandedRaw, err := FormatAPI(api, preview, ModeExpanded, ShellBash, false)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := Parse(expandedRaw)
	if err != nil {
		t.Fatalf("reparse failed: %v\ncmd:\n%s", err, expandedRaw)
	}
	if reparsed.Request.Method != "POST" || reparsed.Request.URL != "https://api.example.com/users/42" ||
		reparsed.Auth == nil || reparsed.Auth.Token != "tok-999" {
		t.Fatalf("reparsed mismatch: %+v", reparsed)
	}
	_ = scenario.Scalar("")
}
