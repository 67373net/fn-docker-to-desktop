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
	"time"

	"fn-docker-to-desktop/internal/auth"
	"fn-docker-to-desktop/internal/desktop"
	"fn-docker-to-desktop/internal/monitor"
	"fn-docker-to-desktop/internal/proxy"
)

// Handler handles all HTTP API requests.
type Handler struct {
	storage      *desktop.Storage
	installer    *desktop.Installer
	proxyMgr     *proxy.Manager
	watcher      *monitor.Watcher
	systemSample *monitor.SystemSampler
	authMgr      *auth.Manager
	webFS        fs.FS
	procPath     string
	iconsDir     string
}

// Config holds configuration to instantiate API Handler.
type Config struct {
	Storage      *desktop.Storage
	Installer    *desktop.Installer
	ProxyMgr     *proxy.Manager
	Watcher      *monitor.Watcher
	SystemSample *monitor.SystemSampler
	AuthMgr      *auth.Manager
	WebFS        fs.FS
	ProcPath     string
	DataDir      string
}

// NewHandler creates a new API Handler.
func NewHandler(cfg Config) *Handler {
	iconsDir := filepath.Join(cfg.DataDir, "icons")
	_ = os.MkdirAll(iconsDir, 0755)

	return &Handler{
		storage:      cfg.Storage,
		installer:    cfg.Installer,
		proxyMgr:     cfg.ProxyMgr,
		watcher:      cfg.Watcher,
		systemSample: cfg.SystemSample,
		authMgr:      cfg.AuthMgr,
		webFS:        cfg.WebFS,
		procPath:     cfg.ProcPath,
		iconsDir:     iconsDir,
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
	mux.HandleFunc("DELETE /api/desktop/items/{id}", h.handleDeleteDesktopItem)
	mux.HandleFunc("POST /api/desktop/items/{id}/toggle", h.handleToggleDesktopItem)

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
			h.serveWithGzip(w, r, fileServer)
		})
	}
}

