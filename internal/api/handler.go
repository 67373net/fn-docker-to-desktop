package api

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fn-docker-to-desktop/internal/auth"
	"fn-docker-to-desktop/internal/desktop"
	"fn-docker-to-desktop/internal/logger"
	"fn-docker-to-desktop/internal/monitor"
	"fn-docker-to-desktop/internal/proxy"
)

// Handler handles all HTTP API requests.
type Handler struct {
	storage        *desktop.Storage
	installer      *desktop.Installer
	proxyMgr       *proxy.Manager
	watcher        *monitor.Watcher
	systemSample   *monitor.SystemSampler
	authMgr        *auth.Manager
	loggerInstance *logger.Logger
	webFS          fs.FS
	procPath       string
	iconsDir       string
	appVersion     string
	inFlightOps    sync.Map
}

// Config holds configuration to instantiate API Handler.
type Config struct {
	Storage      *desktop.Storage
	Installer    *desktop.Installer
	ProxyMgr     *proxy.Manager
	Watcher      *monitor.Watcher
	SystemSample *monitor.SystemSampler
	AuthMgr      *auth.Manager
	Logger       *logger.Logger
	WebFS        fs.FS
	ProcPath     string
	DataDir      string
	AppVersion   string
}

// NewHandler creates a new API Handler.
func NewHandler(cfg Config) *Handler {
	iconsDir := filepath.Join(cfg.DataDir, "icons")
	_ = os.MkdirAll(iconsDir, 0755)
	if cfg.Installer != nil {
		cfg.Installer.SetIconsDir(iconsDir)
	}

	return &Handler{
		storage:        cfg.Storage,
		installer:      cfg.Installer,
		proxyMgr:       cfg.ProxyMgr,
		watcher:        cfg.Watcher,
		systemSample:   cfg.SystemSample,
		authMgr:        cfg.AuthMgr,
		loggerInstance: cfg.Logger,
		webFS:          cfg.WebFS,
		procPath:       cfg.ProcPath,
		iconsDir:       iconsDir,
		appVersion:     cfg.AppVersion,
	}
}

// RegisterRoutes registers all routes on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// API routes
	mux.HandleFunc("GET /api/ports", h.handleGetPorts)
	mux.HandleFunc("GET /api/processes", h.handleGetProcesses)
	mux.HandleFunc("GET /api/system", h.handleGetSystem)
	mux.HandleFunc("GET /api/host", h.handleGetHost)
	mux.HandleFunc("GET /api/events", h.handleEvents)

	mux.HandleFunc("GET /api/desktop/items", h.handleGetDesktopItems)
	mux.HandleFunc("POST /api/desktop/items", h.handleCreateDesktopItem)
	mux.HandleFunc("PUT /api/desktop/items/{id}", h.handleUpdateDesktopItem)
	mux.HandleFunc("POST /api/desktop/items/{id}", h.handleUpdateDesktopItem)
	mux.HandleFunc("DELETE /api/desktop/items/{id}", h.handleDeleteDesktopItem)
	mux.HandleFunc("POST /api/desktop/items/{id}/delete", h.handleDeleteDesktopItem)
	mux.HandleFunc("POST /api/desktop/items/{id}/toggle", h.handleToggleDesktopItem)

	mux.HandleFunc("POST /api/logs/client", h.handleClientLog)

	mux.HandleFunc("GET /api/settings", h.handleGetSettings)
	mux.HandleFunc("POST /api/settings", h.handleUpdateSettings)

	mux.HandleFunc("POST /api/proxy/test", h.handleTestProxy)
	mux.HandleFunc("GET /api/ports/available", h.handleGetAvailablePort)

	mux.HandleFunc("GET /api/icons", h.handleGetIcons)
	mux.HandleFunc("POST /api/icons/upload", h.handleUploadIcon)
	mux.HandleFunc("GET /icons/{filename}", h.handleServeIcon)

	mux.HandleFunc("GET /redirect", h.handleRedirect)

	// Auth routes
	mux.HandleFunc("GET /api/auth/status", h.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/login", h.handleAuthLogin)
	mux.HandleFunc("POST /api/auth/logout", h.handleAuthLogout)

	// Log routes (8 days retention)
	mux.HandleFunc("GET /api/logs", h.handleGetLogs)
	mux.HandleFunc("GET /api/logs/download", h.handleDownloadLogs)

	// Static web assets
	if h.webFS != nil {
		fileServer := http.FileServer(http.FS(h.webFS))
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimPrefix(r.URL.Path, "/")
			if path == "" {
				path = "index.html"
			}
			// Check if file exists in webFS
			f, err := h.webFS.Open(path)
			if err != nil {
				// Fallback to index.html for SPA
				path = "index.html"
				f, err = h.webFS.Open(path)
				if err != nil {
					http.NotFound(w, r)
					return
				}
			}
			f.Close()
			// Prevent browser/iframe caching for HTML and JS
			if strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".js") {
				w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
				w.Header().Set("Pragma", "no-cache")
			}
			h.serveWithGzip(w, r, fileServer)
		})
	}
}

