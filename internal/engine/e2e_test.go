package engine

import (
	"strings"
	"testing"
	"time"

	"tidegate/internal/metrics"
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
