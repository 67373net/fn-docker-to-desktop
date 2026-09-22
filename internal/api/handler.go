package api

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	dockLabelMu    sync.Mutex
	dockLabelItems      []desktop.DockLabelItem
	dockLabelExp        time.Time
	versionCheckMu      sync.Mutex
	versionCheckCached  *VersionCheckResponse
	versionCheckExp     time.Time
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
	mux.HandleFunc("GET /api/system/version-check", h.handleCheckUpdate)
	mux.HandleFunc("GET /api/host", h.handleGetHost)
	mux.HandleFunc("GET /api/events", h.handleEvents)

	mux.HandleFunc("GET /api/desktop/items", h.handleGetDesktopItems)
	mux.HandleFunc("POST /api/desktop/items", h.handleCreateDesktopItem)
	mux.HandleFunc("PUT /api/desktop/items/{id}", h.handleUpdateDesktopItem)
	mux.HandleFunc("POST /api/desktop/items/{id}", h.handleUpdateDesktopItem)
	mux.HandleFunc("DELETE /api/desktop/items/{id}", h.handleDeleteDesktopItem)
	mux.HandleFunc("POST /api/desktop/items/{id}/delete", h.handleDeleteDesktopItem)
	mux.HandleFunc("POST /api/desktop/items/{id}/toggle", h.handleToggleDesktopItem)
	mux.HandleFunc("GET /api/desktop/export", h.handleExportDesktopItems)
	mux.HandleFunc("GET /api/desktop/docklabel", h.handleGetDockLabelItems)
	mux.HandleFunc("POST /api/desktop/docklabel/{id}/toggle", h.handleToggleDockLabelItem)
	mux.HandleFunc("GET /api/desktop/docklabel/icon", h.handleGetDockLabelIcon)
	// Backward compatibility aliases
	mux.HandleFunc("GET /api/desktop/watchcow", h.handleGetDockLabelItems)
	mux.HandleFunc("POST /api/desktop/watchcow/{id}/toggle", h.handleToggleDockLabelItem)
	mux.HandleFunc("GET /api/desktop/watchcow/icon", h.handleGetDockLabelIcon)

	mux.HandleFunc("POST /api/logs/client", h.handleClientLog)

	mux.HandleFunc("GET /api/settings", h.handleGetSettings)
	mux.HandleFunc("POST /api/settings", h.handleUpdateSettings)

	mux.HandleFunc("POST /api/proxy/test", h.handleTestProxy)
	mux.HandleFunc("GET /api/ports/available", h.handleGetAvailablePort)

	mux.HandleFunc("GET /api/icons", h.handleGetIcons)
	mux.HandleFunc("POST /api/icons/upload", h.handleUploadIcon)
	mux.HandleFunc("DELETE /api/icons", h.handleDeleteIcon)
	mux.HandleFunc("DELETE /api/icons/{filename...}", h.handleDeleteIcon)
	mux.HandleFunc("GET /icons/{filename}", h.handleServeIcon)

	mux.HandleFunc("GET /redirect", h.handleRedirect)
	mux.HandleFunc("GET /redirect/", h.handleRedirect)
	mux.HandleFunc("GET /api/redirect", h.handleRedirect)
	mux.HandleFunc("GET /api/redirect/", h.handleRedirect)

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
		serveIndexWithSession := func(w http.ResponseWriter, r *http.Request) {
			data, err := fs.ReadFile(h.webFS, "index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			sessionToken := globalAppSessionMgr.CreateSession()
			http.SetCookie(w, &http.Cookie{
				Name:     "fn_app_session",
				Value:    sessionToken,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			injectTag := fmt.Sprintf("<meta name=\"fn-session-token\" content=\"%s\">\n  <script>window.__FN_SESSION__=%q;</script>\n</head>", sessionToken, sessionToken)
			htmlStr := strings.Replace(string(data), "</head>", injectTag, 1)

			h.serveHTMLBytes(w, r, []byte(htmlStr))
		}

		mux.HandleFunc("GET /api/auth/session", func(w http.ResponseWriter, r *http.Request) {
			sessionToken := globalAppSessionMgr.CreateSession()
			http.SetCookie(w, &http.Cookie{
				Name:     "fn_app_session",
				Value:    sessionToken,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			clientIP := GetClientIP(r)
			isWAN := clientIP != nil && !IsPrivateOrLocalIP(clientIP)
			h.jsonResponse(w, r, map[string]any{
				"session_token": sessionToken,
				"wan_detected":  isWAN,
			}, http.StatusOK)
		})

		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimPrefix(r.URL.Path, "/")
			if path == "" || path == "index.html" {
				serveIndexWithSession(w, r)
				return
			}
			// Check if file exists in webFS
			f, err := h.webFS.Open(path)
			if err != nil {
				// Fallback to index.html with session token for SPA
				serveIndexWithSession(w, r)
				return
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

func (h *Handler) jsonError(w http.ResponseWriter, r *http.Request, msg string, status int) {
	h.jsonResponse(w, r, map[string]string{"error": msg}, status)
}

func (h *Handler) serveHTMLBytes(w http.ResponseWriter, r *http.Request, content []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")

	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") && len(content) > 512 {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		gz := gzip.NewWriter(w)
		defer gz.Close()
		_, _ = gz.Write(content)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
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
	clientIP := GetClientIP(r)
	isWAN := clientIP != nil && !IsPrivateOrLocalIP(clientIP)

	// 1. If explicit password authentication is configured, always enforce password login
	if h.authMgr != nil && h.authMgr.IsAuthRequired() {
		return h.authMgr.IsRequestAuthenticated(r)
	}

	// 2. Validate legitimate frontend session token (from X-App-Session header, cookie, or query param)
	sessionToken := r.Header.Get("X-App-Session")
	if sessionToken == "" {
		if cookie, err := r.Cookie("fn_app_session"); err == nil {
			sessionToken = cookie.Value
		}
	}
	if sessionToken == "" {
		sessionToken = r.URL.Query().Get("session")
	}

	if sessionToken != "" && globalAppSessionMgr.ValidateSession(sessionToken) {
		// Legitimate session initiated by the authenticated user in fnOS (works in fnOS Connect WAN or LAN)
		return true
	}

	// 3. If accessed from WAN (Public IP) and has NO valid frontend session token:
	// Strictly block malicious programs, crawlers, and scanners bypassing the UI!
	if isWAN {
		slog.Warn("[SECURITY] 拦截公网绕过前端界面的未授权 API 访问", "remote", r.RemoteAddr, "clientIP", clientIP.String(), "path", r.URL.Path)
		return false
	}

	// 4. Local network / loopback fallback for direct LAN tools
	return true
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

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

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
	start := time.Now()
	defer func() {
		dur := time.Since(start)
		if dur > 500*time.Millisecond {
			slog.Warn("[PERF] handleGetDesktopItems 响应耗时过长", "duration", dur)
		}
	}()

	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}
	items := h.storage.GetAllItems()

	if h.installer != nil {
		for idx := range items {
			appName := items[idx].AppName
			if appName == "" {
				appName = h.installer.DeriveAppName(items[idx])
			}
			if op, inFlight := h.inFlightOps.Load(items[idx].ID); inFlight {
				items[idx].Reconciling = true
				if opStr, ok := op.(string); ok && opStr != "" {
					items[idx].StatusText = opStr
				} else {
					items[idx].StatusText = "更新中..."
				}
			} else if isRec, statusText := h.installer.GetReconcileStatus(items[idx].ID, appName); isRec {
				items[idx].Reconciling = true
				items[idx].StatusText = statusText
			}
		}
	}
	h.jsonResponse(w, r, items, http.StatusOK)
}

func (h *Handler) handleExportDesktopItems(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}
	items := h.storage.GetAllItems()
	exportData := map[string]any{
		"version":     h.appVersion,
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"total":       len(items),
		"items":       items,
	}

	if r.URL.Query().Get("format") == "json" {
		filename := fmt.Sprintf("fn-desktop-icons-%s.json", time.Now().Format("20060102-150405"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		_ = json.NewEncoder(w).Encode(exportData)
		return
	}

	jsonData, err := json.MarshalIndent(exportData, "", "  ")
	if err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "序列化配置数据失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	zipBuf := new(bytes.Buffer)
	zw := zip.NewWriter(zipBuf)

	// 1. Write desktop-items.json into zip
	jsonHeader := &zip.FileHeader{
		Name:     "desktop-items.json",
		Method:   zip.Deflate,
		Modified: time.Now(),
	}
	jw, err := zw.CreateHeader(jsonHeader)
	if err == nil {
		_, _ = jw.Write(jsonData)
	}

	// 2. Package all referenced local icon files into icons/
	exportedCount := 0
	seenZipEntries := make(map[string]bool)

	for _, item := range items {
		iconSource := strings.TrimSpace(item.Icon)
		if iconSource == "" {
			slog.Info("[EXPORT] 项目未配置独立图标，跳过图标文件打包", "item", item.Name, "id", item.ID)
			continue
		}

		var iconBytes []byte
		var iconZipName string

		// 1. Check data URI: data:image/png;base64,...
		if strings.HasPrefix(iconSource, "data:") {
			idx := strings.Index(iconSource, ",")
			if idx != -1 {
				base64Data := strings.TrimSpace(iconSource[idx+1:])
				if raw, err := base64.StdEncoding.DecodeString(base64Data); err == nil && len(raw) > 0 {
					iconBytes = raw
					iconZipName = fmt.Sprintf("icons/%s_icon.png", item.ID)
				}
			}
		}

		// 2. Local icons directory or uploaded icon
		if iconBytes == nil && (strings.HasPrefix(iconSource, "/icons/") || strings.HasPrefix(iconSource, "icons/")) {
			rel := strings.TrimPrefix(strings.TrimPrefix(iconSource, "/icons/"), "icons/")
			target := filepath.Join(h.iconsDir, filepath.Clean(rel))
			if data, err := os.ReadFile(target); err == nil && len(data) > 0 {
				iconBytes = data
				iconZipName = "icons/" + filepath.Base(rel)
			}
		}

		// 3. Remote URL or cached remote URL
		if iconBytes == nil && (strings.HasPrefix(iconSource, "http://") || strings.HasPrefix(iconSource, "https://")) {
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(iconSource)))
			ext := filepath.Ext(iconSource)
			if ext == "" || len(ext) > 5 {
				ext = ".png"
			}
			cacheFile := filepath.Join(h.iconsDir, "dock_cache_"+hash+ext)
			legacyCache := filepath.Join(h.iconsDir, "wc_cache_"+hash+ext)
			if data, err := os.ReadFile(cacheFile); err == nil && len(data) > 0 {
				iconBytes = data
				iconZipName = "icons/" + hash + ext
			} else if data, err := os.ReadFile(legacyCache); err == nil && len(data) > 0 {
				iconBytes = data
				iconZipName = "icons/" + hash + ext
			} else {
				client := &http.Client{Timeout: 3 * time.Second}
				if resp, err := client.Get(iconSource); err == nil && resp.StatusCode == http.StatusOK {
					data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
					resp.Body.Close()
					if err == nil && len(data) > 0 {
						iconBytes = data
						iconZipName = "icons/" + hash + ext
						_ = os.WriteFile(cacheFile, data, 0644)
					}
				} else if resp != nil {
					resp.Body.Close()
				}
			}
		}

		// 4. Embedded static or local host files
		if iconBytes == nil {
			cleanSource := strings.TrimPrefix(iconSource, "file://")
			base := filepath.Base(cleanSource)
			if h.webFS != nil && (base == "icon.png" || iconSource == "icon.png") {
				if data, err := fs.ReadFile(h.webFS, "icon.png"); err == nil && len(data) > 0 {
					iconBytes = data
					iconZipName = "icons/icon.png"
				}
			} else if h.webFS != nil && (base == "default_item_icon.png" || iconSource == "default_item_icon.png") {
				if data, err := fs.ReadFile(h.webFS, "default_item_icon.png"); err == nil && len(data) > 0 {
					iconBytes = data
					iconZipName = "icons/default_item_icon.png"
				}
			} else {
				for _, cand := range []string{
					cleanSource,
					filepath.Join(h.iconsDir, base),
					filepath.Join("/home/net67373/watchcow-proxy/icons", base),
					filepath.Join("/home/net67373/watchcow/icons", base),
				} {
					if data, err := os.ReadFile(cand); err == nil && len(data) > 0 {
						iconBytes = data
						iconZipName = "icons/" + base
						break
					}
				}
			}
		}

		if iconBytes != nil && iconZipName != "" {
			if !seenZipEntries[iconZipName] {
				seenZipEntries[iconZipName] = true
				iconHeader := &zip.FileHeader{
					Name:     iconZipName,
					Method:   zip.Deflate,
					Modified: time.Now(),
				}
				if iw, err := zw.CreateHeader(iconHeader); err == nil {
					if _, err := iw.Write(iconBytes); err == nil {
						exportedCount++
						slog.Info("[EXPORT] 成功导出图标至备份包", "item", item.Name, "id", item.ID, "zipEntry", iconZipName, "bytes", len(iconBytes))
						continue
					}
				}
			} else {
				exportedCount++
				slog.Info("[EXPORT] 图标已包含在备份包中(复用)", "item", item.Name, "id", item.ID, "zipEntry", iconZipName)
				continue
			}
		}

		slog.Warn("[EXPORT] 图标无法读取，未包含在备份包中", "item", item.Name, "id", item.ID, "iconSource", desktop.SummarizeIconSource(iconSource))
	}

	slog.Info("[EXPORT] 桌面图标备份ZIP已生成", "totalItems", len(items), "exportedIcons", exportedCount, "totalZipEntries", len(seenZipEntries)+1, "zipBytes", zipBuf.Len())

	if err := zw.Close(); err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "打包压缩文件失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	filename := fmt.Sprintf("fn-desktop-icons-%s.zip", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Length", strconv.Itoa(zipBuf.Len()))
	_, _ = w.Write(zipBuf.Bytes())
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
		item.AllUsers = false
	}
	item.Enabled = true

	if item.Mode == desktop.ModeShortcut {
		target := strings.TrimSpace(item.TargetURL)
		if target != "" && !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
			target = "https://" + target
		}
		item.TargetURL = target
		item.Port = 0
		item.Protocol = ""
		item.Path = ""
		item.UIType = "url"
	}

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

	// Ensure icon is permanently saved into iconsDir
	desktop.PersistItemIcon(&item, h.iconsDir)

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

	if item.Mode == desktop.ModeShortcut {
		target := strings.TrimSpace(item.TargetURL)
		if target != "" && !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
			target = "https://" + target
		}
		item.TargetURL = target
		item.Port = 0
		item.Protocol = ""
		item.Path = ""
		item.UIType = "url"
	}

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

	// Clean up old app on fnOS before installing/updating (aligned with WatchCow processDashboardReinstall)
	if hasExisting && oldAppName != "" && h.installer.IsAppInstalled(oldAppName) {
		slog.Info("[API] 正在更新桌面应用，先注销旧版本以确保元数据完整更新...", "oldAppName", oldAppName, "newAppName", item.AppName)
		_ = h.installer.UninstallSingleApp(oldAppName)
	}

	// Ensure icon is permanently saved into iconsDir
	desktop.PersistItemIcon(&item, h.iconsDir)

	if item.Enabled {
		slog.Info("[API] 正在安装更新后的飞牛桌面应用...", "appName", item.AppName, "name", item.Name)
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

	item, ok := h.storage.GetItem(id)
	if !ok {
		slog.Warn("[API] 切换状态未找到指定图标", "id", id)
		h.jsonResponse(w, r, map[string]string{"error": "未找到指定图标"}, http.StatusNotFound)
		return
	}

	targetState := !item.Enabled
	opText := "停用中..."
	if targetState {
		opText = "启用中..."
	}

	if _, loaded := h.inFlightOps.LoadOrStore(id, opText); loaded {
		slog.Warn("[API] 桌面图标正在处理中，拒绝重复并发请求", "id", id)
		h.jsonResponse(w, r, map[string]string{"error": "该桌面图标正在处理中，请勿频繁点击"}, http.StatusConflict)
		return
	}
	defer h.inFlightOps.Delete(id)

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
		"portal_port":            settings.PortalPort,
		"portal_name":            settings.PortalName,
		"portal_ui_type":         settings.PortalUIType,
		"portal_all_users":       settings.PortalAllUsers,
		"portal_icon":            settings.PortalIcon,
		"portal_icon_type":       settings.PortalIconType,
		"portal_icon_text":       settings.PortalIconText,
		"portal_icon_text_color": settings.PortalIconTextColor,
		"portal_icon_bg_color":   settings.PortalIconBgColor,
		"version":                h.appVersion,
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
		"portalIcon", req.PortalIcon,
		"portalIconType", req.PortalIconType,
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
	if req.PortalIcon != "" {
		current.PortalIcon = req.PortalIcon
	}
	if req.PortalIconType != "" {
		current.PortalIconType = req.PortalIconType
	}
	current.PortalIconText = req.PortalIconText
	if req.PortalIconTextColor != "" {
		current.PortalIconTextColor = req.PortalIconTextColor
	}
	if req.PortalIconBgColor != "" {
		current.PortalIconBgColor = req.PortalIconBgColor
	}

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

	if IsRestrictedMetadataTarget(req.TargetURL) {
		slog.Warn("[SECURITY] 拦截针对云元数据/受限内网服务的代理测试请求", "target", req.TargetURL, "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "禁止探测云平台元数据或受限内网服务 (SSRF 防护)"}, http.StatusForbidden)
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

// IconInfo represents icon metadata in the icon library.
type IconInfo struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	LastUsed int64  `json:"last_used"`
	InUse    bool   `json:"in_use"`
}

// /api/icons
func (h *Handler) handleGetIcons(w http.ResponseWriter, r *http.Request) {
	files, err := os.ReadDir(h.iconsDir)
	if err != nil {
		h.jsonResponse(w, r, []IconInfo{}, http.StatusOK)
		return
	}

	usageMap := make(map[string]int64)
	if h.storage != nil {
		items := h.storage.GetAllItems()
		for _, it := range items {
			clean := strings.TrimPrefix(it.Icon, "/icons/")
			clean = strings.TrimPrefix(clean, "icons/")
			ts := it.UpdatedAt.Unix()
			if ts <= 0 {
				ts = it.CreatedAt.Unix()
			}
			if ts > usageMap[clean] {
				usageMap[clean] = ts
			}
		}
		sett := h.storage.GetSettings()
		cleanSett := strings.TrimPrefix(sett.PortalIcon, "/icons/")
		cleanSett = strings.TrimPrefix(cleanSett, "icons/")
		if usageMap[cleanSett] == 0 {
			usageMap[cleanSett] = time.Now().Unix()
		}
	}

	var list []IconInfo
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "dock_cache_") || strings.HasPrefix(name, "wc_cache_") || strings.HasPrefix(name, "copy_") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" && ext != ".svg" && ext != ".ico" {
			continue
		}

		var lastUsed int64
		inUse := false
		if ts, ok := usageMap[name]; ok && ts > 0 {
			lastUsed = ts
			inUse = true
		} else if info, err := f.Info(); err == nil {
			lastUsed = info.ModTime().Unix()
		}

		list = append(list, IconInfo{
			Name:     name,
			URL:      "/icons/" + name,
			LastUsed: lastUsed,
			InUse:    inUse,
		})
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].LastUsed != list[j].LastUsed {
			return list[i].LastUsed > list[j].LastUsed
		}
		return list[i].Name < list[j].Name
	})

	if list == nil {
		list = []IconInfo{}
	}
	h.jsonResponse(w, r, list, http.StatusOK)
}

