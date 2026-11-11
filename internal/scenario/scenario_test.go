package scenario

import (
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const exampleYAML = `
version: 1
name: 登录后查询
variables:
  host: http://127.0.0.1:8080
  api: "{{host}}/api"
http:
  conn_mode: per_vu
  keep_alive: true
  timeout:
    connect: 2s
    total: 10s
data:
  - name: users
    file: users.csv
    mode: sequential
steps:
  - id: login
    name: 登录
    request:
      method: post
      url: "{{host}}/login"
      headers:
        Content-Type: application/json
      body:
        type: json
        content: '{"user":"{{users.name}}","pwd":"{{users.pwd}}"}'
    extract:
      - name: token
        from: jsonpath
        expr: $.data.token
    assert:
      - type: status
        equals: 200
    think:
      type: fixed
      min: 100ms
      max: 100ms
  - id: query
    request:
      url: "{{api}}/items"
      query:
        q: "a b&c"
        n: 5
      headers:
        Authorization: "Bearer {{token}}"
    assert:
      - type: jsonpath
        expr: $.code
        equals: 0
      - type: duration
        max: 500
load:
  mode: vus
  stages:
    - duration: 10s
      target: 100
    - duration: 1m
      target: 100
  graceful_stop: 3s
`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func compileYAML(t *testing.T, src string, files map[string]string) (*Plan, error) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		writeFile(t, dir, name, content)
	}
	def, err := Parse([]byte(src), FormatYAML)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return Compile(def, CompileOptions{BaseDir: dir})
}

var usersCSV = map[string]string{"users.csv": "\xef\xbb\xbfname,pwd\nalice,p1\nbob,p2\n"}

func newEnv(p *Plan) *Env {
	env := &Env{Rand: rand.New(rand.NewPCG(1, 2)), Rows: make([][]string, len(p.Data))}
	for _, v := range p.VarInit {
		env.Vars = append(env.Vars, []byte(v))
	}
	return env
}

func TestCompileExample(t *testing.T) {
	p, err := compileYAML(t, exampleYAML, usersCSV)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "登录后查询" || len(p.Steps) != 2 {
		t.Fatalf("plan = %+v", p)
	}
	if p.HTTP.ConnectTimeout != 2*time.Second || p.HTTP.TotalTimeout != 10*time.Second ||
		p.HTTP.FirstByteTimeout != 10*time.Second || p.HTTP.TLSTimeout != DefaultTLSTimeout || !p.HTTP.Cookies {
		t.Fatalf("http config = %+v", p.HTTP)
	}
	if p.Load.Mode != LoadVUs || p.Load.MaxVUs != 100 || p.Load.TotalDuration != 70*time.Second || p.Load.GracefulStop != 3*time.Second {
		t.Fatalf("load = %+v", p.Load)
	}
	if len(p.Vars) != 1 || p.Vars[0] != "token" {
		t.Fatalf("vars = %v", p.Vars)
	}
	if p.ThinkMax != 100*time.Millisecond {
		t.Fatalf("think max = %v", p.ThinkMax)
	}
	if !p.Data[0].Used || p.Data[0].Rows[1][0] != "bob" {
		t.Fatalf("data = %+v", p.Data[0])
	}

	login := p.Steps[0].(*HTTPStep)
	if login.Method != "POST" || login.URL.Static == nil || login.URL.Static.RequestURI != "/login" || login.URL.Static.Addr != "127.0.0.1:8080" {
		t.Fatalf("login url = %+v", login.URL)
	}
	if !login.NeedBody || !login.HasStatusAssert || login.ContentType != "" {
		t.Fatalf("login flags: needBody=%v status=%v ct=%q", login.NeedBody, login.HasStatusAssert, login.ContentType)
	}
	if login.Think.Kind != ThinkFixed || login.Think.Min != 100*time.Millisecond {
		t.Fatalf("think = %+v", login.Think)
	}
	env := newEnv(p)
	env.Rows[0] = p.Data[0].Rows[0]
	if got := login.Body.String(env); got != `{"user":"alice","pwd":"p1"}` {
		t.Fatalf("body = %s", got)
	}

	query := p.Steps[1].(*HTTPStep)
	if query.URL.Static == nil {
		t.Fatalf("query URL should be static: %+v", query.URL)
	}
	if got := query.URL.Static.RequestURI; got != "/api/items?n=5&q=a+b%26c" {
		t.Fatalf("query uri = %s", got)
	}
	if query.Headers[0].Canonical != "Authorization" || query.Headers[0].Value.Static() {
		t.Fatalf("auth header = %+v", query.Headers[0])
	}
	env.Vars[0] = []byte("tok123")
	if got := query.Headers[0].Value.String(env); got != "Bearer tok123" {
		t.Fatalf("auth = %s", got)
	}
	if query.Assert[1].MaxDuration != 500*time.Millisecond {
		t.Fatalf("plain number duration should be milliseconds, got %v", query.Assert[1].MaxDuration)
	}
}

