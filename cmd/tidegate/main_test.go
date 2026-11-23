package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"tidegate/internal/auth"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
	"tidegate/internal/testserver"
)

func TestExamplesCompile(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*.yaml")
	if len(files) < 2 {
		t.Fatalf("expected at least two examples, found %v", files)
	}
	for _, f := range files {
		def, err := scenario.LoadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if _, err := scenario.Compile(def, scenario.CompileOptions{BaseDir: filepath.Dir(f)}); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
}

// writeExample copies an example scenario with the host replaced by srv
// and a shorter load profile.
func writeExample(t *testing.T, name, base string) string {
	t.Helper()
	src, err := os.ReadFile("../../examples/" + name)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(src), "http://127.0.0.1:18080", base, 1)
	s = strings.Replace(s, "duration: 10s", "duration: 1500ms", 1)
	s = strings.Replace(s, "duration: 2s ", "duration: 200ms ", 1)
	s = strings.Replace(s, "duration: 3s\n", "duration: 300ms\n", 1)
	dir := t.TempDir()
	csvData, _ := os.ReadFile("../../examples/users.csv")
	os.WriteFile(filepath.Join(dir, "users.csv"), csvData, 0o644)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func startServer(t *testing.T) *testserver.Server {
	srv, err := testserver.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	t.Cleanup(func() { srv.Close() })
	return srv
}

func TestRunPingWithOutputs(t *testing.T) {
	srv := startServer(t)
	file := writeExample(t, "ping.yaml", srv.URL())
	dir := t.TempDir()
	out, csvPath, htmlPath, jtlPath := filepath.Join(dir, "r.json"), filepath.Join(dir, "r.csv"), filepath.Join(dir, "r.html"), filepath.Join(dir, "r.jtl")
	var stdout, stderr bytes.Buffer
	code := runMain([]string{"run", file, "--out", out, "--csv", csvPath, "--html", htmlPath, "--jtl", jtlPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, &stdout, &stderr)
	}
	text := stdout.String()
	for _, want := range []string{"场景：单接口压测（/ping）", "请求/秒", "P99", "上行", "========== 汇总 ==========", "按步骤", "ping 心跳", "200×", "已写出结果", "已写出 CSV", "已写出 HTML 报告", "已写出 JTL 日志"} {
		if !strings.Contains(text, want) {
			t.Errorf("stdout lacks %q:\n%s", want, text)
		}
	}

	var res struct {
		Scenario  string
		Mode      string
		Client    string
		Snapshots []struct {
			Seq       int64   `json:"seq"`
			Requests  int64   `json:"requests"`
			RPS       float64 `json:"rps"`
			ActiveVUs int64   `json:"active_vus"`
			Latency   struct {
				P50 float64 `json:"p50_ms"`
				P99 float64 `json:"p99_ms"`
			} `json:"latency"`
		}
		Summary struct {
			Requests      int64            `json:"requests"`
			Errors        int64            `json:"errors"`
			BytesSent     int64            `json:"bytes_sent"`
			BytesReceived int64            `json:"bytes_received"`
			Status        map[string]int64 `json:"status"`
			Steps         []struct {
				ID       string `json:"id"`
				Requests int64  `json:"requests"`
			} `json:"steps"`
		}
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatal(err)
	}
	if res.Scenario != "单接口压测（/ping）" || res.Mode != "vus" || res.Client != "fast" {
		t.Fatalf("header = %+v", res)
	}
	var sum int64
	for i, s := range res.Snapshots {
		if s.Seq != int64(i) {
			t.Fatalf("snapshot %d has seq %d", i, s.Seq)
		}
		sum += s.Requests
	}
	if len(res.Snapshots) < 2 || sum != res.Summary.Requests || res.Summary.Requests == 0 {
		t.Fatalf("%d snapshots, sum %d, summary %d", len(res.Snapshots), sum, res.Summary.Requests)
	}
	if res.Summary.Errors != 0 || res.Summary.Status["200"] != res.Summary.Requests || res.Summary.BytesReceived == 0 {
		t.Fatalf("summary = %+v", res.Summary)
	}
	if len(res.Summary.Steps) != 1 || res.Summary.Steps[0].ID != "ping" || res.Summary.Steps[0].Requests != res.Summary.Requests {
		t.Fatalf("steps = %+v", res.Summary.Steps)
	}

	f, _ := os.Open(csvPath)
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(res.Snapshots)+1 || rows[0][0] != "seq" || rows[0][9] != "requests" || rows[0][13] != "p50_ms" {
		t.Fatalf("csv has %d rows, header %v", len(rows), rows[0])
	}
	var csvSum int64
	for _, r := range rows[1:] {
		n, _ := strconv.ParseInt(r[9], 10, 64)
		csvSum += n
	}
	if csvSum != sum {
		t.Fatalf("csv requests %d != %d", csvSum, sum)
	}
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(htmlBytes), "<!DOCTYPE html>") || !strings.Contains(string(htmlBytes), "单接口压测（/ping）") {
		t.Fatalf("unexpected html report content")
	}
	jf, err := os.Open(jtlPath)
	if err != nil {
		t.Fatal(err)
	}
	defer jf.Close()
	jtlRows, err := csv.NewReader(jf).ReadAll()
	if err != nil || len(jtlRows) < 2 || len(jtlRows[0]) != 17 || jtlRows[0][0] != "timeStamp" {
		t.Fatalf("unexpected JTL rows (%d): %+v err=%v", len(jtlRows), jtlRows, err)
	}
}

