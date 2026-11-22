// Command tidegate is the Tidegate API management and load testing tool.
//
// Subcommands:
//
//	tidegate serve [flags]            (default when invoked with no subcommand)
//	tidegate agent --join ... --token ... [--name ...]
//	tidegate reset-password [--data ./data] [--user admin] [--password-file FILE]
//	tidegate run <scenario> [--out result.json] [--csv result.csv] [--quiet]
//	tidegate testserver [--addr :18080]
//	tidegate version
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

const usage = `Tidegate：API 管理与压测一体化工具

用法：
  tidegate serve [选项]                 启动控制台与内置控制器（默认子命令）
  tidegate agent [选项]                 启动压测发压节点并接入控制器
  tidegate reset-password [选项]        重置管理员账户密码
  tidegate run <场景文件> [选项]        直接运行单次 CLI 压测（YAML 或 JSON）
  tidegate testserver [--addr :18080]   启动内置的高性能被测服务
  tidegate version                      显示版本

serve 常用选项（也可用 TIDEGATE_* 环境变量）：
  --data <目录>                  数据目录，默认 ./data（TIDEGATE_DATA）
  --listen <地址>                HTTP 监听地址，默认 127.0.0.1:8080（TIDEGATE_LISTEN）
  --cluster-listen <地址>        集群节点监听地址，默认 127.0.0.1:7443
  --cluster-token <令牌>         允许外部节点接入的集群令牌
  --tls-self-signed              自动生成自签名 HTTPS 证书
  --tls-cert / --tls-key <文件>  指定 HTTPS 证书和私钥
  --allow-insecure-remote        允许非本机通过明文 HTTP 登录（不推荐）
  --no-local-agent               不自动启动本机发压节点子进程

agent 常用选项：
  --join <host:port>             控制器集群地址（TIDEGATE_JOIN）
  --token <令牌>                 集群认证令牌（TIDEGATE_TOKEN）
  --name <名称>                  节点显示名称（TIDEGATE_NAME）

run 常用选项：
  --out <文件>                   写出每秒快照和汇总（JSON）
  --csv <文件>                   写出每秒聚合指标（CSV）
  --html <文件>                  写出离线单文件 HTML 压测报告
  --quiet                        不输出每秒摘要，只输出汇总
  --client fast|std              HTTP 客户端实现，默认 fast
`

func main() {
	os.Exit(runMainWithIO(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	return runMainWithIO(args, os.Stdin, stdout, stderr)
}

func runMainWithIO(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return cmdServe(nil, stdout, stderr)
	}
	if strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "-v", "--version":
			fmt.Fprintf(stdout, "tidegate %s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return 0
		case "-h", "--help":
			fmt.Fprint(stdout, usage)
			return 0
		default:
			return cmdServe(args, stdout, stderr)
		}
	}
	switch args[0] {
	case "serve":
		return cmdServe(args[1:], stdout, stderr)
	case "agent":
		return cmdAgent(args[1:], stdout, stderr)
	case "reset-password":
		return cmdResetPassword(args[1:], stdin, stdout, stderr)
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "testserver":
		return cmdTestServer(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "tidegate %s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	case "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "未知的子命令 %q\n\n%s", args[0], usage)
	return 2
}

// parseArgs parses flags that may appear before or after positional
// arguments, e.g. `run scenario.yaml --out x.json`.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	return fs
}

func flagError(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}
