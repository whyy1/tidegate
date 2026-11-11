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
	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
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
	quiet := fs.Bool("quiet", false, "不输出每秒摘要")
	client := fs.String("client", string(engine.ClientFast), "HTTP 客户端：fast 或 std")
	cpuProfile := fs.String("cpuprofile", "", "写出 CPU 性能分析文件（pprof）")
	memProfile := fs.String("memprofile", "", "结束时写出堆内存分析文件（pprof）")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return flagError(err)
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "用法：tidegate run <场景文件> [--out result.json] [--csv result.csv] [--quiet]")
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
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			fmt.Fprintf(stderr, "无法创建性能分析文件：%v\n", err)
			return 1
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintf(stderr, "无法开始 CPU 分析：%v\n", err)
			return 1
		}
		defer pprof.StopCPUProfile()
	}

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
	for snap := range eng.Snapshots() {
		elapsed += snap.Duration
		view := snap.View()
		if outF != nil {
			result.Snapshots = append(result.Snapshots, view)
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
	if *memProfile != "" {
		f, err := os.Create(*memProfile)
		if err == nil {
			runtime.GC()
			pprof.WriteHeapProfile(f)
			f.Close()
		}
	}
	return 0
}
