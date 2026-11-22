package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"tidegate/internal/agent"
	"tidegate/internal/cluster"
	"tidegate/internal/engine"
)

func cmdAgent(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("agent", stderr)
	joinAddr := fs.String("join", envOr("TIDEGATE_JOIN", ""), "控制器集群地址（host:port）")
	token := fs.String("token", envOr("TIDEGATE_TOKEN", ""), "节点认证令牌")
	name := fs.String("name", envOr("TIDEGATE_NAME", ""), "节点显示名称（默认使用主机名）")
	workDir := fs.String("work-dir", envOr("TIDEGATE_WORK_DIR", ""), "临时运行文件目录")
	client := fs.String("client", envOr("TIDEGATE_CLIENT", string(engine.ClientFast)), "HTTP 客户端实现：fast 或 std")

	pos, err := parseArgs(fs, args)
	if err != nil {
		return flagError(err)
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "agent 不接受多余的位置参数：%v\n", pos)
		return 2
	}
	if *joinAddr == "" || *token == "" {
		fmt.Fprintln(stderr, "用法：tidegate agent --join <host:port> --token <令牌> [--name <节点名>]")
		return 2
	}
	clientKind := engine.ClientKind(*client)
	if clientKind != engine.ClientFast && clientKind != engine.ClientStd {
		fmt.Fprintf(stderr, "未知的 HTTP 客户端 %q，可选 fast 或 std。\n", *client)
		return 2
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ag, err := agent.New(agent.Options{
		JoinAddr: *joinAddr,
		Token:    *token,
		Name:     *name,
		Version:  version,
		WorkDir:  *workDir,
		Client:   clientKind,
		Logger:   log,
	})
	if err != nil {
		fmt.Fprintf(stderr, "初始化节点失败：%v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if os.Getenv("TIDEGATE_WATCH_STDIN") == "1" {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			stop()
		}()
	}

	fmt.Fprintf(stdout, "发压节点正在连接控制器 %s...\n", *joinAddr)
	err = ag.Run(ctx)
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	var rej *cluster.RejectedError
	if errors.As(err, &rej) {
		fmt.Fprintf(stderr, "控制器拒绝接入：%s\n", rej.Message)
		return 1
	}
	fmt.Fprintf(stderr, "节点运行异常退出：%v\n", err)
	return 1
}
