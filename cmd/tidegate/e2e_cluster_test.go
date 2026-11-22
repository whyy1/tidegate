package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"tidegate/internal/auth"
)

type lineScannerProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
	done  chan error
}

func startManagedProc(t *testing.T, ctx context.Context, bin string, watchStdin bool, args ...string) *lineScannerProc {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	if watchStdin {
		cmd.Env = append(cmd.Environ(), "TIDEGATE_WATCH_STDIN=1")
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", args, err)
	}

	p := &lineScannerProc{
		cmd:   cmd,
		stdin: stdin,
		lines: make(chan string, 128),
		done:  make(chan error, 1),
	}
	readStream := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 16*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			select {
			case p.lines <- line:
			default:
			}
		}
	}
	go readStream(stdout)
	go readStream(stderr)
	go func() {
		p.done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		p.stop()
	})
	return p
}

func (p *lineScannerProc) stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.stdin.Close()
	select {
	case <-p.done:
		return
	case <-time.After(1500 * time.Millisecond):
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
	}
}

func apiDo(t *testing.T, cl *http.Client, method, url, bearer string, body any) (int, http.Header, []byte) {
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
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	} else if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set(auth.RequestHeader, "1")
	}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, raw
}

func mustJSONStatus(t *testing.T, cl *http.Client, method, url string, want int, body any, out any) {
	t.Helper()
	code, _, raw := apiDo(t, cl, method, url, "", body)
	if code != want {
		t.Fatalf("%s %s status=%d (want %d): %s", method, url, code, want, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s decode: %v\nraw=%s", method, url, err, raw)
		}
	}
}