func TestRunMultiStepExample(t *testing.T) {
	srv := startServer(t)
	file := writeExample(t, "login-query.yaml", srv.URL())
	var stdout, stderr bytes.Buffer
	code := runMain([]string{"run", "--quiet", file, "--client", "std"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, &stdout, &stderr)
	}
	text := stdout.String()
	if strings.Contains(text, "请求/秒") {
		t.Fatal("--quiet still printed per-second lines")
	}
	if !strings.Contains(text, "login 登录") || !strings.Contains(text, "query 查询列表") || !strings.Contains(text, "错误      0（0.00%）") {
		t.Fatalf("summary:\n%s", text)
	}
}

func TestRunReportsValidationErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte(`
steps:
  - id: q
    request:
      url: "http://x/{{tokn}}"
load: {stages: [{duration: 1s, target: 1}]}
`), 0o644)
	var stdout, stderr bytes.Buffer
	if code := runMain([]string{"run", bad}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), `步骤 "q"（第 1 步）的 request.url：引用了未定义的变量 "tokn"`) {
		t.Fatalf("stderr = %s", &stderr)
	}
	if code := runMain([]string{"run", filepath.Join(dir, "missing.yaml")}, &stdout, &stderr); code != 1 {
		t.Fatalf("missing file exit %d", code)
	}
	if code := runMain([]string{"run"}, &stdout, &stderr); code != 2 {
		t.Fatalf("no file exit %d", code)
	}
}

func TestVersionAndUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if runMain([]string{"version"}, &stdout, &stderr) != 0 || !strings.HasPrefix(stdout.String(), "tidegate ") {
		t.Fatalf("version = %q", &stdout)
	}
	stdout.Reset()
	if runMain([]string{"--version"}, &stdout, &stderr) != 0 || !strings.HasPrefix(stdout.String(), "tidegate ") {
		t.Fatalf("--version = %q", &stdout)
	}
	stdout.Reset()
	if runMain([]string{"help"}, &stdout, &stderr) != 0 || !strings.Contains(stdout.String(), "tidegate serve") || !strings.Contains(stdout.String(), "tidegate run") {
		t.Fatalf("help stdout = %q", &stdout)
	}
	stdout.Reset()
	if runMain([]string{"--help"}, &stdout, &stderr) != 0 || !strings.Contains(stdout.String(), "tidegate serve") {
		t.Fatalf("--help stdout = %q", &stdout)
	}
	for _, sub := range []string{"serve", "agent", "reset-password"} {
		stdout.Reset()
		stderr.Reset()
		if code := runMain([]string{sub, "--help"}, &stdout, &stderr); code != 0 {
			t.Fatalf("%s --help exit %d\nstderr=%s", sub, code, &stderr)
		}
	}
	if runMain([]string{"bogus"}, &stdout, &stderr) != 2 {
		t.Fatal("unknown command")
	}
}

func TestFormatting(t *testing.T) {
	if formatInt(1234567) != "1,234,567" || formatInt(-1000) != "-1,000" || formatInt(12) != "12" {
		t.Fatal("formatInt")
	}
	if formatBits(12_345_678) != "12.3 Mbps" || formatBytes(1536) != "1.5 KB" || formatMs(0.25) != "0.25ms" || formatMs(1500) != "1.50s" {
		t.Fatal("formatting")
	}
	if displayWidth("并发") != 4 || padLeft("并发", 6) != "  并发" {
		t.Fatal("display width")
	}
}

