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
	username := fs.String("user", envOr(auth.AdminUserEnv, auth.DefaultAdminUsername), "要重置密码的用户名")
	passwordFlag := fs.String("password", "", "直接指定新密码")
	passwordFile := fs.String("password-file", "", "从文件读取新密码（默认从标准输入读取）")
	resetTOTP := fs.Bool("reset-totp", false, "同时清空该账户的双因素认证（2FA）绑定")

	pos, err := parseArgs(fs, args)
	if err != nil {
		return flagError(err)
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "reset-password 不接受多余的位置参数：%v\n", pos)
		return 2
	}

	var password string
	var raw []byte
	switch {
	case *passwordFlag != "":
		password = *passwordFlag
	case *passwordFile != "":
		var rErr error
		raw, rErr = os.ReadFile(*passwordFile)
		if rErr != nil {
			fmt.Fprintf(stderr, "读取密码文件失败：%v\n", rErr)
			return 1
		}
		password = strings.TrimPrefix(string(raw), string(rune(0xFEFF)))
		password = strings.TrimRight(password, "\r\n")
	default:
		if fi, statErr := os.Stdin.Stat(); stdin == os.Stdin && statErr == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
			fmt.Fprint(stderr, "请输入新密码后按回车（或使用 Ctrl+D / Ctrl+Z 结束输入）：")
		}
		var rErr error
		raw, rErr = io.ReadAll(io.LimitReader(stdin, auth.MaxPasswordBytes+8))
		if rErr != nil {
			fmt.Fprintf(stderr, "读取标准输入失败：%v\n", rErr)
			return 1
		}
		password = strings.TrimPrefix(string(raw), string(rune(0xFEFF)))
		password = strings.TrimRight(password, "\r\n")
	}
	defer clear(raw)

	if password == "" {
		fmt.Fprintln(stderr, "新密码不能为空。")
		return 2
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "创建数据目录失败：%v\n", err)
		return 1
	}
	dbPath := filepath.Join(*dataDir, "tidegate.db")
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
	action, err := svc.EnsureAdminAccount(ctx, auth.AdminBootstrapRequest{
		Username:     *username,
		Password:     password,
		SyncExisting: true,
		ResetTOTP:    *resetTOTP,
	})
	if err != nil {
		fmt.Fprintf(stderr, "重置密码失败：%v\n", err)
		return 1
	}

	totpNote := ""
	if *resetTOTP {
		totpNote = "且已解除 2FA 绑定，"
	}
	switch action {
	case "created":
		fmt.Fprintf(stdout, "已创建管理员 %q。\n", strings.TrimSpace(*username))
	default:
		fmt.Fprintf(stdout, "已重置用户 %q 的密码，%s原有的全部登录会话已失效。\n", strings.TrimSpace(*username), totpNote)
	}
	return 0
}
