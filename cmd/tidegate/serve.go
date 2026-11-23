package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tidegate/internal/api"
	"tidegate/internal/auth"
	"tidegate/internal/controller"
	"tidegate/internal/engine"
	"tidegate/internal/store"
	"tidegate/internal/webui"
)

type serveConfig struct {
	DataDir             string
	Listen              string
	ClusterListen       string
	ClusterPublicAddr   string
	ClusterToken        string
	MasterKey           string
	MasterKeyFile       string
	TLSCert             string
	TLSKey              string
	TLSSelfSigned       bool
	AllowInsecureRemote bool
	PublicURL           string
	AllowedOrigins      []string
	TrustedProxies      []string
	AdminUser           string
	AdminPassword       string
	AdminPasswordFile   string
	ResetTOTP           bool
	NoLocalAgent        bool
	NoOpen              bool
	Client              string
	ExecutablePath      string
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

func splitCSVList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func cmdServe(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("serve", stderr)
	dataDir := fs.String("data", envOr("TIDEGATE_DATA", "./data"), "数据目录")
	listen := fs.String("listen", envOr("TIDEGATE_LISTEN", "127.0.0.1:8080"), "HTTP/WebSocket 监听地址")
	clusterListen := fs.String("cluster-listen", envOr("TIDEGATE_CLUSTER_LISTEN", "127.0.0.1:7443"), "集群节点 gRPC 监听地址")
	clusterPublicAddr := fs.String("cluster-public-addr", envOr("TIDEGATE_CLUSTER_PUBLIC_ADDR", ""), "对外公告的集群节点连接地址（host:port）")
	clusterToken := fs.String("cluster-token", envOr("TIDEGATE_CLUSTER_TOKEN", ""), "远程发压节点认证令牌")
	masterKey := fs.String("master-key", envOr("TIDEGATE_MASTER_KEY", ""), "敏感变量加密主密钥（64位十六进制或 Base64，优先于主密钥文件）")
	masterKeyFile := fs.String("master-key-file", envOr("TIDEGATE_MASTER_KEY_FILE", ""), "敏感变量加密主密钥文件（默认 <data>/master.key）")
	tlsCert := fs.String("tls-cert", envOr("TIDEGATE_TLS_CERT", ""), "TLS 证书文件（PEM）")
	tlsKey := fs.String("tls-key", envOr("TIDEGATE_TLS_KEY", ""), "TLS 私钥文件（PEM）")
	tlsSelfSigned := fs.Bool("tls-self-signed", envBool("TIDEGATE_TLS_SELF_SIGNED", false), "自动生成自签名 TLS 证书")
	allowInsecureRemote := fs.Bool("allow-insecure-remote", envBool("TIDEGATE_ALLOW_INSECURE_REMOTE", false), "允许非本机通过明文 HTTP 登录（不推荐）")
	publicURL := fs.String("public-url", envOr("TIDEGATE_PUBLIC_URL", ""), "外部访问 URL（如反代域名 https://tidegate.example.com）")
	allowedOrigins := fs.String("allowed-origins", envOr("TIDEGATE_ALLOWED_ORIGINS", ""), "额外允许的访问源列表（逗号分隔）")
	trustedProxies := fs.String("trusted-proxies", envOr("TIDEGATE_TRUSTED_PROXIES", ""), "可信反向代理 IP/CIDR 列表（逗号分隔）")
	adminUser := fs.String("admin-user", envOr(auth.AdminUserEnv, auth.DefaultAdminUsername), "初始或同步管理员用户名")
	adminPassword := fs.String("admin-password", os.Getenv(auth.AdminPasswordEnv), "管理员初始化或启动同步密码")
	adminPasswordFile := fs.String("admin-password-file", envOr(auth.AdminPasswordFileEnv, ""), "管理员密码文件路径")
	resetTOTP := fs.Bool("reset-totp", envBool(auth.AdminResetTOTPEnv, false), "启动时清空管理员 2FA 绑定")
	noLocalAgent := fs.Bool("no-local-agent", envBool("TIDEGATE_NO_LOCAL_AGENT", false), "不自动启动本机发压节点子进程")
	noOpen := fs.Bool("no-open", envBool("TIDEGATE_NO_OPEN", false), "启动后不自动打开浏览器")
	client := fs.String("client", envOr("TIDEGATE_CLIENT", string(engine.ClientFast)), "HTTP 客户端实现：fast 或 std")

	pos, err := parseArgs(fs, args)
	if err != nil {
		return flagError(err)
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "serve 不接受多余的位置参数：%v\n", pos)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serveWithContext(ctx, serveConfig{
		DataDir:             *dataDir,
		Listen:              *listen,
		ClusterListen:       *clusterListen,
		ClusterPublicAddr:   *clusterPublicAddr,
		ClusterToken:        *clusterToken,
		MasterKey:           *masterKey,
		MasterKeyFile:       *masterKeyFile,
		TLSCert:             *tlsCert,
		TLSKey:              *tlsKey,
		TLSSelfSigned:       *tlsSelfSigned,
		AllowInsecureRemote: *allowInsecureRemote,
		PublicURL:           *publicURL,
		AllowedOrigins:      splitCSVList(*allowedOrigins),
		TrustedProxies:      splitCSVList(*trustedProxies),
		AdminUser:           *adminUser,
		AdminPassword:       *adminPassword,
		AdminPasswordFile:   *adminPasswordFile,
		ResetTOTP:           *resetTOTP,
		NoLocalAgent:        *noLocalAgent,
		NoOpen:              *noOpen,
		Client:              *client,
	}, stdout, stderr, nil)
}

func fillServeConfigEnv(cfg serveConfig) serveConfig {
	if strings.TrimSpace(cfg.ClusterPublicAddr) == "" {
		cfg.ClusterPublicAddr = envOr("TIDEGATE_CLUSTER_PUBLIC_ADDR", "")
	}
	if strings.TrimSpace(cfg.ClusterToken) == "" {
		cfg.ClusterToken = envOr("TIDEGATE_CLUSTER_TOKEN", "")
	}
	if strings.TrimSpace(cfg.MasterKey) == "" {
		cfg.MasterKey = envOr("TIDEGATE_MASTER_KEY", "")
	}
	if strings.TrimSpace(cfg.MasterKeyFile) == "" {
		cfg.MasterKeyFile = envOr("TIDEGATE_MASTER_KEY_FILE", "")
	}
	if strings.TrimSpace(cfg.TLSCert) == "" {
		cfg.TLSCert = envOr("TIDEGATE_TLS_CERT", "")
	}
	if strings.TrimSpace(cfg.TLSKey) == "" {
		cfg.TLSKey = envOr("TIDEGATE_TLS_KEY", "")
	}
	if !cfg.TLSSelfSigned {
		cfg.TLSSelfSigned = envBool("TIDEGATE_TLS_SELF_SIGNED", false)
	}
	if !cfg.AllowInsecureRemote {
		cfg.AllowInsecureRemote = envBool("TIDEGATE_ALLOW_INSECURE_REMOTE", false)
	}
	if strings.TrimSpace(cfg.PublicURL) == "" {
		cfg.PublicURL = envOr("TIDEGATE_PUBLIC_URL", "")
	}
	if len(cfg.AllowedOrigins) == 0 {
		cfg.AllowedOrigins = splitCSVList(envOr("TIDEGATE_ALLOWED_ORIGINS", ""))
	}
	if len(cfg.TrustedProxies) == 0 {
		cfg.TrustedProxies = splitCSVList(envOr("TIDEGATE_TRUSTED_PROXIES", ""))
	}
	if strings.TrimSpace(cfg.AdminUser) == "" {
		cfg.AdminUser = envOr(auth.AdminUserEnv, auth.DefaultAdminUsername)
	}
	if cfg.AdminPassword == "" {
		cfg.AdminPassword = os.Getenv(auth.AdminPasswordEnv)
	}
	if strings.TrimSpace(cfg.AdminPasswordFile) == "" {
		cfg.AdminPasswordFile = envOr(auth.AdminPasswordFileEnv, "")
	}
	if !cfg.ResetTOTP {
		cfg.ResetTOTP = envBool(auth.AdminResetTOTPEnv, false)
	}
	return cfg
}

func serveWithContext(
	ctx context.Context,
	cfg serveConfig,
	stdout, stderr io.Writer,
	onReady func(httpURL, clusterAddr string),
) int {
	cfg = fillServeConfigEnv(cfg)
	if cfg.TLSSelfSigned && (cfg.TLSCert != "" || cfg.TLSKey != "") {
		fmt.Fprintln(stderr, "--tls-self-signed 不能与 --tls-cert / --tls-key 同时使用。")
		return 2
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		fmt.Fprintln(stderr, "--tls-cert 和 --tls-key 必须同时提供。")
		return 2
	}
	clientKind := engine.ClientKind(cfg.Client)
	if clientKind == "" {
		clientKind = engine.ClientFast
	}
	if clientKind != engine.ClientFast && clientKind != engine.ClientStd {
		fmt.Fprintf(stderr, "未知的 HTTP 客户端 %q，可选 fast 或 std。\n", cfg.Client)
		return 2
	}

	listenHost, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		fmt.Fprintf(stderr, "监听地址 %q 无效：%v\n", cfg.Listen, err)
		return 2
	}
	listenLoopback := isLoopbackBindHost(listenHost)

	var tlsCfg *tls.Config
	var selfFingerprint string
	switch {
	case cfg.TLSCert != "":
		tlsCfg, err = loadTLSConfig(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		}
	case cfg.TLSSelfSigned:
		tlsCfg, selfFingerprint, err = selfSignedTLSConfig([]string{listenHost})
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		}
	}
	useTLS := tlsCfg != nil
	if !useTLS && !listenLoopback {
		if cfg.AllowInsecureRemote {
			fmt.Fprintln(stderr, "警告：当前监听非回环地址且未启用 TLS，已通过 --allow-insecure-remote 允许明文 HTTP 远程登录，请勿在不可信网络中使用。")
		} else {
			fmt.Fprintln(stderr, "警告：当前监听非回环地址且未启用 TLS，出于密码安全考虑将拒绝来自非本机的初始化和登录（可改用 TLS 或添加 --allow-insecure-remote）。")
		}
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(stderr, "初始化项目存储失败：%v\n", err)
		return 1
	}
	db, err := store.OpenDB(filepath.Join(cfg.DataDir, "tidegate.db"))
	if err != nil {
		fmt.Fprintf(stderr, "打开数据库失败：%v\n", err)
		return 1
	}
	defer db.Close()

	if err := auth.Migrate(ctx, db); err != nil {
		fmt.Fprintf(stderr, "初始化账户表失败：%v\n", err)
		return 1
	}
	rdb := store.NewResultDB(db)
	var box *store.SecretBox
	if strings.TrimSpace(cfg.MasterKey) != "" {
		box, err = store.NewSecretBoxFromString(cfg.MasterKey)
	} else {
		masterKeyPath := strings.TrimSpace(cfg.MasterKeyFile)
		if masterKeyPath == "" {
			masterKeyPath = filepath.Join(cfg.DataDir, "master.key")
		}
		box, err = store.LoadOrCreateMasterKey(masterKeyPath)
	}
	if err != nil {
		fmt.Fprintf(stderr, "加载敏感变量主密钥失败：%v\n", err)
		return 1
	}
	st.SetSecretVault(store.NewSecretVault(rdb, box))

	allowedOrigins := append([]string(nil), cfg.AllowedOrigins...)
	publicURL := strings.TrimSpace(cfg.PublicURL)
	if publicURL != "" {
		u, uerr := url.Parse(publicURL)
		if uerr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			fmt.Fprintf(stderr, "外部访问地址 %q 无效：应为 http(s)://host[:port]\n", cfg.PublicURL)
			return 2
		}
		pubOrigin := strings.ToLower(u.Scheme) + "://" + u.Host
		if !slices.Contains(allowedOrigins, pubOrigin) {
			allowedOrigins = append(allowedOrigins, pubOrigin)
		}
		publicURL = strings.TrimRight(publicURL, "/")
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	authSvc, err := auth.New(db, auth.Options{Logger: log})
	if err != nil {
		fmt.Fprintf(stderr, "初始化认证服务失败：%v\n", err)
		return 1
	}
	guard, err := auth.NewGuard(authSvc, auth.GuardConfig{
		TLS:                 useTLS,
		AllowInsecureRemote: cfg.AllowInsecureRemote,
		AllowedOrigins:      allowedOrigins,
		TrustedProxies:      cfg.TrustedProxies,
	})
	if err != nil {
		fmt.Fprintf(stderr, "安全策略配置无效：%v\n", err)
		return 2
	}

	adminUser := strings.TrimSpace(cfg.AdminUser)
	if adminUser == "" {
		adminUser = auth.DefaultAdminUsername
	}
	switch {
	case cfg.AdminPassword != "":
		act, berr := authSvc.EnsureAdminAccount(ctx, auth.AdminBootstrapRequest{
			Username:     adminUser,
			Password:     cfg.AdminPassword,
			SyncExisting: true,
			ResetTOTP:    cfg.ResetTOTP,
		})
		if berr != nil {
			fmt.Fprintf(stderr, "同步管理员账户失败：%v\n", berr)
			return 1
		}
		switch act {
		case "created":
			fmt.Fprintf(stdout, "已通过环境变量自动创建管理员账户（%s）。\n", adminUser)
		case "updated":
			fmt.Fprintf(stdout, "已同步环境变量中的管理员凭据（%s）。\n", adminUser)
		}
	case strings.TrimSpace(cfg.AdminPasswordFile) != "":
		passFile := strings.TrimSpace(cfg.AdminPasswordFile)
		filePwd, rerr := auth.ReadPasswordFile(passFile)
		if rerr == nil {
			act, berr := authSvc.EnsureAdminAccount(ctx, auth.AdminBootstrapRequest{
				Username:     adminUser,
				Password:     filePwd,
				SyncExisting: true,
				ResetTOTP:    cfg.ResetTOTP,
			})
			if berr != nil {
				fmt.Fprintf(stderr, "通过 %s 同步管理员失败：%v\n", auth.AdminPasswordFileEnv, berr)
				return 1
			}
			switch act {
			case "created":
				fmt.Fprintf(stdout, "已从 %s 自动初始化管理员账户（%s）。\n", auth.AdminPasswordFileEnv, adminUser)
			case "updated":
				fmt.Fprintf(stdout, "已从 %s 同步更新管理员凭据（%s）。\n", auth.AdminPasswordFileEnv, adminUser)
			}
		} else if errors.Is(rerr, os.ErrNotExist) {
			if pend, perr := authSvc.SetupPending(ctx); perr == nil && pend {
				fmt.Fprintf(stderr, "通过 %s 创建管理员失败：%v\n", auth.AdminPasswordFileEnv, rerr)
				return 1
			}
			if cfg.ResetTOTP {
				if reset, terr := authSvc.AdminResetTOTPByName(ctx, adminUser); terr != nil && !errors.Is(terr, auth.ErrUserNotFound) {
					fmt.Fprintf(stderr, "重置管理员 2FA 失败：%v\n", terr)
					return 1
				} else if reset {
					fmt.Fprintf(stdout, "已根据环境变量解除管理员 %s 的 2FA 绑定。\n", adminUser)
				}
			}
		} else {
			fmt.Fprintf(stderr, "读取管理员密码文件失败：%v\n", rerr)
			return 1
		}
	case cfg.ResetTOTP:
		if reset, terr := authSvc.AdminResetTOTPByName(ctx, adminUser); terr != nil && !errors.Is(terr, auth.ErrUserNotFound) {
			fmt.Fprintf(stderr, "重置管理员 2FA 失败：%v\n", terr)
			return 1
		} else if reset {
			fmt.Fprintf(stdout, "已根据环境变量解除管理员 %s 的 2FA 绑定。\n", adminUser)
		}
	}

	ctrl, err := controller.New(ctx, controller.Options{
		Store:           st,
		Results:         rdb,
		Version:         version,
		ClusterListen:   cfg.ClusterListen,
		ClusterToken:    cfg.ClusterToken,
		StartLocalAgent: !cfg.NoLocalAgent,
		ExecutablePath:  cfg.ExecutablePath,
		Client:          clientKind,
		Logger:          log,
	})
	if err != nil {
		fmt.Fprintf(stderr, "启动控制器失败：%v\n", err)
		return 1
	}
	defer func() { _ = ctrl.Close() }()

	pending, err := authSvc.SetupPending(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "检查初始化状态失败：%v\n", err)
		return 1
	}

	rawLis, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintf(stderr, "监听 HTTP 地址 %s 失败：%v\n", cfg.Listen, err)
		return 1
	}
	lis := rawLis
	if tlsCfg != nil {
		lis = tls.NewListener(rawLis, tlsCfg)
	}

	webHandler := webui.Handler()
	apiSrv, err := api.NewServer(api.Options{
		Controller:        ctrl,
		Store:             st,
		Results:           rdb,
		Auth:              authSvc,
		Guard:             guard,
		Version:           version,
		TLS:               useTLS,
		ListenLoopback:    listenLoopback,
		AllowedOrigins:    allowedOrigins,
		PublicURL:         publicURL,
		ClusterPublicAddr: strings.TrimSpace(cfg.ClusterPublicAddr),
		WebHandler:        webHandler,
		Logger:            log,
	})
	if err != nil {
		_ = lis.Close()
		fmt.Fprintf(stderr, "初始化 HTTP API 失败：%v\n", err)
		return 1
	}
	defer apiSrv.Close()

	httpSrv := &http.Server{
		Handler:           apiSrv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	go authSvc.RunCleanup(bgCtx, 15*time.Minute)

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	baseURL := scheme + "://" + displayHostPort(lis.Addr().String())

	if selfFingerprint != "" {
		fmt.Fprintf(stdout, "已启用自签名 TLS 证书，SHA-256 指纹：%s\n", selfFingerprint)
	}
	if pending {
		code, err := authSvc.IssueSetupCode(ctx)
		if err != nil {
			_ = lis.Close()
			fmt.Fprintf(stderr, "生成初始化码失败：%v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "============================================================")
		fmt.Fprintf(stdout, "首次启动，请访问管理地址完成管理员初始化：\n")
		fmt.Fprintf(stdout, "  管理地址：%s/\n", baseURL)
		fmt.Fprintf(stdout, "  初始化码：%s\n", code)
		fmt.Fprintln(stdout, "============================================================")
	} else {
		if _, err := ctrl.EnsureDefaultProject(); err != nil {
			log.Warn("创建默认项目失败", "err", err)
		}
	}

	if publicURL != "" {
		fmt.Fprintf(stdout, "Tidegate %s 已启动：本机地址 %s/，外部地址 %s/（集群端口 %s）\n", version, baseURL, publicURL, ctrl.ClusterAddr())
	} else {
		fmt.Fprintf(stdout, "Tidegate %s 已启动：访问地址 %s/（集群端口 %s）\n", version, baseURL, ctrl.ClusterAddr())
	}
	if !cfg.NoOpen && webHandler != nil {
		go openBrowser(baseURL)
	}

	if onReady != nil {
		onReady(baseURL, ctrl.ClusterAddr())
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpSrv.Serve(lis)
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "HTTP 服务异常退出：%v\n", err)
			return 1
		}
	case <-ctx.Done():
		fmt.Fprintln(stdout, "\n正在停止 Tidegate 服务...")
		apiSrv.Close()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = httpSrv.Shutdown(shutCtx)
		cancel()
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "关闭 HTTP 服务时出错：%v\n", err)
		}
	}
	return 0
}

func isLoopbackBindHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return auth.IsLoopback(host)
}

func displayHostPort(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func openBrowser(rawURL string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	case "darwin":
		cmd = exec.Command("open", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	_ = cmd.Start()
}
