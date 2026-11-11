// Command harness runs the Tidegate benchmarks: the built-in test server's /ping over
// keep-alive connections at several concurrency levels, comparing the
// net/http client, the custom client and (when installed) k6, plus a
// memory test with 10,000 virtual users and one second of think time.
//
// Usage (from the repository root): bash bench/run.sh [flags]
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type benchCase struct {
	Name   string        `json:"name"`
	Target string        `json:"target"` // tidegate-fast, tidegate-std, k6
	VUs    int           `json:"vus"`
	Think  time.Duration `json:"think"`
}

type result struct {
	benchCase
	Duration      float64 `json:"duration_s"`
	Requests      int64   `json:"requests"`
	RPS           float64 `json:"rps"`
	P50           float64 `json:"p50_ms"`
	P99           float64 `json:"p99_ms"`
	Errors        int64   `json:"errors"`
	CPUSeconds    float64 `json:"cpu_seconds"`
	CPUCores      float64 `json:"cpu_cores"`
	PeakRSS       uint64  `json:"peak_working_set_bytes"`
	PeakPrivate   uint64  `json:"peak_private_bytes"`
	ServerCPU     float64 `json:"server_cpu_cores"`
	Wall          float64 `json:"wall_s"`
	Note          string  `json:"note,omitempty"`
	ExitErr       string  `json:"exit_error,omitempty"`
	TimeWaitAfter int     `json:"time_wait_after"`
	// Steady state (after the ramp), Tidegate only: from per-second snapshots.
	SteadyRPS    float64 `json:"steady_rps,omitempty"`
	SteadyErrors int64   `json:"steady_errors"`
	SteadyP99    float64 `json:"steady_p99_ms,omitempty"` // median of the per-second P99
}

