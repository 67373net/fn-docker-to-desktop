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
	"sync/atomic"
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

const appVersion = "1.1.27"

const startupHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="refresh" content="1">
  <title>正在启动 - 把 Docker 放到桌面</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      background: #f8fafc;
      color: #0f172a;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
      display: flex;
      align-items: center;
      justify-content: center;
      min-height: 100vh;
      overflow: hidden;
    }
    .card {
      text-align: center;
      padding: 2.5rem 2rem;
      max-width: 380px;
      width: 90%;
      background: #ffffff;
      border: 1px solid #e2e8f0;
      border-radius: 16px;
      box-shadow: 0 10px 25px -5px rgba(0, 0, 0, 0.05), 0 8px 10px -6px rgba(0, 0, 0, 0.03);
    }
    .spinner {
      width: 46px;
      height: 46px;
      margin: 0 auto 1.5rem;
      border: 3px solid #e2e8f0;
      border-top-color: #2563eb;
      border-radius: 50%;
      animation: spin 0.8s linear infinite;
    }
    @keyframes spin {
      to { transform: rotate(360deg); }
    }
    h1 {
      font-size: 1.18rem;
      font-weight: 600;
      margin-bottom: 0.5rem;
      color: #0f172a;
      letter-spacing: -0.01em;
    }
    p {
      font-size: 0.88rem;
      color: #64748b;
      line-height: 1.6;
    }
    .sub {
      display: block;
      margin-top: 0.35rem;
      font-size: 0.78rem;
      color: #94a3b8;
    }
  </style>
</head>
<body>
  <div class="card">
    <div class="spinner"></div>
    <h1>把 Docker 放到桌面</h1>
    <p>程序正在启动初始化，请稍候...<span class="sub">即将自动进入主界面</span></p>
  </div>
  <script>
    setTimeout(function() {
      location.reload();
    }, 1000);
  </script>
</body>
</html>`

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

	// Context for graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("把 Docker 放到桌面 (fn-docker-to-desktop) 启动中...")

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

	var isAppReady atomic.Bool
	var activeHandler atomic.Pointer[http.Handler]

	dispatcher := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAppReady.Load() {
			const gwPrefix = "/app/fn-docker-to-desktop"
			reqPath := r.URL.Path
			if strings.HasPrefix(reqPath, gwPrefix+"/") {
				reqPath = strings.TrimPrefix(reqPath, gwPrefix)
				if reqPath == "" {
					reqPath = "/"
				}
			}
			if strings.HasPrefix(reqPath, "/api/") {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"status":"starting","message":"程序正在启动中，请稍候..."}`))
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(startupHTML))
			return
		}
		if h := activeHandler.Load(); h != nil {
			(*h).ServeHTTP(w, r)
		}
	})

	srv := &http.Server{
		Handler:      dispatcher,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // Disable global WriteTimeout so SSE event streams stay open without ERR_INCOMPLETE_CHUNKED_ENCODING
		IdleTimeout:  120 * time.Second,
	}

	// 立即建立 Unix Domain Socket 监听，彻底消除安装后立即打开时的 502 Bad Gateway
	var sockLn net.Listener
	if socketPath != "" {
		_ = os.Remove(socketPath)
		sl, sockErr := net.Listen("unix", socketPath)
		if sockErr == nil {
			_ = os.Chmod(socketPath, 0666)
			sockLn = sl
			slog.Info("飞牛统一网关 Unix Socket 监听建立", "socket", socketPath)
			if socketPath != "/tmp/fn-docker-to-desktop.sock" {
				_ = os.Remove("/tmp/fn-docker-to-desktop.sock")
				_ = os.Symlink(socketPath, "/tmp/fn-docker-to-desktop.sock")
			}
			go func() {
				if err := srv.Serve(sockLn); err != nil && err != http.ErrServerClosed {
					slog.Warn("Unix Socket 服务终止", "error", err)
				}
			}()
		} else {
			slog.Warn("飞牛统一网关 Unix Socket 创建失败", "socket", socketPath, "error", sockErr)
		}
	}

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

		if ln != nil {
			go func() {
				slog.Info("独立 HTTP 端口监听就绪", "address", fmt.Sprintf("http://%s", addr), "port", port)
				if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
					slog.Error("HTTP 服务异常退出", "error", err)
					stop()
				}
			}()
		}
	}

	if sockLn == nil && ln == nil {
		slog.Error("无可用网络监听方式 (既无有效TCP端口亦无有效Unix Socket)，服务退出")
		os.Exit(1)
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
	iconsDir := filepath.Join(*dataDirFlag, "icons")
	installer.SetIconsDir(iconsDir)
	desktop.MigrateLegacyWatchcowIcons(iconsDir)

	// Ensure put-port-on-desktop desktop icon is installed on fnOS (asynchronous)
	go func() {
		if err := installer.SyncSelfApp(settings); err != nil {
			slog.Warn("注册自身桌面图标告警", "error", err)
		}
	}()

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

	// Start real-time Docker events listener for auto-refreshing watchcow / container desktop items
	desktop.StartDockerEventListener(ctx, func() {
		watcher.BroadcastDockLabelChange()
	})

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

	activeHandler.Store(&rootHandler)
	isAppReady.Store(true)
	slog.Info("把 Docker 放到桌面服务已完全就绪")

	// Background startup reconciliation:
	// Gently ensure all enabled items (custom desktop items and enabled docklabel items) are installed in fnOS.
	if installer.HasCLI() {
		go func() {
			time.Sleep(1 * time.Second)
			items := storage.GetAllItems()
			labelItems, err := desktop.ScanDockLabelItems(storage.GetDockLabelState)
			if err == nil {
				for _, li := range labelItems {
					if li.Enabled {
						dItem := desktop.DesktopItem{
							ID:            li.ID,
							Name:          li.Name,
							AppName:       li.AppName,
							ContainerName: li.ContainerName,
							Image:         li.Image,
							Port:          li.Port,
							Protocol:      li.Protocol,
							Path:          li.Path,
							TargetURL:     li.TargetURL,
							UIType:        li.UIType,
							AllUsers:      li.AllUsers,
							Icon:          li.Icon,
							FileTypes:     li.FileTypes,
							NoDisplay:     li.NoDisplay,
							Enabled:       true,
							Mode:          desktop.ItemMode(li.Mode),
						}
						items = append(items, dItem)
					}
				}
			}
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
