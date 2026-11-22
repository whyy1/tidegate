package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/pprof"
	"runtime/trace"
	"sync"
	"time"
)

// profileFlags registers the diagnostic flags --cpuprofile, --trace and
// --trace-for on fs. The returned function starts what was requested once
// the flags are parsed; its result stops and flushes everything.
func profileFlags(fs *flag.FlagSet) func() (func(), error) {
	cpu := fs.String("cpuprofile", "", "写出 CPU 性能分析文件（pprof）")
	tr := fs.String("trace", "", "写出执行跟踪文件（runtime/trace）")
	trFor := fs.Duration("trace-for", 0, "执行跟踪只记录开始后的这段时间（0 表示到结束）")
	start := func() (func(), error) {
		var stops []func()
		stopAll := func() {
			for i := len(stops) - 1; i >= 0; i-- {
				stops[i]()
			}
		}
		if *cpu != "" {
			f, err := os.Create(*cpu)
			if err != nil {
				return nil, fmt.Errorf("无法创建性能分析文件：%v", err)
			}
			if err := pprof.StartCPUProfile(f); err != nil {
				f.Close()
				return nil, fmt.Errorf("无法开始 CPU 分析：%v", err)
			}
			stops = append(stops, func() { pprof.StopCPUProfile(); f.Close() })
		}
		if *tr != "" {
			f, err := os.Create(*tr)
			if err != nil {
				stopAll()
				return nil, fmt.Errorf("无法创建执行跟踪文件：%v", err)
			}
			if err := trace.Start(f); err != nil {
				f.Close()
				stopAll()
				return nil, fmt.Errorf("无法开始执行跟踪：%v", err)
			}
			var once sync.Once
			stop := func() { once.Do(func() { trace.Stop(); f.Close() }) }
			if *trFor > 0 {
				time.AfterFunc(*trFor, stop)
			}
			stops = append(stops, stop)
		}
		return stopAll, nil
	}
	return start
}
