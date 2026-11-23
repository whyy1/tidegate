package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"syscall"
	"time"

	"tidegate/internal/engine"
	"tidegate/internal/exporter"
	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

// resultFile is the --out JSON document.
type resultFile struct {
	Scenario  string                  `json:"scenario"`
	File      string                  `json:"file"`
	Mode      string                  `json:"mode"`
	Client    string                  `json:"client"`
	Version   string                  `json:"version"`
	StartedAt time.Time               `json:"started_at"`
	Snapshots []*metrics.SnapshotView `json:"snapshots"`
	Summary   *metrics.SnapshotView   `json:"summary"`
}

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("run", stderr)
	out := fs.String("out", "", "写出 JSON 结果")
	csvPath := fs.String("csv", "", "写出每秒 CSV")
	htmlPath := fs.String("html", "", "写出独立 HTML 报告")
	jtlPath := fs.String("jtl", "", "写出 JMeter JTL 日志文件")
	quiet := fs.Bool("quiet", false, "不输出每秒摘要")
	client := fs.String("client", envOr("TIDEGATE_CLIENT", string(engine.ClientFast)), "HTTP 客户端：fast 或 std")
	startProfiles := profileFlags(fs)
	memProfile := fs.String("memprofile", "", "结束时写出堆内存分析文件（pprof）")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return flagError(err)
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "用法：tidegate run <场景文件> [--out result.json] [--csv result.csv] [--html report.html] [--jtl result.jtl] [--quiet]")
		return 2
	}
	path := pos[0]

	def, err := scenario.LoadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "%s：%v\n", path, err)
		return 1
	}
	plan, err := scenario.Compile(def, scenario.CompileOptions{BaseDir: filepath.Dir(path)})
	if err != nil {
		fmt.Fprintf(stderr, "%s：%v\n", path, err)
		return 1
	}
	eng, err := engine.New(plan, engine.Options{Client: engine.ClientKind(*client)})
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	// Open output files before the run so a bad path fails fast.
	var csvW *csvWriter
	if *csvPath != "" {
		f, err := os.Create(*csvPath)
		if err != nil {
			fmt.Fprintf(stderr, "无法创建 CSV 文件：%v\n", err)
			return 1
		}
		defer f.Close()
		csvW = newCSVWriter(f)
	}
	var outF *os.File
	if *out != "" {
		outF, err = os.Create(*out)
		if err != nil {
			fmt.Fprintf(stderr, "无法创建结果文件：%v\n", err)
			return 1
		}
		defer outF.Close()
	}
	var htmlF *os.File
	if *htmlPath != "" {
		htmlF, err = os.Create(*htmlPath)
		if err != nil {
			fmt.Fprintf(stderr, "无法创建 HTML 报告文件：%v\n", err)
			return 1
		}
		defer htmlF.Close()
	}
	var jtlF *os.File
	if *jtlPath != "" {
		jtlF, err = os.Create(*jtlPath)
		if err != nil {
			fmt.Fprintf(stderr, "无法创建 JTL 文件：%v\n", err)
			return 1
		}
		defer jtlF.Close()
	}
	stopProfiles, err := startProfiles()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer stopProfiles()

	w := bufio.NewWriter(stdout)
	defer w.Flush()
	printHeader(w, plan, path, *client)
	w.Flush()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		n := 0
		for range sig {
			n++
			if n == 1 {
				fmt.Fprintf(stderr, "\n正在停止：不再发起新请求，等待在途请求（最多 %v）。再按一次 Ctrl+C 立即停止。\n", plan.Load.GracefulStop)
				cancel()
			} else {
				fmt.Fprintln(stderr, "立即停止。")
				eng.Abort()
			}
		}
	}()

	result := &resultFile{
		Scenario: plan.Name, File: path, Mode: plan.Load.Mode.String(), Client: *client,
		Version: version, StartedAt: time.Now(),
	}
	errc := make(chan error, 1)
	go func() { errc <- eng.Run(ctx) }()
	var elapsed time.Duration
	rows := 0
	abortOnce := false
	abortedMsg := ""
	var htmlSnaps []store.SnapshotRecord
	var htmlSamples []store.SampleRecord
	for snap := range eng.Snapshots() {
		elapsed += snap.Duration
		view := snap.View()
		if !abortOnce && len(plan.Load.Thresholds) > 0 {
			if msg, hit := scenario.CheckAbortThresholds(plan.Load.Thresholds, view, elapsed); hit {
				abortOnce = true
				abortedMsg = msg
				fmt.Fprintf(stderr, "\n触发中止阈值：%s，正在停止压测。\n", msg)
				cancel()
			}
		}
		if outF != nil {
			result.Snapshots = append(result.Snapshots, view)
		}
		if htmlF != nil || jtlF != nil {
			snapCopy := *view
			snapCopy.Samples = nil
			if b, err := json.Marshal(&snapCopy); err == nil {
				htmlSnaps = append(htmlSnaps, store.SnapshotRecord{
					Seq:  snap.Seq,
					Time: snap.Time,
					Data: b,
				})
			}
			for _, sm := range view.Samples {
				if len(htmlSamples) >= 200 {
					break
				}
				if b, err := json.Marshal(sm); err == nil {
					htmlSamples = append(htmlSamples, store.SampleRecord{
						ID:   int64(len(htmlSamples) + 1),
						Seq:  snap.Seq,
						Kind: sm.Kind,
						Step: sm.Step,
						Time: sm.Time,
						Data: b,
					})
				}
			}
		}
		if csvW != nil {
			csvW.write(snap, view, elapsed)
		}
		if !*quiet {
			if rows%20 == 0 {
				printColumns(w, plan.Load.Mode)
			}
			printSecond(w, snap, elapsed)
			rows++
			w.Flush()
		}
	}
	if err := <-errc; err != nil {
		fmt.Fprintf(stderr, "运行失败：%v\n", err)
		return 1
	}
	summary := eng.Summary()
	printSummary(w, plan, summary)

	exitCode := 0
	if len(plan.Load.Thresholds) > 0 {
		threshRes, passed := scenario.EvaluateThresholds(plan.Load.Thresholds, summary.View())
		printThresholds(w, threshRes, passed)
		if !passed || abortedMsg != "" {
			exitCode = 3
		}
	}

	if csvW != nil {
		if err := csvW.flush(); err != nil {
			fmt.Fprintf(stderr, "写入 CSV 失败：%v\n", err)
			return 1
		}
		fmt.Fprintf(w, "已写出 CSV：%s\n", *csvPath)
	}
	if outF != nil {
		result.Summary = summary.View()
		enc := json.NewEncoder(outF)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			fmt.Fprintf(stderr, "写入结果文件失败：%v\n", err)
			return 1
		}
		fmt.Fprintf(w, "已写出结果：%s\n", *out)
	}
	if htmlF != nil || jtlF != nil {
		sumJSON, _ := json.Marshal(summary.View())
		scenJSON, _ := json.Marshal(def)
		runStatus := store.RunFinished
		runErr := ""
		if abortedMsg != "" {
			runStatus = store.RunFailed
			runErr = "触发中止阈值：" + abortedMsg
		}
		runMeta := &store.Run{
			ID:           "cli-" + result.StartedAt.UTC().Format("20060102-150405"),
			ScenarioName: plan.Name,
			Scenario:     scenJSON,
			Status:       runStatus,
			CreatedAt:    result.StartedAt,
			StartedAt:    result.StartedAt,
			FinishedAt:   time.Now(),
			Error:        runErr,
			Summary:      sumJSON,
		}
		if htmlF != nil {
			if err := exporter.ExportRunHTML(runMeta, htmlSnaps, nil, htmlSamples, htmlF); err != nil {
				fmt.Fprintf(stderr, "写入 HTML 报告失败：%v\n", err)
				return 1
			}
			fmt.Fprintf(w, "已写出 HTML 报告：%s\n", *htmlPath)
		}
		if jtlF != nil {
			if err := exporter.ExportRunJTL(runMeta, htmlSnaps, htmlSamples, jtlF); err != nil {
				fmt.Fprintf(stderr, "写入 JTL 日志失败：%v\n", err)
				return 1
			}
			fmt.Fprintf(w, "已写出 JTL 日志：%s\n", *jtlPath)
		}
	}
	if *memProfile != "" {
		f, err := os.Create(*memProfile)
		if err == nil {
			runtime.GC()
			pprof.WriteHeapProfile(f)
			f.Close()
		}
	}
	return exitCode
}