func TestParseJSONEquivalent(t *testing.T) {
	src := `{
	  "version": 1, "name": "json",
	  "steps": [{"id": "ping", "request": {"url": "http://127.0.0.1:18080/ping", "query": {"n": 5}},
	             "assert": [{"type": "status", "equals": 200}]}],
	  "load": {"mode": "rate", "stages": [{"duration": "5s", "target": 1000}], "graceful_stop": 1000}
	}`
	def, err := Parse([]byte(src), FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compile(def, CompileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Load.Mode != LoadRate || !p.Load.MaxVUsAuto || p.Load.MaxVUs != 2000 || p.Load.GracefulStop != time.Second {
		t.Fatalf("load = %+v", p.Load)
	}
	if got := p.Steps[0].(*HTTPStep).URL.Static.RequestURI; got != "/ping?n=5" {
		t.Fatalf("uri = %s", got)
	}
	// Round trip through Marshal keeps the definition loadable.
	for _, f := range []Format{FormatJSON, FormatYAML} {
		data, err := Marshal(def, f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(data, f); err != nil {
			t.Fatalf("re-parse %v: %v\n%s", f, err, data)
		}
	}
}

func TestParseErrorsAreChinese(t *testing.T) {
	_, err := Parse([]byte("version: 1\nstepz: []\n"), FormatYAML)
	if err == nil || !strings.Contains(err.Error(), `未知字段 "stepz"`) || !strings.Contains(err.Error(), "第 2 行") {
		t.Fatalf("yaml error = %v", err)
	}
	_, err = Parse([]byte(`{"version":1,"stepz":[]}`), FormatJSON)
	if err == nil || !strings.Contains(err.Error(), `未知字段 "stepz"`) {
		t.Fatalf("json error = %v", err)
	}
	_, err = Parse([]byte("{\n\"version\": 1,\n\"name\": }"), FormatJSON)
	if err == nil || !strings.Contains(err.Error(), "第 3 行") {
		t.Fatalf("json syntax error = %v", err)
	}
	_, err = Parse([]byte("load:\n  stages:\n    - duration: abc\n"), FormatYAML)
	if err == nil || !strings.Contains(err.Error(), "无效的时长") {
		t.Fatalf("duration error = %v", err)
	}
	_, err = Parse(nil, FormatYAML)
	if err == nil || !strings.Contains(err.Error(), "文件为空") {
		t.Fatalf("empty error = %v", err)
	}
}

func TestValidationErrorsNameStepAndField(t *testing.T) {
	src := `
steps:
  - id: login
    request:
      method: "PO ST"
      url: "http://x/{{tokn}}"
    extract:
      - name: token
        from: jsonpath
        expr: "$..a"
    assert:
      - type: status
  - id: login
    request:
      url: "http://x/"
      headers:
        Content-Length: "5"
      body:
        type: json
        content: '{bad'
  - id: third
load:
  mode: vus
  stages:
    - duration: 1s
      target: 1.5
`
	_, err := compileYAML(t, src, nil)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
	msg := err.Error()
	for _, want := range []string{
		`步骤 "login"（第 1 步）的 request.method：请求方法 "PO ST" 无效`,
		`步骤 "login"（第 1 步）的 request.url：引用了未定义的变量 "tokn"`,
		`步骤 "login"（第 1 步）的 extract[0].expr：JSONPath "$..a"：不支持递归查找`,
		`步骤 "login"（第 1 步）的 assert[0]：status 断言需要 equals 或 in`,
		`步骤 "login"（第 2 步）的 id：和第 1 步重复`,
		`步骤 "login"（第 2 步）的 request.headers.Content-Length`,
		`步骤 "login"（第 2 步）的 request.body.content：不是合法的 JSON`,
		`步骤 "third"（第 3 步）的 request：缺少 request`,
		`load.stages[0].target：vus 模式的目标并发必须是整数`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
}

func TestDataErrors(t *testing.T) {
	src := `
data:
  - name: users
    file: users.csv
    mode: unique
steps:
  - id: a
    request:
      url: "http://x/{{users.nmae}}"
load:
  stages: [{duration: 1s, target: 5}]
`
	_, err := compileYAML(t, src, usersCSV)
	if err == nil || !strings.Contains(err.Error(), `数据文件 "users" 没有列 "nmae"（现有列：name、pwd）`) {
		t.Fatalf("err = %v", err)
	}
	src = strings.Replace(src, "nmae", "name", 1)
	_, err = compileYAML(t, src, usersCSV)
	if err == nil || !strings.Contains(err.Error(), "unique 模式要求每个虚拟用户一行，但只有 2 行") {
		t.Fatalf("err = %v", err)
	}
	_, err = compileYAML(t, src, map[string]string{"users.csv": "name,pwd\nalice\n"})
	if err == nil || !strings.Contains(err.Error(), "第 2 行的列数和首行不一致") {
		t.Fatalf("err = %v", err)
	}
	_, err = compileYAML(t, src, nil)
	if err == nil || !strings.Contains(err.Error(), `data "users" 的 file：读取 users.csv 失败`) {
		t.Fatalf("err = %v", err)
	}
}

func TestURLForms(t *testing.T) {
	src := `
variables:
  base: http://127.0.0.1:18080
steps:
  - id: dyn_path
    request:
      url: "{{base}}/items/{{id}}?x={{$randomInt(1,1)}}"
    extract: [{name: id, from: header, expr: X-Id}, {name: host, from: header, expr: X-Host}]
  - id: dyn_host
    request:
      url: "http://{{host}}/p"
  - id: query_only
    request:
      url: "{{base}}?a={{id}}"
load:
  stages: [{duration: 1s, target: 1}]
`
	p, err := compileYAML(t, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	env := newEnv(p)
	env.Vars[0], env.Vars[1] = []byte("42"), []byte("example.com:81")

	s0 := p.Steps[0].(*HTTPStep).URL
	if s0.Origin == nil || s0.Origin.Addr != "127.0.0.1:18080" || s0.Path.String(env) != "/items/42?x=1" {
		t.Fatalf("dyn_path = %+v", s0)
	}
	s1 := p.Steps[1].(*HTTPStep).URL
	if s1.Full == nil {
		t.Fatalf("dyn_host = %+v", s1)
	}
	scheme, auth, uri, ok := SplitURL([]byte(s1.Full.String(env)))
	if !ok || string(scheme) != "http" || string(auth) != "example.com:81" || string(uri) != "/p" {
		t.Fatalf("split = %s %s %s %v", scheme, auth, uri, ok)
	}
	s2 := p.Steps[2].(*HTTPStep).URL
	if s2.Origin == nil || s2.Path.String(env) != "/?a=42" {
		t.Fatalf("query_only = %+v / %s", s2, s2.Path.String(env))
	}
}

func TestOrigin(t *testing.T) {
	cases := []struct {
		url, host, addr, uri string
		tls                  bool
	}{
		{"http://example.com/a?b=1#frag", "example.com", "example.com:80", "/a?b=1", false},
		{"https://example.com:443", "example.com", "example.com:443", "/", true},
		{"https://example.com:8443/x", "example.com:8443", "example.com:8443", "/x", true},
		{"http://[::1]:8080/", "[::1]:8080", "[::1]:8080", "/", false},
	}
	for _, c := range cases {
		tg, err := ParseTarget(c.url)
		if err != nil {
			t.Fatalf("%s: %v", c.url, err)
		}
		if tg.Host != c.host || tg.Addr != c.addr || tg.RequestURI != c.uri || tg.TLS != c.tls {
			t.Errorf("%s: got host=%s addr=%s uri=%s tls=%v", c.url, tg.Host, tg.Addr, tg.RequestURI, tg.TLS)
		}
	}
	for _, bad := range []string{"ftp://x/", "/relative", "http://user:pw@x/", "http://x:99999/"} {
		if _, err := ParseTarget(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestBuiltinFunctions(t *testing.T) {
	src := `
steps:
  - id: a
    request:
      url: "http://x/"
      headers:
        X-Uuid: "{{$uuid}}"
        X-Ts: "{{ $timestamp }}"
        X-Int: "{{$randomInt(-3, 3)}}"
        X-Str: "{{$randomString(12)}}"
load:
  stages: [{duration: 1s, target: 1}]
`
	p, err := compileYAML(t, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	env := newEnv(p)
	h := map[string]*Template{}
	for _, hd := range p.Steps[0].(*HTTPStep).Headers {
		h[hd.Name] = hd.Value
	}
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		u := h["X-Uuid"].String(env)
		if !uuidRe.MatchString(u) || seen[u] {
			t.Fatalf("bad uuid %s", u)
		}
		seen[u] = true
		n, err := strconv.Atoi(h["X-Int"].String(env))
		if err != nil || n < -3 || n > 3 {
			t.Fatalf("randomInt = %d %v", n, err)
		}
		s := h["X-Str"].String(env)
		if len(s) != 12 || !regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(s) {
			t.Fatalf("randomString = %q", s)
		}
	}
	ts, _ := strconv.ParseInt(h["X-Ts"].String(env), 10, 64)
	if d := time.Now().Unix() - ts; d < 0 || d > 2 {
		t.Fatalf("timestamp = %d", ts)
	}
	for _, bad := range []string{"{{$nope}}", "{{$randomInt(5,1)}}", "{{$randomString(0)}}", "{{$uuid(1)}}", "{{a", "{{}}"} {
		src := strings.Replace(`
steps: [{id: a, request: {url: "http://x/BAD"}}]
load: {stages: [{duration: 1s, target: 1}]}`, "BAD", bad, 1)
		if _, err := compileYAML(t, src, nil); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestVariableCycle(t *testing.T) {
	src := `
variables: {a: "{{b}}", b: "{{a}}"}
steps: [{id: s, request: {url: "http://x/{{a}}"}}]
load: {stages: [{duration: 1s, target: 1}]}`
	if _, err := compileYAML(t, src, nil); err == nil || !strings.Contains(err.Error(), "循环引用") {
		t.Fatalf("err = %v", err)
	}
}

func TestBodies(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "FILE")
	src := `
variables: {v: "x y"}
steps:
  - id: form
    request:
      method: POST
      url: http://x/
      body: {type: form, fields: {b: "{{v}}", a: "1&2"}}
  - id: multi
    request:
      method: POST
      url: http://x/
      body:
        type: multipart
        fields: {f: "{{v}}"}
        files: [{field: up, path: a.txt}]
  - id: raw
    request:
      method: POST
      url: http://x/
      headers: {Content-Type: text/plain}
      body: {type: raw, content: "hello {{v}}"}
load: {stages: [{duration: 1s, target: 1}]}`
	def, err := Parse([]byte(src), FormatYAML)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compile(def, CompileOptions{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	env := newEnv(p)
	form := p.Steps[0].(*HTTPStep)
	if got := form.Body.String(env); got != "a=1%262&b=x+y" || form.ContentType != "application/x-www-form-urlencoded" {
		t.Fatalf("form = %q %q", got, form.ContentType)
	}
	multi := p.Steps[1].(*HTTPStep)
	body := multi.Body.String(env)
	boundary := strings.TrimPrefix(multi.ContentType, "multipart/form-data; boundary=")
	if !strings.Contains(body, "--"+boundary+"\r\nContent-Disposition: form-data; name=\"f\"\r\n\r\nx y\r\n") ||
		!strings.Contains(body, `filename="a.txt"`) || !strings.Contains(body, "\r\n\r\nFILE\r\n") || !strings.HasSuffix(body, "--"+boundary+"--\r\n") {
		t.Fatalf("multipart = %q", body)
	}
	raw := p.Steps[2].(*HTTPStep)
	if raw.Body.String(env) != "hello x y" || raw.ContentType != "" {
		t.Fatal("raw body")
	}
}

func TestJSONPath(t *testing.T) {
	doc := []byte(` {"code": 0, "data": {"token": "abc\"é😀", "list": [ {"id": 1}, {"id": 2, "tags": ["x", "y"]} ],
	  "nested": {"a": {"b": [true, null, -1.5e3]}}, "esc\"key": 7, "empty": [], "obj": {"k": [1, {"z": "}"}]}}}`)
	cases := map[string]string{
		"$.code":                  "0",
		"$.data.token":            "abc\"é😀",
		"$.data.list[1].id":       "2",
		"$.data.list[-1].tags[0]": "x",
		"$['data']['list'][0]":    `{"id": 1}`,
		"data.nested.a.b[2]":      "-1.5e3",
		"$.data.nested.a.b[0]":    "true",
		"$.data.nested.a.b[1]":    "null",
		`$.data['esc"key']`:       "7",
		"$.data.obj.k[1].z":       "}",
		"$.data.obj":              `{"k": [1, {"z": "}"}]}`,
	}
	for expr, want := range cases {
		p, err := CompileJSONPath(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		raw, ok := p.Lookup(doc)
		if !ok {
			t.Errorf("%s: not found", expr)
			continue
		}
		got, ok := AppendJSONText(nil, raw)
		if !ok || string(got) != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
	for _, expr := range []string{"$.missing", "$.data.list[5]", "$.data.list[-3]", "$.code.x", "$.data.empty[0]", "$.data.token[0]"} {
		p, _ := CompileJSONPath(expr)
		if _, ok := p.Lookup(doc); ok {
			t.Errorf("%s found", expr)
		}
	}
	for _, bad := range []string{"", "$..x", "$.a[*]", "$.a[?(@.x)]", "$.", "$['a"} {
		if _, err := CompileJSONPath(bad); err == nil {
			t.Errorf("%q compiled", bad)
		}
	}
	p, _ := CompileJSONPath("$.a.b")
	for _, broken := range []string{`{"a": {"b": `, `{"a": `, `{"a" 1}`, `[1,2`, `{"a": {"x": "unterminated}}`} {
		if _, ok := p.Lookup([]byte(broken)); ok {
			t.Errorf("found in broken doc %s", broken)
		}
	}
}

func TestExtractAndAssert(t *testing.T) {
	src := `
steps:
  - id: a
    request: {url: "http://x/"}
    extract:
      - {name: tok, from: jsonpath, expr: $.data.token}
      - {name: num, from: regex, expr: 'id=(\d+)'}
      - {name: whole, from: regex, expr: 'id=\d+', group: 0}
      - {name: hdr, from: header, expr: x-trace}
      - {name: dflt, from: jsonpath, expr: $.nope, default: "none"}
      - {name: miss, from: jsonpath, expr: $.nope}
    assert:
      - {type: status, in: [200, 201]}
      - {type: duration, max: 1s}
      - {type: body_contains, value: "id="}
      - {type: jsonpath, expr: $.code, equals: 0}
      - {type: jsonpath, expr: $.ratio, equals: "1.0"}
      - {type: jsonpath, expr: $.data.token, contains: "bc"}
      - {type: jsonpath, expr: $.nope, exists: false}
load: {stages: [{duration: 1s, target: 1}]}`
	p, err := compileYAML(t, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := p.Steps[0].(*HTTPStep)
	r := &ResponseView{
		Status: 201, Duration: 10 * time.Millisecond,
		Body: []byte(`{"code":0,"ratio":1,"data":{"token":"abc"},"text":"id=42"}`),
		Header: func(name string) ([]byte, bool) {
			if name == "X-Trace" {
				return []byte("t-1"), true
			}
			return nil, false
		},
	}
	want := []string{"abc", "42", "id=42", "t-1", "none"}
	for i, w := range want {
		got, ok := st.Extract[i].Extract(nil, r)
		if !ok || string(got) != w {
			t.Errorf("extract %d = %q %v, want %q", i, got, ok, w)
		}
	}
	if _, ok := st.Extract[5].Extract(nil, r); ok {
		t.Error("missing value without default should fail")
	}
	for i, a := range st.Assert {
		if ok, msg := a.Check(r); !ok {
			t.Errorf("assert %d failed: %s", i, msg)
		}
	}
	r.Status, r.Duration, r.Body = 500, 2*time.Second, []byte(`{"code":1,"ratio":2,"data":{"token":"xyz"},"nope":1}`)
	for i, a := range st.Assert {
		if ok, msg := a.Check(r); ok || !strings.HasPrefix(msg, "断言失败") {
			t.Errorf("assert %d should fail, msg=%q", i, msg)
		}
	}
}

func TestThink(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	u := ThinkTime{Kind: ThinkUniform, Min: 10 * time.Millisecond, Max: 20 * time.Millisecond}
	for i := 0; i < 1000; i++ {
		if d := u.Duration(r); d < u.Min || d > u.Max {
			t.Fatalf("uniform think %v out of range", d)
		}
	}
	if (ThinkTime{Kind: ThinkFixed, Min: time.Second}).Duration(r) != time.Second || (ThinkTime{}).Duration(r) != 0 {
		t.Fatal("fixed/none think")
	}
}

func TestLoadValidation(t *testing.T) {
	base := `
steps: [{id: s, request: {url: "http://x/"}, think: {type: uniform, min: 1s, max: 3s}}]
load: LOAD`
	cases := map[string]string{
		`{mode: bogus, stages: [{duration: 1s, target: 1}]}`: `load.mode：取值 "bogus" 无效`,
		`{stages: []}`:                                                 `load.stages：至少需要一个阶段`,
		`{stages: [{duration: 0s, target: 5}]}`:                        `阶段总时长必须大于 0`,
		`{stages: [{duration: 1s, target: 0}]}`:                        `至少一个阶段的 target 要大于 0`,
		`{mode: vus, max_vus: 5, stages: [{duration: 1s, target: 1}]}`: `load.max_vus：只在 rate 模式下使用`,
	}
	for load, want := range cases {
		_, err := compileYAML(t, strings.Replace(base, "LOAD", load, 1), nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", load, err, want)
		}
	}
	p, err := compileYAML(t, strings.Replace(base, "LOAD", `{mode: rate, stages: [{duration: 0s, target: 100}, {duration: 10s, target: 100}]}`, 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	// 100 iterations/s, each lasting up to 3s think + 2s response time.
	if p.Load.MaxVUs != 500 || p.Load.PeakTarget != 100 {
		t.Fatalf("auto max vus = %d", p.Load.MaxVUs)
	}
}

func BenchmarkTemplateAppend(b *testing.B) {
	src := `
variables: {host: "http://127.0.0.1:18080"}
data: [{name: users, file: users.csv}]
steps:
  - id: login
    request:
      url: "{{host}}/login"
      body: {type: json, content: '{"user":"{{users.name}}","pwd":"{{users.pwd}}","id":"{{$uuid}}","n":{{$randomInt(1,100)}}}'}
    extract: [{name: token, from: jsonpath, expr: $.token}]
load: {stages: [{duration: 1s, target: 1}]}`
	dir := b.TempDir()
	os.WriteFile(filepath.Join(dir, "users.csv"), []byte("name,pwd\nalice,p1\n"), 0o644)
	def, _ := Parse([]byte(src), FormatYAML)
	p, err := Compile(def, CompileOptions{BaseDir: dir})
	if err != nil {
		b.Fatal(err)
	}
	env := newEnv(p)
	env.Rows[0] = p.Data[0].Rows[0]
	body := p.Steps[0].(*HTTPStep).Body
	buf := make([]byte, 0, 256)
	b.ReportAllocs()
	for b.Loop() {
		buf = body.Append(buf[:0], env)
	}
}

func BenchmarkJSONPathLookup(b *testing.B) {
	doc := []byte(`{"code":0,"msg":"ok","data":{"user":"alice","items":[1,2,3],"token":"abcdef0123456789"}}`)
	p, _ := CompileJSONPath("$.data.token")
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := p.Lookup(doc); !ok {
			b.Fatal("not found")
		}
	}
}
