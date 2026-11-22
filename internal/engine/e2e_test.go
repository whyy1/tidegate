package engine

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
)

// e2eScenario logs in with rows of a CSV file, uses the extracted token
// to query /items, checks that the token is required, and has one step
// whose assertion always fails.
const e2eScenario = `
version: 1
name: 登录后查询（端到端）
variables:
  host: BASE
data:
  - {name: users, file: users.csv, mode: sequential}
steps:
  - id: login
    name: 登录
    request:
      method: POST
      url: "{{host}}/login"
      body:
        type: json
        content: '{"user":"{{users.name}}","pwd":"{{users.pwd}}"}'
    extract:
      - {name: token, from: jsonpath, expr: $.data.token}
      - {name: user, from: jsonpath, expr: $.data.user}
    assert:
      - {type: status, equals: 200}
  - id: items
    name: 查询
    request:
      url: "{{host}}/items"
      headers:
        Authorization: "Bearer {{token}}"
    assert:
      - {type: status, equals: 200}
      - {type: jsonpath, expr: $.data.user, exists: true}
      - {type: jsonpath, expr: "$.data.items[1].name", equals: banana}
  - id: no_token
    name: 不带令牌
    request:
      url: "{{host}}/items"
    assert:
      - {type: status, equals: 401}
  - id: wrong_item
    name: 必然失败的断言
    request:
      url: "{{host}}/items"
      headers:
        Authorization: "Bearer {{token}}"
    assert:
      - {type: jsonpath, expr: "$.data.items[0].name", equals: durian}
load:
  mode: vus
  stages:
    - {duration: 500ms, target: 20}
    - {duration: 2500ms, target: 20}
  graceful_stop: 3s
`

func TestEndToEndMultiStep(t *testing.T) {
	srv := startTestServer(t)
	users := "name,pwd\nalice,p1\nbob,p2\ncarol,p3\n"
	for _, client := range clients {
		t.Run(string(client), func(t *testing.T) {
			p := compile(t, e2eScenario, srv.URL(), map[string]string{"users.csv": users})
			r := run(t, p, Options{Client: client})
			checkConsistent(t, r)
			s := r.summary

			if n := len(r.snaps); n < 3 || n > 4 {
				t.Fatalf("got %d snapshots for a 3s run", n)
			}
			// Ramp to 20 VUs during the first 500ms.
			if r.snaps[0].ActiveVUs < 15 || s.ActiveVUs != 20 {
				t.Fatalf("active VUs first=%d peak=%d", r.snaps[0].ActiveVUs, s.ActiveVUs)
			}
			login, items, noToken, wrong := s.Steps[0], s.Steps[1], s.Steps[2], s.Steps[3]
			if login.ID != "login" || login.Name != "登录" || wrong.ID != "wrong_item" {
				t.Fatalf("step identities: %+v", s.Steps)
			}
			if login.Requests < 100 {
				t.Fatalf("only %d logins", login.Requests)
			}
			if login.ErrorCount() != 0 || login.Status[200] != login.Requests {
				t.Fatalf("login status=%v errors=%v samples=%+v", login.Status, login.Errors, s.Samples)
			}
			// The extracted token works: every authorized query succeeds.
			if items.ErrorCount() != 0 || items.Status[200] != items.Requests || items.Requests == 0 {
				t.Fatalf("items status=%v errors=%v samples=%+v", items.Status, items.Errors, s.Samples)
			}
			// Without the token the server refuses (asserted as expected).
			if noToken.ErrorCount() != 0 || noToken.Status[401] != noToken.Requests {
				t.Fatalf("no_token status=%v errors=%v", noToken.Status, noToken.Errors)
			}
			// The failing assertion is classified as an assertion error,
			// not as an HTTP error, for every request of that step.
			if wrong.Errors[metrics.ErrAssertion] != wrong.Requests || wrong.Requests == 0 || wrong.Status[200] != wrong.Requests {
				t.Fatalf("wrong_item errors=%v status=%v requests=%d", wrong.Errors, wrong.Status, wrong.Requests)
			}
			if s.Total.ErrorCount() != wrong.Requests {
				t.Fatalf("total errors %d != assertion errors %d", s.Total.ErrorCount(), wrong.Requests)
			}
			// Iterations complete only after the last step.
			if s.Iterations != wrong.Requests {
				t.Fatalf("iterations %d != last step requests %d", s.Iterations, wrong.Requests)
			}
			// Steps run in order, so counts are non-increasing.
			if !(login.Requests >= items.Requests && items.Requests >= noToken.Requests && noToken.Requests >= wrong.Requests) {
				t.Fatalf("step counts out of order: %d %d %d %d", login.Requests, items.Requests, noToken.Requests, wrong.Requests)
			}
			// Assertion samples explain the failure and mask the token.
			var found bool
			for _, smp := range s.Samples {
				if smp.Kind != metrics.SampleKind(metrics.ErrAssertion) {
					t.Fatalf("unexpected sample %+v", smp)
				}
				if smp.StepID != "wrong_item" || !strings.Contains(smp.Error, "断言失败") || !strings.Contains(smp.Error, "apple") {
					t.Fatalf("sample = %+v", smp)
				}
				for _, h := range smp.RequestHeaders {
					if h.Name == "Authorization" && h.Value != "***" {
						t.Fatalf("token leaked into sample: %v", h)
					}
				}
				if !strings.Contains(smp.Body, `"items"`) {
					t.Fatalf("sample body = %q", smp.Body)
				}
				found = true
			}
			if !found {
				t.Fatal("no assertion samples")
			}
			// Bytes are attributed to steps; login sends a JSON body.
			if login.BytesSent <= items.BytesSent/items.Requests*login.Requests/2 || login.BytesReceived == 0 {
				t.Fatalf("login bytes sent=%d received=%d", login.BytesSent, login.BytesReceived)
			}
			for i, snap := range r.snaps[:len(r.snaps)-1] {
				if snap.Duration < 900*time.Millisecond || snap.Duration > 1100*time.Millisecond {
					t.Fatalf("snapshot %d covers %v", i, snap.Duration)
				}
			}
		})
	}
}

