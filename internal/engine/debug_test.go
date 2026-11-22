package engine

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"tidegate/internal/scenario"
)

func TestRunDebugSingleAndDryRun(t *testing.T) {
	beforeG := runtime.NumGoroutine()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Trace-Id", "tr-42")
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "cookie-99", Path: "/"})
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"token":"tok-abc"},"ok":true}`))
		case "/items":
			if got := r.Header.Get("Authorization"); got != "Bearer tok-abc" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			c, err := r.Cookie("sid")
			if err != nil || c.Value != "cookie-99" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"user":"alice","items":[1,2]}`))
		case "/redirect":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("arrived"))
		case "/bin":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0xff, 0xfe, 0x00, 0x01, 0x89, 0x50})
		case "/gzip":
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "application/json")
			gz := gzip.NewWriter(w)
			_, _ = gz.Write([]byte(`{"compressed":true}`))
			_ = gz.Close()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	for _, client := range []ClientKind{ClientFast, ClientStd} {
		t.Run("multistep_"+string(client), func(t *testing.T) {
			def, err := scenario.Parse([]byte(`
version: 1
name: login-query-dryrun
variables:
  token: ""
http:
  cookies: true
  keep_alive: true
steps:
  - id: login
    name: 登录
    request:
      method: POST
      url: "`+srv.URL+`/login"
      headers:
        Content-Type: application/json
        X-Secret: raw-secret-visible-in-debug
      body:
        type: json
        content: '{"user":"alice"}'
    extract:
      - name: token
        from: jsonpath
        expr: $.data.token
      - name: trace
        from: header
        expr: X-Trace-Id
    assert:
      - type: status
        equals: 200
      - type: jsonpath
        expr: $.ok
        equals: "true"
    think:
      type: fixed
      min: 2s
      max: 2s
  - id: query
    name: 查询
    request:
      method: GET
      url: "`+srv.URL+`/items"
      headers:
        Authorization: "Bearer {{token}}"
    assert:
      - type: status
        equals: 200
      - type: jsonpath
        expr: $.user
        equals: "alice"
load:
  stages:
    - duration: 1s
      target: 1
`), scenario.FormatYAML)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := scenario.Compile(def, scenario.CompileOptions{})
			if err != nil {
				t.Fatal(err)
			}

			start := time.Now()
			res, err := RunDebug(context.Background(), plan, DebugOptions{
				Client:    client,
				SkipThink: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(start) >= 1500*time.Millisecond {
				t.Fatalf("SkipThink did not skip 2s think time: elapsed=%v", time.Since(start))
			}
			if !res.Passed || len(res.Steps) != 2 {
				t.Fatalf("unexpected dry-run result: %+v", res)
			}

			s0 := res.Steps[0]
			if !s0.Passed || s0.Status != 200 || s0.ExtractedVars["token"] != "tok-abc" || s0.ExtractedVars["trace"] != "tr-42" {
				t.Fatalf("step0 mismatch: %+v", s0)
			}
			// Interactive debug keeps raw Authorization / secret headers intact.
			foundSecret := false
			for _, h := range s0.ActualRequest.Headers {
				if h.Name == "X-Secret" && h.Value == "raw-secret-visible-in-debug" {
					foundSecret = true
				}
			}
			if !foundSecret {
				t.Fatalf("expected X-Secret in ActualRequest.Headers, got %+v", s0.ActualRequest.Headers)
			}

			s1 := res.Steps[1]
			if !s1.Passed || s1.Status != 200 || s1.ActualRequest.Body != "" || !strings.Contains(s1.Body, `"alice"`) {
				t.Fatalf("step1 mismatch: %+v", s1)
			}
			if !s1.Timing.Reused {
				t.Fatalf("expected step 2 to reuse keep-alive connection, timing=%+v", s1.Timing)
			}
			foundAuth := false
			foundCookie := false
			for _, h := range s1.ActualRequest.Headers {
				if h.Name == "Authorization" && h.Value == "Bearer tok-abc" {
					foundAuth = true
				}
				if h.Name == "Cookie" && strings.Contains(h.Value, "sid=cookie-99") {
					foundCookie = true
				}
			}
			if !foundAuth || !foundCookie {
				t.Fatalf("expected expanded Authorization and jar Cookie in step1 headers, got %+v", s1.ActualRequest.Headers)
			}
			if res.FinalVars["token"] != "tok-abc" || res.FinalVars["trace"] != "tr-42" {
				t.Fatalf("FinalVars mismatch: %+v", res.FinalVars)
			}
		})
	}

	t.Run("redirects_binary_gzip_and_non_shortcircuit", func(t *testing.T) {
		def, err := scenario.Parse([]byte(`
version: 1
name: features
http:
  follow_redirects: true
steps:
  - id: redir
    request:
      method: GET
      url: "`+srv.URL+`/redirect"
    assert:
      - type: status
        equals: 201
      - type: body_contains
        value: arrived
  - id: bin
    request:
      method: GET
      url: "`+srv.URL+`/bin"
  - id: gz
    request:
      method: GET
      url: "`+srv.URL+`/gzip"
load:
  stages:
    - duration: 1s
      target: 1
`), scenario.FormatYAML)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := scenario.Compile(def, scenario.CompileOptions{})
		if err != nil {
			t.Fatal(err)
		}
		res, err := RunDebug(context.Background(), plan, DebugOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Passed || len(res.Steps) != 3 {
			t.Fatalf("expected StopOnError=false to run all 3 steps and fail overall, got %+v", res)
		}
		// Redirect step: first assert fails (status 200 != 201), second passes (body contains arrived).
		r0 := res.Steps[0]
		if len(r0.Redirects) != 1 || r0.Redirects[0].Status != 302 || r0.Redirects[0].Location != "/final" {
			t.Fatalf("redirects mismatch: %+v", r0.Redirects)
		}
		if len(r0.Asserts) != 2 || r0.Asserts[0].Passed || !r0.Asserts[1].Passed {
			t.Fatalf("expected non-short-circuit asserts [false, true], got %+v", r0.Asserts)
		}
		if r0.ErrorKind != "assertion" || r0.Error == "" {
			t.Fatalf("expected ErrorKind=assertion and non-empty Error, got kind=%q err=%q", r0.ErrorKind, r0.Error)
		}
		// Binary step
		r1 := res.Steps[1]
		if !r1.IsBinary || r1.Body != "" {
			t.Fatalf("expected binary body, got IsBinary=%v Body=%q", r1.IsBinary, r1.Body)
		}
		raw, err := base64.StdEncoding.DecodeString(r1.BodyBase64)
		if err != nil || len(raw) != 6 || raw[0] != 0xff {
			t.Fatalf("decoded binary mismatch: err=%v raw=%x", err, raw)
		}
		// Gzip step
		r2 := res.Steps[2]
		if r2.IsBinary || r2.Body != `{"compressed":true}` {
			t.Fatalf("gzip decoded mismatch: %+v", r2)
		}
	})

	t.Run("stop_on_error", func(t *testing.T) {
		def, err := scenario.Parse([]byte(`
version: 1
name: stop-on-error
steps:
  - id: missing
    request:
      method: GET
      url: "`+srv.URL+`/missing"
  - id: second
    request:
      method: GET
      url: "`+srv.URL+`/final"
load:
  stages:
    - duration: 1s
      target: 1
`), scenario.FormatYAML)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := scenario.Compile(def, scenario.CompileOptions{})
		if err != nil {
			t.Fatal(err)
		}
		res, err := RunDebug(context.Background(), plan, DebugOptions{StopOnError: true})
		if err != nil {
			t.Fatal(err)
		}
		if res.Passed || len(res.Steps) != 1 || res.Steps[0].ErrorKind != "http_4xx" {
			t.Fatalf("expected early stop at step 0 with http_4xx, got %+v", res)
		}
	})

	// Close test server and allow background goroutines to exit cleanly.
	srv.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= beforeG+1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutine count grew from %d to %d", beforeG, runtime.NumGoroutine())
}

func TestRunDebugDataRowsAndCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok:" + r.URL.Query().Get("u")))
	}))
	defer srv.Close()

	dir := t.TempDir()
	csvPath := filepath.Join(dir, "users.csv")
	if err := os.WriteFile(csvPath, []byte("name,role\nalice,admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	def, err := scenario.Parse([]byte(`
version: 1
name: data-row
data:
  - name: users
    file: users.csv
steps:
  - id: s1
    request:
      method: GET
      url: "`+srv.URL+`/?u={{users.name}}"
load:
  stages:
    - duration: 1s
      target: 1
`), scenario.FormatYAML)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := scenario.Compile(def, scenario.CompileOptions{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	res, err := RunDebug(context.Background(), plan, DebugOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || res.DataRows["users"]["name"] != "alice" || res.DataRows["users"]["role"] != "admin" {
		t.Fatalf("DataRows mismatch: %+v", res)
	}
	if res.Steps[0].Body != "ok:alice" {
		t.Fatalf("unexpected body %q", res.Steps[0].Body)
	}

	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunDebug(cctx, plan, DebugOptions{}); err == nil {
		t.Fatal("expected canceled error")
	}
}

func TestRunDebugControlFlowAndOAuth2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"dbg-token-123","expires_in":3600}`))
		case "/step":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<res><ok>yes</ok></res>`))
		case "/loop":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"loop":"ok"}`))
		case "/branch":
			if r.Header.Get("Authorization") != "Bearer dbg-token-123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"branch":"then"}`))
		case "/weighted":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"weighted":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	def := &scenario.Definition{
		Version: 1,
		Name:    "debug-control-flow",
		Steps: []scenario.Step{
			{
				ID:   "grp",
				Name: "事务组",
				Group: &scenario.GroupStep{
					Children: []scenario.Step{
						{
							ID: "s_xml",
							Request: &scenario.Request{
								Method: "GET",
								URL:    srv.URL + "/step",
							},
							Extract: []scenario.Extract{
								{Name: "okflag", From: "xpath", Expr: "/res/ok/text()"},
							},
							Assert: []scenario.Assert{
								{Type: "status", Equals: ptrScalarScenario("200")},
							},
						},
						{
							ID: "s_loop",
							Loop: &scenario.LoopStep{
								Count: 50, // RunDebug caps single-pass dry-run loops at 20
								Children: []scenario.Step{
									{
										ID: "loop_req",
										Request: &scenario.Request{
											Method: "GET",
											URL:    srv.URL + "/loop",
										},
									},
								},
							},
						},
						{
							ID: "s_if",
							If: &scenario.IfStep{
								Expr: `{{okflag}} == "yes"`,
								Then: []scenario.Step{
									{
										ID: "then_oauth",
										Request: &scenario.Request{
											Method: "GET",
											URL:    srv.URL + "/branch",
											OAuth2: &scenario.OAuth2Auth{
												TokenURL:     srv.URL + "/oauth/token",
												ClientID:     "cid",
												ClientSecret: "csec",
											},
										},
										Assert: []scenario.Assert{
											{Type: "json_schema", Value: `{"type":"object","required":["branch"]}`},
										},
									},
								},
								Else: []scenario.Step{
									{
										ID:      "else_noop",
										Request: &scenario.Request{Method: "GET", URL: srv.URL + "/missing"},
									},
								},
							},
						},
						{
							ID: "s_weighted",
							Weighted: &scenario.WeightedStep{
								Branches: []scenario.WeightedBranch{
									{Name: "only", Weight: 100, Steps: []scenario.Step{{ID: "w_req", Request: &scenario.Request{Method: "GET", URL: srv.URL + "/weighted"}}}},
								},
							},
						},
					},
				},
			},
		},
		Load: scenario.Load{Stages: []scenario.Stage{{Duration: scenario.Duration(time.Second), Target: 1}}},
	}

	plan, err := scenario.Compile(def, scenario.CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	res, err := RunDebug(context.Background(), plan, DebugOptions{SkipThink: true})
	if err != nil {
		t.Fatalf("RunDebug: %v", err)
	}
	if !res.Passed {
		t.Fatalf("RunDebug expected Passed=true, steps=%+v", res.Steps)
	}
	// 1 (s_xml) + 20 (capped loop_req) + 1 (then_oauth) + 1 (w_req) = 23 executed HTTP steps.
	if len(res.Steps) != 23 {
		t.Fatalf("expected 23 debug steps (loop capped at 20), got %d", len(res.Steps))
	}
	if res.FinalVars["okflag"] != "yes" {
		t.Fatalf("FinalVars = %+v", res.FinalVars)
	}
	var gotBearer bool
	for _, st := range res.Steps {
		if st.StepID == "then_oauth" {
			for _, h := range st.ActualRequest.Headers {
				if strings.EqualFold(h.Name, "Authorization") && h.Value == "Bearer dbg-token-123" {
					gotBearer = true
				}
			}
		}
	}
	if !gotBearer {
		t.Fatal("expected OAuth2 Authorization Bearer header on then_oauth debug request")
	}
}

func ptrScalarScenario(s string) *scenario.Scalar {
	v := scenario.Scalar(s)
	return &v
}