func main() {
	duration := flag.Duration("duration", 30*time.Second, "duration of every run")
	vusList := flag.String("vus", "100,1000,10000", "concurrency levels")
	thinkVUs := flag.Int("think-vus", 10000, "virtual users of the memory test (0 to skip)")
	think := flag.Duration("think", time.Second, "think time of the memory test")
	targets := flag.String("targets", "tidegate-fast,tidegate-std,k6", "what to run")
	outDir := flag.String("out", "bench/out", "output directory")
	addr := flag.String("addr", "127.0.0.1:18080", "test server address")
	twLimit := flag.Int("time-wait-limit", 3000, "wait before large runs until fewer TIME_WAIT sockets remain")
	flag.Parse()

	must(os.MkdirAll(*outDir, 0o755))
	exe := filepath.Join(*outDir, "tidegate"+exeSuffix())
	fmt.Println("== 编译 tidegate")
	build := exec.Command("go", "build", "-trimpath", "-o", exe, "./cmd/tidegate")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	must(build.Run())

	k6 := findK6()
	k6Version := ""
	if k6 != "" {
		out, _ := exec.Command(k6, "version").Output()
		k6Version = strings.TrimSpace(string(out))
		fmt.Printf("== 找到 k6：%s（%s）\n", k6, k6Version)
	} else {
		fmt.Println("== 未找到 k6（PATH、K6_BIN 和默认安装路径都没有），跳过 k6 对比")
	}

	var cases []benchCase
	for _, v := range strings.Split(*vusList, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		must(err)
		for _, t := range strings.Split(*targets, ",") {
			cases = append(cases, benchCase{Target: strings.TrimSpace(t), VUs: n})
		}
	}
	if *thinkVUs > 0 {
		for _, t := range strings.Split(*targets, ",") {
			cases = append(cases, benchCase{Target: strings.TrimSpace(t), VUs: *thinkVUs, Think: *think})
		}
	}

	fmt.Println("== 启动被测服务", *addr)
	srv := exec.Command(exe, "testserver", "--addr", *addr)
	srvLog, _ := os.Create(filepath.Join(*outDir, "testserver.log"))
	srv.Stdout, srv.Stderr = srvLog, srvLog
	must(srv.Start())
	defer func() {
		srv.Process.Kill()
		srv.Wait()
		srvLog.Close()
	}()
	base := "http://" + *addr
	must(waitReady(base + "/ping"))
	srvWatch := watch(srv.Process.Pid)

	machine := machineInfo()
	var results []result
	for _, c := range cases {
		c.Name = fmt.Sprintf("%s-%d", c.Target, c.VUs)
		if c.Think > 0 {
			c.Name += "-think"
		}
		if c.Target == "k6" && k6 == "" {
			results = append(results, result{benchCase: c, Note: "未做 k6 对比：本机没有 k6"})
			continue
		}
		if c.VUs >= 2000 {
			waitTimeWait(*twLimit)
		}
		fmt.Printf("== %s：%d 个虚拟用户，思考时间 %v，时长 %v\n", c.Name, c.VUs, c.Think, *duration)
		r := runCase(c, exe, k6, base, *outDir, *duration, srvWatch)
		r.TimeWaitAfter, _ = timeWaitCount()
		fmt.Printf("   每秒请求 %.0f  P50 %.3fms  P99 %.3fms  错误 %d  CPU %.2f 核  内存峰值 %.1f MB  被测服务 CPU %.2f 核\n",
			r.RPS, r.P50, r.P99, r.Errors, r.CPUCores, float64(r.PeakRSS)/(1<<20), r.ServerCPU)
		if r.Note != "" || r.ExitErr != "" {
			fmt.Printf("   备注：%s %s\n", r.Note, r.ExitErr)
		}
		results = append(results, r)
	}

	stamp := time.Now().Format("20060102-150405")
	report := map[string]any{"machine": machine, "k6": k6Version, "duration": duration.String(), "results": results}
	data, _ := json.MarshalIndent(report, "", "  ")
	must(os.WriteFile(filepath.Join(*outDir, "results-"+stamp+".json"), data, 0o644))
	md := markdown(machine, k6Version, *duration, results)
	must(os.WriteFile(filepath.Join(*outDir, "results-"+stamp+".md"), []byte(md), 0o644))
	fmt.Println()
	fmt.Println(md)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// findK6 looks in K6_BIN, then PATH, then the default install location.
func findK6() string {
	if p := os.Getenv("K6_BIN"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("k6"); err == nil {
		return p
	}
	for _, p := range defaultK6Paths() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func waitReady(url string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("被测服务没有就绪")
}

// waitTimeWait waits until closed connections of earlier runs have left
// TIME_WAIT, so a large run does not run out of ephemeral ports.
func waitTimeWait(limit int) {
	start := time.Now()
	for {
		n, err := timeWaitCount()
		if err != nil || n < limit {
			if time.Since(start) > time.Second {
				fmt.Printf("   TIME_WAIT 已降到 %d，等待了 %v\n", n, time.Since(start).Round(time.Second))
			}
			return
		}
		if time.Since(start) > 6*time.Minute {
			fmt.Printf("   TIME_WAIT 仍有 %d 个，不再等待\n", n)
			return
		}
		if time.Since(start) < time.Second {
			fmt.Printf("   等待 TIME_WAIT 连接释放（当前 %d 个）…\n", n)
		}
		time.Sleep(5 * time.Second)
	}
}

// rampFor returns how long a run takes to reach vus virtual users. Opening
// every connection in the same instant overflows the target's listen queue
// (about 200 on Windows) and the refused connects distort every tool's
// numbers, so larger runs open at most 200 connections per second.
func rampFor(vus int) time.Duration {
	if vus <= 200 {
		return 0
	}
	return time.Duration((vus+199)/200) * time.Second
}

const scenarioTemplate = `version: 1
name: bench ping %d vus
steps:
  - id: ping
    request:
      url: %s/ping
%sload:
  mode: vus
  stages:
    - duration: %s
      target: %d
    - duration: %s
      target: %d
  graceful_stop: 5s
`

func runCase(c benchCase, exe, k6, base, outDir string, d time.Duration, srvWatch *procWatch) result {
	r := result{benchCase: c}
	resultPath := filepath.Join(outDir, c.Name+".json")
	os.Remove(resultPath)
	var cmd *exec.Cmd
	switch c.Target {
	case "tidegate-fast", "tidegate-std":
		think := ""
		if c.Think > 0 {
			think = fmt.Sprintf("    think:\n      type: fixed\n      min: %s\n", c.Think)
		}
		scn := filepath.Join(outDir, fmt.Sprintf("scenario-%d-%s.yaml", c.VUs, strings.ReplaceAll(c.Think.String(), ".", "_")))
		must(os.WriteFile(scn, []byte(fmt.Sprintf(scenarioTemplate, c.VUs, base, think, rampFor(c.VUs), c.VUs, d, c.VUs)), 0o644))
		cmd = exec.Command(exe, "run", scn, "--quiet", "--out", resultPath, "--client", strings.TrimPrefix(c.Target, "tidegate-"))
	case "k6":
		script := "bench/k6/ping.js"
		if c.Think > 0 {
			script = "bench/k6/ping-think.js"
		}
		cmd = exec.Command(k6, "run", "--quiet", "--no-usage-report", "--no-color",
			"--stage", fmt.Sprintf("%s:%d", rampFor(c.VUs), c.VUs), "--stage", fmt.Sprintf("%s:%d", d, c.VUs),
			"--summary-export", resultPath,
			"--summary-trend-stats", "avg,min,med,max,p(90),p(95),p(99)",
			"-e", "URL="+base+"/ping", "-e", "THINK="+strconv.FormatFloat(c.Think.Seconds(), 'f', -1, 64),
			script)
	default:
		r.Note = "未知的测试对象"
		return r
	}
	logf, _ := os.Create(filepath.Join(outDir, c.Name+".log"))
	defer logf.Close()
	cmd.Stdout, cmd.Stderr = logf, logf
	srvCPU0 := srvWatch.cpu()
	start := time.Now()
	if err := cmd.Start(); err != nil {
		r.ExitErr = err.Error()
		return r
	}
	w := watch(cmd.Process.Pid)
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				w.sample()
			}
		}
	}()
	err := cmd.Wait()
	close(stop)
	r.Wall = time.Since(start).Seconds()
	w.sample()
	if err != nil {
		r.ExitErr = err.Error()
	}
	r.CPUSeconds = (cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()).Seconds()
	if r.CPUSeconds == 0 {
		r.CPUSeconds = w.cpu().Seconds()
	}
	r.PeakRSS, r.PeakPrivate = w.peak()
	if r.PeakRSS == 0 {
		r.PeakRSS = maxRSS(cmd.ProcessState)
	}
	w.close()
	r.ServerCPU = (srvWatch.cpu() - srvCPU0).Seconds() / r.Wall

	data, err := os.ReadFile(resultPath)
	if err != nil {
		r.Note = "没有结果文件，见日志 " + c.Name + ".log"
		return r
	}
	if c.Target == "k6" {
		parseK6(data, &r)
	} else {
		parseTidegate(data, &r, rampFor(c.VUs))
	}
	if r.Wall > 0 {
		r.CPUCores = r.CPUSeconds / r.Wall
	}
	return r
}