type responseRecorder struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int64
}

func (rec *responseRecorder) WriteHeader(code int) {
	rec.statusCode = code
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *responseRecorder) Write(b []byte) (int, error) {
	if rec.statusCode == 0 {
		rec.statusCode = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytesWritten += int64(n)
	return n, err
}

func (rec *responseRecorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// RequestLoggingMiddleware logs all incoming HTTP requests and their completion status.
func RequestLoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		// Immediate audit entry for any non-idempotent operation (POST, PUT, DELETE)
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			slog.Info("[HTTP-IN] 收到操作请求", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		}

		next.ServeHTTP(rec, r)

		duration := time.Since(start)

		// Routine high-frequency telemetry & static assets
		isHighFreq := r.URL.Path == "/api/events" || r.URL.Path == "/api/system"
		isAsset := strings.HasPrefix(r.URL.Path, "/icons/") ||
			strings.HasSuffix(r.URL.Path, ".png") ||
			strings.HasSuffix(r.URL.Path, ".ico") ||
			strings.HasSuffix(r.URL.Path, ".css")

		if isHighFreq || isAsset {
			if rec.statusCode >= 400 {
				slog.Warn("[HTTP] 异常响应", "method", r.Method, "path", r.URL.Path, "status", rec.statusCode, "duration", duration, "remote", r.RemoteAddr)
			}
			return
		}

		// Log page access and JS bundle load specifically so version loading can be verified
		if r.URL.Path == "/" || r.URL.Path == "/index.html" || strings.HasPrefix(r.URL.Path, "/app.js") {
			slog.Info("[HTTP] 访问前端页面与核心脚本", "path", r.URL.Path, "status", rec.statusCode, "duration", duration, "remote", r.RemoteAddr)
			return
		}

		if rec.statusCode >= 400 {
			slog.Warn("[HTTP] 请求失败", "method", r.Method, "path", r.URL.Path, "status", rec.statusCode, "duration", duration, "remote", r.RemoteAddr)
		} else {
			slog.Info("[HTTP] 请求完成", "method", r.Method, "path", r.URL.Path, "status", rec.statusCode, "duration", duration, "remote", r.RemoteAddr)
		}
	})
}

