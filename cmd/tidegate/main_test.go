package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
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
	out, csvPath := filepath.Join(dir, "r.json"), filepath.Join(dir, "r.csv")
	var stdout, stderr bytes.Buffer
	code := runMain([]string{"run", file, "--out", out, "--csv", csvPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, &stdout, &stderr)
	}
	text := stdout.String()
	for _, want := range []string{"场景：单接口压测（/ping）", "请求/秒", "P99", "上行", "========== 汇总 ==========", "按步骤", "ping 心跳", "200×", "已写出结果", "已写出 CSV"} {
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
