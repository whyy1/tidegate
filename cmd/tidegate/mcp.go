package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"tidegate/internal/mcp"
)

func cmdMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("mcp", stderr)
	targetURL := fs.String("url", envOr("TIDEGATE_URL", "https://127.0.0.1:8080"), "Tidegate 控制器地址（也可使用 TIDEGATE_URL）")
	token := fs.String("token", envOr("TIDEGATE_TOKEN", ""), "API 访问令牌 tg_...（也可使用 TIDEGATE_TOKEN）")
	caFile := fs.String("ca-file", envOr("TIDEGATE_CA_FILE", ""), "自定义 HTTPS CA 证书路径（PEM）")
	insecure := fs.Bool("insecure", envBool("TIDEGATE_MCP_INSECURE", false), "跳过自签名 HTTPS 证书校验（仅本地开发）")

	pos, err := parseArgs(fs, args)
	if err != nil {
		return flagError(err)
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "mcp 不接受多余的位置参数：%v\n", pos)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := mcp.RunStdioProxy(ctx, mcp.StdioOptions{
		Endpoint: *targetURL,
		Token:    *token,
		CAFile:   *caFile,
		Insecure: *insecure,
	}, stdin, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "tidegate mcp 启动或运行失败：%v\n", err)
		return 1
	}
	return 0
}