func parseTidegate(data []byte, r *result, ramp time.Duration) {
	var res struct {
		Snapshots []struct {
			Seq        int64   `json:"seq"`
			DurationMs float64 `json:"duration_ms"`
			Requests   int64   `json:"requests"`
			Errors     int64   `json:"errors"`
			Latency    struct {
				P99 float64 `json:"p99_ms"`
			} `json:"latency"`
		} `json:"snapshots"`
		Summary struct {
			DurationMs float64 `json:"duration_ms"`
			Requests   int64   `json:"requests"`
			RPS        float64 `json:"rps"`
			Errors     int64   `json:"errors"`
			Latency    struct {
				P50 float64 `json:"p50_ms"`
				P99 float64 `json:"p99_ms"`
			} `json:"latency"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		r.Note = "结果文件无法解析：" + err.Error()
		return
	}
	s := res.Summary
	r.Duration, r.Requests, r.RPS, r.Errors = s.DurationMs/1000, s.Requests, s.RPS, s.Errors
	r.P50, r.P99 = s.Latency.P50, s.Latency.P99
	var reqs int64
	var ms float64
	var p99s []float64
	for _, sn := range res.Snapshots {
		if sn.Seq < int64(ramp/time.Second) || sn.DurationMs < 900 {
			continue
		}
		reqs += sn.Requests
		ms += sn.DurationMs
		r.SteadyErrors += sn.Errors
		p99s = append(p99s, sn.Latency.P99)
	}
	if ms > 0 {
		r.SteadyRPS = float64(reqs) / ms * 1000
		sort.Float64s(p99s)
		r.SteadyP99 = p99s[len(p99s)/2]
	}
}

func parseK6(data []byte, r *result) {
	var res struct {
		Metrics map[string]map[string]float64 `json:"metrics"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		r.Note = "k6 结果无法解析：" + err.Error()
		return
	}
	reqs := res.Metrics["http_reqs"]
	dur := res.Metrics["http_req_duration"]
	r.Requests = int64(reqs["count"])
	r.RPS = reqs["rate"]
	if r.RPS > 0 {
		r.Duration = float64(r.Requests) / r.RPS
	}
	r.P50, r.P99 = dur["med"], dur["p(99)"]
	if f, ok := res.Metrics["http_req_failed"]; ok {
		r.Errors = int64(f["passes"])
	}
}

func markdown(machine string, k6Version string, d time.Duration, rs []result) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "机器：%s\n\n", machine)
	fmt.Fprintf(&b, "每组保持时长：%v。超过 200 个虚拟用户时前面另加爬坡（每秒 200 个用户）。“每秒请求”“P50”“P99”“错误”是含爬坡的全程数字；“稳定”三列只统计爬坡结束后的整秒（稳定 P99 是每秒 P99 的中位数），只有 Tidegate 有，k6 不输出分段数据。k6：%s\n\n", d, orDash(k6Version))
	b.WriteString("| 对象 | 虚拟用户 | 思考时间 | 稳定每秒请求 | 稳定 P99 (ms) | 稳定错误 | 每秒请求 | P50 (ms) | P99 (ms) | 错误 | 发压 CPU（平均核数） | 每万请求 CPU 秒 | 内存峰值（工作集） | 内存峰值（私有） | 被测服务 CPU（核） |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range rs {
		if r.Note != "" && r.Requests == 0 {
			fmt.Fprintf(&b, "| %s | %d | %s | %s |  |  |  |  |  |  |  |  |  |  |  |\n", r.Target, r.VUs, thinkStr(r.Think), r.Note)
			continue
		}
		perTenK := 0.0
		if r.Requests > 0 {
			perTenK = r.CPUSeconds / float64(r.Requests) * 10000
		}
		steady := "- | - | -"
		if r.SteadyRPS > 0 {
			steady = fmt.Sprintf("%.0f | %.3f | %d", r.SteadyRPS, r.SteadyP99, r.SteadyErrors)
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %.0f | %.3f | %.3f | %d | %.2f | %.3f | %.0f MB | %.0f MB | %.2f |\n",
			r.Target, r.VUs, thinkStr(r.Think), steady, r.RPS, r.P50, r.P99, r.Errors, r.CPUCores, perTenK,
			float64(r.PeakRSS)/(1<<20), float64(r.PeakPrivate)/(1<<20), r.ServerCPU)
	}
	return b.String()
}

func thinkStr(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	return d.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
