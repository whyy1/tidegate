package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"tidegate/internal/agent"
	"tidegate/internal/cluster"
	"tidegate/internal/engine"
)

func parseAgentLabels(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		p := strings.TrimSpace(pair)
		if p == "" {
			continue
		}
		k, v, ok := strings.Cut(p, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("节点标签格式无效 %q，应为 key=value", p)
		}
		out[k] = v
	}
	return out, nil
}

func cmdAgent(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("agent", stderr)
	joinAddr := fs.String("join", envOr("TIDEGATE_JOIN", ""), "控制器集群地址（host:port）")
	token := fs.String("token", envOr("TIDEGATE_TOKEN", ""), "节点认证或加入令牌")
	name := fs.String("name", envOr("TIDEGATE_NAME", ""), "节点显示名称（默认使用主机名）")
	workDir := fs.String("work-dir", envOr("TIDEGATE_WORK_DIR", ""), "临时运行文件目录")
	certDir := fs.String("cert-dir", envOr("TIDEGATE_CERT_DIR", ""), "mTLS 节点证书持久化目录")
	caFP := fs.String("ca-fingerprint", envOr("TIDEGATE_CA_FINGERPRINT", ""), "控制器集群 CA SHA-256 指纹校验值")
	labelsRaw := fs.String("labels", envOr("TIDEGATE_LABELS", ""), "节点标签（key=value,key2=value2）")
	insecure := fs.Bool("insecure", envBool("TIDEGATE_AGENT_INSECURE", false), "禁用集群 mTLS 明文连接（仅测试用）")
	client := fs.String("client", envOr("TIDEGATE_CLIENT", string(engine.ClientFast)), "HTTP 客户端实现：fast 或 std")

	pos, err := parseArgs(fs, args)
	if err != nil {
		return flagError(err)
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "agent 不接受多余的位置参数：%v\n", pos)
		return 2
	}
	if *joinAddr == "" {
		fmt.Fprintln(stderr, "用法：tidegate agent --join <host:port> --token <令牌> [--ca-fingerprint <SHA256>] [--labels k=v]")
		return 2
	}
	labels, err := parseAgentLabels(*labelsRaw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	resolvedCertDir := strings.TrimSpace(*certDir)
	if resolvedCertDir == "" && strings.TrimSpace(*workDir) != "" {
		resolvedCertDir = filepath.Join(strings.TrimSpace(*workDir), "certs")
	}
	clientKind := engine.ClientKind(*client)
	if clientKind != engine.ClientFast && clientKind != engine.ClientStd {
		fmt.Fprintf(stderr, "未知的 HTTP 客户端 %q，可选 fast 或 std。\n", *client)
		return 2
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ag, err := agent.New(agent.Options{
		JoinAddr:          *joinAddr,
		Token:             *token,
		Name:              *name,
		Version:           version,
		WorkDir:           *workDir,
		Client:            clientKind,
		Labels:            labels,
		CAFingerprint:     *caFP,
		CertDir:           resolvedCertDir,
		InsecurePlaintext: *insecure,
		Logger:            log,
	})
	if err != nil {
		fmt.Fprintf(stderr, "初始化节点失败：%v\n", err)
		return 2
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
