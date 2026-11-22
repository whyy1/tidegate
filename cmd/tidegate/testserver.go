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
	withTLS := fs.Bool("tls", false, "启用自签名 HTTPS/1.1 测试服务")
	withH2 := fs.Bool("h2", false, "启用自签名 HTTPS/HTTP2 测试服务")
	grpcAddr := fs.String("grpc-addr", "", "可选：同时监听带 Server Reflection 的 gRPC 测试地址，例如 :18081")
	// Diagnostics, not listed in the usage text.
	stats := fs.Duration("stats", 0, "按此间隔输出连接统计")
	startProfiles := profileFlags(fs)
	if _, err := parseArgs(fs, args); err != nil {
		return flagError(err)
	}
	type srvRunner interface {
		URL() string
		Serve() error
		Close() error
		Requests() int64
	}
	var (
		runner   srvRunner
		statsSrc interface{ TakeStats() testserver.Stats }
	)
	if *withH2 {
		h2, err := testserver.ListenH2(*addr)
		if err != nil {
			fmt.Fprintf(stderr, "无法监听 HTTPS/HTTP2 %s：%v\n", *addr, err)
			return 1
		}
		runner = h2
	} else if *withTLS {
		srv, err := testserver.ListenTLS(*addr)
		if err != nil {
			fmt.Fprintf(stderr, "无法监听 HTTPS %s：%v\n", *addr, err)
			return 1
		}
		runner = srv
		statsSrc = srv
	} else {
		srv, err := testserver.Listen(*addr)
		if err != nil {
			fmt.Fprintf(stderr, "无法监听 %s：%v\n", *addr, err)
			return 1
		}
		runner = srv
		statsSrc = srv
	}

	var grpcSrv *testserver.GRPCTestServer
	if *grpcAddr != "" {
		var err error
		grpcSrv, err = testserver.ListenGRPC(*grpcAddr, *withTLS || *withH2)
		if err != nil {
			fmt.Fprintf(stderr, "无法监听 gRPC %s：%v\n", *grpcAddr, err)
			runner.Close()
			return 1
		}
		grpcSrv.Start()
		fmt.Fprintf(stdout, "gRPC 反射测试服务已启动：%s\n", grpcSrv.Addr())
	}

	stop, err := startProfiles()
	if err != nil {
		fmt.Fprintln(stderr, err)
		if grpcSrv != nil {
			grpcSrv.Close()
		}
		runner.Close()
		return 1
	}
	defer stop()
	fmt.Fprintf(stdout, "测试服务已启动：%s\n", runner.URL())
	fmt.Fprintln(stdout, "路由：/ping  /echo  /delay?ms=N  /status?code=N  /bytes?n=N  /login（POST）  /items  /redirect?to=路径")
	fmt.Fprintln(stdout, "按 Ctrl+C 停止。")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	errc := make(chan error, 1)
	go func() { errc <- runner.Serve() }()
	var tick <-chan time.Time
	if *stats > 0 && statsSrc != nil {
		t := time.NewTicker(*stats)
		defer t.Stop()
		tick = t.C
	}
	start := time.Now()
	var last testserver.Stats
	if statsSrc != nil {
		last = statsSrc.TakeStats()
	}
	for {
		select {
		case <-sig:
			if grpcSrv != nil {
				grpcSrv.Close()
			}
			runner.Close()
			fmt.Fprintf(stdout, "测试服务已停止，共处理 %d 个请求。\n", runner.Requests())
			return 0
		case err := <-errc:
			if grpcSrv != nil {
				grpcSrv.Close()
			}
			if err != nil {
				fmt.Fprintf(stderr, "测试服务出错：%v\n", err)
				return 1
			}
			runner.Close()
			return 0
		case now := <-tick:
			if statsSrc == nil {
				continue
			}
			st := statsSrc.TakeStats()
			if st.Accepted != last.Accepted || st.Open != 0 || st.MaxAcceptWait > *stats {
				fmt.Fprintf(stdout, "%8.3fs 已接受连接 %d（+%d）  当前连接 %d  单次 Accept 最长等待 %v  接受到首个请求最长 %v\n",
					now.Sub(start).Seconds(), st.Accepted, st.Accepted-last.Accepted, st.Open,
					st.MaxAcceptWait.Round(time.Microsecond), st.MaxFirstRequest.Round(time.Microsecond))
			}
			last = st
		}
	}
}
