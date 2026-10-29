package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"warden/internal/api"
	"warden/internal/auth"
	"warden/internal/config"
	"warden/internal/filemgr"
	"warden/internal/fleet"
	"warden/internal/metrics"
	"warden/internal/monitor"
	"warden/internal/platform/docker"
	"warden/internal/store"
	"warden/internal/system"
)

var (
	version   = "1.0.0"
	buildTime = "dev"
)

func main() {
	var (
		listen      = flag.String("listen", "", "控制端监听地址，例如 :8080")
		cfgPath     = flag.String("config", "", "配置文件路径")
		mode        = flag.String("mode", "", "运行模式：server（控制端）或 agent（被管端）")
		agentListen = flag.String("agent-listen", "", "Agent 监听地址，例如 :8090")
		agentToken  = flag.String("agent-token", "", "Agent 访问令牌，与控制端登记的一致")
		agentName   = flag.String("agent-name", "", "Agent 备注名，仅用于日志展示")
		showVer     = flag.Bool("version", false, "输出版本号")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("Warden %s (%s)\n", version, buildTime)
		return
	}
	if *cfgPath != "" {
		os.Setenv("WARDEN_CONFIG", *cfgPath)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载配置失败:", err)
		os.Exit(1)
	}
	// 命令行优先于配置文件
	if *mode != "" {
		cfg.Mode = strings.ToLower(*mode)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *agentListen != "" {
		cfg.AgentListen = *agentListen
	}
	if *agentToken != "" {
		cfg.AgentToken = *agentToken
	}
	if *agentName != "" {
		cfg.AgentName = *agentName
	}

	setupLogger(cfg.LogLevel)

	if cfg.Mode == "agent" {
		runAgent(cfg)
		return
	}
	runServer(cfg)
}

// ---------- 控制端 ----------

func runServer(cfg *config.Config) {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		slog.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}

	files, err := filemgr.New(cfg.FileRoot)
	if err != nil {
		slog.Error("初始化文件服务失败", "err", err, "root", cfg.FileRoot)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg, err := fleet.New(db, cfg.SecretKey())
	if err != nil {
		slog.Error("初始化主机注册表失败", "err", err)
		os.Exit(1)
	}
	reg.StartPolling(ctx, 20*time.Second)

	collector := metrics.New()
	collector.Start(ctx)

	notifier := monitor.NewNotifier(db)
	monitorSvc := monitor.New(db, notifier)
	monitorSvc.Start(ctx)

	deps := &api.Deps{
		Cfg:     cfg,
		Store:   db,
		Auth:    auth.New(db, cfg.JWTSecret, cfg.TokenTTL()),
		Metrics: collector,
		Docker:  docker.New(cfg.DockerHost),
		Files:   files,
		FW:      system.NewFirewall(),
		Monitor: monitorSvc,
		Notify:  notifier,
		Fleet:   reg,
		Version: version,
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.NewRouter(deps),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	slog.Info("Warden 控制端已启动",
		"listen", cfg.Listen, "version", version, "data_dir", cfg.DataDir,
		"file_root", cfg.FileRoot, "initialized", deps.Auth.Initialized())
	if !isLoopback(cfg.Listen) {
		slog.Warn("服务正在监听非本地地址，请务必配置 HTTPS 或置于反向代理之后")
	}

	serve(srv, cfg)
}

// ---------- 被管端 ----------

func runAgent(cfg *config.Config) {
	if cfg.AgentToken == "" {
		slog.Error("Agent 模式必须配置访问令牌",
			"hint", "使用 -agent-token 或配置文件 agent_token")
		os.Exit(1)
	}

	files, err := filemgr.New(cfg.FileRoot)
	if err != nil {
		slog.Error("初始化文件服务失败", "err", err, "root", cfg.FileRoot)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	collector := metrics.New()
	collector.Start(ctx)

	deps := &api.Deps{
		Cfg:     cfg,
		Metrics: collector,
		Docker:  docker.New(cfg.DockerHost),
		Files:   files,
		FW:      system.NewFirewall(),
		Version: version,
	}

	name := cfg.AgentName
	if name == "" {
		name = "agent"
	}
	srv := &http.Server{
		Addr:              cfg.AgentListen,
		Handler:           api.NewAgentRouter(deps, cfg.AgentToken),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	slog.Info("Warden Agent 已启动",
		"listen", cfg.AgentListen, "name", name, "version", version,
		"file_root", cfg.FileRoot, "token_set", cfg.AgentToken != "")

	serve(srv, cfg)
}

// serve 启动 HTTP(S) 服务并等待退出信号。
func serve(srv *http.Server, cfg *config.Config) {
	go func() {
		var err error
		if cfg.CertFile != "" && cfg.KeyFile != "" {
			err = srv.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP 服务异常退出", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("收到退出信号，正在关闭…")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("优雅关闭失败", "err", err)
	}
	slog.Info("已退出")
}

func setupLogger(level string) {
	lv := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	}
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv})
	slog.SetDefault(slog.New(h))
}

func isLoopback(addr string) bool {
	return strings.HasPrefix(addr, "127.0.0.1") || strings.HasPrefix(addr, "localhost") || strings.HasPrefix(addr, "[::1]")
}
