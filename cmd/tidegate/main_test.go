package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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
	if runMain(nil, &stdout, &stderr) != 2 || !strings.Contains(stderr.String(), "tidegate run") {
		t.Fatal("usage")
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