// DELETE /api/icons or /api/icons/{filename}
func (h *Handler) handleDeleteIcon(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if filename == "" {
		filename = r.URL.Query().Get("filename")
	}
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "" || filename == "." || filename == "/" {
		h.jsonError(w, r, "缺少图标文件名", http.StatusBadRequest)
		return
	}

	// Protect built-in icons
	if filename == "default_item_icon.png" || filename == "icon.png" {
		h.jsonError(w, r, "系统内置图标禁止删除", http.StatusForbidden)
		return
	}

	// Check if in use
	if h.storage != nil {
		items := h.storage.GetAllItems()
		for _, it := range items {
			clean := strings.TrimPrefix(it.Icon, "/icons/")
			clean = strings.TrimPrefix(clean, "icons/")
			if clean == filename || it.Icon == filename {
				h.jsonError(w, r, "该图标正在被桌面图标条目使用中，无法删除", http.StatusBadRequest)
				return
			}
		}
		sett := h.storage.GetSettings()
		cleanSett := strings.TrimPrefix(sett.PortalIcon, "/icons/")
		cleanSett = strings.TrimPrefix(cleanSett, "icons/")
		if cleanSett == filename || sett.PortalIcon == filename {
			h.jsonError(w, r, "该图标正在作为管理面板图标使用中，无法删除", http.StatusBadRequest)
			return
		}
	}

	targetPath := filepath.Join(h.iconsDir, filename)
	if _, err := os.Stat(targetPath); err != nil {
		if os.IsNotExist(err) {
			h.jsonError(w, r, "图标文件不存在", http.StatusNotFound)
			return
		}
		h.jsonError(w, r, "访问图标文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := os.Remove(targetPath); err != nil {
		slog.Error("[AUDIT] 删除图标失败", "filename", filename, "error", err)
		h.jsonError(w, r, "删除图标失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("[AUDIT] 成功删除图标", "filename", filename)
	h.jsonResponse(w, r, map[string]bool{"success": true}, http.StatusOK)
}

// ResizeIconImage pads and scales an image to fit targetSize x targetSize square (e.g. 256x256)
// with centered aspect ratio and smooth bilinear interpolation.
func ResizeIconImage(src image.Image, targetSize int) *image.RGBA {
	bounds := src.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	if srcW == 0 || srcH == 0 {
		return image.NewRGBA(image.Rect(0, 0, targetSize, targetSize))
	}

	var dstW, dstH int
	if srcW >= srcH {
		dstW = targetSize
		dstH = int(float64(srcH) * float64(targetSize) / float64(srcW))
		if dstH < 1 {
			dstH = 1
		}
	} else {
		dstH = targetSize
		dstW = int(float64(srcW) * float64(targetSize) / float64(srcH))
		if dstW < 1 {
			dstW = 1
		}
	}

	dst := image.NewRGBA(image.Rect(0, 0, targetSize, targetSize))
	offsetX := (targetSize - dstW) / 2
	offsetY := (targetSize - dstH) / 2

	for y := 0; y < dstH; y++ {
		var srcY float64
		if dstH > 1 {
			srcY = float64(y) * float64(srcH-1) / float64(dstH-1)
		}
		y0 := int(srcY)
		y1 := y0 + 1
		if y1 >= srcH {
			y1 = srcH - 1
		}
		yWeight := srcY - float64(y0)

		for x := 0; x < dstW; x++ {
			var srcX float64
			if dstW > 1 {
				srcX = float64(x) * float64(srcW-1) / float64(dstW-1)
			}
			x0 := int(srcX)
			x1 := x0 + 1
			if x1 >= srcW {
				x1 = srcW - 1
			}
			xWeight := srcX - float64(x0)

			c00 := src.At(bounds.Min.X+x0, bounds.Min.Y+y0)
			c10 := src.At(bounds.Min.X+x1, bounds.Min.Y+y0)
			c01 := src.At(bounds.Min.X+x0, bounds.Min.Y+y1)
			c11 := src.At(bounds.Min.X+x1, bounds.Min.Y+y1)

			r00, g00, b00, a00 := c00.RGBA()
			r10, g10, b10, a10 := c10.RGBA()
			r01, g01, b01, a01 := c01.RGBA()
			r11, g11, b11, a11 := c11.RGBA()

			interpolate := func(v00, v10, v01, v11 uint32) uint8 {
				top := float64(v00)*(1-xWeight) + float64(v10)*xWeight
				bottom := float64(v01)*(1-xWeight) + float64(v11)*xWeight
				val := top*(1-yWeight) + bottom*yWeight
				return uint8(val / 257)
			}

			dst.SetRGBA(offsetX+x, offsetY+y, color.RGBA{
				R: interpolate(r00, r10, r01, r11),
				G: interpolate(g00, g10, g01, g11),
				B: interpolate(b00, b10, b01, b11),
				A: interpolate(a00, a10, a01, a11),
			})
		}
	}
	return dst
}

// /api/icons/upload
func (h *Handler) handleUploadIcon(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		slog.Warn("[AUDIT] 上传图标鉴权未通过", "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		slog.Warn("[AUDIT] 上传图标文件解析失败或超过大小限制", "error", err, "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "图标文件大小不能超过 10MB"}, http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("icon")
	if err != nil {
		slog.Warn("[AUDIT] 未获取到上传文件", "error", err, "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "未获取到上传文件"}, http.StatusBadRequest)
		return
	}
	defer file.Close()

	if header.Size > 10<<20 {
		slog.Warn("[AUDIT] 上传图标文件超过 10MB 限制", "sizeBytes", header.Size, "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "图标文件大小不能超过 10MB"}, http.StatusBadRequest)
		return
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" && ext != ".svg" && ext != ".ico" {
		slog.Warn("[AUDIT] 上传图标格式不受支持", "filename", header.Filename, "ext", ext, "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "仅支持 PNG/JPG/WebP/SVG/ICO 图标"}, http.StatusBadRequest)
		return
	}

	fileData, err := io.ReadAll(file)
	if err != nil {
		slog.Warn("[AUDIT] 读取上传文件失败", "error", err, "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "读取上传文件失败: " + err.Error()}, http.StatusBadRequest)
		return
	}

	if len(fileData) > 10<<20 {
		slog.Warn("[AUDIT] 上传图标实际读取大小超过 10MB", "sizeBytes", len(fileData), "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "图标文件大小不能超过 10MB"}, http.StatusBadRequest)
		return
	}

	cleanBase := desktop.SanitizeFileName(header.Filename)

	if ext == ".svg" {
		if err := SanitizeSVGContent(fileData); err != nil {
			slog.Warn("[SECURITY] 拦截包含潜在不安全代码的 SVG 文件", "error", err, "remote", r.RemoteAddr)
			h.jsonResponse(w, r, map[string]string{"error": "SVG 图标包含潜在不安全脚本代码，已被安全拦截"}, http.StatusBadRequest)
			return
		}
	} else if ext != ".ico" {
		// Attempt to decode and auto-resize/compress raster images to 256x256 square PNG
		if srcImg, _, err := image.Decode(bytes.NewReader(fileData)); err == nil {
			b := srcImg.Bounds()
			if b.Dx() > 0 && b.Dy() > 0 {
				resized := ResizeIconImage(srcImg, 256)
				var buf bytes.Buffer
				enc := png.Encoder{CompressionLevel: png.BestCompression}
				if err := enc.Encode(&buf, resized); err == nil && buf.Len() > 0 {
					origSize := len(fileData)
					fileData = buf.Bytes()
					cleanBase = strings.TrimSuffix(cleanBase, filepath.Ext(cleanBase)) + ".png"
					slog.Info("[AUDIT] 图标自动压缩完成", "originalSize", origSize, "compressedSize", len(fileData), "dim", "256x256")
				}
			}
		}
	}

	filename := fmt.Sprintf("%d_%s", time.Now().Unix(), cleanBase)
	destPath := filepath.Join(h.iconsDir, filename)

	slog.Info("[AUDIT] 正在保存用户上传的图标...", "originalFilename", header.Filename, "savedAs", filename, "sizeBytes", len(fileData), "remote", r.RemoteAddr)

	out, err := os.Create(destPath)
	if err != nil {
		slog.Error("[AUDIT] 创建图标文件失败", "destPath", destPath, "error", err)
		h.jsonResponse(w, r, map[string]string{"error": "创建文件失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}
	defer out.Close()

	if _, err := out.Write(fileData); err != nil {
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
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasSuffix(strings.ToLower(filename), ".svg") {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	}
	http.ServeFile(w, r, iconPath)
}

// /redirect or /api/redirect: supports target query param and WatchCow CGI path: /redirect/<appName>/_
func (h *Handler) handleRedirect(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		target = r.URL.Query().Get("url")
	}

	var foundItem *desktop.DesktopItem

	// If no query parameter, parse path: /redirect/<appName>/_ or /redirect/<appName>
	if target == "" {
		pathInfo := r.URL.Path
		if idx := strings.Index(pathInfo, "/redirect/"); idx != -1 {
			pathInfo = pathInfo[idx+len("/redirect/"):]
		} else if idx := strings.Index(pathInfo, "/redirect"); idx != -1 {
			pathInfo = pathInfo[idx+len("/redirect"):]
		}
		pathInfo = strings.TrimPrefix(pathInfo, "/")
		parts := strings.Split(pathInfo, "/")
		if len(parts) > 0 && parts[0] != "" {
			appName := parts[0]
			if item, ok := h.storage.GetItemByAppName(appName); ok {
				foundItem = &item
				if item.TargetURL != "" {
					target = item.TargetURL
				} else if item.Port > 0 {
					proto := item.Protocol
					if proto == "" {
						proto = "http"
					}
					p := item.Path
					if p == "" {
						p = "/"
					}
					host := r.Host
					if hName, _, err := net.SplitHostPort(host); err == nil {
						host = hName
					}
					target = fmt.Sprintf("%s://%s:%d%s", proto, host, item.Port, p)
				}
			}
		}
	}

	if foundItem == nil {
		if id := r.URL.Query().Get("id"); id != "" {
			if item, ok := h.storage.GetItem(id); ok {
				foundItem = &item
				if target == "" {
					if item.TargetURL != "" {
						target = item.TargetURL
					} else if item.Port > 0 {
						proto := item.Protocol
						if proto == "" {
							proto = "http"
						}
						p := item.Path
						if p == "" {
							p = "/"
						}
						host := r.Host
						if hName, _, err := net.SplitHostPort(host); err == nil {
							host = hName
						}
						target = fmt.Sprintf("%s://%s:%d%s", proto, host, item.Port, p)
					}
				}
			}
		}
	}

	if target == "" {
		slog.Warn("[AUDIT] 桌面图标跳转缺少目标地址", "path", r.URL.Path, "query", r.URL.RawQuery, "remote", r.RemoteAddr)
		http.Error(w, "缺少跳转目标 target", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "http://" + target
	}

	slog.Info("[AUDIT] 桌面快捷方式跳转触发", "target", target, "path", r.URL.Path, "remote", r.RemoteAddr)

	// If item has notice enabled, render interstitial notice page before proceeding
	if foundItem != nil && foundItem.NoticeEnabled && strings.TrimSpace(foundItem.NoticeContent) != "" {
		h.renderNoticePage(w, *foundItem, target)
		return
	}

	w.Header().Set("Location", target)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusFound)

	escapedTarget := html.EscapeString(target)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta http-equiv="refresh" content="0; url=%s">
    <title>正在跳转...</title>
    <script>
        window.location.replace("%s");
    </script>
</head>
<body>
    <p>正在跳转至 <a href="%s">%s</a>...</p>
</body>
</html>`, escapedTarget, template.JSEscapeString(target), escapedTarget, escapedTarget)
}

func (h *Handler) renderNoticePage(w http.ResponseWriter, item desktop.DesktopItem, target string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	escapedTitle := html.EscapeString(item.Name)
	escapedNotice := html.EscapeString(item.NoticeContent)

	iconDataUrl := ""
	if strings.HasPrefix(item.Icon, "data:image/") {
		iconDataUrl = item.Icon
	} else if item.Icon != "" {
		cleanName := filepath.Clean(strings.TrimPrefix(strings.TrimPrefix(item.Icon, "/icons/"), "icons/"))
		iconPath := filepath.Join(h.iconsDir, cleanName)
		if data, err := os.ReadFile(iconPath); err == nil && len(data) > 0 {
			mimeType := "image/png"
			if strings.HasSuffix(cleanName, ".svg") {
				mimeType = "image/svg+xml"
			} else if strings.HasSuffix(cleanName, ".jpg") || strings.HasSuffix(cleanName, ".jpeg") {
				mimeType = "image/jpeg"
			} else if strings.HasSuffix(cleanName, ".webp") {
				mimeType = "image/webp"
			} else if strings.HasSuffix(cleanName, ".ico") {
				mimeType = "image/x-icon"
			}
			iconDataUrl = fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data))
		}
	}
	if iconDataUrl == "" {
		if data, err := os.ReadFile(filepath.Join(h.iconsDir, "default_item_icon.png")); err == nil && len(data) > 0 {
			iconDataUrl = "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
		} else if data, err := os.ReadFile(filepath.Join(h.iconsDir, "icon.png")); err == nil && len(data) > 0 {
			iconDataUrl = "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
		} else if data, err := os.ReadFile(filepath.Join(h.iconsDir, "../web/default_item_icon.png")); err == nil && len(data) > 0 {
			iconDataUrl = "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
		} else if data, err := os.ReadFile(filepath.Join(h.iconsDir, "../icon.png")); err == nil && len(data) > 0 {
			iconDataUrl = "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
		}
	}

	targetJson, _ := json.Marshal(target)
	idJson, _ := json.Marshal(item.ID)

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>%s - 开屏提示</title>
  <style>
    :root {
      --bg-page: #f1f5f9;
      --bg-card: #ffffff;
      --text-main: #0f172a;
      --text-muted: #64748b;
      --border: #e2e8f0;
      --primary: #2563eb;
      --primary-hover: #1d4ed8;
      --notice-bg: #eff6ff;
      --notice-border: #bfdbfe;
      --notice-text: #1e3a8a;
    }
    @media (prefers-color-scheme: dark) {
      :root {
        --bg-page: #0b0f17;
        --bg-card: #151b28;
        --text-main: #f8fafc;
        --text-muted: #94a3b8;
        --border: #242f42;
        --primary: #3b82f6;
        --primary-hover: #2563eb;
        --notice-bg: #172554;
        --notice-border: #1e40af;
        --notice-text: #dbeafe;
      }
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC", "Microsoft YaHei", sans-serif;
      background-color: var(--bg-page);
      color: var(--text-main);
      display: flex;
      align-items: center;
      justify-content: center;
      min-height: 100vh;
      padding: 1.5rem;
    }
    .notice-card {
      background: var(--bg-card);
      border: 1px solid var(--border);
      border-radius: 14px;
      padding: 24px;
      max-width: 460px;
      width: 100%%;
      box-shadow: 0 10px 25px -5px rgba(0, 0, 0, 0.1), 0 8px 10px -6px rgba(0, 0, 0, 0.05);
      animation: fadeIn 0.2s ease-out;
    }
    @keyframes fadeIn {
      from { opacity: 0; transform: translateY(6px); }
      to { opacity: 1; transform: translateY(0); }
    }
    .notice-header {
      display: flex;
      align-items: center;
      gap: 14px;
      margin-bottom: 16px;
    }
    .notice-icon {
      width: 48px;
      height: 48px;
      border-radius: 10px;
      object-fit: cover;
      background: var(--bg-page);
      border: 1px solid var(--border);
      flex-shrink: 0;
    }
    .notice-title-group {
      flex: 1;
      overflow: hidden;
    }
    .notice-app-name {
      font-size: 17px;
      font-weight: 700;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      color: var(--text-main);
    }
    .notice-badge {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      font-size: 11px;
      color: var(--primary);
      margin-top: 2px;
      font-weight: 600;
    }
    .notice-body {
      background: var(--notice-bg);
      border: 1px solid var(--notice-border);
      color: var(--notice-text);
      border-radius: 10px;
      padding: 14px 16px;
      font-size: 14px;
      line-height: 1.6;
      white-space: pre-wrap;
      word-break: break-word;
      max-height: 260px;
      overflow-y: auto;
      margin-bottom: 20px;
    }
    .notice-footer {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      flex-wrap: wrap;
    }
    .skip-label {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      font-size: 13px;
      color: var(--text-muted);
      cursor: pointer;
      user-select: none;
    }
    .btn-proceed {
      background-color: var(--primary);
      color: #ffffff;
      border: none;
      padding: 9px 18px;
      border-radius: 8px;
      font-size: 14px;
      font-weight: 600;
      cursor: pointer;
      transition: background-color 0.15s, transform 0.1s;
      outline: none;
    }
    .btn-proceed:hover {
      background-color: var(--primary-hover);
    }
    .btn-proceed:active {
      transform: scale(0.98);
    }
  </style>
</head>
<body>
  <div class="notice-card">
    <div class="notice-header">
      <img src="%s" alt="icon" class="notice-icon" onerror="this.onerror=null; this.style.display='none';">
      <div class="notice-title-group">
        <div class="notice-app-name">%s</div>
      </div>
    </div>
    <div class="notice-body">%s</div>
    <div class="notice-footer">
      <label class="skip-label">
        <input type="checkbox" id="skip-today">
        <span>今日不再提示</span>
      </label>
      <button type="button" class="btn-proceed" id="btn-proceed">进入应用</button>
    </div>
  </div>
  <script>
    (function() {
      const TARGET_URL = %s;
      const ITEM_ID = %s;
      const skipKey = 'fn_notice_skip_' + ITEM_ID;
      const today = new Date().toISOString().slice(0, 10);

      try {
        if (localStorage.getItem(skipKey) === today) {
          window.location.replace(TARGET_URL);
          return;
        }
      } catch (e) {}

      const btn = document.getElementById('btn-proceed');
      const chk = document.getElementById('skip-today');

      function proceed() {
        if (chk && chk.checked) {
          try {
            localStorage.setItem(skipKey, today);
          } catch (e) {}
        }
        window.location.replace(TARGET_URL);
      }

      if (btn) {
        btn.addEventListener('click', proceed);
        btn.focus();
      }

      window.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') {
          proceed();
        }
      });
    })();
  </script>
</body>
</html>`, escapedTitle, iconDataUrl, escapedTitle, escapedNotice, string(targetJson), string(idJson))
}

// Auth handlers
func (h *Handler) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	clientIP := GetClientIP(r)
	isWAN := clientIP != nil && !IsPrivateOrLocalIP(clientIP)
	authRequired := (h.authMgr != nil && h.authMgr.IsAuthRequired()) || isWAN
	authenticated := h.checkAuth(r)
	h.jsonResponse(w, r, map[string]any{
		"auth_required": authRequired,
		"authenticated": authenticated,
		"wan_detected":  isWAN,
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

	clientIP := ""
	if ip := GetClientIP(r); ip != nil {
		clientIP = ip.String()
	}
	if clientIP != "" && globalSecurityMgr.IsBlockedByRateLimit(clientIP) {
		slog.Warn("[SECURITY] 拦截频繁登录尝试", "clientIP", clientIP, "remote", r.RemoteAddr)
		h.jsonResponse(w, r, map[string]string{"error": "尝试登录失败次数过多，已被临时锁定，请 5 分钟后再试"}, http.StatusTooManyRequests)
		return
	}

	if h.authMgr == nil || !h.authMgr.IsAuthRequired() {
		h.jsonResponse(w, r, map[string]bool{"success": true}, http.StatusOK)
		return
	}

	if !h.authMgr.VerifyPassword(req.Password) {
		if clientIP != "" {
			globalSecurityMgr.RecordFailedLogin(clientIP)
		}
		h.jsonResponse(w, r, map[string]string{"error": "密码错误"}, http.StatusUnauthorized)
		return
	}

	if clientIP != "" {
		globalSecurityMgr.ResetFailedLogin(clientIP)
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

	source := r.URL.Query().Get("source")
	level := r.URL.Query().Get("level")
	search := r.URL.Query().Get("search")
	limit := 5000
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

	resp, err := logInst.ReadLogs(source, level, search, limit)
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

	source := r.URL.Query().Get("source")
	date := r.URL.Query().Get("date")

	logInst := h.loggerInstance
	if logInst == nil {
		logInst = logger.GetDefault()
	}

	if logInst == nil {
		http.Error(w, "日志系统未就绪", http.StatusInternalServerError)
		return
	}

	if source == "lifecycle" {
		filePath := logInst.GetLifecycleLogFilePath()
		fi, err := os.Stat(filePath)
		if err != nil || fi.IsDir() {
			http.Error(w, "未找到系统生命周期日志文件", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\"fn-docker-to-desktop-lifecycle.log\"")
		http.ServeFile(w, r, filePath)
		return
	}

	filePath := logInst.GetLogFilePath(date)
	fi, err := os.Stat(filePath)
	if err != nil || fi.IsDir() {
		filePath = "/tmp/fn-docker-to-desktop.log"
		fi, err = os.Stat(filePath)
		if err != nil || fi.IsDir() {
			http.Error(w, "未找到日志文件", http.StatusNotFound)
			return
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"fn-docker-to-desktop.log\"")
	http.ServeFile(w, r, filePath)
}

func (h *Handler) getDockLabelItemsCached() ([]desktop.DockLabelItem, error) {
	h.dockLabelMu.Lock()
	if h.dockLabelItems != nil && time.Now().Before(h.dockLabelExp) {
		cached := h.dockLabelItems
		h.dockLabelMu.Unlock()
		return cached, nil
	}
	h.dockLabelMu.Unlock()

	items, err := desktop.ScanDockLabelItems(h.storage.GetDockLabelState)
	if err != nil {
		return nil, err
	}

	h.dockLabelMu.Lock()
	h.dockLabelItems = items
	h.dockLabelExp = time.Now().Add(10 * time.Second)
	h.dockLabelMu.Unlock()
	return items, nil
}

func (h *Handler) invalidateDockLabelCache() {
	h.dockLabelMu.Lock()
	h.dockLabelItems = nil
	h.dockLabelExp = time.Time{}
	h.dockLabelMu.Unlock()
}

// /api/desktop/docklabel
func (h *Handler) handleGetDockLabelItems(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() {
		dur := time.Since(start)
		if dur > 3000*time.Millisecond {
			slog.Warn("[PERF] handleGetDockLabelItems 扫描耗时过长", "duration", dur)
		}
	}()

	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("refresh") == "true" {
		h.invalidateDockLabelCache()
	}
	rawItems, err := h.getDockLabelItemsCached()
	if err != nil {
		slog.Warn("[DOCKLABEL] 扫描 Docker 容器标签失败", "error", err)
		h.jsonResponse(w, r, []desktop.DockLabelItem{}, http.StatusOK)
		return
	}
	items := make([]desktop.DockLabelItem, len(rawItems))
	copy(items, rawItems)
	for i := range items {
		appName := items[i].AppName
		if appName == "" {
			appName = desktop.DeriveDockLabelAppName(items[i].ContainerName, items[i].EntryName, "")
		}
		if op, inFlight := h.inFlightOps.Load(items[i].ID); inFlight {
			items[i].Reconciling = true
			if opStr, ok := op.(string); ok && opStr != "" {
				items[i].StatusText = opStr
			} else {
				items[i].StatusText = "更新中..."
			}
		} else if h.installer != nil {
			if isRec, statusText := h.installer.GetReconcileStatus(items[i].ID, appName); isRec {
				items[i].Reconciling = true
				items[i].StatusText = statusText
			}
		}
	}
	h.jsonResponse(w, r, items, http.StatusOK)
}

// /api/desktop/docklabel/{id}/toggle
func (h *Handler) handleToggleDockLabelItem(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		h.jsonResponse(w, r, map[string]string{"error": "id is required"}, http.StatusBadRequest)
		return
	}

	curEnabled := h.storage.GetDockLabelState(id, false)
	targetEnabled := !curEnabled
	opText := "停用中..."
	if targetEnabled {
		opText = "启用中..."
	}

	if _, loaded := h.inFlightOps.LoadOrStore(id, opText); loaded {
		slog.Warn("[DOCKLABEL] 容器标签条目正在处理中，拒绝重复并发请求", "id", id)
		h.jsonResponse(w, r, map[string]string{"error": "该图标正在处理中，请稍候"}, http.StatusConflict)
		return
	}
	defer h.inFlightOps.Delete(id)

	items, err := desktop.ScanDockLabelItems(h.storage.GetDockLabelState)
	if err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "扫描容器标签失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	var targetItem *desktop.DockLabelItem
	for i := range items {
		if items[i].ID == id {
			targetItem = &items[i]
			break
		}
	}
	if targetItem == nil {
		h.jsonResponse(w, r, map[string]string{"error": "未找到对应的容器标签项目"}, http.StatusNotFound)
		return
	}

	targetState := !targetItem.Enabled
	targetItem.Enabled = targetState
	_ = h.storage.SetDockLabelState(id, targetState)

	if h.installer != nil {
		appName := targetItem.AppName
		if appName == "" || !strings.HasPrefix(appName, "fndocker.dock-") {
			appName = desktop.DeriveDockLabelAppName(targetItem.ContainerName, targetItem.EntryName, "")
		}
		targetItem.AppName = appName

		iconToUse := targetItem.LocalIconPath
		if isLocal, baseName := desktop.IsLocalOrLoopbackIconURL(targetItem.Icon); isLocal && baseName != "" {
			target := filepath.Join(h.iconsDir, baseName)
			if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
				iconToUse = target
			} else if resolved := desktop.ResolveWatchcowIconPath(baseName, "", nil); resolved != "" {
				if data, err := os.ReadFile(resolved); err == nil {
					_ = os.WriteFile(target, data, 0644)
				}
				iconToUse = resolved
			}
		}
		if iconToUse == "" && targetItem.Icon != "" {
			iconToUse = desktop.ResolveWatchcowIconPath(targetItem.Icon, "", nil)
		}
		if iconToUse == "" {
			iconToUse = targetItem.Icon
		}
		if strings.HasPrefix(iconToUse, "http://") || strings.HasPrefix(iconToUse, "https://") {
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(iconToUse)))
			ext := filepath.Ext(iconToUse)
			if ext == "" || len(ext) > 5 {
				ext = ".png"
			}
			cachePath := filepath.Join(h.iconsDir, "dock_cache_"+hash+ext)
			legacyCachePath := filepath.Join(h.iconsDir, "wc_cache_"+hash+ext)
			if info, err := os.Stat(cachePath); err == nil && info.Size() > 0 {
				iconToUse = cachePath
			} else if info, err := os.Stat(legacyCachePath); err == nil && info.Size() > 0 {
				iconToUse = legacyCachePath
			}
		}
		idHash := fmt.Sprintf("%x", sha256.Sum256([]byte(targetItem.ID)))
		idCachePath := filepath.Join(h.iconsDir, "dock_cache_"+idHash+".png")
		if (iconToUse == "" || strings.HasPrefix(iconToUse, "http")) {
			if fi, err := os.Stat(idCachePath); err == nil && fi.Size() > 0 {
				iconToUse = idCachePath
			}
		}

		dItem := desktop.DesktopItem{
			ID:            targetItem.ID,
			Name:          targetItem.Name,
			AppName:       targetItem.AppName,
			ContainerName: targetItem.ContainerName,
			Image:         targetItem.Image,
			Port:          targetItem.Port,
			Protocol:      targetItem.Protocol,
			Path:          targetItem.Path,
			TargetURL:     targetItem.TargetURL,
			UIType:        targetItem.UIType,
			AllUsers:      targetItem.AllUsers,
			Icon:          iconToUse,
			FileTypes:     targetItem.FileTypes,
			NoDisplay:     targetItem.NoDisplay,
			Enabled:       targetItem.Enabled,
			Mode:          desktop.ItemMode(targetItem.Mode),
		}
		if targetState {
			_ = h.installer.InstallItem(dItem)
		} else {
			// Collect active app names from custom desktop items to protect them
			var protected []string
			for _, exist := range h.storage.GetAllItems() {
				if exist.Enabled && exist.AppName != "" {
					protected = append(protected, exist.AppName)
				}
			}
			_ = h.installer.UninstallItem(dItem, protected...)
			// Defensively uninstall our app's own legacy package names, protecting any active desktop items
			legacyPrefixes := []string{
				"fndocker.wc-" + targetItem.ContainerName,
			}
			if targetItem.EntryName != "" && targetItem.EntryName != "default" {
				legacyPrefixes = append(legacyPrefixes,
					fmt.Sprintf("fndocker.wc-%s-%s", targetItem.ContainerName, targetItem.EntryName),
				)
			}
			for _, pfx := range legacyPrefixes {
				legacyItem := dItem
				legacyItem.AppName = pfx
				_ = h.installer.UninstallItem(legacyItem, protected...)
			}
		}
	}

	slog.Info("<=== [DOCKLABEL] 切换容器标签条目状态成功", "id", id, "name", targetItem.Name, "enabled", targetItem.Enabled)
	h.invalidateDockLabelCache()
	idHash := fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
	_ = os.Remove(filepath.Join(h.iconsDir, "dock_cache_"+idHash+".png"))
	_ = os.Remove(filepath.Join(h.iconsDir, "wc_cache_"+idHash+".png"))
	h.inFlightOps.Delete(id)
	h.jsonResponse(w, r, targetItem, http.StatusOK)
}

func (h *Handler) getDefaultItemIconBytes() ([]byte, error) {
	if h.webFS != nil {
		if data, err := fs.ReadFile(h.webFS, "default_item_icon.png"); err == nil && len(data) > 0 {
			return data, nil
		}
	}
	if data, err := os.ReadFile(filepath.Join(h.iconsDir, "default_item_icon.png")); err == nil && len(data) > 0 {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func (h *Handler) serveDefaultItemIcon(w http.ResponseWriter, r *http.Request) {
	if h.webFS != nil {
		if data, err := fs.ReadFile(h.webFS, "default_item_icon.png"); err == nil && len(data) > 0 {
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.WriteHeader(http.StatusOK)
			w.Write(data)
			return
		}
	}
	if data, err := os.ReadFile(filepath.Join(h.iconsDir, "default_item_icon.png")); err == nil && len(data) > 0 {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
		return
	}
	http.NotFound(w, r)
}

// /api/desktop/docklabel/icon
func (h *Handler) handleGetDockLabelIcon(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() {
		dur := time.Since(start)
		if dur > 1000*time.Millisecond {
			slog.Warn("[PERF] handleGetDockLabelIcon 获取图标耗时过长", "duration", dur, "id", r.URL.Query().Get("id"))
		}
	}()

	id := r.URL.Query().Get("id")
	if id == "" {
		h.serveDefaultItemIcon(w, r)
		return
	}

	// 0. Fast-path disk cache check: instantaneous response without touching Docker socket or network
	idHash := fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
	cachePath := filepath.Join(h.iconsDir, "dock_cache_"+idHash+".png")
	legacyCachePath := filepath.Join(h.iconsDir, "wc_cache_"+idHash+".png")
	if info, err := os.Stat(cachePath); err == nil && info.Size() > 0 {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, cachePath)
		return
	}
	if info, err := os.Stat(legacyCachePath); err == nil && info.Size() > 0 {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, legacyCachePath)
		return
	}

	items, err := h.getDockLabelItemsCached()
	if err != nil || len(items) == 0 {
		h.serveDefaultItemIcon(w, r)
		return
	}

	var found *desktop.DockLabelItem
	for i := range items {
		if items[i].ID == id || strings.TrimPrefix(items[i].ID, "docklabel-") == strings.TrimPrefix(id, "watchcow-") {
			found = &items[i]
			break
		}
	}
	if found == nil {
		h.serveDefaultItemIcon(w, r)
		return
	}

	data, ct, err := desktop.ResolveDockLabelIconBytes(found, h.iconsDir)
	if err == nil && len(data) > 0 {
		if ct == "" {
			ct = "image/png"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}

	// Fallback to default icon: only cache to disk if no icon was configured at all
	slog.Debug("[DOCKLABEL-ICON] 容器标签未配置或未找到自定义图标，回退至系统默认图标",
		"id", id,
		"name", found.Name,
		"iconVal", found.Icon,
		"localIconPath", found.LocalIconPath,
	)
	if strings.TrimSpace(found.Icon) == "" {
		if data, err := h.getDefaultItemIconBytes(); err == nil && len(data) > 0 {
			_ = os.WriteFile(cachePath, data, 0644)
		}
	}
	h.serveDefaultItemIcon(w, r)
}

// VersionCheckResponse represents the response of checking for application updates.
type VersionCheckResponse struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	HasUpdate      bool   `json:"has_update"`
	ReleaseName    string `json:"release_name"`
	ReleaseNotes   string `json:"release_notes"`
	PublishedAt    string `json:"published_at"`
	HTMLURL        string `json:"html_url"`
	Arch           string `json:"arch"`
	DownloadURL    string `json:"download_url"`
	AcceleratedURL string `json:"accelerated_url"`
	CheckedAt      string `json:"checked_at"`
	Error          string `json:"error,omitempty"`
}

type ghReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghReleaseResponse struct {
	TagName     string           `json:"tag_name"`
	Name        string           `json:"name"`
	Body        string           `json:"body"`
	PublishedAt string           `json:"published_at"`
	HTMLURL     string           `json:"html_url"`
	Assets      []ghReleaseAsset `json:"assets"`
}

func compareVersions(v1, v2 string) int {
	clean := func(s string) []int {
		s = strings.TrimPrefix(strings.TrimSpace(s), "v")
		parts := strings.Split(s, ".")
		res := make([]int, 0, 3)
		for _, p := range parts {
			num := 0
			hasNum := false
			for _, r := range p {
				if r >= '0' && r <= '9' {
					num = num*10 + int(r-'0')
					hasNum = true
				} else {
					break
				}
			}
			if hasNum {
				res = append(res, num)
			}
		}
		for len(res) < 3 {
			res = append(res, 0)
		}
		return res
	}

	p1 := clean(v1)
	p2 := clean(v2)
	for i := 0; i < len(p1) && i < len(p2); i++ {
		if p1[i] < p2[i] {
			return -1
		}
		if p1[i] > p2[i] {
			return 1
		}
	}
	return 0
}

func (h *Handler) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	force := r.URL.Query().Get("force") == "true" || r.URL.Query().Get("force") == "1"

	h.versionCheckMu.Lock()
	if !force && h.versionCheckCached != nil && time.Now().Before(h.versionCheckExp) {
		cached := *h.versionCheckCached
		h.versionCheckMu.Unlock()
		h.jsonResponse(w, r, cached, http.StatusOK)
		return
	}
	h.versionCheckMu.Unlock()

	arch := "x86"
	if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
		arch = "arm"
	}

	res := VersionCheckResponse{
		CurrentVersion: h.appVersion,
		Arch:           arch,
		CheckedAt:      time.Now().Format("2006-01-02 15:04:05"),
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	fetchRelease := func(endpoint string) (*ghReleaseResponse, error) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "fn-docker-to-desktop/"+h.appVersion)

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP status %d", resp.StatusCode)
		}

		var ghResp ghReleaseResponse
		if err := json.NewDecoder(resp.Body).Decode(&ghResp); err != nil {
			return nil, err
		}
		return &ghResp, nil
	}

	// Try direct GitHub API first
	ghRelease, err := fetchRelease("https://api.github.com/repos/67373net/fn-docker-to-desktop/releases/latest")
	if err != nil {
		slog.Debug("Direct GitHub releases query failed, trying mirror fallback", "error", err)
		// Fallback to proxy mirror
		ghRelease, err = fetchRelease("https://ghproxy.net/https://api.github.com/repos/67373net/fn-docker-to-desktop/releases/latest")
	}

	if err != nil {
		slog.Warn("Version check failed", "error", err)
		res.Error = "检测新版本失败，网络连接超时或无法访问 GitHub (请稍后重试)"
		h.jsonResponse(w, r, res, http.StatusOK)
		return
	}

	latestTag := strings.TrimPrefix(ghRelease.TagName, "v")
	res.LatestVersion = latestTag
	res.ReleaseName = ghRelease.Name
	res.ReleaseNotes = ghRelease.Body
	res.PublishedAt = ghRelease.PublishedAt
	res.HTMLURL = ghRelease.HTMLURL

	if compareVersions(h.appVersion, latestTag) < 0 {
		res.HasUpdate = true
	}

	// Match download URL for host architecture
	targetAsset := fmt.Sprintf("fn-docker-to-desktop-%s.fpk", arch)
	for _, asset := range ghRelease.Assets {
		if asset.Name == targetAsset {
			res.DownloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if res.DownloadURL == "" {
		res.DownloadURL = fmt.Sprintf("https://github.com/67373net/fn-docker-to-desktop/releases/download/%s/%s", ghRelease.TagName, targetAsset)
	}
	res.AcceleratedURL = fmt.Sprintf("https://mirror.ghproxy.com/%s", res.DownloadURL)

	h.versionCheckMu.Lock()
	h.versionCheckCached = &res
	h.versionCheckExp = time.Now().Add(15 * time.Minute)
	h.versionCheckMu.Unlock()

	h.jsonResponse(w, r, res, http.StatusOK)
}
