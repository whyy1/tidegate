// Command tidegate is the Tidegate API management and load testing tool.
//
// S1 subcommands:
//
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
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

const usage = `Tidegate：API 管理与压测一体化工具

用法：
  tidegate run <场景文件> [选项]     运行压测场景（YAML 或 JSON）
  tidegate testserver [--addr :18080]  启动内置的被测服务
  tidegate version                    显示版本

run 的选项：
  --out <文件>       写出每秒快照和汇总（JSON）
  --csv <文件>       写出每秒聚合指标（CSV）
  --quiet            不输出每秒摘要，只输出汇总
  --client fast|std  HTTP 客户端实现，默认 fast（自研），std 为 Go 标准库

界面和控制器（tidegate serve）、发压节点（tidegate agent）将在后续版本提供。
`

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "testserver":
		return cmdTestServer(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "tidegate %s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	case "serve", "agent":
		fmt.Fprintf(stderr, "子命令 %s 尚未实现（计划在 S2 阶段提供）。\n", args[0])
		return 2
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