func (h *Handler) jsonResponse(w http.ResponseWriter, r *http.Request, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	bytes, err := json.Marshal(data)
	if err != nil {
		slog.Error("JSON 序列化失败", "path", r.URL.Path, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"json marshal failed"}`))
		return
	}

	if status >= 400 {
		slog.Warn("[API 错误响应]", "method", r.Method, "path", r.URL.Path, "status", status, "response", string(bytes))
	}

	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") && len(bytes) > 512 {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(status)
		gz := gzip.NewWriter(w)
		defer gz.Close()
		_, _ = gz.Write(bytes)
		return
	}

	w.WriteHeader(status)
	_, _ = w.Write(bytes)
}

func (h *Handler) serveWithGzip(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		gzw := &gzipResponseWriter{Writer: gz, ResponseWriter: w}
		next.ServeHTTP(gzw, r)
		return
	}
	next.ServeHTTP(w, r)
}

type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func (h *Handler) checkAuth(r *http.Request) bool {
	if h.authMgr == nil || !h.authMgr.IsAuthRequired() {
		return true
	}
	return h.authMgr.IsRequestAuthenticated(r)
}

// /api/ports
func (h *Handler) handleGetPorts(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	ports := monitor.ScanPorts(h.procPath, false)
	desktopItems := h.storage.GetAllItems()
	portItemCount := make(map[int]int)
	portFirstItem := make(map[int]string)
	for _, item := range desktopItems {
		if item.Port > 0 {
			portItemCount[item.Port]++
			if _, ok := portFirstItem[item.Port]; !ok {
				portFirstItem[item.Port] = item.Name
			}
		}
	}

	for i := range ports {
		if count, ok := portItemCount[ports[i].LocalPort]; ok && count > 0 {
			ports[i].HasDesktop = true
			ports[i].DesktopCount = count
			ports[i].DesktopName = portFirstItem[ports[i].LocalPort]
		}
	}

	h.jsonResponse(w, r, ports, http.StatusOK)
}

// /api/processes
func (h *Handler) handleGetProcesses(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}
	procs := monitor.ScanAllProcesses(h.procPath)
	h.jsonResponse(w, r, procs, http.StatusOK)
}

// /api/system
func (h *Handler) handleGetSystem(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}
	metrics := h.systemSample.SampleWithHistory()
	h.jsonResponse(w, r, metrics, http.StatusOK)
}

// /api/host
func (h *Handler) handleGetHost(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}
	hostInfo := monitor.GetHostInfo(h.procPath)
	h.jsonResponse(w, r, hostInfo, http.StatusOK)
}

// /api/events SSE
func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := h.watcher.Subscribe("ports")
	defer h.watcher.Unsubscribe(ch)

	// Send initial snapshot
	initialPorts := monitor.ScanPorts(h.procPath, false)
	desktopItems := h.storage.GetAllItems()
	pItemCount := make(map[int]int)
	pMap := make(map[int]string)
	for _, item := range desktopItems {
		if item.Port > 0 {
			pItemCount[item.Port]++
			if _, ok := pMap[item.Port]; !ok {
				pMap[item.Port] = item.Name
			}
		}
	}
	for i := range initialPorts {
		if count, ok := pItemCount[initialPorts[i].LocalPort]; ok && count > 0 {
			initialPorts[i].HasDesktop = true
			initialPorts[i].DesktopCount = count
			initialPorts[i].DesktopName = pMap[initialPorts[i].LocalPort]
		}
	}

	initData, _ := json.Marshal(map[string]interface{}{
		"type":     "snapshot",
		"snapshot": initialPorts,
		"system":   h.systemSample.SampleWithHistory(),
		"time":     time.Now().Unix(),
	})
	fmt.Fprintf(w, "data: %s\n\n", initData)
	flusher.Flush()

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case eventBytes, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", string(eventBytes))
			flusher.Flush()
		}
	}
}

// /api/desktop/items
func (h *Handler) handleGetDesktopItems(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}
	items := h.storage.GetAllItems()
	h.jsonResponse(w, r, items, http.StatusOK)
}

func (h *Handler) handleCreateDesktopItem(w http.ResponseWriter, r *http.Request) {
	slog.Info("===> [API] 收到创建桌面图标请求", "remote", r.RemoteAddr, "contentLength", r.ContentLength)

	if !h.checkAuth(r) {
		slog.Warn("[API] 创建桌面图标鉴权未通过: 请求未认证", "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "未授权访问，请先登录"}, http.StatusUnauthorized)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("[API] 读取创建桌面图标请求体失败", "error", err)
		h.jsonResponse(w, r, map[string]string{"error": "读取请求失败: " + err.Error()}, http.StatusBadRequest)
		return
	}

	var rawMap map[string]interface{}
	_ = json.Unmarshal(bodyBytes, &rawMap)

	var item desktop.DesktopItem
	if err := json.Unmarshal(bodyBytes, &item); err != nil {
		slog.Error("[API] 解析创建桌面图标 JSON 失败", "error", err, "rawPayload", string(bodyBytes))
		h.jsonResponse(w, r, map[string]string{"error": "参数解析失败: " + err.Error()}, http.StatusBadRequest)
		return
	}

	if item.ID == "" {
		item.ID = fmt.Sprintf("item-%d", time.Now().UnixNano()%1000000)
	}
	if item.Name == "" {
		item.Name = "应用图标"
	}
	if item.UIType == "" {
		item.UIType = "url"
	}
	if item.Protocol == "" {
		item.Protocol = "http"
	}
	if item.Path == "" {
		item.Path = "/"
	}
	if _, hasAllUsers := rawMap["all_users"]; !hasAllUsers {
		item.AllUsers = true
	}
	item.Enabled = true

	// Derive or validate app name
	if item.AppName == "" {
		item.AppName = h.installer.DeriveAppName(item)
		slog.Info("[API] 未指定应用包名，系统自动派生生成", "appName", item.AppName, "name", item.Name)
	} else {
		if err := desktop.ValidateAppName(item.AppName); err != nil {
			slog.Warn("[API] 用户指定应用包名格式不符合规范", "appName", item.AppName, "error", err)
			h.jsonResponse(w, r, map[string]string{"error": "应用包名格式不符合规范: " + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Check if package name is already in use by another item
	for _, exist := range h.storage.GetAllItems() {
		if exist.ID != item.ID && exist.AppName == item.AppName {
			slog.Warn("[API] 桌面应用包名标识已存在", "appName", item.AppName, "conflictID", exist.ID, "conflictName", exist.Name)
			h.jsonResponse(w, r, map[string]string{"error": fmt.Sprintf("应用包名 %q 已被「%s」使用，请指定其他包名或留空由系统生成", item.AppName, exist.Name)}, http.StatusBadRequest)
			return
		}
	}

	slog.Info("[API] 桌面图标参数解析完成，准备安装",
		"id", item.ID,
		"name", item.Name,
		"appName", item.AppName,
		"mode", item.Mode,
		"port", item.Port,
		"targetURL", item.TargetURL,
		"container", item.ContainerName,
		"allUsers", item.AllUsers,
		"uiType", item.UIType,
		"hasIcon", item.Icon != "",
	)

	// If mode is proxy, start the reverse proxy
	if item.Mode == desktop.ModeProxy {
		if item.Port <= 0 {
			item.Port = proxy.RecommendAvailablePort(18000, nil)
			slog.Info("[API] 自动推荐分配反向代理本地端口", "port", item.Port)
		}
		if err := h.proxyMgr.StartProxy(item.ID, item.Port, item.TargetURL, item.SkipTLSVerify); err != nil {
			slog.Error("[API] 启动反向代理监听失败", "id", item.ID, "port", item.Port, "target", item.TargetURL, "error", err)
			h.jsonResponse(w, r, map[string]string{"error": "启动反向代理失败: " + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Install to fnOS desktop
	slog.Info("[API] 正在调用安装器注册到飞牛桌面系统...", "appName", item.AppName, "name", item.Name)
	if err := h.installer.InstallItem(item); err != nil {
		slog.Error("[API] 安装到飞牛桌面失败", "appName", item.AppName, "name", item.Name, "error", err)
		h.jsonResponse(w, r, map[string]string{"error": "安装到飞牛桌面失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}
	item.Installed = true

	if err := h.storage.SaveItem(item); err != nil {
		slog.Error("[API] 保存桌面图标配置到存储数据库失败", "appName", item.AppName, "id", item.ID, "error", err)
		h.jsonResponse(w, r, map[string]string{"error": "保存失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	slog.Info("<=== [API] 桌面图标创建并安装成功！", "appName", item.AppName, "id", item.ID, "name", item.Name, "port", item.Port)
	h.jsonResponse(w, r, item, http.StatusOK)
}

func (h *Handler) handleUpdateDesktopItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	slog.Info("===> [API] 收到更新桌面图标请求", "id", id, "remote", r.RemoteAddr)

	if !h.checkAuth(r) {
		slog.Warn("[API] 更新桌面图标鉴权未通过: 请求未认证", "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "未授权访问，请先登录"}, http.StatusUnauthorized)
		return
	}

	if id == "" {
		h.jsonResponse(w, r, map[string]string{"error": "缺少ID"}, http.StatusBadRequest)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("[API] 读取更新桌面图标请求数据失败", "id", id, "error", err)
		h.jsonResponse(w, r, map[string]string{"error": "读取请求失败: " + err.Error()}, http.StatusBadRequest)
		return
	}

	var rawMap map[string]interface{}
	_ = json.Unmarshal(bodyBytes, &rawMap)

	var item desktop.DesktopItem
	if err := json.Unmarshal(bodyBytes, &item); err != nil {
		slog.Error("[API] 解析更新桌面图标 JSON 失败", "id", id, "error", err)
		h.jsonResponse(w, r, map[string]string{"error": "参数解析失败: " + err.Error()}, http.StatusBadRequest)
		return
	}
	item.ID = id

	existing, hasExisting := h.storage.GetItem(id)
	oldAppName := ""
	if hasExisting {
		oldAppName = existing.AppName
	}

	// Preserve enabled state if not explicitly specified in payload
	if _, hasEnabled := rawMap["enabled"]; !hasEnabled {
		if hasExisting {
			item.Enabled = existing.Enabled
		} else {
			item.Enabled = true
		}
	}

	slog.Info("[API] 更新桌面图标参数解析完成", "id", id, "name", item.Name, "mode", item.Mode, "port", item.Port, "enabled", item.Enabled, "oldAppName", oldAppName, "newAppName", item.AppName)

	if item.Mode == desktop.ModeProxy && item.Enabled {
		_ = h.proxyMgr.StartProxy(item.ID, item.Port, item.TargetURL, item.SkipTLSVerify)
	} else if item.Mode != desktop.ModeProxy {
		h.proxyMgr.StopProxy(item.ID)
	}

	// Derive or validate app name
	if item.AppName == "" {
		if oldAppName != "" {
			item.AppName = oldAppName
		} else {
			item.AppName = h.installer.DeriveAppName(item)
		}
	} else {
		if err := desktop.ValidateAppName(item.AppName); err != nil {
			slog.Warn("[API] 更新桌面图标包名格式不合法", "appName", item.AppName, "error", err)
			h.jsonResponse(w, r, map[string]string{"error": "应用包名格式不符合规范: " + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Check if package name is already in use by another item
	for _, exist := range h.storage.GetAllItems() {
		if exist.ID != item.ID && exist.AppName == item.AppName {
			slog.Warn("[API] 更新桌面应用包名已存在冲突", "appName", item.AppName, "conflictID", exist.ID)
			h.jsonResponse(w, r, map[string]string{"error": fmt.Sprintf("应用包名 %q 已被「%s」使用，请更改包名", item.AppName, exist.Name)}, http.StatusBadRequest)
			return
		}
	}

	// Clean up old app on fnOS before reinstalling/updating if appName changed
	if hasExisting && oldAppName != "" && oldAppName != item.AppName {
		slog.Info("[API] 检测到应用包名变更，注销旧版本应用以确保注销旧图标...", "oldAppName", oldAppName, "newAppName", item.AppName)
		_ = h.installer.UninstallSingleApp(oldAppName)
	}

	if item.Enabled {
		slog.Info("[API] 正在更新/重新安装飞牛桌面应用...", "appName", item.AppName, "name", item.Name)
		if err := h.installer.InstallItem(item); err != nil {
			slog.Error("[API] 更新飞牛桌面应用失败", "appName", item.AppName, "error", err)
			h.jsonResponse(w, r, map[string]string{"error": "更新飞牛桌面应用失败: " + err.Error()}, http.StatusInternalServerError)
			return
		}
		item.Installed = true
	} else {
		slog.Info("[API] 应用已禁用，执行注销卸载...", "appName", item.AppName)
		_ = h.installer.UninstallItem(item)
		item.Installed = false
	}

	if err := h.storage.SaveItem(item); err != nil {
		slog.Error("[API] 保存更新桌面图标数据失败", "appName", item.AppName, "id", id, "error", err)
		h.jsonResponse(w, r, map[string]string{"error": "保存失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	slog.Info("<=== [API] 桌面图标更新完成！", "appName", item.AppName, "id", id, "name", item.Name)
	h.jsonResponse(w, r, item, http.StatusOK)
}

func (h *Handler) handleDeleteDesktopItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	slog.Info("===> [AUDIT] 收到移出/删除桌面图标请求", "id", id, "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)

	if !h.checkAuth(r) {
		slog.Warn("[AUDIT] 移出桌面图标鉴权未通过", "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "未授权访问，请先登录"}, http.StatusUnauthorized)
		return
	}

	if id == "" {
		slog.Warn("[AUDIT] 移出桌面图标缺少ID参数", "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "缺少ID"}, http.StatusBadRequest)
		return
	}

	h.proxyMgr.StopProxy(id)
	if existing, ok := h.storage.GetItem(id); ok {
		slog.Info("[AUDIT] 找到桌面图标，正在通过 appcenter-cli 停止并注销飞牛桌面应用...",
			"id", id,
			"appName", existing.AppName,
			"name", existing.Name,
			"port", existing.Port,
			"mode", existing.Mode,
		)
		if err := h.installer.UninstallItem(existing); err != nil {
			slog.Warn("[AUDIT] 桌面应用卸载产生输出/警告", "appName", existing.AppName, "error", err)
		} else {
			slog.Info("[AUDIT] 飞牛桌面应用已成功卸载并注销", "appName", existing.AppName)
		}
	} else {
		slog.Info("[AUDIT] 未在存储中找到图标记录，尝试按 ID 派生标识执行注销...", "id", id)
		_ = h.installer.UninstallItemByID(id)
	}

	if err := h.storage.DeleteItem(id); err != nil {
		slog.Error("[AUDIT] 从数据库删除桌面图标记录失败", "id", id, "error", err)
		h.jsonResponse(w, r, map[string]string{"error": "删除失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	slog.Info("<=== [AUDIT] 桌面图标已成功从飞牛桌面注销并从存储数据库移除", "id", id)
	h.jsonResponse(w, r, map[string]bool{"success": true}, http.StatusOK)
}

type clientLogRequest struct {
	Level   string      `json:"level"`
	Type    string      `json:"type"`
	Action  string      `json:"action"`
	Message string      `json:"message"`
	Stack   string      `json:"stack"`
	Details interface{} `json:"details"`
}

func (h *Handler) handleClientLog(w http.ResponseWriter, r *http.Request) {
	var req clientLogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "参数解析失败"}, http.StatusBadRequest)
		return
	}

	if req.Level == "error" || req.Type == "error" {
		slog.Error("[AUDIT-CLIENT] 前端捕获异常",
			"action", req.Action,
			"message", req.Message,
			"stack", req.Stack,
			"details", req.Details,
			"remote", r.RemoteAddr,
		)
	} else {
		slog.Info("[AUDIT-CLIENT] 前端用户操作",
			"action", req.Action,
			"message", req.Message,
			"details", req.Details,
			"remote", r.RemoteAddr,
		)
	}
	h.jsonResponse(w, r, map[string]bool{"ok": true}, http.StatusOK)
}

func (h *Handler) handleToggleDesktopItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	slog.Info("===> [API] 收到切换桌面图标状态请求", "id", id, "remote", r.RemoteAddr)

	if !h.checkAuth(r) {
		slog.Warn("[API] 切换桌面图标状态鉴权未通过", "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "未授权访问，请先登录"}, http.StatusUnauthorized)
		return
	}

	if _, loaded := h.inFlightOps.LoadOrStore(id, true); loaded {
		slog.Warn("[API] 桌面图标正在处理中，拒绝重复并发请求", "id", id)
		h.jsonResponse(w, r, map[string]string{"error": "该桌面图标正在处理中，请勿频繁点击"}, http.StatusConflict)
		return
	}
	defer h.inFlightOps.Delete(id)

	item, ok := h.storage.GetItem(id)
	if !ok {
		slog.Warn("[API] 切换状态未找到指定图标", "id", id)
		h.jsonResponse(w, r, map[string]string{"error": "未找到指定图标"}, http.StatusNotFound)
		return
	}

	targetState := !item.Enabled
	slog.Info("[API] 准备切换桌面图标状态", "id", id, "name", item.Name, "当前状态", item.Enabled, "目标状态", targetState)

	item.Enabled = targetState
	if item.AppName == "" {
		item.AppName = h.installer.DeriveAppName(item)
	}

	if item.Enabled {
		if item.Mode == desktop.ModeProxy {
			_ = h.proxyMgr.StartProxy(item.ID, item.Port, item.TargetURL, item.SkipTLSVerify)
		}
		slog.Info("[API] 正在启用并安装桌面应用...", "appName", item.AppName, "name", item.Name)
		if err := h.installer.InstallItem(item); err != nil {
			slog.Error("[API] 启用飞牛桌面应用失败", "appName", item.AppName, "name", item.Name, "error", err)
			h.jsonResponse(w, r, map[string]string{"error": "启用桌面应用失败: " + err.Error()}, http.StatusInternalServerError)
			return
		}
		item.Installed = true
	} else {
		if item.Mode == desktop.ModeProxy {
			h.proxyMgr.StopProxy(item.ID)
		}
		slog.Info("[API] 正在禁用并卸载桌面应用...", "appName", item.AppName, "name", item.Name)
		_ = h.installer.UninstallItem(item)
		item.Installed = false
	}

	_ = h.storage.SaveItem(item)
	slog.Info("<=== [API] 桌面图标状态切换成功！", "id", id, "name", item.Name, "enabled", item.Enabled, "appName", item.AppName)
	h.jsonResponse(w, r, item, http.StatusOK)
}

// /api/settings
func (h *Handler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}
	settings := h.storage.GetSettings()
	settings.AuthPassword = ""
	// Include version in response for frontend display
	resp := map[string]interface{}{
		"portal_port":      settings.PortalPort,
		"portal_name":      settings.PortalName,
		"portal_ui_type":   settings.PortalUIType,
		"portal_all_users": settings.PortalAllUsers,
		"portal_icon":      settings.PortalIcon,
		"version":          h.appVersion,
	}
	h.jsonResponse(w, r, resp, http.StatusOK)
}

func (h *Handler) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		slog.Warn("[AUDIT] 修改系统设置鉴权未通过", "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	var req struct {
		desktop.Settings
		ClearPassword bool `json:"clear_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("[AUDIT] 修改系统设置参数解析失败", "error", err, "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "参数解析失败: " + err.Error()}, http.StatusBadRequest)
		return
	}

	slog.Info("[AUDIT] 收到修改系统设置请求",
		"portalName", req.PortalName,
		"portalUIType", req.PortalUIType,
		"portalAllUsers", req.PortalAllUsers,
		"hasNewPassword", req.AuthPassword != "",
		"clearPassword", req.ClearPassword,
		"remote", r.RemoteAddr,
	)

	current := h.storage.GetSettings()
	if req.ClearPassword {
		current.AuthPassword = ""
		if h.authMgr != nil {
			h.authMgr.SetPassword("")
		}
	} else if req.AuthPassword != "" {
		current.AuthPassword = req.AuthPassword
		if h.authMgr != nil {
			h.authMgr.SetPassword(req.AuthPassword)
		}
	}
	if req.PortalName != "" {
		current.PortalName = req.PortalName
	}
	if req.PortalUIType != "" {
		current.PortalUIType = req.PortalUIType
	}
	current.PortalAllUsers = req.PortalAllUsers

	if err := h.storage.UpdateSettings(current); err != nil {
		slog.Error("[AUDIT] 保存系统设置失败", "error", err)
		h.jsonResponse(w, r, map[string]string{"error": "更新配置失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	if err := h.installer.SyncSelfApp(current); err != nil {
		slog.Warn("[AUDIT] 同步自身桌面图标遇到告警", "error", err)
	}

	slog.Info("<=== [AUDIT] 系统设置更新保存成功并即时生效！")
	current.AuthPassword = ""
	h.jsonResponse(w, r, current, http.StatusOK)
}

// /api/proxy/test
func (h *Handler) handleTestProxy(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	var req struct {
		TargetURL string `json:"target_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TargetURL == "" {
		h.jsonResponse(w, r, map[string]string{"error": "请输入目标地址"}, http.StatusBadRequest)
		return
	}

	result := proxy.TestTarget(req.TargetURL, 5*time.Second)
	h.jsonResponse(w, r, result, http.StatusOK)
}

// /api/ports/available
func (h *Handler) handleGetAvailablePort(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	startPort := 18000
	if q := r.URL.Query().Get("start"); q != "" {
		if p, err := strconv.Atoi(q); err == nil && p > 1024 {
			startPort = p
		}
	}

	usedPorts := make(map[int]bool)
	items := h.storage.GetAllItems()
	for _, item := range items {
		if item.Port > 0 {
			usedPorts[item.Port] = true
		}
	}
	activePorts := monitor.ScanPorts(h.procPath, false)
	for _, p := range activePorts {
		usedPorts[p.LocalPort] = true
	}

	recommended := proxy.RecommendAvailablePort(startPort, usedPorts)
	h.jsonResponse(w, r, map[string]int{"recommended_port": recommended}, http.StatusOK)
}

// /api/icons
func (h *Handler) handleGetIcons(w http.ResponseWriter, r *http.Request) {
	files, err := os.ReadDir(h.iconsDir)
	var list []string
	if err == nil {
		for _, f := range files {
			if !f.IsDir() {
				list = append(list, f.Name())
			}
		}
	}
	h.jsonResponse(w, r, list, http.StatusOK)
}

// /api/icons/upload
func (h *Handler) handleUploadIcon(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		slog.Warn("[AUDIT] 上传图标鉴权未通过", "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		slog.Warn("[AUDIT] 上传图标文件解析失败", "error", err, "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "文件解析失败"}, http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("icon")
	if err != nil {
		slog.Warn("[AUDIT] 未获取到上传文件", "error", err, "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "未获取到上传文件"}, http.StatusBadRequest)
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" && ext != ".svg" && ext != ".ico" {
		slog.Warn("[AUDIT] 上传图标格式不受支持", "filename", header.Filename, "ext", ext, "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "仅支持 PNG/JPG/WebP/SVG/ICO 图标"}, http.StatusBadRequest)
		return
	}

	cleanBase := desktop.SanitizeFileName(header.Filename)
	filename := fmt.Sprintf("%d_%s", time.Now().Unix(), cleanBase)
	destPath := filepath.Join(h.iconsDir, filename)

	slog.Info("[AUDIT] 正在保存用户上传的图标...", "originalFilename", header.Filename, "savedAs", filename, "sizeBytes", header.Size, "remote", r.RemoteAddr)

	out, err := os.Create(destPath)
	if err != nil {
		slog.Error("[AUDIT] 创建图标文件失败", "destPath", destPath, "error", err)
		h.jsonResponse(w, r, map[string]string{"error": "创建文件失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		slog.Error("[AUDIT] 写入图标文件数据失败", "destPath", destPath, "error", err)
		h.jsonResponse(w, r, map[string]string{"error": "保存文件失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	slog.Info("<=== [AUDIT] 用户上传图标成功保存", "filename", filename, "url", "/icons/"+filename)
	h.jsonResponse(w, r, map[string]string{"filename": filename, "url": "/icons/" + filename}, http.StatusOK)
}

func (h *Handler) handleServeIcon(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	filename = filepath.Base(filename)
	iconPath := filepath.Join(h.iconsDir, filename)
	if _, err := os.Stat(iconPath); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, iconPath)
}

// /redirect?target=...
func (h *Handler) handleRedirect(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "缺少跳转目标 target", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "http://" + target
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// Auth handlers
func (h *Handler) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	authRequired := h.authMgr != nil && h.authMgr.IsAuthRequired()
	authenticated := !authRequired || h.checkAuth(r)
	h.jsonResponse(w, r, map[string]bool{
		"auth_required": authRequired,
		"authenticated": authenticated,
	}, http.StatusOK)
}

func (h *Handler) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "格式错误"}, http.StatusBadRequest)
		return
	}

	if h.authMgr == nil || !h.authMgr.IsAuthRequired() {
		h.jsonResponse(w, r, map[string]bool{"success": true}, http.StatusOK)
		return
	}

	if !h.authMgr.VerifyPassword(req.Password) {
		h.jsonResponse(w, r, map[string]string{"error": "密码错误"}, http.StatusUnauthorized)
		return
	}

	token := h.authMgr.GenerateToken()
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400 * 30,
	})

	h.jsonResponse(w, r, map[string]string{"token": token}, http.StatusOK)
}

func (h *Handler) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	h.jsonResponse(w, r, map[string]bool{"success": true}, http.StatusOK)
}

func (h *Handler) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	date := r.URL.Query().Get("date")
	level := r.URL.Query().Get("level")
	search := r.URL.Query().Get("search")
	limit := 1000
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}

	logInst := h.loggerInstance
	if logInst == nil {
		logInst = logger.GetDefault()
	}

	if logInst == nil {
		h.jsonResponse(w, r, map[string]string{"error": "日志系统未初始化"}, http.StatusInternalServerError)
		return
	}

	resp, err := logInst.ReadLogs(date, level, search, limit)
	if err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "读取日志失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	h.jsonResponse(w, r, resp, http.StatusOK)
}

func (h *Handler) handleDownloadLogs(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	logInst := h.loggerInstance
	if logInst == nil {
		logInst = logger.GetDefault()
	}

	if logInst == nil {
		http.Error(w, "日志系统未就绪", http.StatusInternalServerError)
		return
	}

	filePath := logInst.GetLogFilePath(date)
	fi, err := os.Stat(filePath)
	if err != nil || fi.IsDir() {
		http.Error(w, "未找到对应日期的日志文件", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"fn-docker-to-desktop-%s.log\"", date))
	http.ServeFile(w, r, filePath)
}