func waitAgentCount(t *testing.T, cl *http.Client, baseURL string, want int, timeout time.Duration) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []map[string]any
	for time.Now().Before(deadline) {
		code, _, raw := apiDo(t, cl, http.MethodGet, baseURL+"/api/v1/agents", "", nil)
		if code == http.StatusOK {
			var list []map[string]any
			if json.Unmarshal(raw, &list) == nil {
				last = list
				if len(list) == want {
					return list
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d agents; last=%+v", want, last)
	return nil
}

func TestE2ELive3AgentClusterAndWebAPI(t *testing.T) {
	binName := "tidegate"
	if runtime.GOOS == "windows" {
		binName = "tidegate.exe"
	}
	binPath := filepath.Join(t.TempDir(), binName)
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build binary: %v\n%s", err, out)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Start real `tidegate testserver` process on loopback ephemeral port.
	tsProc := startManagedProc(t, ctx, binPath, false, "testserver", "--addr", "127.0.0.1:0")
	var targetURL string
	tsDeadline := time.After(10 * time.Second)
	for targetURL == "" {
		select {
		case line := <-tsProc.lines:
			if v, ok := strings.CutPrefix(line, "测试服务已启动："); ok {
				targetURL = strings.TrimSpace(v)
			}
		case err := <-tsProc.done:
			t.Fatalf("testserver exited early: %v", err)
		case <-tsDeadline:
			t.Fatal("timed out waiting for testserver URL")
		}
	}

	// 2. Start real `tidegate serve --no-local-agent --no-open --tls-self-signed` process.
	t.Setenv(auth.AdminPasswordFileEnv, "")
	dataDir := t.TempDir()
	srvProc := startManagedProc(
		t,
		ctx,
		binPath,
		false,
		"serve",
		"--data", dataDir,
		"--listen", "127.0.0.1:0",
		"--cluster-listen", "127.0.0.1:0",
		"--no-local-agent",
		"--no-open",
		"--tls-self-signed",
	)
	var baseURL, clusterAddr, setupCode string
	srvAddrRe := regexp.MustCompile(`访问地址\s+(https://[^/]+)/（集群端口\s+([^）]+)）`)
	srvDeadline := time.After(15 * time.Second)
	for baseURL == "" || clusterAddr == "" || setupCode == "" {
		select {
		case line := <-srvProc.lines:
			if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "  初始化码："); ok {
				setupCode = strings.TrimSpace(v)
			}
			if m := srvAddrRe.FindStringSubmatch(line); len(m) == 3 {
				baseURL = m[1]
				clusterAddr = m[2]
			}
		case err := <-srvProc.done:
			t.Fatalf("serve exited early: %v", err)
		case <-srvDeadline:
			t.Fatalf("timed out waiting for serve startup (base=%q cluster=%q setup=%q)", baseURL, clusterAddr, setupCode)
		}
	}

	adminCl := newInsecureHTTPSClient(t)

	// 3. Verify Web UI HTML & strict CSP headers.
	code, hdrs, uiHTML := apiDo(t, adminCl, http.MethodGet, baseURL+"/", "", nil)
	if code != http.StatusOK || !strings.Contains(string(uiHTML), "Tidegate") {
		t.Fatalf("GET / status=%d body=%s", code, uiHTML)
	}
	if csp := hdrs.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("expected CSP header containing default-src 'self', got %q", csp)
	}

	// 4. Complete initial admin setup, create member + viewer users, verify viewer RBAC guard.
	mustJSONStatus(t, adminCl, http.MethodPost, baseURL+"/api/v1/setup", http.StatusCreated, map[string]string{
		"code":     setupCode,
		"username": "admin",
		"password": "e2e-admin-password-123",
	}, nil)

	mustJSONStatus(t, adminCl, http.MethodPost, baseURL+"/api/v1/users", http.StatusCreated, map[string]string{
		"username": "e2e-viewer",
		"password": "e2e-viewer-password-123",
		"role":     "viewer",
	}, nil)

	viewerCl := newInsecureHTTPSClient(t)
	mustJSONStatus(t, viewerCl, http.MethodPost, baseURL+"/api/v1/auth/login", http.StatusOK, map[string]string{
		"username": "e2e-viewer",
		"password": "e2e-viewer-password-123",
	}, nil)
	var projects []struct {
		ID string `json:"id"`
	}
	mustJSONStatus(t, viewerCl, http.MethodGet, baseURL+"/api/v1/projects", http.StatusOK, nil, &projects)
	if len(projects) == 0 {
		t.Fatal("expected default project")
	}
	pid := projects[0].ID

	// Viewer must be blocked (403) on write mutations.
	vCode, _, _ := apiDo(t, viewerCl, http.MethodPost, baseURL+"/api/v1/projects", "", map[string]string{"name": "forbidden"})
	if vCode != http.StatusForbidden {
		t.Fatalf("viewer POST /projects status=%d, want 403", vCode)
	}

	// 5. Personal API token (tg_) Bearer auth for /api/v1/metrics/prometheus.
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	mustJSONStatus(t, adminCl, http.MethodPost, baseURL+"/api/v1/auth/tokens", http.StatusCreated, map[string]any{
		"name":     "ci-scraper",
		"role":     "admin",
		"ttl_days": 7,
	}, &tokResp)
	if !strings.HasPrefix(tokResp.Plaintext, "tg_") {
		t.Fatalf("unexpected token plaintext: %q", tokResp.Plaintext)
	}
	promClient := newInsecureHTTPSClient(t)
	promClient.Jar = nil
	pCode, _, pBody := apiDo(t, promClient, http.MethodGet, baseURL+"/api/v1/metrics/prometheus", tokResp.Plaintext, nil)
	if pCode != http.StatusOK || !strings.Contains(string(pBody), "tidegate_") {
		t.Fatalf("GET /metrics/prometheus status=%d body=%s", pCode, pBody)
	}

	// 6. Verify CA fingerprint + PEM download, mint 3 single-use join tokens, start 3 real agent OS processes.
	var sysInfo struct {
		ClusterCAFingerprint string `json:"cluster_ca_fingerprint"`
	}
	mustJSONStatus(t, adminCl, http.MethodGet, baseURL+"/api/v1/system/info", http.StatusOK, nil, &sysInfo)
	if len(sysInfo.ClusterCAFingerprint) != 64 {
		t.Fatalf("unexpected CA fingerprint: %q", sysInfo.ClusterCAFingerprint)
	}
	caCode, caHdr, caPEM := apiDo(t, adminCl, http.MethodGet, baseURL+"/api/v1/agents/ca?format=pem", "", nil)
	if caCode != http.StatusOK || !strings.Contains(caHdr.Get("Content-Type"), "x-pem-file") {
		t.Fatalf("GET /agents/ca?format=pem code=%d ct=%s", caCode, caHdr.Get("Content-Type"))
	}
	if block, _ := pem.Decode(caPEM); block == nil {
		t.Fatalf("invalid CA PEM returned")
	}

	issueJoin := func(name string) string {
		var jt struct {
			Token string `json:"token"`
		}
		mustJSONStatus(t, adminCl, http.MethodPost, baseURL+"/api/v1/agents/join-tokens", http.StatusCreated, map[string]any{
			"name":       name,
			"ttl_hours":  1,
			"single_use": true,
		}, &jt)
		return jt.Token
	}

	certDir1 := filepath.Join(t.TempDir(), "certs1")
	certDir2 := filepath.Join(t.TempDir(), "certs2")
	certDir3 := filepath.Join(t.TempDir(), "certs3")

	ag1Proc := startManagedProc(t, ctx, binPath, true, "agent",
		"--join", clusterAddr, "--token", issueJoin("agent-1"),
		"--ca-fingerprint", sysInfo.ClusterCAFingerprint,
		"--cert-dir", certDir1, "--labels", "zone=a,tier=edge", "--name", "agent-1")
	ag2Proc := startManagedProc(t, ctx, binPath, true, "agent",
		"--join", clusterAddr, "--token", issueJoin("agent-2"),
		"--ca-fingerprint", sysInfo.ClusterCAFingerprint,
		"--cert-dir", certDir2, "--labels", "zone=a,tier=edge", "--name", "agent-2")
	_ = startManagedProc(t, ctx, binPath, true, "agent",
		"--join", clusterAddr, "--token", issueJoin("agent-3"),
		"--ca-fingerprint", sysInfo.ClusterCAFingerprint,
		"--cert-dir", certDir3, "--labels", "zone=b,tier=edge", "--name", "agent-3")

	agentsList := waitAgentCount(t, adminCl, baseURL, 3, 15*time.Second)

	// 7. Persist-cert reconnect test: stop agent-2, then restart WITHOUT --token using --cert-dir.
	ag2Proc.stop()
	waitAgentCount(t, adminCl, baseURL, 2, 10*time.Second)
	_ = startManagedProc(t, ctx, binPath, true, "agent",
		"--join", clusterAddr,
		"--ca-fingerprint", sysInfo.ClusterCAFingerprint,
		"--cert-dir", certDir2, "--labels", "zone=a,tier=edge", "--name", "agent-2")
	agentsList = waitAgentCount(t, adminCl, baseURL, 3, 15*time.Second)

	idByName := map[string]string{}
	for _, ag := range agentsList {
		idByName[fmt.Sprint(ag["name"])] = fmt.Sprint(ag["id"])
	}

	// Set custom weights: agent-1 weight 2.0, agent-2 weight 1.0.
	mustJSONStatus(t, adminCl, http.MethodPatch, baseURL+"/api/v1/agents/"+idByName["agent-1"], http.StatusOK, map[string]any{"weight": 2.0}, nil)
	mustJSONStatus(t, adminCl, http.MethodPatch, baseURL+"/api/v1/agents/"+idByName["agent-2"], http.StatusOK, map[string]any{"weight": 1.0}, nil)

	// 8. Import JMeter .jmx preview + apply, debug imported API, create scenario & export k6.
	jmxContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<jmeterTestPlan version="1.2" properties="5.0" jmeter="5.6">
  <hashTree>
    <TestPlan testname="E2E Plan"/>
    <hashTree>
      <ThreadGroup testname="TG"/>
      <hashTree>
        <HTTPSamplerProxy testname="Ping API">
          <stringProp name="HTTPSampler.method">GET</stringProp>
          <stringProp name="HTTPSampler.path">%s/ping</stringProp>
        </HTTPSamplerProxy>
        <hashTree/>
      </hashTree>
    </hashTree>
  </hashTree>
</jmeterTestPlan>`, targetURL)

	mustJSONStatus(t, adminCl, http.MethodPost, baseURL+"/api/v1/projects/"+pid+"/import/preview", http.StatusOK, map[string]any{
		"format":  "jmeter",
		"content": jmxContent,
	}, nil)
	mustJSONStatus(t, adminCl, http.MethodPost, baseURL+"/api/v1/projects/"+pid+"/import/apply", http.StatusOK, map[string]any{
		"format":   "jmeter",
		"content":  jmxContent,
		"strategy": "merge",
	}, nil)

	var tree struct {
		Children []struct {
			Type     string `json:"type"`
			ID       string `json:"id"`
			Name     string `json:"name"`
			Children []struct {
				ID string `json:"id"`
			} `json:"children"`
		} `json:"children"`
	}
	mustJSONStatus(t, adminCl, http.MethodGet, baseURL+"/api/v1/projects/"+pid+"/tree", http.StatusOK, nil, &tree)
	var apiID string
	for _, n := range tree.Children {
		if n.Type == "api" {
			apiID = n.ID
			break
		}
		for _, ch := range n.Children {
			apiID = ch.ID
			break
		}
	}
	if apiID == "" {
		t.Fatalf("imported API not found in tree: %+v", tree)
	}

	var dbgResp struct {
		Result struct {
			Passed bool `json:"passed"`
			Status int  `json:"status"`
		} `json:"result"`
	}
	mustJSONStatus(t, adminCl, http.MethodPost, baseURL+"/api/v1/projects/"+pid+"/debug", http.StatusOK, map[string]any{
		"api_id": apiID,
	}, &dbgResp)
	if !dbgResp.Result.Passed || dbgResp.Result.Status != 200 {
		t.Fatalf("debug imported API failed: %+v", dbgResp)
	}

	var scen struct {
		ID string `json:"id"`
	}
	mustJSONStatus(t, adminCl, http.MethodPost, baseURL+"/api/v1/projects/"+pid+"/scenarios", http.StatusCreated, map[string]any{
		"name": "E2E Cluster Scenario",
		"steps": []map[string]any{
			{"id": "ping_step", "name": "Ping Step", "type": "http", "api_id": apiID},
		},
		"load": map[string]any{
			"mode": "vus",
			"stages": []map[string]any{
				{"duration": "0s", "target": 4},
				{"duration": "6s", "target": 4},
			},
		},
	}, &scen)

	k6Code, _, k6Body := apiDo(t, adminCl, http.MethodGet, baseURL+"/api/v1/projects/"+pid+"/scenarios/"+scen.ID+"/export?format=k6", "", nil)
	if k6Code != http.StatusOK || !strings.Contains(string(k6Body), "k6/http") {
		t.Fatalf("k6 export failed code=%d body=%s", k6Code, k6Body)
	}

	// 9. Start distributed run matching zone=a under redistribute policy, then kill agent-1 mid-run!
	var runCreated struct {
		ID string `json:"id"`
	}
	mustJSONStatus(t, adminCl, http.MethodPost, baseURL+"/api/v1/projects/"+pid+"/runs", http.StatusCreated, map[string]any{
		"scenario_id":         scen.ID,
		"node_selector":       map[string]string{"zone": "a"},
		"node_failure_policy": "redistribute",
	}, &runCreated)

	// Wait until snapshots contain both zone=a nodes (and NOT agent-3 from zone=b).
	snapDeadline := time.Now().Add(10 * time.Second)
	var sawTwoNodes bool
	for time.Now().Before(snapDeadline) {
		var snaps []struct {
			Nodes []struct {
				AgentName string `json:"agent_name"`
			} `json:"nodes"`
		}
		mustJSONStatus(t, adminCl, http.MethodGet, baseURL+"/api/v1/runs/"+runCreated.ID+"/snapshots", http.StatusOK, nil, &snaps)
		for _, s := range snaps {
			for _, nd := range s.Nodes {
				if nd.AgentName == "agent-3" {
					t.Fatalf("agent-3 (zone=b) should not be selected by node_selector zone=a")
				}
			}
			if len(s.Nodes) == 2 {
				sawTwoNodes = true
			}
		}
		if sawTwoNodes {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !sawTwoNodes {
		t.Fatal("timed out waiting for both agent-1 and agent-2 snapshots")
	}

	// Kill agent-1 mid-run to exercise live redistribute failover!
	ag1Proc.stop()

	var finalRun struct {
		Status  string `json:"status"`
		Error   string `json:"error"`
		Summary *struct {
			Requests int64 `json:"requests"`
			Nodes    []struct {
				AgentName string `json:"agent_name"`
			} `json:"nodes"`
		} `json:"summary"`
		Alerts []struct {
			Kind string `json:"kind"`
		} `json:"alerts"`
	}
	runDeadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(runDeadline) {
		mustJSONStatus(t, adminCl, http.MethodGet, baseURL+"/api/v1/runs/"+runCreated.ID, http.StatusOK, nil, &finalRun)
		if finalRun.Status == "finished" || finalRun.Status == "failed" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if finalRun.Status != "finished" || finalRun.Summary == nil || finalRun.Summary.Requests <= 0 {
		t.Fatalf("unexpected final run: %+v", finalRun)
	}
	if len(finalRun.Summary.Nodes) != 2 {
		t.Fatalf("expected 2 per-node summaries retained after failover, got %+v", finalRun.Summary.Nodes)
	}
	var sawOffline bool
	for _, al := range finalRun.Alerts {
		if al.Kind == "node_offline" {
			sawOffline = true
		}
	}
	if !sawOffline {
		t.Fatalf("expected node_offline alert in %+v", finalRun.Alerts)
	}

	// 10. Verify histogram + all 5 run export formats + audit logs + agent-3 revocation.
	var histResp struct {
		Bands   []int64 `json:"bands"`
		Buckets []any   `json:"buckets"`
		Latency struct {
			Count int64 `json:"count"`
		} `json:"latency"`
	}
	mustJSONStatus(t, adminCl, http.MethodGet, baseURL+"/api/v1/runs/"+runCreated.ID+"/histogram", http.StatusOK, nil, &histResp)
	if len(histResp.Bands) != 12 || len(histResp.Buckets) == 0 || histResp.Latency.Count <= 0 {
		t.Fatalf("unexpected histogram response: %+v", histResp)
	}

	for _, fmtName := range []string{"html", "csv", "json", "har", "jtl"} {
		ec, _, eb := apiDo(t, adminCl, http.MethodGet, baseURL+"/api/v1/runs/"+runCreated.ID+"/export?format="+fmtName, "", nil)
		if ec != http.StatusOK || len(eb) == 0 {
			t.Fatalf("export format=%s status=%d len=%d", fmtName, ec, len(eb))
		}
		if fmtName == "csv" || fmtName == "jtl" {
			if _, err := csv.NewReader(bytes.NewReader(eb)).ReadAll(); err != nil {
				t.Fatalf("invalid %s CSV output: %v", fmtName, err)
			}
		}
	}

	var auditList []struct {
		Action string `json:"action"`
	}
	mustJSONStatus(t, adminCl, http.MethodGet, baseURL+"/api/v1/audit-logs?limit=50", http.StatusOK, nil, &auditList)
	if len(auditList) < 4 {
		t.Fatalf("expected audit log entries >= 4, got %d", len(auditList))
	}

	// Revoke agent-3 via REST API and confirm disconnect.
	delCode, _, _ := apiDo(t, adminCl, http.MethodDelete, baseURL+"/api/v1/agents/"+idByName["agent-3"], "", nil)
	if delCode != http.StatusNoContent {
		t.Fatalf("DELETE /agents/{agent-3} status=%d, want 204", delCode)
	}
	waitAgentCount(t, adminCl, baseURL, 1, 10*time.Second)
}