func TestControlFlowExecution(t *testing.T) {
	var thenHits, elseHits, w1Hits, w2Hits, loopHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tx/start":
			_, _ = w.Write([]byte(`{"go":"yes"}`))
		case "/tx/loop":
			loopHits.Add(1)
			_, _ = w.Write([]byte(`{"step":"loop"}`))
		case "/branch/then":
			thenHits.Add(1)
			_, _ = w.Write([]byte(`{"branch":"then"}`))
		case "/branch/else":
			elseHits.Add(1)
			_, _ = w.Write([]byte(`{"branch":"else"}`))
		case "/w1":
			w1Hits.Add(1)
			_, _ = w.Write([]byte(`{"w":1}`))
		case "/w2":
			w2Hits.Add(1)
			_, _ = w.Write([]byte(`{"w":2}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	def := &scenario.Definition{
		Version: 1,
		Name:    "control-flow-e2e",
		Steps: []scenario.Step{
			{
				ID:   "tx",
				Name: "下单事务组",
				Group: &scenario.GroupStep{
					Children: []scenario.Step{
						{
							ID: "tx_start",
							Request: &scenario.Request{
								Method: "GET",
								URL:    srv.URL + "/tx/start",
							},
							Extract: []scenario.Extract{
								{Name: "go", From: "jsonpath", Expr: "$.go"},
							},
							Assert: []scenario.Assert{
								{Type: "status", Equals: e2ePtrScalar("200")},
							},
						},
						{
							ID: "tx_loop",
							Loop: &scenario.LoopStep{
								Count: 2,
								Children: []scenario.Step{
									{
										ID: "tx_loop_req",
										Request: &scenario.Request{
											Method: "GET",
											URL:    srv.URL + "/tx/loop",
										},
									},
								},
							},
						},
					},
				},
			},
			{
				ID: "branch_if",
				If: &scenario.IfStep{
					Expr: `{{go}} == "yes"`,
					Then: []scenario.Step{
						{ID: "then_step", Request: &scenario.Request{Method: "GET", URL: srv.URL + "/branch/then"}},
					},
					Else: []scenario.Step{
						{ID: "else_step", Request: &scenario.Request{Method: "GET", URL: srv.URL + "/branch/else"}},
					},
				},
			},
			{
				ID: "ab_mix",
				Weighted: &scenario.WeightedStep{
					Branches: []scenario.WeightedBranch{
						{Name: "b1", Weight: 70, Steps: []scenario.Step{{ID: "w1_step", Request: &scenario.Request{Method: "GET", URL: srv.URL + "/w1"}}}},
						{Name: "b2", Weight: 30, Steps: []scenario.Step{{ID: "w2_step", Request: &scenario.Request{Method: "GET", URL: srv.URL + "/w2"}}}},
					},
				},
			},
		},
		Load: scenario.Load{
			Mode: "vus",
			Stages: []scenario.Stage{
				{Duration: scenario.Duration(400 * time.Millisecond), Target: 2},
			},
		},
	}

	for _, cl := range clients {
		t.Run(string(cl), func(t *testing.T) {
			thenHits.Store(0)
			elseHits.Store(0)
			w1Hits.Store(0)
			w2Hits.Store(0)
			loopHits.Store(0)

			plan, err := scenario.Compile(def, scenario.CompileOptions{})
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			r := run(t, plan, Options{Client: cl, Shards: 2})
			checkConsistent(t, r)

			if r.summary.Total.ErrorCount() > 0 || r.summary.Iterations == 0 {
				t.Fatalf("unexpected errors=%d iterations=%d", r.summary.Total.ErrorCount(), r.summary.Iterations)
			}
			sv := r.summary.View()
			stepByID := make(map[string]metrics.StepView, len(sv.Steps))
			var leafReqs int64
			for i := range sv.Steps {
				st := sv.Steps[i]
				stepByID[st.ID] = st
				if st.IsGroup {
					continue
				}
				leafReqs += st.Requests
			}
			txStep, hasTx := stepByID["tx"]
			if !hasTx || !txStep.IsGroup || txStep.Requests < r.summary.Iterations || txStep.Requests > r.summary.Iterations+2 {
				t.Fatalf("tx Group step mismatch: %+v (iterations=%d)", txStep, r.summary.Iterations)
			}
			if sv.Requests != leafReqs {
				t.Fatalf("sv.Requests=%d double-counted or missed leaf steps=%d", sv.Requests, leafReqs)
			}
			if stepByID["tx_loop_req"].Requests != loopHits.Load() || loopHits.Load() < 2*r.summary.Iterations || loopHits.Load() > 2*txStep.Requests+2 {
				t.Fatalf("loop accounting mismatch: step=%d server=%d iters=%d", stepByID["tx_loop_req"].Requests, loopHits.Load(), r.summary.Iterations)
			}
			if stepByID["then_step"].Requests != thenHits.Load() || elseHits.Load() != 0 || thenHits.Load() < r.summary.Iterations || thenHits.Load() > txStep.Requests {
				t.Fatalf("if branching mismatch: step=%d then=%d else=%d iters=%d", stepByID["then_step"].Requests, thenHits.Load(), elseHits.Load(), r.summary.Iterations)
			}
			if stepByID["w1_step"].Requests != w1Hits.Load() || stepByID["w2_step"].Requests != w2Hits.Load() || w1Hits.Load()+w2Hits.Load() != r.summary.Iterations {
				t.Fatalf("weighted total mismatch: w1=%d(%d) w2=%d(%d) iterations=%d", w1Hits.Load(), stepByID["w1_step"].Requests, w2Hits.Load(), stepByID["w2_step"].Requests, r.summary.Iterations)
			}
		})
	}
}

func TestOAuth2DNSOverrideAndHTTP2(t *testing.T) {
	t.Run("oauth2_cache_and_dns_override", func(t *testing.T) {
		var tokenHits atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth/token":
				tokenHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"cached-tok-777","expires_in":3600}`))
			case "/api/data":
				if r.Header.Get("Authorization") != "Bearer cached-tok-777" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			t.Fatal(err)
		}
		customBase := fmt.Sprintf("http://custom.tidegate.test:%s", port)

		for _, cl := range clients {
			t.Run(string(cl), func(t *testing.T) {
				tokenHits.Store(0)
				def := &scenario.Definition{
					Version: 1,
					Name:    "oauth2-dns",
					HTTP: scenario.HTTPOptions{
						DNSOverrides: map[string]string{"custom.tidegate.test": "127.0.0.1"},
					},
					Steps: []scenario.Step{
						{
							ID: "protected",
							Request: &scenario.Request{
								Method: "GET",
								URL:    customBase + "/api/data",
								OAuth2: &scenario.OAuth2Auth{
									TokenURL:     customBase + "/oauth/token",
									ClientID:     "svc",
									ClientSecret: "secret",
									ClientAuth:   "body",
								},
							},
							Assert: []scenario.Assert{
								{Type: "status", Equals: e2ePtrScalar("200")},
							},
						},
					},
					Load: scenario.Load{
						Mode: "vus",
						Stages: []scenario.Stage{
							{Duration: scenario.Duration(350 * time.Millisecond), Target: 3},
						},
					},
				}
				plan, err := scenario.Compile(def, scenario.CompileOptions{})
				if err != nil {
					t.Fatal(err)
				}
				r := run(t, plan, Options{Client: cl, Shards: 2})
				if r.summary.Total.ErrorCount() > 0 || r.summary.Total.Requests < 3 {
					t.Fatalf("unexpected oauth2/dns result: reqs=%d errs=%d", r.summary.Total.Requests, r.summary.Total.ErrorCount())
				}
				if got := tokenHits.Load(); got != 1 {
					t.Fatalf("expected OAuth2 token endpoint hit exactly once across VUs, got %d", got)
				}
			})
		}
	})

	t.Run("http2_auto_std_client", func(t *testing.T) {
		var h2Reqs atomic.Int64
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor != 2 {
				http.Error(w, "want HTTP/2", http.StatusHTTPVersionNotSupported)
				return
			}
			h2Reqs.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"h2":true}`))
		}))
		srv.EnableHTTP2 = true
		srv.StartTLS()
		defer srv.Close()

		def := &scenario.Definition{
			Version: 1,
			Name:    "h2-test",
			HTTP: scenario.HTTPOptions{
				HTTPVersion:        "http2",
				InsecureSkipVerify: true,
			},
			Steps: []scenario.Step{
				{
					ID: "h2_get",
					Request: &scenario.Request{
						Method: "GET",
						URL:    srv.URL + "/h2",
					},
					Assert: []scenario.Assert{
						{Type: "status", Equals: e2ePtrScalar("200")},
					},
				},
			},
			Load: scenario.Load{
				Mode:        "vus",
				MaxRequests: 6,
				Stages: []scenario.Stage{
					{Duration: scenario.Duration(2 * time.Second), Target: 2},
				},
			},
		}
		plan, err := scenario.Compile(def, scenario.CompileOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// Explicitly pass ClientFast to verify engine promotes to stdClient for HTTP/2.
		r := run(t, plan, Options{Client: ClientFast, Shards: 1})
		if r.summary.Total.ErrorCount() > 0 || h2Reqs.Load() != 6 || r.summary.Total.Requests != 6 {
			t.Fatalf("HTTP/2 run mismatch: reqs=%d h2Reqs=%d errs=%d", r.summary.Total.Requests, h2Reqs.Load(), r.summary.Total.ErrorCount())
		}
	})
}

func e2ePtrScalar(s string) *scenario.Scalar {
	v := scenario.Scalar(s)
	return &v
}
