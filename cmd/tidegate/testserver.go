package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"tidegate/internal/testserver"
)

func cmdTestServer(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("testserver", stderr)
	addr := fs.String("addr", ":18080", "监听地址")
	if _, err := parseArgs(fs, args); err != nil {
		return flagError(err)
	}
	srv, err := testserver.Listen(*addr)
	if err != nil {
		fmt.Fprintf(stderr, "无法监听 %s：%v\n", *addr, err)
		return 1
	}
	fmt.Fprintf(stdout, "测试服务已启动：%s\n", srv.URL())
	fmt.Fprintln(stdout, "路由：/ping  /echo  /delay?ms=N  /status?code=N  /bytes?n=N  /login（POST）  /items  /redirect?to=路径")
	fmt.Fprintln(stdout, "按 Ctrl+C 停止。")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve() }()
	select {
	case <-sig:
	case err := <-errc:
		if err != nil {
			fmt.Fprintf(stderr, "测试服务出错：%v\n", err)
			return 1
		}
	}
	srv.Close()
	fmt.Fprintf(stdout, "测试服务已停止，共处理 %d 个请求。\n", srv.Requests())
	return 0
}