func (h *Handler) jsonResponse(w http.ResponseWriter, r *http.Request, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	bytes, err := json.Marshal(data)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"json marshal failed"}`))
		return
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
	portMap := make(map[int]string)
	for _, item := range desktopItems {
		if item.Port > 0 {
			portMap[item.Port] = item.Name
		}
	}

	for i := range ports {
		if name, ok := portMap[ports[i].LocalPort]; ok {
			ports[i].HasDesktop = true
			ports[i].DesktopName = name
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
	pMap := make(map[int]string)
	for _, item := range desktopItems {
		if item.Port > 0 {
			pMap[item.Port] = item.Name
		}
	}
	for i := range initialPorts {
		if name, ok := pMap[initialPorts[i].LocalPort]; ok {
			initialPorts[i].HasDesktop = true
			initialPorts[i].DesktopName = name
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
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	var item desktop.DesktopItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
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
	item.Enabled = true

	// If mode is proxy, start the reverse proxy
	if item.Mode == desktop.ModeProxy {
		if item.Port <= 0 {
			item.Port = proxy.RecommendAvailablePort(18000, nil)
		}
		if err := h.proxyMgr.StartProxy(item.ID, item.Port, item.TargetURL, item.SkipTLSVerify); err != nil {
			h.jsonResponse(w, r, map[string]string{"error": "启动反向代理失败: " + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Install to fnOS desktop
	if err := h.installer.InstallItem(item); err != nil {
		slog.Warn("安装到桌面遇到告警", "error", err)
	}
	item.Installed = true

	if err := h.storage.SaveItem(item); err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "保存失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	h.jsonResponse(w, r, item, http.StatusOK)
}

func (h *Handler) handleUpdateDesktopItem(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		h.jsonResponse(w, r, map[string]string{"error": "缺少ID"}, http.StatusBadRequest)
		return
	}

	var item desktop.DesktopItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "参数解析失败: " + err.Error()}, http.StatusBadRequest)
		return
	}
	item.ID = id

	if item.Mode == desktop.ModeProxy && item.Enabled {
		_ = h.proxyMgr.StartProxy(item.ID, item.Port, item.TargetURL, item.SkipTLSVerify)
	} else if item.Mode != desktop.ModeProxy {
		h.proxyMgr.StopProxy(item.ID)
	}

	if item.Enabled {
		_ = h.installer.InstallItem(item)
		item.Installed = true
	} else {
		_ = h.installer.UninstallItem(item.ID)
		item.Installed = false
	}

	_ = h.storage.SaveItem(item)
	h.jsonResponse(w, r, item, http.StatusOK)
}

func (h *Handler) handleDeleteDesktopItem(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		h.jsonResponse(w, r, map[string]string{"error": "缺少ID"}, http.StatusBadRequest)
		return
	}

	h.proxyMgr.StopProxy(id)
	_ = h.installer.UninstallItem(id)
	_ = h.storage.DeleteItem(id)

	h.jsonResponse(w, r, map[string]bool{"success": true}, http.StatusOK)
}

func (h *Handler) handleToggleDesktopItem(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")
	item, ok := h.storage.GetItem(id)
	if !ok {
		h.jsonResponse(w, r, map[string]string{"error": "未找到指定图标"}, http.StatusNotFound)
		return
	}

	item.Enabled = !item.Enabled
	if item.Enabled {
		if item.Mode == desktop.ModeProxy {
			_ = h.proxyMgr.StartProxy(item.ID, item.Port, item.TargetURL, item.SkipTLSVerify)
		}
		_ = h.installer.InstallItem(item)
		item.Installed = true
	} else {
		if item.Mode == desktop.ModeProxy {
			h.proxyMgr.StopProxy(item.ID)
		}
		_ = h.installer.UninstallItem(item.ID)
		item.Installed = false
	}

	_ = h.storage.SaveItem(item)
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
	h.jsonResponse(w, r, settings, http.StatusOK)
}

func (h *Handler) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	var newSettings desktop.Settings
	if err := json.NewDecoder(r.Body).Decode(&newSettings); err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "参数解析失败: " + err.Error()}, http.StatusBadRequest)
		return
	}

	current := h.storage.GetSettings()
	if newSettings.AuthPassword != "" {
		current.AuthPassword = newSettings.AuthPassword
		if h.authMgr != nil {
			h.authMgr.SetPassword(newSettings.AuthPassword)
		}
	}
	if newSettings.PortalPort > 0 {
		current.PortalPort = newSettings.PortalPort
	}
	if newSettings.PortalName != "" {
		current.PortalName = newSettings.PortalName
	}
	if newSettings.PortalUIType != "" {
		current.PortalUIType = newSettings.PortalUIType
	}
	current.PortalAllUsers = newSettings.PortalAllUsers

	if err := h.storage.UpdateSettings(current); err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "更新配置失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}

	if err := h.installer.SyncSelfApp(current); err != nil {
		slog.Warn("同步自身桌面图标遇到告警", "error", err)
	}

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
		h.jsonResponse(w, r, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
		return
	}

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "文件解析失败"}, http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("icon")
	if err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "未获取到上传文件"}, http.StatusBadRequest)
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" && ext != ".svg" && ext != ".ico" {
		h.jsonResponse(w, r, map[string]string{"error": "仅支持 PNG/JPG/WebP/SVG/ICO 图标"}, http.StatusBadRequest)
		return
	}

	cleanBase := desktop.SanitizeFileName(header.Filename)
	filename := fmt.Sprintf("%d_%s", time.Now().Unix(), cleanBase)
	destPath := filepath.Join(h.iconsDir, filename)

	out, err := os.Create(destPath)
	if err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "创建文件失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		h.jsonResponse(w, r, map[string]string{"error": "保存文件失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}

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
