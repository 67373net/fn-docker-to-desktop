package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"fn-docker-to-desktop/internal/api"
	"fn-docker-to-desktop/internal/auth"
	"fn-docker-to-desktop/internal/desktop"
	"fn-docker-to-desktop/internal/monitor"
	"fn-docker-to-desktop/internal/proxy"
	"fn-docker-to-desktop/web"
)

func main() {
	portFlag := flag.Int("port", 0, "Server port (default: from settings or env PORT or 5900)")
	hostFlag := flag.String("host", "", "Server host (default: from env HOST or 0.0.0.0)")
	dataDirFlag := flag.String("data", "data", "Data directory")
	iconPathFlag := flag.String("icon", "icon.png", "Product icon path")
	flag.Parse()

	logHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(logHandler))

	slog.Info("把Docker放到桌面 (fn-docker-to-desktop) 启动中...")

	// Storage
	storage, err := desktop.NewStorage(*dataDirFlag)
	if err != nil {
		slog.Error("初始化存储失败", "error", err)
		os.Exit(1)
	}

	settings := storage.GetSettings()

	// Auth password from env or settings
	authPassword := os.Getenv("AUTH_PASSWORD")
	if authPassword == "" {
		authPassword = settings.AuthPassword
	}
	authMgr := auth.NewManager(authPassword)

	// Determine server port
	explicitPort := false
	port := 5900
	if *portFlag > 0 {
		port = *portFlag
		explicitPort = true
	} else if envPort := os.Getenv("PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			port = p
			explicitPort = true
		}
	} else if settings.PortalPort > 0 {
		port = settings.PortalPort
	}

	host := "0.0.0.0"
	if *hostFlag != "" {
		host = *hostFlag
	} else if envHost := os.Getenv("HOST"); envHost != "" {
		host = envHost
	}

	// Try binding to port, if not explicit and occupied, fallback to available port
	addr := fmt.Sprintf("%s:%d", host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil && !explicitPort {
		slog.Warn("默认端口已被占用，正在寻找可用端口...", "port", port, "error", err)
		port = proxy.RecommendAvailablePort(5910, nil)
		addr = fmt.Sprintf("%s:%d", host, port)
		ln, err = net.Listen("tcp", addr)
	}
	if err != nil {
		slog.Error("无法监听端口", "address", addr, "error", err)
		os.Exit(1)
	}

	// Save active port in settings
	if settings.PortalPort != port {
		settings.PortalPort = port
		_ = storage.UpdateSettings(settings)
	}

	// Check proc path (detect host mount)
	procPath := "/proc"
	if _, err := os.Stat("/host/root/proc"); err == nil {
		procPath = "/host/root/proc"
	}

	// Proxy manager
	proxyMgr := proxy.NewManager()

	// fnOS installer
	installer := desktop.NewInstaller(*dataDirFlag, *iconPathFlag)

	// Ensure put-port-on-desktop desktop icon is installed on fnOS
	if err := installer.SyncSelfApp(settings); err != nil {
		slog.Warn("注册自身桌面图标告警", "error", err)
	}

	// Restore active proxies from database
	items := storage.GetAllItems()
	for _, item := range items {
		if item.Mode == desktop.ModeProxy && item.Enabled && item.Port > 0 && item.TargetURL != "" {
			if err := proxyMgr.StartProxy(item.ID, item.Port, item.TargetURL, item.SkipTLSVerify); err != nil {
				slog.Error("恢复代理失败", "id", item.ID, "port", item.Port, "error", err)
			}
		}
	}

	// Monitor & Watcher
	systemSampler := monitor.NewSystemSampler(procPath)
	watcher := monitor.NewWatcher(procPath, 1500*time.Millisecond)
	watcher.Start()

	// API Handler
	handler := api.NewHandler(api.Config{
		Storage:      storage,
		Installer:    installer,
		ProxyMgr:     proxyMgr,
		Watcher:      watcher,
		SystemSample: systemSampler,
		AuthMgr:      authMgr,
		WebFS:        web.Assets,
		ProcPath:     procPath,
		DataDir:      *dataDirFlag,
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown handling
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("服务监听已就绪", "address", fmt.Sprintf("http://%s", addr), "port", port)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP 服务异常退出", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("收到终止信号，正在关闭服务...")

	watcher.Stop()
	proxyMgr.StopAll()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	slog.Info("服务已安全退出")
}