func TestProfileFlagsAndStats(t *testing.T) {
	srv := startServer(t)
	file := writeExample(t, "ping.yaml", srv.URL())
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "trace.out")
	var stdout, stderr bytes.Buffer
	code := runMain([]string{"run", "--quiet", "--trace", tracePath, "--trace-for", "50ms", file}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, &stderr)
	}
	info, err := os.Stat(tracePath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("trace file missing or empty: info=%v err=%v", info, err)
	}
	st := srv.TakeStats()
	if st.Accepted == 0 {
		t.Fatalf("expected accepted connections on testserver, got %+v", st)
	}
}

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newInsecureHTTPSClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar:     jar,
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func TestServeWithTLSAndPasswordFileAndResetPassword(t *testing.T) {
	dataDir := t.TempDir()
	pwPath := filepath.Join(t.TempDir(), "admin.pw")
	const initPass = "initial admin passphrase 2026"
	if err := os.WriteFile(pwPath, []byte(initPass+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(auth.AdminPasswordFileEnv, pwPath)

	var stdout, stderr syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type readyInfo struct {
		url string
	}
	readyCh := make(chan readyInfo, 1)
	doneCh := make(chan int, 1)
	go func() {
		doneCh <- serveWithContext(ctx, serveConfig{
			DataDir:       dataDir,
			Listen:        "127.0.0.1:0",
			ClusterListen: "127.0.0.1:0",
			TLSSelfSigned: true,
			NoLocalAgent:  true,
			NoOpen:        true,
		}, &stdout, &stderr, func(httpURL, _ string) {
			readyCh <- readyInfo{url: httpURL}
		})
	}()

	var baseURL string
	select {
	case info := <-readyCh:
		baseURL = info.url
	case code := <-doneCh:
		t.Fatalf("serve exited early with %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for serveWithContext ready")
	}

	if !strings.HasPrefix(baseURL, "https://") {
		t.Fatalf("expected https URL, got %q", baseURL)
	}
	cl := newInsecureHTTPSClient(t)

	resp, err := cl.Get(baseURL + "/api/v1/setup/status")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		SetupRequired bool `json:"setup_required"`
		TLS           bool `json:"tls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if st.SetupRequired || !st.TLS {
		t.Fatalf("setup/status mismatch: %+v", st)
	}

	loginBody, _ := json.Marshal(map[string]string{
		"username": auth.DefaultAdminUsername,
		"password": initPass,
	})
	req, _ := http.NewRequest("POST", baseURL+"/api/v1/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.RequestHeader, "1")
	resp, err = cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", resp.StatusCode)
	}

	resp, err = cl.Get(baseURL + "/api/v1/projects")
	if err != nil {
		t.Fatal(err)
	}
	var projs []struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&projs)
	resp.Body.Close()
	if len(projs) != 1 || projs[0].Name != "默认项目" {
		t.Fatalf("expected default project, got %+v", projs)
	}

	cancel()
	if code := <-doneCh; code != 0 {
		t.Fatalf("serve exit code = %d\nstderr=%s", code, stderr.String())
	}

	// Verify reset-password CLI against the persisted database.
	var rOut, rErr bytes.Buffer
	const newPass = "reset admin passphrase 2026!"
	rCode := runMainWithIO(
		[]string{"reset-password", "--data", dataDir, "--user", auth.DefaultAdminUsername},
		strings.NewReader(newPass+"\n"),
		&rOut,
		&rErr,
	)
	if rCode != 0 {
		t.Fatalf("reset-password exit %d\nstderr=%s", rCode, rErr.String())
	}
	if !strings.Contains(rOut.String(), "已重置用户") {
		t.Fatalf("unexpected reset-password stdout: %s", rOut.String())
	}

	// Confirm the new password actually works after server restart.
	t.Setenv(auth.AdminPasswordFileEnv, "")
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	readyCh2 := make(chan readyInfo, 1)
	doneCh2 := make(chan int, 1)
	go func() {
		doneCh2 <- serveWithContext(ctx2, serveConfig{
			DataDir:       dataDir,
			Listen:        "127.0.0.1:0",
			ClusterListen: "127.0.0.1:0",
			NoLocalAgent:  true,
			NoOpen:        true,
		}, &stdout, &stderr, func(httpURL, _ string) {
			readyCh2 <- readyInfo{url: httpURL}
		})
	}()
	baseURL2 := (<-readyCh2).url
	cl2 := newInsecureHTTPSClient(t)

	badBody, _ := json.Marshal(map[string]string{"username": auth.DefaultAdminUsername, "password": initPass})
	badReq, _ := http.NewRequest("POST", baseURL2+"/api/v1/auth/login", bytes.NewReader(badBody))
	badReq.Header.Set("Content-Type", "application/json")
	badReq.Header.Set(auth.RequestHeader, "1")
	resp, err = cl2.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password should fail with 401, got %d", resp.StatusCode)
	}

	okBody, _ := json.Marshal(map[string]string{"username": auth.DefaultAdminUsername, "password": newPass})
	okReq, _ := http.NewRequest("POST", baseURL2+"/api/v1/auth/login", bytes.NewReader(okBody))
	okReq.Header.Set("Content-Type", "application/json")
	okReq.Header.Set(auth.RequestHeader, "1")
	resp, err = cl2.Do(okReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("new password login status = %d", resp.StatusCode)
	}

	cancel2()
	if code := <-doneCh2; code != 0 {
		t.Fatalf("second serve exit %d", code)
	}
}

func TestServeWithRealAgentSubprocess(t *testing.T) {
	binName := "tidegate"
	if runtime.GOOS == "windows" {
		binName = "tidegate.exe"
	}
	binPath := filepath.Join(t.TempDir(), binName)
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}

	t.Setenv(auth.AdminPasswordFileEnv, "")
	dataDir := t.TempDir()
	var stdout, stderr syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readyCh := make(chan string, 1)
	doneCh := make(chan int, 1)
	go func() {
		doneCh <- serveWithContext(ctx, serveConfig{
			DataDir:        dataDir,
			Listen:         "127.0.0.1:0",
			ClusterListen:  "127.0.0.1:0",
			NoLocalAgent:   false,
			NoOpen:         true,
			ExecutablePath: binPath,
		}, &stdout, &stderr, func(httpURL, _ string) {
			readyCh <- httpURL
		})
	}()

	var baseURL string
	select {
	case baseURL = <-readyCh:
	case code := <-doneCh:
		t.Fatalf("serve exited early with %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for serve")
	}

	var setupCode string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimRight(line, "\r")
		if v, ok := strings.CutPrefix(line, "  初始化码："); ok {
			setupCode = strings.TrimSpace(v)
			break
		}
	}
	if setupCode == "" {
		t.Fatalf("did not find setup code in stdout:\n%s", stdout.String())
	}

	cl := newInsecureHTTPSClient(t)
	setupBody, _ := json.Marshal(map[string]string{
		"code":     setupCode,
		"username": "admin",
		"password": "subproc password 123",
	})
	setupReq, _ := http.NewRequest("POST", baseURL+"/api/v1/setup", bytes.NewReader(setupBody))
	setupReq.Header.Set("Content-Type", "application/json")
	setupReq.Header.Set(auth.RequestHeader, "1")
	resp, err := cl.Do(setupReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup status = %d", resp.StatusCode)
	}

	deadline := time.Now().Add(10 * time.Second)
	foundLocal := false
	for time.Now().Before(deadline) {
		resp, err := cl.Get(baseURL + "/api/v1/agents")
		if err != nil {
			t.Fatal(err)
		}
		var agents []struct {
			Name string `json:"name"`
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err := json.Unmarshal(b, &agents); err == nil && len(agents) >= 1 && agents[0].Name == "local" {
			foundLocal = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !foundLocal {
		t.Fatalf("local agent subprocess did not register within timeout\nstderr:\n%s", stderr.String())
	}

	cancel()
	if code := <-doneCh; code != 0 {
		t.Fatalf("serve exit code = %d\nstderr=%s", code, stderr.String())
	}
}

func TestRunThresholdsAndExitCode(t *testing.T) {
	srv := startServer(t)
	dir := t.TempDir()

	writeScen := func(name, thresholdsYAML, dur string) string {
		p := filepath.Join(dir, name)
		src := `version: 1
name: Threshold CLI Test
steps:
  - id: ping
    name: ping
    request:
      method: GET
      url: ` + srv.URL() + `/ping
load:
  mode: vus
  stages:
    - duration: ` + dur + `
      target: 1
` + thresholdsYAML
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// 1. Passing thresholds exit 0 and write threshold card to HTML report.
	passFile := writeScen("pass.yaml", `  thresholds:
    - metric: p99_ms
      op: "<="
      value: 5000
    - metric: error_rate
      value: 0.05
`, "300ms")
	htmlPath := filepath.Join(dir, "pass.html")
	var out1, err1 bytes.Buffer
	code := runMain([]string{"run", passFile, "--quiet", "--html", htmlPath}, &out1, &err1)
	if code != 0 {
		t.Fatalf("passing threshold exit = %d\nstdout:\n%s\nstderr:\n%s", code, &out1, &err1)
	}
	if !strings.Contains(out1.String(), "阈值校验（全部通过）") {
		t.Fatalf("expected passing threshold summary, got:\n%s", &out1)
	}
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(htmlBytes), "阈值评估") || !strings.Contains(string(htmlBytes), "全部通过") {
		t.Fatalf("expected threshold card in HTML report")
	}

	// 2. Failing threshold returns exit code 3.
	failFile := writeScen("fail.yaml", `  thresholds:
    - metric: rps
      op: ">="
      value: 999999999
`, "300ms")
	var out2, err2 bytes.Buffer
	code = runMain([]string{"run", failFile, "--quiet"}, &out2, &err2)
	if code != 3 {
		t.Fatalf("failing threshold exit = %d, want 3\nstdout:\n%s\nstderr:\n%s", code, &out2, &err2)
	}
	if !strings.Contains(out2.String(), "阈值校验（未达标）") {
		t.Fatalf("expected failing threshold summary, got:\n%s", &out2)
	}

	// 3. AbortOnFail interrupts a longer run early and exits 3.
	abortPath := filepath.Join(dir, "abort.yaml")
	abortYAML := `version: 1
name: Threshold Abort Test
steps:
  - id: ping
    name: ping
    request:
      method: GET
      url: ` + srv.URL() + `/ping
load:
  mode: vus
  stages:
    - duration: 0s
      target: 2
    - duration: 10s
      target: 2
  thresholds:
    - metric: rps
      op: ">="
      value: 999999999
      abort_on_fail: true
      grace_period: 200ms
`
	if err := os.WriteFile(abortPath, []byte(abortYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var out3, err3 bytes.Buffer
	code = runMain([]string{"run", abortPath, "--quiet"}, &out3, &err3)
	elapsed := time.Since(start)
	if code != 3 {
		t.Fatalf("abort threshold exit = %d, want 3\nstdout:\n%s\nstderr:\n%s", code, &out3, &err3)
	}
	if elapsed >= 4*time.Second {
		t.Fatalf("abort_on_fail took %v (expected early stop < 4s)", elapsed)
	}
	if !strings.Contains(err3.String(), "触发中止阈值") {
		t.Fatalf("expected abort message on stderr, got:\n%s", &err3)
	}
}

func TestCLICollectionTestAndJUnit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		case "/fail":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	dataDir := t.TempDir()
	st, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	pr, err := st.CreateProject(&store.Project{Name: "CLITestProj"})
	if err != nil {
		t.Fatal(err)
	}
	fPass, err := st.CreateFolder(pr.ID, &store.Folder{Name: "PassSuite"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPI(pr.ID, &store.API{
		Name:     "HealthOK",
		FolderID: fPass.ID,
		Request:  store.APIRequest{Method: "GET", URL: ts.URL + "/ok"},
		PostScript: `assert(resp.json("$.status") == "ok", "health status ok");`,
	}); err != nil {
		t.Fatal(err)
	}

	fFail, err := st.CreateFolder(pr.ID, &store.Folder{Name: "FailSuite"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPI(pr.ID, &store.API{
		Name:     "ServerFail",
		FolderID: fFail.ID,
		Request:  store.APIRequest{Method: "GET", URL: ts.URL + "/fail"},
	}); err != nil {
		t.Fatal(err)
	}

	junitPass := filepath.Join(t.TempDir(), "pass-junit.xml")
	var out1, err1 bytes.Buffer
	code := runMain([]string{"test", dataDir, "--folder", "PassSuite", "--junit", junitPass}, &out1, &err1)
	if code != 0 {
		t.Fatalf("expected exit 0 on passing suite, got %d\nstdout=%s\nstderr=%s", code, &out1, &err1)
	}
	xmlBytes, err := os.ReadFile(junitPass)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Tests    int `xml:"tests,attr"`
		Failures int `xml:"failures,attr"`
	}
	if err := xml.Unmarshal(xmlBytes, &parsed); err != nil {
		t.Fatalf("invalid junit xml: %v\n%s", err, xmlBytes)
	}
	if parsed.Tests != 1 || parsed.Failures != 0 {
		t.Fatalf("unexpected passing junit summary: %+v", parsed)
	}

	junitFail := filepath.Join(t.TempDir(), "fail-junit.xml")
	var out2, err2 bytes.Buffer
	code = runMain([]string{"test", dataDir, "--folder", "FailSuite", "--junit", junitFail}, &out2, &err2)
	if code != 3 {
		t.Fatalf("expected exit 3 on failing suite, got %d\nstdout=%s\nstderr=%s", code, &out2, &err2)
	}
	failXML, err := os.ReadFile(junitFail)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(failXML), `<failure`) {
		t.Fatalf("expected <failure> element in junit xml:\n%s", failXML)
	}
}

func TestResetPasswordCLIAndServeAdminEnvSync(t *testing.T) {
	dataDir := t.TempDir()
	var out, errBuf bytes.Buffer
	code := runMainWithIO([]string{
		"reset-password",
		"--data", dataDir,
		"--user", "opsadmin",
		"--password", "FirstPass-2026!",
	}, strings.NewReader(""), &out, &errBuf)
	if code != 0 {
		t.Fatalf("reset-password create exit %d: stdout=%s stderr=%s", code, out.String(), errBuf.String())
	}

	// Start serve with TIDEGATE_ADMIN_PASSWORD set to a new value + PUBLIC_URL + MASTER_KEY,
	// verifying declarative sync updates the existing user's password on boot.
	t.Setenv("TIDEGATE_ADMIN_USER", "opsadmin")
	t.Setenv("TIDEGATE_ADMIN_PASSWORD", "SecondPass-2026!")
	t.Setenv("TIDEGATE_MASTER_KEY", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	t.Setenv("TIDEGATE_PUBLIC_URL", "https://tidegate.localtest.me")
	t.Setenv("TIDEGATE_TRUSTED_PROXIES", "loopback")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyURL := make(chan string, 1)
	done := make(chan int, 1)
	var serveOut, serveErr bytes.Buffer
	go func() {
		done <- cmdServeWithEnv(ctx, dataDir, &serveOut, &serveErr, func(httpURL, _ string) {
			readyURL <- httpURL
		})
	}()

	var base string
	select {
	case base = <-readyURL:
	case <-time.After(10 * time.Second):
		t.Fatalf("serve did not become ready; stdout=%s stderr=%s", serveOut.String(), serveErr.String())
	}

	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 5 * time.Second}

	// Old password must fail; synced env password must succeed.
	loginWith := func(pwd string) int {
		req, _ := http.NewRequest("POST", base+"/api/v1/auth/login", strings.NewReader(`{"username":"opsadmin","password":`+strconv.Quote(pwd)+`}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Tidegate-Request", "1")
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if st := loginWith("FirstPass-2026!"); st != 401 {
		t.Fatalf("old password status = %d, want 401", st)
	}
	if st := loginWith("SecondPass-2026!"); st != 200 {
		t.Fatalf("synced env password status = %d, want 200", st)
	}

	// system/info reflects public_url and resolved non-wildcard cluster_addr.
	resp, err := cl.Get(base + "/api/v1/system/info")
	if err != nil {
		t.Fatal(err)
	}
	var sysInfo map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&sysInfo)
	resp.Body.Close()
	if sysInfo["public_url"] != "https://tidegate.localtest.me" {
		t.Fatalf("expected public_url in system/info, got %+v", sysInfo)
	}

	cancel()
	if exitCode := <-done; exitCode != 0 {
		t.Fatalf("serve exit = %d stderr=%s", exitCode, serveErr.String())
	}
}

func cmdServeWithEnv(ctx context.Context, dataDir string, stdout, stderr io.Writer, onReady func(string, string)) int {
	return serveWithContext(ctx, serveConfig{
		DataDir:           dataDir,
		Listen:            "127.0.0.1:0",
		ClusterListen:     "127.0.0.1:0",
		PublicURL:         os.Getenv("TIDEGATE_PUBLIC_URL"),
		TrustedProxies:    splitCSVList(os.Getenv("TIDEGATE_TRUSTED_PROXIES")),
		MasterKey:         os.Getenv("TIDEGATE_MASTER_KEY"),
		AdminUser:         os.Getenv(auth.AdminUserEnv),
		AdminPassword:     os.Getenv(auth.AdminPasswordEnv),
		AdminPasswordFile: os.Getenv(auth.AdminPasswordFileEnv),
		NoLocalAgent:      true,
		NoOpen:            true,
	}, stdout, stderr, onReady)
}
