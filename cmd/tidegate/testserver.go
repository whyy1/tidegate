package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tidegate/internal/testserver"
)

func cmdTestServer(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("testserver", stderr)
	addr := fs.String("addr", ":18080", "监听地址")
	// Diagnostics, not listed in the usage text.
	stats := fs.Duration("stats", 0, "按此间隔输出连接统计")
	startProfiles := profileFlags(fs)
	if _, err := parseArgs(fs, args); err != nil {
		return flagError(err)
	}
	srv, err := testserver.Listen(*addr)
	if err != nil {
		fmt.Fprintf(stderr, "无法监听 %s：%v\n", *addr, err)
		return 1
	}
	stop, err := startProfiles()
	if err != nil {
		fmt.Fprintln(stderr, err)
		srv.Close()
		return 1
	}
	defer stop()
	fmt.Fprintf(stdout, "测试服务已启动：%s\n", srv.URL())
	fmt.Fprintln(stdout, "路由：/ping  /echo  /delay?ms=N  /status?code=N  /bytes?n=N  /login（POST）  /items  /redirect?to=路径")
	fmt.Fprintln(stdout, "按 Ctrl+C 停止。")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve() }()
	var tick <-chan time.Time
	if *stats > 0 {
		t := time.NewTicker(*stats)
		defer t.Stop()
		tick = t.C
	}
	start := time.Now()
	last := srv.TakeStats()
	for {
		select {
		case <-sig:
			srv.Close()
			fmt.Fprintf(stdout, "测试服务已停止，共处理 %d 个请求。\n", srv.Requests())
			return 0
		case err := <-errc:
			if err != nil {
				fmt.Fprintf(stderr, "测试服务出错：%v\n", err)
				return 1
			}
			srv.Close()
			return 0
		case now := <-tick:
			st := srv.TakeStats()
			if st.Accepted != last.Accepted || st.Open != 0 || st.MaxAcceptWait > *stats {
				fmt.Fprintf(stdout, "%8.3fs 已接受连接 %d（+%d）  当前连接 %d  单次 Accept 最长等待 %v  接受到首个请求最长 %v\n",
					now.Sub(start).Seconds(), st.Accepted, st.Accepted-last.Accepted, st.Open,
					st.MaxAcceptWait.Round(time.Microsecond), st.MaxFirstRequest.Round(time.Microsecond))
			}
			last = st
		}
	}
}
