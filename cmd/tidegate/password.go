package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"tidegate/internal/auth"
	"tidegate/internal/store"
)

func cmdResetPassword(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("reset-password", stderr)
	dataDir := fs.String("data", envOr("TIDEGATE_DATA", "./data"), "数据目录")
	username := fs.String("user", auth.DefaultAdminUsername, "要重置密码的用户名")
	passwordFile := fs.String("password-file", "", "从文件读取新密码（默认从标准输入读取）")

	pos, err := parseArgs(fs, args)
	if err != nil {
		return flagError(err)
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "reset-password 不接受多余的位置参数：%v\n", pos)
		return 2
	}

	var raw []byte
	if *passwordFile != "" {
		raw, err = os.ReadFile(*passwordFile)
		if err != nil {
			fmt.Fprintf(stderr, "读取密码文件失败：%v\n", err)
			return 1
		}
	} else {
		if fi, statErr := os.Stdin.Stat(); stdin == os.Stdin && statErr == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
			fmt.Fprint(stderr, "请输入新密码后按回车（或使用 Ctrl+D / Ctrl+Z 结束输入）：")
		}
		raw, err = io.ReadAll(io.LimitReader(stdin, auth.MaxPasswordBytes+8))
		if err != nil {
			fmt.Fprintf(stderr, "读取标准输入失败：%v\n", err)
			return 1
		}
	}
	defer clear(raw)

	password := strings.TrimPrefix(string(raw), string(rune(0xFEFF)))
	password = strings.TrimRight(password, "\r\n")
	if password == "" {
		fmt.Fprintln(stderr, "新密码不能为空。")
		return 2
	}

	dbPath := filepath.Join(*dataDir, "tidegate.db")
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintf(stderr, "未找到数据库 %s，请确认 --data 目录正确且服务已完成初始化。\n", dbPath)
		return 1
	}
	db, err := store.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "打开数据库失败：%v\n", err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()
	if err := auth.Migrate(ctx, db); err != nil {
		fmt.Fprintf(stderr, "校验数据库结构失败：%v\n", err)
		return 1
	}
	svc, err := auth.New(db, auth.Options{})
	if err != nil {
		fmt.Fprintf(stderr, "初始化认证服务失败：%v\n", err)
		return 1
	}
	pending, err := svc.SetupPending(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "检查用户表失败：%v\n", err)
		return 1
	}
	if pending {
		fmt.Fprintln(stderr, "系统尚未初始化管理员账户，请先启动服务并完成初始化设置。")
		return 1
	}
	if err := svc.ResetPassword(ctx, *username, password); err != nil {
		fmt.Fprintf(stderr, "重置密码失败：%v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "已重置用户 %q 的密码，原有的全部登录会话已失效。\n", strings.TrimSpace(*username))
	return 0
}
