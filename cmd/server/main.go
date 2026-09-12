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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fn-docker-to-desktop/internal/api"
	"fn-docker-to-desktop/internal/auth"
	"fn-docker-to-desktop/internal/cgi"
	"fn-docker-to-desktop/internal/desktop"
	"fn-docker-to-desktop/internal/logger"
	"fn-docker-to-desktop/internal/monitor"
	"fn-docker-to-desktop/internal/proxy"
	"fn-docker-to-desktop/web"
)

const appVersion = "1.1.14"

func main() {
	modeFlag := flag.String("mode", "server", "Run mode: server or cgi")
	portFlag := flag.Int("port", 0, "Server port (default: from settings or env PORT or 5900)")
	hostFlag := flag.String("host", "", "Server host (default: from env HOST or 0.0.0.0)")
	dataDirFlag := flag.String("data", "data", "Data directory")
	iconPathFlag := flag.String("icon", "icon.png", "Product icon path")
	socketFlag := flag.String("socket", "", "Unix domain socket path for fnOS unified gateway")
	flag.Parse()

	// If running in CGI mode (e.g. from fnOS desktop shortcut via ui/index.cgi)
	if *modeFlag == "cgi" || os.Getenv("GATEWAY_INTERFACE") != "" {
		actualSocket := *socketFlag
		if actualSocket == "" {
			actualSocket = "/tmp/fn-docker-to-desktop.sock"
		}
		cgi.RunCGI(actualSocket)
		return
	}

	// 1. Initialize 8-day rolling logger with auto-pruning
	logInst, err := logger.Init(*dataDirFlag, 8)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志系统失败: %v\n", err)
	} else {
		defer logInst.Close()
	}

	// 2. Global panic recovery to record any crash in logs
	defer func() {
		if r := recover(); r != nil {
			logger.RecoverAndLog("主服务未捕获 Panic", r)
			os.Exit(2)
		}
	}()

	slog.Info("把 Docker 放到桌面 (fn-docker-to-desktop) 启动中...")

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

	// Determine fnOS Unified Gateway Unix Domain Socket
	socketPath := *socketFlag
	if socketPath == "" {
		if dest := os.Getenv("TRIM_APPDEST"); dest != "" {
			socketPath = filepath.Join(dest, "app.sock")
		} else if _, err := os.Stat("/usr/local/apps/@appcenter/fn-docker-to-desktop"); err == nil {
			socketPath = "/usr/local/apps/@appcenter/fn-docker-to-desktop/app.sock"
		} else if _, err := os.Stat("/var/apps/fn-docker-to-desktop/target"); err == nil {
			socketPath = "/var/apps/fn-docker-to-desktop/target/app.sock"
		}
	}

	// Determine server TCP port:
	// If in fnOS Unified Gateway socket mode and no port is explicitly requested, do NOT listen on TCP!
	port := 0
	if *portFlag > 0 {
		port = *portFlag
	} else if envPort := os.Getenv("PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			port = p
		}
	} else if socketPath == "" {
		// Standalone mode without unix socket requires TCP port
		if settings.PortalPort > 0 {
			port = settings.PortalPort
		} else {
			port = 5900
		}
	}

	host := "0.0.0.0"
	if *hostFlag != "" {
		host = *hostFlag
	} else if envHost := os.Getenv("HOST"); envHost != "" {
		host = envHost
	}

	// Output diagnostic information
	logger.LogDiagnostic(appVersion, port, host, *dataDirFlag, *iconPathFlag, socketPath)

	var ln net.Listener
	var addr string

	if port > 0 {
		// Try binding to port, with retry logic to avoid race condition on restart
		addr = fmt.Sprintf("%s:%d", host, port)
		var err error
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			slog.Warn("初始端口绑定失败，正在重试 (可能前次进程端口正在释放)...", "address", addr, "error", err)
			for i := 0; i < 3; i++ {
				time.Sleep(500 * time.Millisecond)
				ln, err = net.Listen("tcp", addr)
				if err == nil {
					slog.Info("端口重试绑定成功", "address", addr)
					break
				}
			}
		}

		if err != nil {
			slog.Warn("目标端口已被占用，正在自动查找未被占用的合适端口...", "requestedPort", port, "address", addr, "error", err)
			altPort := proxy.RecommendAvailablePort(port+1, nil)
			if altPort == 0 {
				altPort = proxy.RecommendAvailablePort(5950, nil)
			}
			if altPort > 0 {
				altAddr := fmt.Sprintf("%s:%d", host, altPort)
				altLn, altErr := net.Listen("tcp", altAddr)
				if altErr == nil {
					slog.Info("已成功找到并切换至未被占用的合适端口运行", "originalPort", port, "newPort", altPort, "address", altAddr)
					port = altPort
					addr = altAddr
					ln = altLn
					err = nil
				}
			}
		}
		if err != nil {
			slog.Error("无法监听指定TCP端口，服务退出", "address", addr, "error", err)
			os.Exit(1)
		}

		// Save active port in settings
		if settings.PortalPort != port {
			settings.PortalPort = port
			_ = storage.UpdateSettings(settings)
		}
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

	// Ensure each item has a valid, unique AppName (resolves any legacy package name collisions)
	for idx := range items {
		expected := installer.DeriveAppName(items[idx])
		if items[idx].AppName != expected {
			slog.Info("自动修正桌面应用唯一包标识", "id", items[idx].ID, "oldAppName", items[idx].AppName, "newAppName", expected)
			items[idx].AppName = expected
			_ = storage.SaveItem(items[idx])
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
		Logger:       logInst,
		WebFS:        web.Assets,
		ProcPath:     procPath,
		DataDir:      *dataDirFlag,
		AppVersion:   appVersion,
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Wrap with Security Headers and HTTP Request Logging Middleware
	securedHandler := api.SecurityHeadersMiddleware(mux)
	loggingHandler := api.RequestLoggingMiddleware(securedHandler)

	// Middleware to support fnOS Unified Gateway prefix and normalize paths
	var rootHandler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const gwPrefix = "/app/fn-docker-to-desktop"
		if r.URL.Path == gwPrefix {
			http.Redirect(w, r, gwPrefix+"/", http.StatusMovedPermanently)
			return
		}
		if strings.HasPrefix(r.URL.Path, gwPrefix+"/") {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, gwPrefix)
			if r.URL.Path == "" {
				r.URL.Path = "/"
			}
		}
		// Defensive normalization: trim trailing slash for /api routes
		if strings.HasPrefix(r.URL.Path, "/api/") && len(r.URL.Path) > 5 && strings.HasSuffix(r.URL.Path, "/") {
			r.URL.Path = strings.TrimSuffix(r.URL.Path, "/")
		}
		loggingHandler.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Handler:      rootHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Initialize fnOS Unified Gateway Unix Domain Socket
	var sockLn net.Listener
	if socketPath != "" {
		_ = os.Remove(socketPath)
		sl, sockErr := net.Listen("unix", socketPath)
		if sockErr == nil {
			_ = os.Chmod(socketPath, 0666)
			sockLn = sl
			slog.Info("飞牛统一网关 Unix Socket 监听就绪", "socket", socketPath)
			if socketPath != "/tmp/fn-docker-to-desktop.sock" {
				_ = os.Remove("/tmp/fn-docker-to-desktop.sock")
				_ = os.Symlink(socketPath, "/tmp/fn-docker-to-desktop.sock")
			}
		} else {
			slog.Warn("飞牛统一网关 Unix Socket 创建失败", "socket", socketPath, "error", sockErr)
		}
	}

	if sockLn == nil && ln == nil {
		slog.Error("无可用网络监听方式 (既无有效TCP端口亦无有效Unix Socket)，服务退出")
		os.Exit(1)
	}

	// Graceful shutdown handling
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if sockLn != nil {
		go func() {
			slog.Info("飞牛统一网关服务就绪", "socket", socketPath)
			if err := srv.Serve(sockLn); err != nil && err != http.ErrServerClosed {
				slog.Warn("Unix Socket 服务终止", "error", err)
			}
		}()
	}

	if ln != nil {
		go func() {
			slog.Info("独立 HTTP 端口监听就绪", "address", fmt.Sprintf("http://%s", addr), "port", port)
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				slog.Error("HTTP 服务异常退出", "error", err)
				stop()
			}
		}()
	}

	// Background startup reconciliation:
	// Gently ensure all enabled items are installed in fnOS without restarting or modifying already installed apps.
	if installer.HasCLI() {
		go func() {
			time.Sleep(1 * time.Second)
			items := storage.GetAllItems()
			installer.ReconcileInstalledItems(items)
		}()
	}

	<-ctx.Done()
	slog.Info("收到终止信号，正在关闭服务...")

	watcher.Stop()
	proxyMgr.StopAll()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	if sockLn != nil {
		_ = sockLn.Close()
		if socketPath != "" {
			_ = os.Remove(socketPath)
		}
		_ = os.Remove("/tmp/fn-docker-to-desktop.sock")
	}

	slog.Info("服务已安全退出")
}
